// Package qq 提供 QQ 机器人的适配器实现。
//
// Adapter 是 QQ 机器人适配器，管理 WebSocket 连接和事件分发。
//
// 使用方式：
//
//	cfg := &config.QQConfig{...}
//	adapter, err := qq.NewAdapter(cfg, aiService)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer adapter.Stop()
//
//	if err := adapter.Start(ctx); err != nil {
//	    log.Fatal(err)
//	}
package qq

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/tencent-connect/botgo"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/event"

	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/config"
)

// Adapter 是 QQ 机器人适配器。
//
// 管理 WebSocket 连接和事件分发，协调客户端和 AI 服务。
//
// 字段说明：
//   - config: QQ 配置
//   - aiConfig: AI 配置（复用全局配置）
//   - client: QQ 客户端，处理 Token 和 API 调用
//   - aiService: 简化版 AI 服务
//   - handler: 事件处理器
//   - wg: 等待组，用于优雅关闭
//   - started: 是否已启动
//   - startMu: 启动状态锁
//
// 线程安全：该结构体是并发安全的。
type Adapter struct {
	config    *config.QQConfig
	aiConfig  *config.AIConfig
	client    *Client
	aiService *ai.SimpleService
	handler   *DefaultHandler
	wg        sync.WaitGroup
	started   bool
	startMu   sync.Mutex
	stopChan  chan struct{} // 用于通知 session goroutine 停止
}

// NewAdapter 创建一个新的 QQ 适配器实例。
//
// 该函数初始化 QQ 客户端和事件处理器。
//
// 参数:
//   - cfg: QQ 配置，必须包含有效的 AppID 和 AppSecret
//   - aiCfg: AI 配置，复用全局配置
//   - aiService: AI 服务（可选，为 nil 时仅基础命令可用）
//   - buildInfo: 构建信息（可选，用于 !version 命令）
//
// 返回值:
//   - *Adapter: 创建的适配器实例
//   - error: 创建过程中发生的错误
//
// 错误情况：
//   - 配置为空
//   - AI 配置为空
//   - 创建客户端失败
func NewAdapter(cfg *config.QQConfig, aiCfg *config.AIConfig, aiService *ai.SimpleService, buildInfo *BuildInfo) (*Adapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("QQ配置不能为空")
	}
	if aiCfg == nil {
		return nil, fmt.Errorf("AI配置不能为空")
	}

	client, err := NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("创建QQ客户端失败: %w", err)
	}

	// 创建命令注册表
	registry := NewCommandRegistry()

	// 注册基础命令（始终可用）
	registry.Register("ping", &PingCommand{}, "检查机器人是否在线")
	registry.Register("help", &HelpCommand{registry: registry}, "列出所有可用命令")
	if buildInfo != nil {
		registry.Register("version", &VersionCommand{buildInfo: buildInfo}, "显示版本信息")
	}

	// 注册 AI 命令（仅当 AI 服务可用时）
	var contextMgr *ContextManager
	var serviceAdapter SimpleAIService
	if aiService != nil {
		contextMgr = NewContextManager(aiCfg.Context)
		registry.Register("ai", NewAICommand(aiService, contextMgr), "与 AI 对话")
		serviceAdapter = &aiServiceAdapter{aiService}
		slog.Info("QQ AI 命令已启用")
	} else {
		slog.Info("QQ AI 命令未启用（AI 服务不可用）")
	}

	// 创建处理器
	handler := NewDefaultHandler(client, serviceAdapter, aiCfg, registry, contextMgr, buildInfo)

	return &Adapter{
		config:    cfg,
		aiConfig:  aiCfg,
		client:    client,
		aiService: aiService,
		handler:   handler,
	}, nil
}

// aiServiceAdapter 将 *ai.SimpleService 适配到 SimpleAIService 接口。
type aiServiceAdapter struct {
	svc *ai.SimpleService
}

// IsEnabled 检查 AI 服务是否已启用。
func (a *aiServiceAdapter) IsEnabled() bool {
	return a.svc.IsEnabled()
}

