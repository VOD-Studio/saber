// Package ai 提供 AI 服务相关功能，包括对话管理、流式响应和工具调用。
//
// 该包封装了 OpenAI 兼容的 API 客户端，支持：
//   - 流式和非流式对话
//   - 上下文管理（每个房间独立的持久化对话历史）
//   - MCP 工具调用（让 AI 执行实际操作）
//   - 主动聊天功能（AI 驱动的主动消息）
//
// 主要组件：
//   - Service: AI 服务编排器，协调所有 AI 相关操作
//   - Client: OpenAI 兼容的 API 客户端
//   - ContextManager: 对话上下文管理器
//   - StreamHandler: 流式响应处理器
//   - ProactiveManager: 主动聊天管理器
//   - MessageBuilder: 消息构建器
//   - ResponseHandler: 响应处理器
//   - ToolExecutor: 工具执行器
package ai

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	appcontext "rua.plus/saber/internal/context"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/mcp"
)

// PromptProvider 定义提示词提供者接口。
// 用于获取房间的系统提示词（合并基础提示词和人格提示词）。
type PromptProvider interface {
	GetSystemPrompt(roomID id.RoomID, basePrompt string) string
}

// Service 是 AI 服务的核心结构体。
type Service struct {
	// core 是共享核心逻辑。
	core *Core
	// matrixService 是 Matrix 命令服务，用于发送消息。
	matrixService *matrix.CommandService
	// contextManager 是对话上下文管理器。
	contextManager *ContextManager
	// mcpManager 是 MCP 管理器。
	mcpManager *mcp.Manager
	// mediaService 是媒体服务。
	mediaService *matrix.MediaService
	// promptProvider 是提示词提供者（可选字段）。
	promptProvider PromptProvider
	// msgBuilder 是消息构建器。
	msgBuilder *MessageBuilder
	// respHandler 是响应处理器。
	respHandler *ResponseHandler
	// toolExecutor 是工具执行器。
	toolExecutor *ToolExecutor
}

// NewService 创建一个新的 AI 服务实例。
//
// 参数:
//   - cfg: AI 配置
//   - matrixService: Matrix 命令服务
//   - mcpManager: MCP 管理器
//   - mediaService: 媒体服务
//
// 返回值:
//   - *Service: 创建的 AI 服务实例
//   - error: 创建过程中发生的错误
func NewService(cfg *config.AIConfig, matrixService *matrix.CommandService, mcpManager *mcp.Manager, mediaService *matrix.MediaService) (*Service, error) {
	if cfg == nil {
		return nil, fmt.Errorf("AI配置不能为空")
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("AI配置验证失败: %w", err)
	}

	var contextManager *ContextManager
	if cfg.Context.Enabled {
		contextManager = NewContextManager(cfg.Context)
	}

	// 创建 Core 实例
	core, err := NewCore(cfg)
	if err != nil {
		return nil, err
	}

	service := &Service{
		core:           core,
		matrixService:  matrixService,
		contextManager: contextManager,
		mcpManager:     mcpManager,
		mediaService:   mediaService,
		msgBuilder:     NewMessageBuilder(),
		respHandler:    NewResponseHandler(nil), // 将在下面重新初始化
		toolExecutor:   NewToolExecutor(nil),    // 将在下面重新初始化
	}

	// 重新初始化处理器（需要 Service 实例）
	service.respHandler = NewResponseHandler(service)
	service.toolExecutor = NewToolExecutor(service)

	slog.Info("AI服务初始化完成",
		"enabled", cfg.Enabled,
		"provider", cfg.Provider,
		"default_model", cfg.DefaultModel,
		"context_enabled", cfg.Context.Enabled,
		"rate_limit_per_minute", cfg.RateLimitPerMinute)

	return service, nil
}

// getClient 获取指定模型的 AI 客户端，使用缓存机制。
//
// 参数:
//   - modelName: 模型名称
//
// 返回值:
//   - *Client: AI 客户端实例
//   - error: 获取过程中发生的错误
func (s *Service) getClient(modelName string) (*Client, error) {
	return s.core.GetClient(modelName)
}

// IsEnabled 检查 AI 服务是否已启用。
//
// 返回值:
//   - bool: 如果 AI 服务已启用则返回 true
func (s *Service) IsEnabled() bool {
	return s.core.IsEnabled()
}

