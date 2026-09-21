// Package ai 保留应用装配和 Matrix 专用命令、人格及主动聊天的兼容入口。
// 模型核心位于 model，消息处理与历史位于 conversation，平台协议位于 matrix。
package ai

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/conversation"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/mcp"
	"rua.plus/saber/internal/model"
	"rua.plus/saber/internal/task"
)

// PromptProvider 定义提示词提供者接口。
// 用于获取房间的系统提示词（合并基础提示词和人格提示词）。
type PromptProvider interface {
	GetSystemPrompt(roomID id.RoomID, basePrompt string) string
}

// Service 装配模型、通用聊天处理器以及旧 Matrix 命令兼容入口。
type Service struct {
	config *config.Config
	// circuitBreaker 在任务之间保留失败状态。
	circuitBreaker *model.CircuitBreaker

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
	// chatProcessor 是所有聊天平台共享的消息处理链路。
	chatProcessor *conversation.Processor
	// respHandler 是响应处理器。
	respHandler *ResponseHandler
	// toolExecutor 是工具执行器。
	toolExecutor *ToolExecutor
	// tasks 在应用启动时绑定，接收聊天任务并负责后台生命周期。
	tasks *task.Manager
	// taskDir 是应用启动时的规范化工作目录，不执行全局 chdir。
	taskDir string
	// executor 同时管理本地容器工具和 MCP 工具权限。
	executor *execution.Executor
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
func NewService(appConfig *config.Config, matrixService *matrix.CommandService, mcpManager *mcp.Manager, mediaService *matrix.MediaService) (*Service, error) {
	if appConfig == nil {
		return nil, fmt.Errorf("AI配置不能为空")
	}

	cfg := &appConfig.AI
	if err := appConfig.Agent.Validate(); err != nil {
		return nil, fmt.Errorf("agent 配置验证失败: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("AI配置验证失败: %w", err)
	}

	var contextManager *ContextManager
	if appConfig.Agent.Context.Enabled {
		contextManager = NewContextManager(appConfig.Agent.Context)
	}

	// 创建 Core 实例
	core, err := NewCore(cfg)
	if err != nil {
		return nil, err
	}

	service := &Service{
		config:         appConfig,
		core:           core,
		matrixService:  matrixService,
		contextManager: contextManager,
		mcpManager:     mcpManager,
		mediaService:   mediaService,
		respHandler:    NewResponseHandler(nil), // 将在下面重新初始化
		toolExecutor:   NewToolExecutor(nil),    // 将在下面重新初始化
	}
	if c := appConfig.Agent.CircuitBreaker; c.Enabled {
		service.circuitBreaker = NewCircuitBreaker(c.FailureThreshold, time.Duration(c.ResetTimeout)*time.Second)
	}

	// 重新初始化处理器（需要 Service 实例）
	service.respHandler = NewResponseHandler(service)
	var history *conversation.ContextManager
	if contextManager != nil {
		if matrixService != nil {
			contextManager.account = string(matrixService.BotID())
		}
		history = contextManager.history
	}
	service.chatProcessor = &conversation.Processor{
		Run: service.RunAgent, History: history, Timeout: time.Duration(appConfig.Agent.TimeoutSeconds) * time.Second,
		Display: displayConfig(appConfig.Matrix.StreamEdit),
	}
	service.toolExecutor = NewToolExecutor(service)
	service.executor, err = execution.New(config.ExecutionConfig{}, nil, nil)
	if err != nil {
		return nil, err
	}
	if mcpManager != nil {
		mcpManager.SetAuthorizer(service.executor.CheckMCP)
	}

	slog.Info("AI服务初始化完成",
		"enabled", cfg.Enabled,
		"default_model", cfg.DefaultModel,
		"context_enabled", appConfig.Agent.Context.Enabled,
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
	if s.tasks != nil {
		if err := s.tasks.Close(); err != nil {
			slog.Error("关闭任务服务失败", "error", err)
		}
	}
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

	cfg, _ := s.core.GetConfig().GetModelConfig(modelName)

	messages := []openai.ChatCompletionMessage{
		{Role: string(RoleSystem), Content: systemPrompt},
		{Role: string(RoleUser), Content: userMessage},
	}

	req := ChatCompletionRequest{
		Model:       modelName,
		Messages:    messages,
		MaxTokens:   cfg.MaxTokens,
		Temperature: *cfg.Temperature,
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

	// 使用指定的模型或默认模型
	if modelName == "" {
		modelName = s.core.GetModelRegistry().GetDefault()
	}

	cfg, _ := s.core.GetConfig().GetModelConfig(modelName)
	// 使用指定的温度或模型默认值
	if temperature == 0 && cfg.Temperature != nil {
		temperature = *cfg.Temperature
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

	if modelName == "" {
		modelName = s.core.GetModelRegistry().GetDefault()
	}

	cfg, _ := s.core.GetConfig().GetModelConfig(modelName)
	if temperature == 0 && cfg.Temperature != nil {
		temperature = *cfg.Temperature
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

// handleAICommand 保留 Matrix 命令入口，消息规范化和媒体下载由 Matrix adapter 完成。
func (s *Service) handleAICommand(ctx context.Context, userID id.UserID, roomID id.RoomID, modelName string, args []string) error {
	adapter := matrix.NewChatAdapter(s.matrixService, s.mediaService, s.config.Matrix.Media, s.config.Matrix.StreamEdit.Enabled, func(ctx context.Context, message chat.Message, reply chat.Adapter) (agent.Result, error) {
		return s.handleChat(ctx, message, reply, modelName)
	})
	return adapter.Handle(ctx, userID, roomID, args)
}

// HandleChat 是内存或其他聊天 adapter 可复用的统一消息入口。
func (s *Service) HandleChat(ctx context.Context, message chat.Message, reply chat.Adapter) (agent.Result, error) {
	return s.handleChat(ctx, message, reply, s.GetModelRegistry().GetDefault())
}

func (s *Service) handleChat(ctx context.Context, message chat.Message, reply chat.Adapter, modelName string) (agent.Result, error) {
	if !s.IsEnabled() {
		return agent.Result{}, fmt.Errorf("AI功能未启用")
	}
	if err := message.Validate(); err != nil {
		return agent.Result{}, err
	}
	if s.tasks != nil {
		text := message.Text
		if body, ok := matrix.GetReplyBody(ctx); ok {
			text = body
		}
		if action, taskID, ok := naturalTaskCommand(text); ok {
			return agent.Result{}, s.replyTaskCommand(ctx, message, reply, action, taskID)
		}
	} else {
		if err := s.core.WaitForRateLimit(ctx); err != nil {
			return agent.Result{}, fmt.Errorf("AI请求速率限制: %w", err)
		}
	}
	if len(message.Attachments) > 0 && s.config.Matrix.Media.Model != "" {
		modelName = s.config.Matrix.Media.Model
	}
	req, err := s.taskRequest(message, modelName)
	if err != nil {
		return agent.Result{}, err
	}
	identity := chat.Identity{Session: message.Session, SenderID: message.SenderID}
	toolCtx := chat.WithIdentity(ctx, identity)
	if s.executor != nil {
		if dir, err := s.executor.Workspace(identity); err == nil {
			toolCtx = execution.WithTask(toolCtx, 0, dir)
		}
	}
	req.Tools, _ = s.toolExecutor.PrepareTools(toolCtx)
	if s.tasks != nil {
		return agent.Result{}, s.submitTask(ctx, message, req, reply)
	}
	return s.chatProcessor.Handle(ctx, message, req, reply)
}

func (s *Service) taskRequest(message chat.Message, modelName string) (agent.Request, error) {
	cfg := s.core.GetConfig()
	prompt := cfg.SystemPrompt
	// 计划和即时任务使用相同的人格与模型配置，不复制群聊历史。
	if message.Session.Platform == "matrix" && s.promptProvider != nil {
		prompt = s.promptProvider.GetSystemPrompt(id.RoomID(message.Session.Conversation), prompt)
	}
	modelCfg, _ := cfg.GetModelConfig(modelName)
	if modelCfg.BaseURL == "" || modelCfg.Temperature == nil {
		return agent.Request{}, fmt.Errorf("模型 %q 缺少有效提供商配置", modelName)
	}
	req := agent.Request{Model: modelName, Stream: s.config.Agent.StreamEnabled, MaxTokens: modelCfg.MaxTokens, Temperature: *modelCfg.Temperature}
	if prompt != "" {
		req.Messages = []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: prompt}}
	}
	return req, nil
}

func displayConfig(cfg config.StreamEditConfig) chat.Display {
	return chat.Display{
		CharThreshold: cfg.CharThreshold, TimeThreshold: time.Duration(cfg.TimeThresholdMs) * time.Millisecond,
		EditInterval: time.Duration(cfg.EditIntervalMs) * time.Millisecond, MaxEdits: cfg.MaxEdits,
	}
}