// ChatWithSystem 使用系统提示词进行聊天。
func (a *aiServiceAdapter) ChatWithSystem(ctx context.Context, userID, systemPrompt, message string) (string, error) {
	return a.svc.ChatWithSystem(ctx, userID, systemPrompt, message)
}

// Start 启动适配器。
//
// 启动 QQ 客户端和 WebSocket 连接。
//
// 参数:
//   - ctx: 上下文，用于控制生命周期
//
// 返回值:
//   - error: 启动过程中发生的错误
func (a *Adapter) Start(ctx context.Context) error {
	a.startMu.Lock()
	defer a.startMu.Unlock()

	if a.started {
		return fmt.Errorf("适配器已经启动")
	}

	// 启动 QQ 客户端
	if err := a.client.Start(ctx); err != nil {
		return fmt.Errorf("启动QQ客户端失败: %w", err)
	}

	// 获取 WebSocket 连接信息
	wsInfo, err := a.client.GetAPI().WS(ctx, nil, "")
	if err != nil {
		return fmt.Errorf("获取WebSocket连接信息失败: %w", err)
	}

	// 创建会话管理器
	// 注意: botgo SessionManager 没有 Stop 方法，session.Start() 会阻塞直到出错
	// 因此我们使用 stopChan 来实现非阻塞关闭

	// 注册事件处理器
	intent := dto.Intent(0)
	intent |= event.RegisterHandlers(
		event.ReadyHandler(a.handler.HandleReady),
		event.C2CMessageEventHandler(a.handler.HandleC2CMessage),
		event.GroupATMessageEventHandler(a.handler.HandleGroupATMessage),
	)

	// 初始化 stopChan
	a.stopChan = make(chan struct{})

	// 启动 WebSocket 连接
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		slog.Info("QQ WebSocket连接启动", "intent", intent)

		// 创建 session（局部变量，因为 SDK 不提供 Stop 方法）
		session := botgo.NewSessionManager()

		// 在另一个 goroutine 中运行 session.Start
		// 这样我们可以通过 stopChan 控制退出
		sessionDone := make(chan error, 1)
		go func() {
			sessionDone <- session.Start(wsInfo, a.client.GetTokenSource(), &intent)
		}()

		// 等待 session 完成或收到停止信号
		select {
		case err := <-sessionDone:
			if err != nil {
				slog.Error("WebSocket会话错误", "error", err)
			}
		case <-a.stopChan:
			slog.Info("收到停止信号，WebSocket连接将随进程退出")
			// session.Start() 没有停止方法，进程退出时会自然终止
		}
	}()

	a.started = true
	slog.Info("QQ适配器启动成功")
	return nil
}

// Stop 停止适配器。
//
// 优雅地停止 QQ 客户端和 WebSocket 连接。
// 该方法会等待所有 goroutine 完成。
func (a *Adapter) Stop() {
	a.startMu.Lock()
	defer a.startMu.Unlock()

	if !a.started {
		return
	}

	// 先通知 session goroutine 停止
	if a.stopChan != nil {
		close(a.stopChan)
	}

	// 停止 QQ 客户端（Token 刷新）
	if a.client != nil {
		a.client.Stop()
	}

	// 等待 session goroutine 完成（现在它会快速退出）
	a.wg.Wait()
	a.started = false
	slog.Info("QQ适配器已停止")
}

// IsEnabled 检查适配器是否已启用。
//
// 返回值:
//   - bool: 如果适配器已启用则返回 true
func (a *Adapter) IsEnabled() bool {
	return a.config.Enabled
}

// GetClient 获取 QQ 客户端。
//
// 返回值:
//   - *Client: QQ 客户端实例
func (a *Adapter) GetClient() *Client {
	return a.client
}

// GetConfig 获取 QQ 配置。
//
// 返回值:
//   - *config.QQConfig: QQ 配置实例
func (a *Adapter) GetConfig() *config.QQConfig {
	return a.config
}