// SetPromptProvider 设置提示词提供者。
// 提示词提供者用于获取房间的系统提示词（合并基础提示词和人格提示词）。
func (s *Service) SetPromptProvider(pp PromptProvider) {
	s.promptProvider = pp
}

// GetModelRegistry 获取模型注册表。
//
// 返回值:
//   - *ModelRegistry: 模型注册表实例
func (s *Service) GetModelRegistry() *ModelRegistry {
	return s.core.GetModelRegistry()
}

// Stop 停止 AI 服务的所有后台任务。
//
// 必须在服务不再使用时调用，否则会导致 goroutine 泄漏。
// 它会停止上下文管理器的后台清理 goroutine。
func (s *Service) Stop() {
	if s.contextManager != nil {
		s.contextManager.Stop()
		slog.Debug("AI 服务上下文管理器已停止")
	}
}

// GenerateSimpleResponse 使用 AI 生成简单的响应。
//
// 该方法用于生成简单的 AI 响应，不涉及上下文管理或消息发送。
// 适用于主动聊天等需要 AI 生成内容但不需要完整命令流程的场景。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - systemPrompt: 系统提示词
//   - userMessage: 用户消息
//
// 返回值:
//   - string: AI 生成的响应内容
//   - error: 生成过程中发生的错误
func (s *Service) GenerateSimpleResponse(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	if !s.IsEnabled() {
		return "", fmt.Errorf("AI功能未启用")
	}

	if err := s.core.WaitForRateLimit(ctx); err != nil {
		return "", fmt.Errorf("AI请求速率限制: %w", err)
	}

	modelName := s.core.GetModelRegistry().GetDefault()
	client, err := s.getClient(modelName)
	if err != nil {
		return "", fmt.Errorf("获取AI客户端失败: %w", err)
	}

	cfg := s.core.GetConfig()

	messages := []openai.ChatCompletionMessage{
		{Role: string(RoleSystem), Content: systemPrompt},
		{Role: string(RoleUser), Content: userMessage},
	}

	req := ChatCompletionRequest{
		Model:       modelName,
		Messages:    messages,
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
	}

	slog.Debug("发送简单AI请求", "model", modelName, "system_prompt", systemPrompt, "user_message", userMessage)

	resp, err := client.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("AI请求失败: %w", err)
	}

	slog.Debug("简单AI响应成功", "model", modelName, "content_length", len(resp.Content))

	return resp.Content, nil
}

// GenerateSimpleResponseWithModel 使用指定模型生成响应。
//
// 该方法允许指定模型名称和温度参数，适用于需要使用特定模型配置的场景。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - modelName: 要使用的模型名称（为空则使用默认模型）
//   - temperature: 生成温度（0 表示使用全局默认值）
//   - systemPrompt: 系统提示词
//   - userMessage: 用户消息
//
// 返回值:
//   - string: AI 生成的响应内容
//   - error: 生成过程中发生的错误
func (s *Service) GenerateSimpleResponseWithModel(ctx context.Context, modelName string, temperature float64, systemPrompt, userMessage string) (string, error) {
	if !s.IsEnabled() {
		return "", fmt.Errorf("AI功能未启用")
	}

	if err := s.core.WaitForRateLimit(ctx); err != nil {
		return "", fmt.Errorf("AI请求速率限制: %w", err)
	}

	cfg := s.core.GetConfig()

	// 使用指定的模型或默认模型
	if modelName == "" {
		modelName = s.core.GetModelRegistry().GetDefault()
	}

	// 使用指定的温度或全局默认值
	if temperature == 0 {
		temperature = cfg.Temperature
	}

	client, err := s.getClient(modelName)
	if err != nil {
		return "", fmt.Errorf("获取AI客户端失败: %w", err)
	}

	messages := []openai.ChatCompletionMessage{
		{Role: string(RoleSystem), Content: systemPrompt},
		{Role: string(RoleUser), Content: userMessage},
	}

	req := ChatCompletionRequest{
		Model:       modelName,
		Messages:    messages,
		MaxTokens:   cfg.MaxTokens,
		Temperature: temperature,
	}

	slog.Debug("发送简单AI请求（指定模型）", "model", modelName, "temperature", temperature, "system_prompt", systemPrompt, "user_message", userMessage)

	resp, err := client.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("AI请求失败: %w", err)
	}

	slog.Debug("简单AI响应成功（指定模型）", "model", modelName, "content_length", len(resp.Content))

	return resp.Content, nil
}

