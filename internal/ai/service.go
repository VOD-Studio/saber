// Package ai 保留应用装配和 Matrix 专用命令、人格及主动聊天的兼容入口。
// 模型核心位于 model，消息处理与历史位于 conversation，平台协议位于 matrix。
package ai

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/conversation"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/mcp"
	"rua.plus/saber/internal/memory"
	"rua.plus/saber/internal/model"
	"rua.plus/saber/internal/task"
)

// PromptProvider 定义提示词提供者接口。
// 用于获取会话的系统提示词（合并基础提示词和人格提示词）。
// 实现按 chat.Session.Platform 决定是否提供平台专属人格；非目标平台应原样返回 basePrompt。
type PromptProvider interface {
	GetSystemPrompt(session chat.Session, basePrompt string) string
}

// Service 装配模型、通用聊天处理器以及旧 Matrix 命令兼容入口。
type Service struct {
	config *config.Config
	// circuitBreaker 在任务之间保留失败状态。
	circuitBreaker *model.CircuitBreaker

	// core 是共享核心逻辑。
	core *Core
	// matrixService 保留 Matrix 专属兼容入口：注册 !task/!schedule 命令、取 BotID 作为
	// 旧历史键的账号；普通文本回执与任务文件均已改经平台端口发送。
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
	// toolExecutor 是工具执行器。
	toolExecutor *ToolExecutor
	// tasks 在应用启动时绑定，接收聊天任务并负责后台生命周期。
	tasks *task.Manager
	// taskDir 是应用启动时的规范化工作目录，不执行全局 chdir。
	taskDir string
	// taskFiles 按平台保存具备文件发送能力的投递 adapter（chat.FileAdapter）。
	// 键只有平台名，不区分账号；多账号同平台的限制见 taskFileAdapter。
	taskFiles sync.Map
	// taskStreams 按平台保存可编辑的后台任务临时回复设置。
	taskStreams sync.Map
	// executor 同时管理本地容器工具和 MCP 工具权限。
	executor *execution.Executor
	// memory 是长期记忆服务；未启用时工具与快照注入都不生效。
	memory *memory.Service
	// runningReviews 记录正在执行复盘的空间键，防止同一空间并发复盘。
	runningReviews sync.Map
	// tweetAdapter 是推文读写端口，由支持推文的平台在启动时注入。
	// 未注入时 saber_tweet 不会出现在任何工具列表中。
	tweetAdapter chat.TweetAdapter
	// emojiAdapter 是表情目录与收藏端口，由支持表情的平台在启动时注入。
	// 未注入时 saber_emoji 不会出现在任何工具列表中。
	emojiAdapter chat.EmojiAdapter
	// tweetPosts 是主动发帖的进程内限流状态，按本地日期计次。
	tweetPosts tweetPostLimiter
}

// ServiceOption 配置 AI 服务的可选依赖。平台专属服务由接入端注入，
// 核心装配本身不要求任何聊天平台存在。
type ServiceOption func(*serviceOptions)

