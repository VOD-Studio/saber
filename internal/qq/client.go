// Package qq 提供 QQ 机器人的适配器实现。
//
// 该包封装了腾讯 botgo SDK，实现了与 Saber 机器人框架的集成。
// 支持 C2C 私聊消息和群 @ 消息的接收与处理。
//
// 主要组件：
//   - Client: botgo SDK 封装，处理 Token 管理和 API 调用
//   - Adapter: QQ 机器人适配器，管理 WebSocket 连接和事件分发
//
// 使用方式：
//
//	cfg := &config.QQConfig{...}
//	client, err := qq.NewClient(cfg)
//	if err != nil {
//	    log.Fatal(err)
//	}
package qq

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tencent-connect/botgo"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/token"
	"golang.org/x/oauth2"

	"rua.plus/saber/internal/config"
)

// SimpleAIService 定义简化版 AI 服务接口。
//
// 用于解耦 QQ 包与 ai 包的直接依赖，便于测试。
type SimpleAIService interface {
	// IsEnabled 检查 AI 服务是否已启用
	IsEnabled() bool
	// ChatWithSystem 使用系统提示词进行聊天
	ChatWithSystem(ctx context.Context, userID, systemPrompt, message string) (string, error)
}

// Client 封装 botgo SDK 客户端。
//
// 提供 Token 自动刷新和 API 调用功能。
//
// 字段说明：
//   - config: QQ 配置
//   - api: botgo API 实例
//   - tokenSource: Token 来源
//   - credentials: QQ 机器人凭证
//   - cancelFunc: 取消函数，用于停止 Token 刷新
//
// 线程安全：该结构体是并发安全的。
type Client struct {
	config      *config.QQConfig
	api         openapi.OpenAPI
	tokenSource oauth2.TokenSource
	credentials *token.QQBotCredentials
	cancelFunc  context.CancelFunc
}

// NewClient 创建一个新的 QQ 客户端实例。
//
// 该函数初始化 botgo SDK，配置 Token 管理。
//
// 参数:
//   - cfg: QQ 配置，必须包含有效的 AppID 和 AppSecret
//
// 返回值:
//   - *Client: 创建的客户端实例
//   - error: 创建过程中发生的错误
//
// 错误情况：
//   - 配置为空
//   - AppID 或 AppSecret 为空
func NewClient(cfg *config.QQConfig) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("QQ配置不能为空")
	}

	// 创建 QQ 机器人凭证
	credentials := &token.QQBotCredentials{
		AppID:     cfg.AppID,
		AppSecret: cfg.AppSecret,
	}

	slog.Info("QQ客户端初始化成功",
		"app_id", cfg.AppID,
		"sandbox", cfg.Sandbox)

	return &Client{
		config:      cfg,
		credentials: credentials,
	}, nil
}

// Start 启动客户端，开始 Token 自动刷新。
//
// 该方法启动一个后台 goroutine 定期刷新 Token，
// 确保 API 调用始终使用有效的访问令牌。
//
// 参数:
//   - ctx: 上下文，用于控制 Token 刷新 goroutine 的生命周期
//
// 返回值:
//   - error: 启动过程中发生的错误
func (c *Client) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	c.cancelFunc = cancel

	// 创建 TokenSource
	c.tokenSource = token.NewQQBotTokenSource(c.credentials)

	// 启动后台 Token 刷新
	go func() {
		if err := token.StartRefreshAccessToken(ctx, c.tokenSource); err != nil {
			slog.Error("Token 刷新失败", "error", err)
		}
	}()

	// 创建 API 实例
	if c.config.Sandbox {
		c.api = botgo.NewSandboxOpenAPI(c.config.AppID, c.tokenSource)
	} else {
		c.api = botgo.NewOpenAPI(c.config.AppID, c.tokenSource)
	}
	c.api = c.api.WithTimeout(time.Duration(c.config.TimeoutSeconds) * time.Second)

	slog.Info("QQ客户端启动成功",
		"app_id", c.config.AppID,
		"sandbox", c.config.Sandbox)

	return nil
}

// Stop 停止客户端。
//
// 该方法停止 Token 刷新 goroutine，释放资源。
// 应在程序退出时调用。
func (c *Client) Stop() {
	if c.cancelFunc != nil {
		c.cancelFunc()
		slog.Debug("QQ客户端已停止")
	}
}

// GetAPI 获取 botgo API 实例。
//
// 返回值:
//   - openapi.OpenAPI: API 实例
func (c *Client) GetAPI() openapi.OpenAPI {
	return c.api
}

// GetConfig 获取 QQ 配置。
//
// 返回值:
//   - *config.QQConfig: QQ 配置实例
func (c *Client) GetConfig() *config.QQConfig {
	return c.config
}

// GetTokenSource 获取 TokenSource。
//
// 返回值:
//   - oauth2.TokenSource: Token 来源
func (c *Client) GetTokenSource() oauth2.TokenSource {
	return c.tokenSource
}
