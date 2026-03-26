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
}

// NewAdapter 创建一个新的 QQ 适配器实例。
//
// 该函数初始化 QQ 客户端和事件处理器。
//
// 参数:
//   - cfg: QQ 配置，必须包含有效的 AppID 和 AppSecret
//   - aiCfg: AI 配置，复用全局配置
//   - aiService: AI 服务，用于生成回复
//
// 返回值:
//   - *Adapter: 创建的适配器实例
//   - error: 创建过程中发生的错误
//
// 错误情况：
//   - 配置为空
//   - AI 配置为空
//   - AI 服务为空
//   - 创建客户端失败
func NewAdapter(cfg *config.QQConfig, aiCfg *config.AIConfig, aiService *ai.SimpleService) (*Adapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("QQ配置不能为空")
	}
	if aiCfg == nil {
		return nil, fmt.Errorf("AI配置不能为空")
	}
	if aiService == nil {
		return nil, fmt.Errorf("AI服务不能为空")
	}

	client, err := NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("创建QQ客户端失败: %w", err)
	}

	// 创建包装处理器，适配 ai.SimpleService 到 SimpleAIService
	handler := NewDefaultHandler(client, &aiServiceAdapter{aiService}, aiCfg)

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
	session := botgo.NewSessionManager()

	// 注册事件处理器
	intent := dto.Intent(0)
	intent |= event.RegisterHandlers(
		event.ReadyHandler(a.handler.HandleReady),
		event.C2CMessageEventHandler(a.handler.HandleC2CMessage),
		event.GroupATMessageEventHandler(a.handler.HandleGroupATMessage),
	)

	// 启动 WebSocket 连接
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		slog.Info("QQ WebSocket连接启动", "intent", intent)

		// 启动 session - 参数为: wsInfo, tokenSource, intent
		if err := session.Start(wsInfo, a.client.GetTokenSource(), &intent); err != nil {
			slog.Error("WebSocket会话错误", "error", err)
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

	// 停止 QQ 客户端
	if a.client != nil {
		a.client.Stop()
	}

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