// GenerateStreamingSimpleResponse 使用流式请求生成响应。
//
// 该方法使用流式请求方式获取 AI 响应，但内部收集所有内容后返回完整结果。
// 适用于需要更快响应反馈的场景（如主动消息决策）。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - modelName: 要使用的模型名称（为空则使用默认模型）
//   - temperature: 生成温度（0 表示使用全局默认值）
//   - systemPrompt: 系统提示词
//   - userMessage: 用户消息
//
// 返回值:
//   - string: AI 生成的响应内容
//   - error: 生成过程中发生的错误
func (s *Service) GenerateStreamingSimpleResponse(ctx context.Context, modelName string, temperature float64, systemPrompt, userMessage string) (string, error) {
	if !s.IsEnabled() {
		return "", fmt.Errorf("AI功能未启用")
	}

	if err := s.core.WaitForRateLimit(ctx); err != nil {
		return "", fmt.Errorf("AI请求速率限制: %w", err)
	}

	cfg := s.core.GetConfig()

	if modelName == "" {
		modelName = s.core.GetModelRegistry().GetDefault()
	}

	if temperature == 0 {
		temperature = cfg.Temperature
	}

	client, err := s.getClient(modelName)
	if err != nil {
		return "", fmt.Errorf("获取AI客户端失败: %w", err)
	}

	messages := []openai.ChatCompletionMessage{
		{Role: string(RoleSystem), Content: systemPrompt},
		{Role: string(RoleUser), Content: userMessage},
	}

	req := ChatCompletionRequest{
		Model:       modelName,
		Messages:    messages,
		Stream:      true,
		MaxTokens:   cfg.MaxTokens,
		Temperature: temperature,
	}

	slog.Debug("发送流式简单AI请求", "model", modelName, "temperature", temperature)

	resp, err := client.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("AI请求失败: %w", err)
	}

	slog.Debug("流式简单AI响应成功", "model", modelName, "content_length", len(resp.Content))

	return resp.Content, nil
}

