package ai

import (
	"time"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/model"
)

// Core 是独立模型核心的兼容名称。
type Core = model.Core

// Client 是独立模型客户端的兼容名称。
type Client = model.Client

// ChatCompletionRequest 保留旧命令层的模型请求名称。
type ChatCompletionRequest = model.ChatCompletionRequest

// ChatCompletionResponse 保留旧命令层的模型响应名称。
type ChatCompletionResponse = model.ChatCompletionResponse

// ModelRegistry 是模型注册表的兼容名称。
type ModelRegistry = model.ModelRegistry

// ModelInfo 是模型描述的兼容名称。
type ModelInfo = model.ModelInfo

// RetryConfigWrapper 是模型重试配置的兼容名称。
type RetryConfigWrapper = model.RetryConfigWrapper

// NewCore 创建不依赖聊天平台的模型核心。
func NewCore(cfg *config.AIConfig) (*Core, error) { return model.NewCore(cfg) }

// NewClientWithModel 创建模型客户端。
func NewClientWithModel(cfg *config.ModelConfig) (*Client, error) {
	return model.NewClientWithModel(cfg)
}

// NewModelRegistry 创建模型注册表。
func NewModelRegistry(cfg *config.AIConfig) *ModelRegistry { return model.NewModelRegistry(cfg) }

// NewCircuitBreaker 创建模型请求熔断器。
func NewCircuitBreaker(threshold int, timeout time.Duration) *model.CircuitBreaker {
	return model.NewCircuitBreaker(threshold, timeout)
}
