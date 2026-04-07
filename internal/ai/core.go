// Package ai 提供 AI 服务相关功能。
package ai

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/sashabaranov/go-openai"
	"golang.org/x/time/rate"

	"rua.plus/saber/internal/config"
)

// Core 封装 AI 客户端的核心逻辑，不依赖任何平台特定类型。
// 该结构体被 Service 和 SimpleService 共同使用，提供：
//   - AI 客户端缓存管理
//   - 模型注册表
//   - 速率限制
//   - 基础聊天完成功能
//
// 线程安全：所有方法都是并发安全的。
type Core struct {
	// globalConfig 是全局 AI 配置。
	globalConfig *config.AIConfig

	// clients 存储模型名称到客户端实例的映射，用于客户端复用。
	clients map[string]*Client

	// clientsMu 保护 clients 映射的并发访问。
	clientsMu sync.RWMutex

	// rateLimiter 是可选的速率限制器。
	// 当 globalConfig.RateLimitPerMinute > 0 时启用。
	rateLimiter *rate.Limiter

	// modelRegistry 管理模型注册和默认模型切换。
	modelRegistry *ModelRegistry
}

// NewCore 创建一个新的 Core 实例。
//
// 参数:
//   - cfg: AI 配置，用于初始化客户端管理器和模型注册表
//
// 返回值:
//   - *Core: 创建的 Core 实例
//   - error: 创建过程中发生的错误
func NewCore(cfg *config.AIConfig) (*Core, error) {
	if cfg == nil {
		return nil, fmt.Errorf("AI配置不能为空")
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("AI配置验证失败: %w", err)
	}

	// 创建速率限制器（可选）
	var rateLimiter *rate.Limiter
	if cfg.RateLimitPerMinute > 0 {
		// 速率转换为每秒，burst 大小设为每分钟限制的 1/6
		rateLimiter = rate.NewLimiter(
			rate.Limit(cfg.RateLimitPerMinute)/60,
			cfg.RateLimitPerMinute/6,
		)
	}

	return &Core{
		globalConfig:  cfg,
		clients:       make(map[string]*Client),
		rateLimiter:   rateLimiter,
		modelRegistry: NewModelRegistry(cfg),
	}, nil
}

// GetClient 获取指定模型的 AI 客户端（线程安全，带缓存）。
//
// 使用双重检查锁定模式，避免每次获取客户端都需要写锁。
// 如果客户端不存在，会自动创建并缓存。
//
// 参数:
//   - modelName: 模型名称
//
// 返回值:
//   - *Client: AI 客户端实例
//   - error: 获取或创建过程中发生的错误
func (c *Core) GetClient(modelName string) (*Client, error) {
	// 第一次检查（读锁）
	c.clientsMu.RLock()
	client, exists := c.clients[modelName]
	c.clientsMu.RUnlock()

	if exists {
		return client, nil
	}

	// 获取写锁进行创建
	c.clientsMu.Lock()
	defer c.clientsMu.Unlock()

	// 再次检查（可能其他 goroutine 已创建）
	if client, exists := c.clients[modelName]; exists {
		return client, nil
	}

	// 获取模型配置
	modelConfig, _ := c.globalConfig.GetModelConfig(modelName)
	cfg := &modelConfig

	// 创建新客户端
	newClient, err := NewClientWithModel(cfg)
	if err != nil {
		return nil, fmt.Errorf("创建AI客户端失败 (模型: %s): %w", modelName, err)
	}

	c.clients[modelName] = newClient
	slog.Debug("创建了新的AI客户端", "model", modelName)

	return newClient, nil
}

// IsEnabled 检查 AI 服务是否已启用。
//
// 返回值:
//   - bool: 如果 AI 服务已启用则返回 true
func (c *Core) IsEnabled() bool {
	return c.globalConfig.Enabled
}

// GetModelRegistry 获取模型注册表。
//
// 返回值:
//   - *ModelRegistry: 模型注册表实例
func (c *Core) GetModelRegistry() *ModelRegistry {
	return c.modelRegistry
}

// GetConfig 获取全局 AI 配置。
//
// 返回值:
//   - *config.AIConfig: 全局 AI 配置实例
func (c *Core) GetConfig() *config.AIConfig {
	return c.globalConfig
}

// WaitForRateLimit 等待速率限制器允许请求。
//
// 如果未配置速率限制器，则立即返回 nil。
//
// 参数:
//   - ctx: 上下文，用于取消等待
//
// 返回值:
//   - error: 等待过程中发生的错误（如上下文取消）
func (c *Core) WaitForRateLimit(ctx context.Context) error {
	if c.rateLimiter == nil {
		return nil
	}
	return c.rateLimiter.Wait(ctx)
}

// CreateChatCompletion 创建聊天完成（底层方法）。
//
// 这是一个基础方法，直接调用 AI 客户端创建聊天完成。
// 调用方需要自行处理速率限制和日志记录。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - modelName: 使用的模型名称
//   - messages: 聊天消息列表
//   - maxTokens: 最大生成 token 数
//   - temperature: 生成温度
//
// 返回值:
//   - *ChatCompletionResponse: 聊天完成响应
//   - error: 创建过程中发生的错误
func (c *Core) CreateChatCompletion(
	ctx context.Context,
	modelName string,
	messages []openai.ChatCompletionMessage,
	maxTokens int,
	temperature float64,
) (*ChatCompletionResponse, error) {
	client, err := c.GetClient(modelName)
	if err != nil {
		return nil, err
	}

	req := ChatCompletionRequest{
		Model:       modelName,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}

	return client.CreateChatCompletion(ctx, req)
}