// handleAICommand 处理 AI 命令的核心逻辑。
func (s *Service) handleAICommand(ctx context.Context, userID id.UserID, roomID id.RoomID, modelName string, args []string) error {
	ctx = appcontext.WithUserContext(ctx, userID, roomID)

	if !s.IsEnabled() {
		return fmt.Errorf("AI功能未启用")
	}

	if err := s.core.WaitForRateLimit(ctx); err != nil {
		return fmt.Errorf("AI请求速率限制: %w", err)
	}

	userInput := strings.Join(args, " ")
	if userInput == "" {
		return fmt.Errorf("请输入要发送给AI的消息")
	}

	if s.contextManager != nil {
		s.contextManager.AddMessage(roomID, RoleUser, userInput, userID)
	}

	// 检查是否有媒体信息（当前消息的图片）
	mediaInfo := matrix.GetMediaInfo(ctx)
	// 检查是否有引用消息的媒体信息（引用消息中的图片）
	referencedMediaInfo := matrix.GetReferencedMediaInfo(ctx)

	var messages []openai.ChatCompletionMessage
	hasImage := false

	// 收集所有需要下载的图片
	var imageDataList []string
	imageCount := 0

	cfg := s.core.GetConfig()

	// 处理当前消息的图片
	if mediaInfo != nil && mediaInfo.Type == "image" && s.mediaService != nil && cfg.Media.Enabled {
		imageData, err := s.mediaService.DownloadImage(ctx, mediaInfo)
		if err != nil {
			slog.Warn("下载当前消息图片失败", "error", err)
		} else {
			imageDataList = append(imageDataList, imageData)
			imageCount++
			slog.Debug("下载当前消息图片成功", "image_count", imageCount)
		}
	}

	// 处理引用消息的图片
	if referencedMediaInfo != nil && referencedMediaInfo.Type == "image" && s.mediaService != nil && cfg.Media.Enabled {
		imageData, err := s.mediaService.DownloadImage(ctx, referencedMediaInfo)
		if err != nil {
			slog.Warn("下载引用消息图片失败", "error", err)
		} else {
			imageDataList = append(imageDataList, imageData)
			imageCount++
			slog.Debug("下载引用消息图片成功", "image_count", imageCount)
		}
	}

	// 根据图片数量构建消息
	if imageCount > 0 {
		hasImage = true
		if imageCount == 1 {
			// 单图片情况
			messages = s.msgBuilder.BuildMultimodalMessages(s, ctx, roomID, userInput, imageDataList[0])
		} else {
			// 多图片情况
			messages = s.msgBuilder.BuildMultiImageMessages(s, ctx, roomID, userInput, imageDataList)
		}
		slog.Debug("构建多模态消息", "has_image", true, "image_count", imageCount)
	} else {
		messages = s.msgBuilder.BuildTextMessages(s, roomID, userInput)
	}

	// 如果有图片且配置了专用模型，使用专用模型
	actualModel := modelName
	if hasImage && cfg.Media.Model != "" {
		actualModel = cfg.Media.Model
		slog.Debug("使用图片识别专用模型", "media_model", actualModel, "default_model", modelName)
	}

	req := ChatCompletionRequest{
		Messages:    messages,
		Stream:      cfg.StreamEnabled,
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
		Model:       actualModel,
	}

	slog.Debug("AI请求准备完成",
		"model", actualModel,
		"messages_count", len(messages),
		"stream", cfg.StreamEnabled,
		"max_tokens", cfg.MaxTokens,
		"temperature", cfg.Temperature)

	retryConfig := &RetryConfigWrapper{
		MaxRetries:     cfg.Retry.MaxRetries,
		InitialDelay:   time.Duration(cfg.Retry.InitialDelayMs) * time.Millisecond,
		MaxDelay:       time.Duration(cfg.Retry.MaxDelayMs) * time.Millisecond,
		BackoffFactor:  cfg.Retry.BackoffFactor,
		FallbackModels: cfg.Retry.FallbackModels,
	}

	// 如果启用了熔断器，创建熔断器实例
	if cfg.CircuitBreaker.Enabled {
		retryConfig.CircuitBreaker = NewCircuitBreaker(
			cfg.CircuitBreaker.FailureThreshold,
			time.Duration(cfg.CircuitBreaker.ResetTimeout)*time.Second,
		)
		slog.Debug("熔断器已启用",
			"failure_threshold", cfg.CircuitBreaker.FailureThreshold,
			"reset_timeout", cfg.CircuitBreaker.ResetTimeout)
	}

	fallbackHandler := &FallbackModelHandler{
		MainModel:   actualModel,
		RetryConfig: retryConfig,
	}

	_, err := fallbackHandler.TryWithFallback(ctx, func(model string) (any, error) {
		client, clientErr := s.getClient(model)
		if clientErr != nil {
			slog.Error("创建AI客户端失败", "model", model, "error", clientErr)
			return nil, clientErr
		}

		req.Model = model
		slog.Debug("发送AI请求", "model", model, "base_url", cfg.BaseURL)

		tools, useToolCalling := s.toolExecutor.PrepareTools()
		if useToolCalling {
			req.Tools = tools
		}

		respCtx := &ResponseContext{
			UserID:      userID,
			RoomID:      roomID,
			Messages:    messages,
			Model:       model,
			UseToolCall: useToolCalling,
			Tools:       tools,
		}

		mode := s.respHandler.DetermineResponseMode(cfg.StreamEnabled, cfg.StreamEdit.Enabled, useToolCalling)
		slog.Debug("响应模式", "mode", mode, "model", model)

		switch mode {
		case ResponseModeStreamingWithTools:
			return s.toolExecutor.ExecuteStreamingWithToolCalling(ctx, client, req, roomID, messages, tools, model)

		case ResponseModeStreaming:
			if err := s.respHandler.ExecuteStreamingResponse(ctx, client, req, respCtx); err != nil {
				return nil, err
			}
			return nil, nil

		case ResponseModeToolCalling:
			return s.respHandler.ExecuteDirectResponseWithTools(ctx, client, req, respCtx)

		default:
			return s.respHandler.ExecuteDirectResponse(ctx, client, req, respCtx)
		}
	})

	if err != nil {
		slog.Error("AI命令执行失败", "error", err)
	} else {
		slog.Debug("AI命令执行成功")
	}

	return err
}
