// Package ai 保留应用装配和 Matrix 专用命令、人格及主动聊天的兼容入口。
// 模型核心位于 model，消息处理与历史位于 conversation，平台协议位于 matrix。
package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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
// 用于获取会话的系统提示词（合并基础提示词和人格提示词）。
// 实现按 chat.Session.Platform 决定是否提供平台专属人格；非目标平台应原样返回 basePrompt。
type PromptProvider interface {
	GetSystemPrompt(session chat.Session, basePrompt string) string
}

// ChatEntrypoint 是平台接入端注入的聊天命令入口。
// 实现负责把该平台的原生命令规范化为 chat.Message，并携带 adapter 交给 Service 处理，
// 因此 ai 核心无需知道具体平台的 adapter 构造方式。
type ChatEntrypoint interface {
	// HandleCommand 处理平台聊天命令，modelName 为空时使用默认模型。
	HandleCommand(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string, modelName string) error
	// NormalizeCommand 把平台命令文本（含 "!task list" 这样的前缀）规范化为通用消息，
	// 并返回面向该会话的出站 adapter，供只读命令复用同一套会话作用域与回复能力。
	NormalizeCommand(ctx context.Context, userID id.UserID, roomID id.RoomID, text string) (chat.Message, chat.Adapter, error)
	// Session 解析一次入站事件所在的会话作用域（账号与线程隔离），
	// 使上下文类命令无需自行拼平台的会话标识。
	Session(ctx context.Context, roomID id.RoomID) chat.Session
	// OutboundAdapter 返回该平台具备流式编辑与媒体解析的出站 adapter，用于交付 Agent 结果。
	OutboundAdapter() chat.Adapter
	// EventID 返回触发当前处理的原生事件标识；没有入站事件时返回空串。
	EventID(ctx context.Context) string
}

// ErrNoChatEntrypoint 表示当前没有平台接入端接管聊天命令。
var ErrNoChatEntrypoint = errors.New("未接入聊天平台，无法处理聊天命令")

// Service 装配模型、通用聊天处理器以及旧 Matrix 命令兼容入口。
type Service struct {
	config *config.Config
	// circuitBreaker 在任务之间保留失败状态。
	circuitBreaker *model.CircuitBreaker

	// core 是共享核心逻辑。
	core *Core
	// matrixService 保留 Matrix 专属兼容入口：注册 !task/!schedule 命令、取 BotID 作为
	// 旧历史键的账号，以及上传任务文件；普通文本回执已改经平台端口发送。
	matrixService *matrix.CommandService
	// contextManager 是对话上下文管理器。
	contextManager *ContextManager
	// mcpManager 是 MCP 管理器。
	mcpManager *mcp.Manager
	// mediaService 是媒体服务。
	mediaService *matrix.MediaService
	// promptProvider 是提示词提供者（可选字段）。
	promptProvider PromptProvider
	// entry 是平台注入的聊天命令入口，负责把平台原生命令规范化为 chat.Message。
	entry ChatEntrypoint
	// chatProcessor 是所有聊天平台共享的消息处理链路。
	chatProcessor *conversation.Processor
	// toolExecutor 是工具执行器。
	toolExecutor *ToolExecutor
	// tasks 在应用启动时绑定，接收聊天任务并负责后台生命周期。
	tasks *task.Manager
	// taskDir 是应用启动时的规范化工作目录，不执行全局 chdir。
	taskDir string
	// taskStreams 按平台保存可编辑的后台任务临时回复设置。
	taskStreams sync.Map
	// executor 同时管理本地容器工具和 MCP 工具权限。
	executor *execution.Executor
}

// ServiceOption 配置 AI 服务的可选依赖。平台专属服务由接入端注入，
// 核心装配本身不要求任何聊天平台存在。
type ServiceOption func(*serviceOptions)

// serviceOptions 汇集 NewService 的可选依赖。
type serviceOptions struct {
	matrixService *matrix.CommandService
	mediaService  *matrix.MediaService
	mcpManager    *mcp.Manager
}

// WithMatrix 注入 Matrix 命令与媒体服务，是 Matrix 专属的兼容入口，
// 仅用于命令注册、旧历史账号与任务文件上传；不再承担普通消息发送。
// 两者均可传 nil。
func WithMatrix(matrixService *matrix.CommandService, mediaService *matrix.MediaService) ServiceOption {
	return func(o *serviceOptions) {
		o.matrixService = matrixService
		o.mediaService = mediaService
	}
}

// WithMCP 注入 MCP 管理器。
func WithMCP(mcpManager *mcp.Manager) ServiceOption {
	return func(o *serviceOptions) { o.mcpManager = mcpManager }
}