// serviceOptions 汇集 NewService 的可选依赖。
type serviceOptions struct {
	matrixService *matrix.CommandService
	mediaService  *matrix.MediaService
	mcpManager    *mcp.Manager
	memory        *memory.Service
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

// WithMemory 注入长期记忆服务；未注入时不注册记忆工具，也不注入记忆快照。
func WithMemory(service *memory.Service) ServiceOption {
	return func(o *serviceOptions) { o.memory = service }
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
		memory:         o.memory,
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

// auxiliaryTimeout 是评估、摘要等辅助模型请求的时限，沿用主请求的请求超时。
// 推理模型要先把思考 token 写完才会给出结论，固定短时限会与截断一样让任务误判预算耗尽。
func (s *Service) auxiliaryTimeout(modelName string) time.Duration {
	seconds := s.config.AI.TimeoutSeconds
	if cfg, ok := s.core.GetConfig().GetModelConfig(modelName); ok && cfg.RequestTimeoutSeconds > 0 {
		seconds = cfg.RequestTimeoutSeconds
	}
	if seconds <= 0 {
		seconds = 120
	}
	return time.Duration(seconds) * time.Second
}

// auxiliaryMaxTokens 返回评估、摘要等辅助请求的输出上限。
// 与主请求一致沿用模型自身的生成预算（0 表示不发送上限）；
// Responses 的 max_output_tokens 和推理模型的 completion tokens 都包含思考 token，
// 固定小额度会在结论或 JSON 产出前被上游截断。仅当模型配置缺失时退回全局默认。
func (s *Service) auxiliaryMaxTokens(modelName string) int {
	if cfg, ok := s.core.GetConfig().GetModelConfig(modelName); ok {
		return cfg.MaxTokens
	}
	return config.DefaultAIConfig().MaxTokens
}

// agentRetry 返回主请求与辅助请求共用的重试策略。
// 评估、摘要同样受运营者配置的瞬时错误重试保护：一次 5xx 不应终止整次任务。
func (s *Service) agentRetry() *RetryConfigWrapper {
	retry := &RetryConfigWrapper{
		MaxRetries:    s.config.Agent.Retry.MaxRetries,
		InitialDelay:  time.Duration(s.config.Agent.Retry.InitialDelayMs) * time.Millisecond,
		MaxDelay:      time.Duration(s.config.Agent.Retry.MaxDelayMs) * time.Millisecond,
		BackoffFactor: s.config.Agent.Retry.BackoffFactor,
	}
	if !s.config.Agent.Retry.Enabled {
		retry.MaxRetries = 0
	}
	if s.config.Agent.Retry.FallbackEnabled {
		retry.FallbackModels = s.config.Agent.Retry.FallbackModels
	}
	retry.CircuitBreaker = s.circuitBreaker
	return retry
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

// SetTweetAdapter 注入推文读写端口，由支持推文的平台在启动时调用。
//
// 这里只做依赖登记：平台开关、账号匹配与授权都在工具可见性和执行阶段复核，
// 因此运行期修改配置或撤销管理员不会让旧工具快照继续生效。
func (s *Service) SetTweetAdapter(adapter chat.TweetAdapter) {
	s.tweetAdapter = adapter
}

// SetEmojiAdapter 注入表情目录与收藏端口，由支持表情的平台在启动时调用。
//
// 与推文端口一样只做依赖登记：平台开关、账号匹配与工具可见性都在执行阶段复核，
// 因此运行期关闭平台或换掉账号不会让旧工具快照继续生效。
func (s *Service) SetEmojiAdapter(adapter chat.EmojiAdapter) {
	s.emojiAdapter = adapter
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
	if s.executor != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := s.executor.Close(ctx); err != nil {
			slog.Error("关闭执行环境失败", "error", err)
		}
		cancel()
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

// HandleChat 是内存或其他聊天 adapter 可复用的统一消息入口。
func (s *Service) HandleChat(ctx context.Context, message chat.Message, reply chat.Adapter) (agent.Result, error) {
	return s.handleChat(ctx, message, reply, s.GetModelRegistry().GetDefault(), true, true, false)
}

// HandleChatModel 与 HandleChat 相同，但由平台指定本轮使用的模型。
// 供接入端在解析平台专属命令（例如 !ai-gpt-4）后复用同一条聊天链路。
func (s *Service) HandleChatModel(ctx context.Context, message chat.Message, reply chat.Adapter, modelName string) (agent.Result, error) {
	return s.handleChat(ctx, message, reply, modelName, true, true, false)
}

// HandleTweet 是推文互动的处理入口，由支持推文的平台注入。
//
// 推文回合与普通聊天共用模型与历史，但有三处明确差异：不解析聊天控制命令、
// 不提交持久化任务、不准备任何工具；回复正文由平台 adapter 发成公开评论。
// 回合被标记为公开线程，即使将来某条路径漏掉了工具裁剪，执行阶段也会再次拒绝。
func (s *Service) HandleTweet(ctx context.Context, message chat.Message, reply chat.Adapter) (agent.Result, error) {
	return s.handleChat(withPublicThread(ctx), message, reply, s.GetModelRegistry().GetDefault(), false, false, true)
}

// handleChat 是所有聊天入口共用的处理链路。
//
// allowNaturalControl 控制自然语言任务/技能识别；useTasks 决定是否提交持久化任务；
// publicThread 标记「回复会成为公开内容」的回合：此时不准备工具、不注入记忆，
// 并追加公开回复约束与平台上下文。
func (s *Service) handleChat(ctx context.Context, message chat.Message, reply chat.Adapter, modelName string, allowNaturalControl, useTasks, publicThread bool) (agent.Result, error) {
	if !s.IsEnabled() {
		return agent.Result{}, fmt.Errorf("AI功能未启用")
	}
	if err := message.Validate(); err != nil {
		return agent.Result{}, err
	}
	if s.tasks != nil && useTasks {
		if allowNaturalControl {
			if action, taskID, ok := naturalTaskCommand(message.CommandText()); ok {
				return agent.Result{}, s.replyTaskCommand(ctx, message, reply, action, taskID)
			}
		}
	} else {
		if err := s.core.WaitForRateLimit(ctx); err != nil {
			return agent.Result{}, fmt.Errorf("AI请求速率限制: %w", err)
		}
	}
	// 共享命令注册表已经处理过的正文不会到达这里；剩余正文中首词为 `/name`
	// 且当前可信空间存在同名技能时，把技能正文作为运行时指令直接执行。
	var invocation skillInvocation
	invoked := false
	if allowNaturalControl {
		invocation, invoked = s.lookupSkillInvocation(ctx, message)
		if invoked {
			// 技能正文只进入本次运行的 RuntimeContext；持久化请求只保留用户参数或技能描述。
			userText := invocation.userText()
			message.Text, message.ControlText = userText, &userText
		}
	}
	if len(message.Attachments) > 0 && s.config.AI.MediaModel != "" {
		modelName = s.config.AI.MediaModel
	}
	req, err := s.taskRequest(message, modelName)
	if err != nil {
		return agent.Result{}, err
	}
	if invoked {
		req = appendRuntimeContext(req, invocation.runtimeInstruction(s.skillInvokeBudget()))
	}
	// 平台上下文只进入本次运行的运行时资料，不进持久化请求，也不改写用户消息本身；
	// 普通聊天没有 Context 时这里什么都不追加，行为保持不变。
	if contextText := platformUntrustedContext(message.Context); contextText != "" {
		req = appendRuntimeContext(req, contextText)
	}
	if publicThread {
		// 公开回复约束是系统提示的一部分。
		req.Messages = append(req.Messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: tweetPublicReplyInstruction})
	}
	identity := chat.Identity{Session: message.Session, SenderID: message.SenderID, Direct: message.Direct}
	toolCtx := chat.WithIdentity(ctx, identity)
	if publicThread {
		toolCtx = withPublicThread(toolCtx)
	}
	if s.executor != nil {
		if dir, err := s.executor.Workspace(identity); err == nil {
			toolCtx = execution.WithTask(toolCtx, 0, dir)
		}
	}
	// 表情说明只加在非公开聊天回合：公开推文回复不提供任何工具，也不应引导模型
	// 去查询表情；普通 Violet 聊天才需要「先查 token、再原样写回正文」的提示。
	if !publicThread && s.emojiToolAvailable(toolCtx) {
		req.Messages = append(req.Messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: emojiReplyInstruction})
	}
	req.Tools, _ = s.toolExecutor.PrepareTools(toolCtx)
	if s.tasks != nil && useTasks {
		return agent.Result{}, s.submitTask(ctx, message, req, reply)
	}
	// 公开互动回合不注入记忆：记忆可能包含私聊内容，不能因一次公开评论而外泄。
	if publicThread {
		return s.chatProcessor.Handle(ctx, message, req, reply)
	}
	// 无持久化任务时在此装配快照；有任务时改在续接恢复之后装配，避免旧快照被续接继承。
	return s.chatProcessor.Handle(ctx, message, s.augmentMemory(toolCtx, req), reply)
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
