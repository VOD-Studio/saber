// Package ai 提供 AI 服务相关功能。
package ai

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/config"
)

// SimpleService 是简化版的 AI 服务，只支持基础聊天功能。
// 适用于不需要流式响应、工具调用或媒体处理的场景（如 QQ 机器人）。
//
// 功能限制：
//   - 不支持 streaming（QQ 不支持消息编辑）
//   - 不支持 tool_calling
//   - 不支持 media（图片）
//   - 不支持 proactive（主动消息）
//   - 第一阶段不支持上下文管理（可后续添加）
type SimpleService struct {
	core *Core // 共享核心逻辑
}

// NewSimpleService 创建一个新的简化版 AI 服务实例。
//
// 参数:
//   - cfg: AI 配置，用于初始化 Core
//
// 返回值:
//   - *SimpleService: 创建的 SimpleService 实例
//   - error: 创建过程中发生的错误
func NewSimpleService(cfg *config.AIConfig) (*SimpleService, error) {
	core, err := NewCore(cfg)
	if err != nil {
		return nil, err
	}

	slog.Info("简化版AI服务初始化完成",
		"enabled", cfg.Enabled,
		"default_model", cfg.DefaultModel)

	return &SimpleService{core: core}, nil
}

// IsEnabled 检查 AI 服务是否已启用。
//
// 返回值:
//   - bool: 如果 AI 服务已启用则返回 true
func (s *SimpleService) IsEnabled() bool {
	return s.core.IsEnabled()
}

// GetModelRegistry 获取模型注册表。
//
// 返回值:
//   - *ModelRegistry: 模型注册表实例
func (s *SimpleService) GetModelRegistry() *ModelRegistry {
	return s.core.GetModelRegistry()
}

// Chat 发送用户消息并获取 AI 响应。
//
// 这是最基础的聊天方法，使用默认模型和配置。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - userID: 用户标识符（用于日志，格式由调用方决定）
//   - message: 用户消息内容
//
// 返回值:
//   - string: AI 生成的响应内容
//   - error: 生成过程中发生的错误
func (s *SimpleService) Chat(ctx context.Context, userID, message string) (string, error) {
	return s.ChatWithSystem(ctx, userID, "", message)
}

// ChatWithSystem 发送用户消息并获取 AI 响应，支持自定义系统提示词。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - userID: 用户标识符（用于日志）
//   - systemPrompt: 系统提示词（为空则使用配置中的默认值）
//   - message: 用户消息内容
//
// 返回值:
//   - string: AI 生成的响应内容
//   - error: 生成过程中发生的错误
func (s *SimpleService) ChatWithSystem(ctx context.Context, userID, systemPrompt, message string) (string, error) {
	if !s.core.IsEnabled() {
		return "", fmt.Errorf("AI功能未启用")
	}

	// 速率限制
	if err := s.core.WaitForRateLimit(ctx); err != nil {
		return "", fmt.Errorf("AI请求速率限制: %w", err)
	}

	// 使用配置中的默认系统提示词
	if systemPrompt == "" {
		systemPrompt = s.core.GetConfig().SystemPrompt
	}

	modelName := s.core.GetModelRegistry().GetDefault()
	cfg := s.core.GetConfig()

	messages := []openai.ChatCompletionMessage{
		{Role: string(RoleSystem), Content: systemPrompt},
		{Role: string(RoleUser), Content: message},
	}

	slog.Debug("SimpleService: 发送AI请求",
		"user_id", userID,
		"model", modelName,
		"message_length", len(message))

	resp, err := s.core.CreateChatCompletion(
		ctx,
		modelName,
		messages,
		cfg.MaxTokens,
		cfg.Temperature,
	)
	if err != nil {
		return "", fmt.Errorf("AI请求失败: %w", err)
	}

	slog.Debug("SimpleService: AI响应成功",
		"user_id", userID,
		"model", resp.Model,
		"content_length", len(resp.Content))

	return resp.Content, nil
}

// ChatWithModel 使用指定模型发送消息并获取响应。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - userID: 用户标识符（用于日志）
//   - modelName: 模型名称（为空则使用默认模型）
//   - temperature: 生成温度（0 表示使用全局默认值）
//   - systemPrompt: 系统提示词（为空则使用配置中的默认值）
//   - message: 用户消息内容
//
// 返回值:
//   - string: AI 生成的响应内容
//   - error: 生成过程中发生的错误
func (s *SimpleService) ChatWithModel(ctx context.Context, userID, modelName string, temperature float64, systemPrompt, message string) (string, error) {
	if !s.core.IsEnabled() {
		return "", fmt.Errorf("AI功能未启用")
	}

	if err := s.core.WaitForRateLimit(ctx); err != nil {
		return "", fmt.Errorf("AI请求速率限制: %w", err)
	}

	if modelName == "" {
		modelName = s.core.GetModelRegistry().GetDefault()
	}

	cfg := s.core.GetConfig()

	if systemPrompt == "" {
		systemPrompt = cfg.SystemPrompt
	}

	if temperature == 0 {
		temperature = cfg.Temperature
	}

	messages := []openai.ChatCompletionMessage{
		{Role: string(RoleSystem), Content: systemPrompt},
		{Role: string(RoleUser), Content: message},
	}

	slog.Debug("SimpleService: 发送AI请求（指定模型）",
		"user_id", userID,
		"model", modelName,
		"temperature", temperature)

	resp, err := s.core.CreateChatCompletion(
		ctx,
		modelName,
		messages,
		cfg.MaxTokens,
		temperature,
	)
	if err != nil {
		return "", fmt.Errorf("AI请求失败: %w", err)
	}

	return resp.Content, nil
}