// NewService 创建一个新的 AI 服务实例。
//
// 参数:
//   - appConfig: 应用配置
//   - opts: 可选依赖，平台服务与 MCP 管理器由调用方按需注入
//
// 返回值:
//   - *Service: 创建的 AI 服务实例
//   - error: 创建过程中发生的错误
func NewService(appConfig *config.Config, opts ...ServiceOption) (*Service, error) {
	if appConfig == nil {
		return nil, fmt.Errorf("AI配置不能为空")
	}

	var o serviceOptions
	for _, opt := range opts {
		opt(&o)
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
		matrixService:  o.matrixService,
		contextManager: contextManager,
		mcpManager:     o.mcpManager,
		mediaService:   o.mediaService,
		toolExecutor:   NewToolExecutor(nil), // 将在下面重新初始化
	}
	if c := appConfig.Agent.CircuitBreaker; c.Enabled {
		service.circuitBreaker = NewCircuitBreaker(c.FailureThreshold, time.Duration(c.ResetTimeout)*time.Second)
	}

	// 历史作用域账号取自平台注入的命令服务，未接入 Matrix 时留空
	var history *conversation.ContextManager
	if contextManager != nil {
		if o.matrixService != nil {
			contextManager.account = string(o.matrixService.BotID())
		}
		history = contextManager.history
	}
	service.chatProcessor = &conversation.Processor{
		Run: service.RunAgent, History: history, Timeout: time.Duration(appConfig.Agent.TimeoutSeconds) * time.Second,
		DisplayFor: func(session chat.Session) chat.Display {
			if session.Platform == "matrix" {
				return displayConfig(appConfig.Matrix.StreamEdit)
			}
			return chat.Display{}
		},
	}
	service.toolExecutor = NewToolExecutor(service)
	service.executor, err = execution.New(config.ExecutionConfig{}, nil, nil)
	if err != nil {
		return nil, err
	}
	if o.mcpManager != nil {
		o.mcpManager.SetAuthorizer(service.executor.CheckMCP)
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

// SetChatEntrypoint 注入平台聊天命令入口。未注入时平台命令会被拒绝，
// 但聊天核心仍可经 HandleChat 由其他 adapter 直接调用。
func (s *Service) SetChatEntrypoint(entry ChatEntrypoint) {
	s.entry = entry
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

// handleAICommand 把平台聊天命令交给注入的入口处理，ai 自身不再构造平台 adapter。
func (s *Service) handleAICommand(ctx context.Context, userID id.UserID, roomID id.RoomID, modelName string, args []string) error {
	if s.entry == nil {
		return ErrNoChatEntrypoint
	}
	return s.entry.HandleCommand(ctx, userID, roomID, args, modelName)
}

// NormalizeCommand 由注入的平台入口把命令文本规范化为通用消息与出站 adapter。
func (s *Service) NormalizeCommand(ctx context.Context, userID id.UserID, roomID id.RoomID, text string) (chat.Message, chat.Adapter, error) {
	if s.entry == nil {
		return chat.Message{}, nil, ErrNoChatEntrypoint
	}
	return s.entry.NormalizeCommand(ctx, userID, roomID, text)
}

// eventID 返回触发当前处理的原生事件标识，未接入平台时为空。
func (s *Service) eventID(ctx context.Context) string {
	if s.entry == nil {
		return ""
	}
	return s.entry.EventID(ctx)
}

// Session 由注入的平台入口解析会话作用域，未接入平台时返回 ErrNoChatEntrypoint。
func (s *Service) Session(ctx context.Context, roomID id.RoomID) (chat.Session, error) {
	if s.entry == nil {
		return chat.Session{}, ErrNoChatEntrypoint
	}
	return s.entry.Session(ctx, roomID), nil
}

// replyCommand 经注入的平台入口把一次性回执发回命令所在会话。
// text 使用 Markdown 书写，具体渲染（如 Matrix 的 HTML 富文本）由平台 adapter 完成；
// 未接入平台时返回 ErrNoChatEntrypoint。
func (s *Service) replyCommand(ctx context.Context, userID id.UserID, roomID id.RoomID, command, text string) error {
	message, adapter, err := s.NormalizeCommand(ctx, userID, roomID, command)
	if err != nil {
		return err
	}
	_, err = adapter.Send(ctx, chat.Reply{Session: message.Session, ReplyTo: message.ID, Text: text})
	return err
}

// HandleChat 是内存或其他聊天 adapter 可复用的统一消息入口。
func (s *Service) HandleChat(ctx context.Context, message chat.Message, reply chat.Adapter) (agent.Result, error) {
	return s.handleChat(ctx, message, reply, s.GetModelRegistry().GetDefault())
}

// HandleChatModel 与 HandleChat 相同，但由平台指定本轮使用的模型。
// 供接入端在解析平台专属命令（例如 !ai-gpt-4）后复用同一条聊天链路。
func (s *Service) HandleChatModel(ctx context.Context, message chat.Message, reply chat.Adapter, modelName string) (agent.Result, error) {
	return s.handleChat(ctx, message, reply, modelName)
}

func (s *Service) handleChat(ctx context.Context, message chat.Message, reply chat.Adapter, modelName string) (agent.Result, error) {
	if !s.IsEnabled() {
		return agent.Result{}, fmt.Errorf("AI功能未启用")
	}
	if err := message.Validate(); err != nil {
		return agent.Result{}, err
	}
	if s.tasks != nil {
		if action, taskID, ok := naturalTaskCommand(message.CommandText()); ok {
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
	// promptProvider 按平台自行决定是否注入人格，非目标平台原样返回 basePrompt。
	if s.promptProvider != nil {
		prompt = s.promptProvider.GetSystemPrompt(message.Session, prompt)
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
