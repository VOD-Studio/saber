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
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"golang.org/x/time/rate"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	appcontext "rua.plus/saber/internal/context"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/mcp"
)

// ResponseMode 表示 AI 响应模式。
type ResponseMode int

const (
	// ResponseModeDirect 表示直接响应模式（非流式，无工具调用）。
	ResponseModeDirect ResponseMode = iota
	// ResponseModeStreaming 表示流式响应模式。
	ResponseModeStreaming
	// ResponseModeToolCalling 表示工具调用模式。
	ResponseModeToolCalling
	// ResponseModeStreamingWithTools 表示流式响应带工具调用模式。
	ResponseModeStreamingWithTools
)

// String 返回响应模式的字符串表示。
func (m ResponseMode) String() string {
	switch m {
	case ResponseModeDirect:
		return "direct"
	case ResponseModeStreaming:
		return "streaming"
	case ResponseModeToolCalling:
		return "tool_calling"
	case ResponseModeStreamingWithTools:
		return "streaming_with_tools"
	default:
		return "unknown"
	}
}

// ResponseContext 封装 AI 响应请求的上下文。
type ResponseContext struct {
	UserID      id.UserID
	RoomID      id.RoomID
	Messages    []openai.ChatCompletionMessage
	Model       string
	UseToolCall bool
	Tools       []openai.Tool
}

// determineResponseMode 根据配置和工具调用状态决定响应模式。
func determineResponseMode(streamEnabled, streamEdit, useToolCall bool) ResponseMode {
	if streamEnabled && streamEdit {
		if useToolCall {
			return ResponseModeStreamingWithTools
		}
		return ResponseModeStreaming
	}
	if useToolCall {
		return ResponseModeToolCalling
	}
	return ResponseModeDirect
}

// executeToolCallingLoop 执行工具调用循环，处理 AI 响应中的工具调用。
//
// 它最多执行配置的 MaxIterations 次迭代，每次迭代都会：
// 1. 向 AI 发送当前消息历史
// 2. 检查 AI 响应是否包含工具调用
// 3. 如果没有工具调用，返回最终内容
// 4. 如果有工具调用，执行每个工具并将其结果添加到消息历史中
// 5. 继续下一次迭代
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - messages: 聊天消息历史
//   - modelName: 使用的 AI 模型名称
//   - tools: 可用的工具列表
//
// 返回值:
//   - string: 最终的 AI 响应内容
//   - error: 执行过程中发生的错误
func (s *Service) executeToolCallingLoop(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	modelName string,
	tools []openai.Tool,
) (string, error) {
	currentMessages := make([]openai.ChatCompletionMessage, len(messages))
	copy(currentMessages, messages)

	maxIterations := s.globalConfig.ToolCalling.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 5
	}

	for i := 0; i < maxIterations; i++ {
		req := ChatCompletionRequest{
			Model:    modelName,
			Messages: currentMessages,
			Tools:    tools,
		}

		client, err := s.getClient(modelName)
		if err != nil {
			return "", fmt.Errorf("获取AI客户端失败: %w", err)
		}

		resp, err := client.CreateChatCompletion(ctx, req)
		if err != nil {
			return "", fmt.Errorf("AI请求失败: %w", err)
		}

		// No tool calls - return final content
		if len(resp.ToolCalls) == 0 {
			return resp.Content, nil
		}

		// 先将带工具调用的助手消息添加到历史记录
		// 这是 AI 查看其之前工具调用所必需的
		currentMessages = append(currentMessages, openai.ChatCompletionMessage{
			Role:      openai.ChatMessageRoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// Process tool calls
		for _, toolCall := range resp.ToolCalls {
			// Log tool call
			slog.Debug("执行工具调用", "tool_name", toolCall.Function.Name, "tool_id", toolCall.ID)

			// Parse args
			var args map[string]any
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				// Add error as tool result
				currentMessages = append(currentMessages, openai.ChatCompletionMessage{
					Role:       "tool",
					ToolCallID: toolCall.ID,
					Content:    fmt.Sprintf("Error parsing arguments: %v", err),
				})
				continue
			}

			// Execute tool
			if s.mcpManager == nil {
				currentMessages = append(currentMessages, openai.ChatCompletionMessage{
					Role:       "tool",
					ToolCallID: toolCall.ID,
					Content:    "Error: MCP manager not initialized",
				})
				continue
			}

			toolName := toolCall.Function.Name
			serverName := s.mcpManager.GetServerForTool(toolName)
			if serverName == "" {
				currentMessages = append(currentMessages, openai.ChatCompletionMessage{
					Role:       "tool",
					ToolCallID: toolCall.ID,
					Content:    fmt.Sprintf("Error: No server found for tool %s", toolName),
				})
				continue
			}

			result, err := s.mcpManager.CallTool(ctx, serverName, toolName, args)
			if err != nil {
				// Add error as tool result
				currentMessages = append(currentMessages, openai.ChatCompletionMessage{
					Role:       "tool",
					ToolCallID: toolCall.ID,
					Content:    fmt.Sprintf("Error: %v", err),
				})
				continue
			}

			// Add tool result to messages
			resultJSON, _ := json.Marshal(result)
			currentMessages = append(currentMessages, openai.ChatCompletionMessage{
				Role:       "tool",
				ToolCallID: toolCall.ID,
				Content:    string(resultJSON),
			})
		}
	}

	return "", fmt.Errorf("max tool iterations (%d) exceeded", maxIterations)
}

// executeStreamingWithToolCalling 执行支持工具调用的流式响应。
//
// 此方法结合了流式输出和工具调用能力：
// 1. 使用流式请求获取 AI 响应
// 2. 如果响应包含工具调用，累积工具调用参数
// 3. 执行工具并将结果添加到消息历史
// 4. 继续流式请求直到获得最终响应
//
// 参数:
//   - ctx: 上下文
//   - client: AI 客户端
//   - req: 聊天完成请求
//   - roomID: Matrix 房间 ID
//   - messages: 消息历史
//   - tools: 可用工具列表
//   - model: 使用的模型名称
//
// 返回值:
//   - any: 响应结果
//   - error: 错误信息
func (s *Service) executeStreamingWithToolCalling(
	ctx context.Context,
	client *Client,
	req ChatCompletionRequest,
	roomID id.RoomID,
	messages []openai.ChatCompletionMessage,
	_ []openai.Tool,
	model string,
) (any, error) {
	currentMessages := make([]openai.ChatCompletionMessage, len(messages))
	copy(currentMessages, messages)

	eventID := matrix.GetEventID(ctx)

	maxIterations := s.globalConfig.ToolCalling.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 5
	}

	for i := 0; i < maxIterations; i++ {
		// 创建流编辑器和处理器
		editor := NewStreamEditor(s.matrixService, roomID, "", s.globalConfig.StreamEdit, eventID)
		handler := NewStreamToolHandler(editor, s.globalConfig.StreamEdit)

		// 发送流式请求
		req.Messages = currentMessages
		err := client.CreateStreamingChatCompletionWithTools(ctx, req, handler)
		if err != nil {
			slog.Error("流式AI请求失败", "model", model, "iteration", i+1, "error", err)
			return nil, err
		}

		// 检查是否有工具调用
		if !handler.HasToolCalls() || handler.GetFinishReason() != "tool_calls" {
			// 没有工具调用，流式响应已完成
			slog.Debug("流式AI请求完成（无工具调用）", "model", model, "iteration", i+1)
			return nil, nil
		}

		// 有工具调用，需要执行
		toolCalls := handler.GetAccumulatedToolCalls()
		accumulatedContent := handler.GetAccumulatedContent()
		slog.Debug("检测到工具调用，开始执行",
			"iteration", i+1,
			"tool_count", len(toolCalls),
			"accumulated_content_length", len(accumulatedContent))

		// 如果有累积的文本内容，添加到消息历史
		if accumulatedContent != "" {
			currentMessages = append(currentMessages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: accumulatedContent,
			})
		}

		// 添加带工具调用的助手消息
		currentMessages = append(currentMessages, openai.ChatCompletionMessage{
			Role:      openai.ChatMessageRoleAssistant,
			Content:   accumulatedContent,
			ToolCalls: toolCalls,
		})

		// 执行每个工具调用
		for _, toolCall := range toolCalls {
			slog.Debug("执行工具调用", "tool_name", toolCall.Function.Name, "tool_id", toolCall.ID)

			// 解析参数
			var args map[string]any
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				currentMessages = append(currentMessages, openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					ToolCallID: toolCall.ID,
					Content:    fmt.Sprintf("Error parsing arguments: %v", err),
				})
				continue
			}

			// 执行工具
			result, execErr := s.executeToolCall(ctx, toolCall.Function.Name, args)
			if execErr != nil {
				currentMessages = append(currentMessages, openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					ToolCallID: toolCall.ID,
					Content:    fmt.Sprintf("Error: %v", execErr),
				})
				continue
			}

			// 添加工具结果
			resultJSON, _ := json.Marshal(result)
			currentMessages = append(currentMessages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: toolCall.ID,
				Content:    string(resultJSON),
			})
		}

		slog.Debug("工具调用执行完成，继续流式请求", "iteration", i+1)
	}

	slog.Error("超过最大工具调用迭代次数", "max_iterations", maxIterations)
	return nil, fmt.Errorf("max tool iterations (%d) exceeded", maxIterations)
}

// executeToolCall 执行单个工具调用。
//
// 参数:
//   - ctx: 上下文
//   - toolName: 工具名称
//   - args: 工具参数
//
// 返回值:
//   - any: 工具执行结果
//   - error: 错误信息
func (s *Service) executeToolCall(ctx context.Context, toolName string, args map[string]any) (any, error) {
	if s.mcpManager == nil {
		return nil, fmt.Errorf("MCP manager not initialized")
	}

	serverName := s.mcpManager.GetServerForTool(toolName)
	if serverName == "" {
		return nil, fmt.Errorf("no server found for tool %s", toolName)
	}

	result, err := s.mcpManager.CallTool(ctx, serverName, toolName, args)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// Service 是 AI 服务的核心结构体。
type Service struct {
	globalConfig   *config.AIConfig
	matrixService  *matrix.CommandService
	contextManager *ContextManager
	mcpManager     *mcp.Manager
	mediaService   *matrix.MediaService
	clients        map[string]*Client
	clientsMu      sync.RWMutex
	rateLimiter    *rate.Limiter
	modelRegistry  *ModelRegistry
}

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

	var rateLimiter *rate.Limiter
	if cfg.RateLimitPerMinute > 0 {
		rateLimiter = rate.NewLimiter(rate.Limit(cfg.RateLimitPerMinute)/60, cfg.RateLimitPerMinute/6)
	}

	service := &Service{
		globalConfig:   cfg,
		matrixService:  matrixService,
		contextManager: contextManager,
		mcpManager:     mcpManager,
		mediaService:   mediaService,
		clients:        make(map[string]*Client),
		rateLimiter:    rateLimiter,
		modelRegistry:  NewModelRegistry(cfg),
	}

	slog.Info("AI服务初始化完成",
		"enabled", cfg.Enabled,
		"provider", cfg.Provider,
		"default_model", cfg.DefaultModel,
		"context_enabled", cfg.Context.Enabled,
		"rate_limit_per_minute", cfg.RateLimitPerMinute)

	return service, nil
}

func (s *Service) getClient(modelName string) (*Client, error) {
	// 第一次检查（读锁）
	s.clientsMu.RLock()
	client, exists := s.clients[modelName]
	s.clientsMu.RUnlock()

	if exists {
		return client, nil
	}

	// 获取写锁进行创建
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()

	// 再次检查（可能其他 goroutine 已创建）
	if client, exists := s.clients[modelName]; exists {
		return client, nil
	}

	modelConfig, _ := s.globalConfig.GetModelConfig(modelName)
	cfg := &modelConfig

	newClient, err := NewClientWithModel(cfg)
	if err != nil {
		return nil, fmt.Errorf("创建AI客户端失败 (模型: %s): %w", modelName, err)
	}

	s.clients[modelName] = newClient
	slog.Debug("创建了新的AI客户端", "model", modelName)
	return newClient, nil
}

// IsEnabled 检查 AI 服务是否已启用。
//
// 返回值:
//   - bool: 如果 AI 服务已启用则返回 true
func (s *Service) IsEnabled() bool {
	return s.globalConfig.Enabled
}

// GetModelRegistry 获取模型注册表。
//
// 返回值:
//   - *ModelRegistry: 模型注册表实例
func (s *Service) GetModelRegistry() *ModelRegistry {
	return s.modelRegistry
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

	if s.rateLimiter != nil {
		if err := s.rateLimiter.Wait(ctx); err != nil {
			return "", fmt.Errorf("AI请求速率限制: %w", err)
		}
	}

	modelName := s.modelRegistry.GetDefault()
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
		MaxTokens:   s.globalConfig.MaxTokens,
		Temperature: s.globalConfig.Temperature,
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

	if s.rateLimiter != nil {
		if err := s.rateLimiter.Wait(ctx); err != nil {
			return "", fmt.Errorf("AI请求速率限制: %w", err)
		}
	}

	// 使用指定的模型或默认模型
	if modelName == "" {
		modelName = s.modelRegistry.GetDefault()
	}

	// 使用指定的温度或全局默认值
	if temperature == 0 {
		temperature = s.globalConfig.Temperature
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
		MaxTokens:   s.globalConfig.MaxTokens,
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

	if s.rateLimiter != nil {
		if err := s.rateLimiter.Wait(ctx); err != nil {
			return "", fmt.Errorf("AI请求速率限制: %w", err)
		}
	}

	if modelName == "" {
		modelName = s.modelRegistry.GetDefault()
	}

	if temperature == 0 {
		temperature = s.globalConfig.Temperature
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
		MaxTokens:   s.globalConfig.MaxTokens,
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

func (s *Service) prependSystemPrompt(messages []openai.ChatCompletionMessage, prompt string) []openai.ChatCompletionMessage {
	hasSystem := false
	for _, msg := range messages {
		if msg.Role == string(RoleSystem) {
			hasSystem = true
			break
		}
	}
	if hasSystem {
		return messages
	}
	result := make([]openai.ChatCompletionMessage, 0, len(messages)+1)
	result = append(result, openai.ChatCompletionMessage{
		Role:    string(RoleSystem),
		Content: prompt,
	})
	return append(result, messages...)
}

// sendResponse 根据上下文中的 EventID 发送响应消息。
//
// 如果上下文中包含 EventID，则发送回复消息；否则发送普通文本消息。
// 这消除了多处重复的消息发送逻辑。
//
// 参数:
//   - ctx: 上下文（可能包含 EventID）
//   - roomID: 目标房间 ID
//   - content: 要发送的消息内容
//
// 返回值:
//   - error: 发送过程中发生的错误
func (s *Service) sendResponse(ctx context.Context, roomID id.RoomID, content string) error {
	eventID := matrix.GetEventID(ctx)
	if eventID != "" {
		_, err := s.matrixService.SendReply(ctx, roomID, content, eventID)
		return err
	}
	return s.matrixService.SendText(ctx, roomID, content)
}

// executeDirectResponse 执行直接（非流式）AI 响应。
func (s *Service) executeDirectResponse(
	ctx context.Context,
	client *Client,
	req ChatCompletionRequest,
	respCtx *ResponseContext,
) (*ChatCompletionResponse, error) {
	roomID := respCtx.RoomID

	if err := s.matrixService.StartTyping(ctx, roomID, 30000); err != nil {
		slog.Warn("无法启动 typing indicator", "error", err)
	}

	resp, err := client.CreateChatCompletion(ctx, req)

	if stopErr := s.matrixService.StopTyping(ctx, roomID); stopErr != nil {
		slog.Warn("无法停止 typing indicator", "error", stopErr)
	}

	if err != nil {
		return nil, err
	}

	slog.Debug("AI响应成功",
		"model", resp.Model,
		"content_length", len(resp.Content),
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"total_tokens", resp.Usage.TotalTokens)

	slog.Info("AI 响应", "model", resp.Model, "content_length", len(resp.Content))

	if err := s.sendResponse(ctx, roomID, resp.Content); err != nil {
		slog.Error("发送 AI 响应失败", "error", err)
		return nil, fmt.Errorf("发送响应失败：%w", err)
	}

	if s.contextManager != nil {
		s.contextManager.AddMessage(roomID, RoleAssistant, resp.Content, s.matrixService.BotID())
	}

	return resp, nil
}

// executeStreamingResponse 执行流式 AI 响应。
func (s *Service) executeStreamingResponse(
	ctx context.Context,
	client *Client,
	req ChatCompletionRequest,
	respCtx *ResponseContext,
) error {
	roomID := respCtx.RoomID

	slog.Debug("使用流式响应模式", "char_threshold", s.globalConfig.StreamEdit.CharThreshold)

	eventID := matrix.GetEventID(ctx)
	editor := NewStreamEditor(s.matrixService, roomID, "", s.globalConfig.StreamEdit, eventID)
	handler := NewSmartStreamHandler(editor, s.globalConfig.StreamEdit.CharThreshold, s.globalConfig.StreamEdit.TimeThresholdMs)

	streamErr := client.CreateStreamingChatCompletion(ctx, req, handler)
	if streamErr != nil {
		slog.Error("流式AI请求失败", "model", req.Model, "error", streamErr)
		return streamErr
	}

	slog.Debug("流式AI请求完成", "model", req.Model)
	return nil
}

// prepareTools 准备 MCP 工具列表。
func (s *Service) prepareTools() ([]openai.Tool, bool) {
	if s.mcpManager == nil || !s.mcpManager.IsEnabled() {
		return nil, false
	}

	mcpTools := s.mcpManager.ListTools()
	if len(mcpTools) == 0 {
		return nil, false
	}

	tools := make([]openai.Tool, 0, len(mcpTools))
	for _, mcpTool := range mcpTools {
		tools = append(tools, openai.Tool{
			Type: "function",
			Function: &openai.FunctionDefinition{
				Name:        mcpTool.Name,
				Description: mcpTool.Description,
				Parameters:  mcpTool.InputSchema,
			},
		})
	}

	slog.Debug("启用工具调用", "tool_count", len(tools))
	return tools, true
}

// buildTextMessages 构建纯文本消息列表。
// 它从上下文管理器获取历史消息，并添加当前用户消息。
//
// 参数:
//   - roomID: Matrix 房间 ID
//   - userInput: 用户输入的文本
//
// 返回值:
//   - []openai.ChatCompletionMessage: 构建的消息列表
func (s *Service) buildTextMessages(roomID id.RoomID, userInput string) []openai.ChatCompletionMessage {
	var messages []openai.ChatCompletionMessage
	if s.contextManager != nil {
		messages = s.contextManager.GetContext(roomID)
	} else {
		messages = []openai.ChatCompletionMessage{
			{Role: string(RoleUser), Content: userInput},
		}
	}

	if s.globalConfig.SystemPrompt != "" {
		messages = s.prependSystemPrompt(messages, s.globalConfig.SystemPrompt)
	}

	return messages
}

// buildMultimodalMessages 构建多模态消息列表（文本 + 图片）。
// 它从上下文管理器获取历史消息，并添加包含文本和图片的用户消息。
//
// 参数:
//   - ctx: 上下文
//   - roomID: Matrix 房间 ID
//   - userInput: 用户输入的文本
//   - imageData: Base64 Data URL 格式的图片数据
//
// 返回值:
//   - []openai.ChatCompletionMessage: 构建的消息列表
func (s *Service) buildMultimodalMessages(_ context.Context, roomID id.RoomID, userInput, imageData string) []openai.ChatCompletionMessage {
	var messages []openai.ChatCompletionMessage
	if s.contextManager != nil {
		messages = s.contextManager.GetContext(roomID)
	}

	// 构建当前用户消息（文本 + 图片）
	userMessage := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleUser,
		MultiContent: []openai.ChatMessagePart{
			{
				Type: openai.ChatMessagePartTypeText,
				Text: userInput,
			},
			{
				Type: openai.ChatMessagePartTypeImageURL,
				ImageURL: &openai.ChatMessageImageURL{
					URL:    imageData,
					Detail: openai.ImageURLDetailAuto,
				},
			},
		},
	}

	messages = append(messages, userMessage)

	if s.globalConfig.SystemPrompt != "" {
		messages = s.prependSystemPrompt(messages, s.globalConfig.SystemPrompt)
	}

	return messages
}

// buildMultiImageMessages 构建包含多张图片的多模态消息列表。
// 支持同时处理用户发送的图片和引用消息中的图片。
//
// 参数:
//   - ctx: 上下文
//   - roomID: Matrix 房间 ID
//   - userInput: 用户输入的文本
//   - imageDataList: Base64 Data URL 格式的图片数据列表
//
// 返回值:
//   - []openai.ChatCompletionMessage: 构建的消息列表
func (s *Service) buildMultiImageMessages(_ context.Context, roomID id.RoomID, userInput string, imageDataList []string) []openai.ChatCompletionMessage {
	var messages []openai.ChatCompletionMessage
	if s.contextManager != nil {
		messages = s.contextManager.GetContext(roomID)
	}

	// 构建消息部分，首先是文本
	parts := []openai.ChatMessagePart{
		{
			Type: openai.ChatMessagePartTypeText,
			Text: userInput,
		},
	}

	// 添加所有图片
	for _, imageData := range imageDataList {
		parts = append(parts, openai.ChatMessagePart{
			Type: openai.ChatMessagePartTypeImageURL,
			ImageURL: &openai.ChatMessageImageURL{
				URL:    imageData,
				Detail: openai.ImageURLDetailAuto,
			},
		})
	}

	userMessage := openai.ChatCompletionMessage{
		Role:         openai.ChatMessageRoleUser,
		MultiContent: parts,
	}

	messages = append(messages, userMessage)

	if s.globalConfig.SystemPrompt != "" {
		messages = s.prependSystemPrompt(messages, s.globalConfig.SystemPrompt)
	}

	return messages
}

func (s *Service) handleAICommand(ctx context.Context, userID id.UserID, roomID id.RoomID, modelName string, args []string) error {
	ctx = appcontext.WithUserContext(ctx, userID, roomID)

	if !s.IsEnabled() {
		return fmt.Errorf("AI功能未启用")
	}

	if s.rateLimiter != nil {
		if err := s.rateLimiter.Wait(ctx); err != nil {
			return fmt.Errorf("AI请求速率限制: %w", err)
		}
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

	// 处理当前消息的图片
	if mediaInfo != nil && mediaInfo.Type == "image" && s.mediaService != nil && s.globalConfig.Media.Enabled {
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
	if referencedMediaInfo != nil && referencedMediaInfo.Type == "image" && s.mediaService != nil && s.globalConfig.Media.Enabled {
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
			// 单图片情况，使用原有函数
			messages = s.buildMultimodalMessages(ctx, roomID, userInput, imageDataList[0])
		} else {
			// 多图片情况，使用新函数
			messages = s.buildMultiImageMessages(ctx, roomID, userInput, imageDataList)
		}
		slog.Debug("构建多模态消息", "has_image", true, "image_count", imageCount)
	} else {
		messages = s.buildTextMessages(roomID, userInput)
	}

	// 如果有图片且配置了专用模型，使用专用模型
	actualModel := modelName
	if hasImage && s.globalConfig.Media.Model != "" {
		actualModel = s.globalConfig.Media.Model
		slog.Debug("使用图片识别专用模型", "media_model", actualModel, "default_model", modelName)
	}

	req := ChatCompletionRequest{
		Messages:    messages,
		Stream:      s.globalConfig.StreamEnabled,
		MaxTokens:   s.globalConfig.MaxTokens,
		Temperature: s.globalConfig.Temperature,
		Model:       actualModel,
	}

	slog.Debug("AI请求准备完成",
		"model", actualModel,
		"messages_count", len(messages),
		"messages", messages,
		"stream", s.globalConfig.StreamEnabled,
		"max_tokens", s.globalConfig.MaxTokens,
		"temperature", s.globalConfig.Temperature)

	retryConfig := &RetryConfigWrapper{
		MaxRetries:     s.globalConfig.Retry.MaxRetries,
		InitialDelay:   time.Duration(s.globalConfig.Retry.InitialDelayMs) * time.Millisecond,
		MaxDelay:       time.Duration(s.globalConfig.Retry.MaxDelayMs) * time.Millisecond,
		BackoffFactor:  s.globalConfig.Retry.BackoffFactor,
		FallbackModels: s.globalConfig.Retry.FallbackModels,
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
		slog.Debug("发送AI请求", "model", model, "base_url", s.globalConfig.BaseURL)

		tools, useToolCalling := s.prepareTools()
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

		mode := determineResponseMode(s.globalConfig.StreamEnabled, s.globalConfig.StreamEdit.Enabled, useToolCalling)
		slog.Debug("响应模式", "mode", mode, "model", model)

		switch mode {
		case ResponseModeStreamingWithTools:
			return s.executeStreamingWithToolCalling(ctx, client, req, roomID, messages, tools, model)

		case ResponseModeStreaming:
			if err := s.executeStreamingResponse(ctx, client, req, respCtx); err != nil {
				return nil, err
			}
			return nil, nil

		case ResponseModeToolCalling:
			return s.executeDirectResponseWithTools(ctx, client, req, respCtx)

		default:
			return s.executeDirectResponse(ctx, client, req, respCtx)
		}
	})

	if err != nil {
		slog.Error("AI命令执行失败", "error", err)
	} else {
		slog.Debug("AI命令执行成功")
	}

	return err
}

// executeDirectResponseWithTools 执行带工具调用的直接响应。
func (s *Service) executeDirectResponseWithTools(
	ctx context.Context,
	_ *Client,
	_ ChatCompletionRequest,
	respCtx *ResponseContext,
) (*ChatCompletionResponse, error) {
	roomID := respCtx.RoomID

	if err := s.matrixService.StartTyping(ctx, roomID, 30000); err != nil {
		slog.Warn("无法启动 typing indicator", "error", err)
	}

	finalContent, chatErr := s.executeToolCallingLoop(ctx, respCtx.Messages, respCtx.Model, respCtx.Tools)

	if stopErr := s.matrixService.StopTyping(ctx, roomID); stopErr != nil {
		slog.Warn("无法停止 typing indicator", "error", stopErr)
	}

	if chatErr != nil {
		slog.Error("AI请求失败", "model", respCtx.Model, "error", chatErr)
		return nil, chatErr
	}

	slog.Info("AI 响应", "model", respCtx.Model, "content_length", len(finalContent))

	if err := s.sendResponse(ctx, roomID, finalContent); err != nil {
		slog.Error("发送 AI 响应失败", "error", err)
		return nil, fmt.Errorf("发送响应失败：%w", err)
	}

	if s.contextManager != nil {
		s.contextManager.AddMessage(roomID, RoleAssistant, finalContent, s.matrixService.BotID())
	}

	return &ChatCompletionResponse{
		Content: finalContent,
		Model:   respCtx.Model,
	}, nil
}

// AICommand 处理默认的 AI 聊天命令。
type AICommand struct {
	service *Service
}

// NewAICommand 创建一个新的 AI 命令处理器。
//
// 参数:
//   - service: AI 服务实例
//
// 返回值:
//   - *AICommand: 创建的命令处理器
func NewAICommand(service *Service) *AICommand {
	return &AICommand{service: service}
}

// Handle 处理 AI 聊天命令。
//
// 参数:
//   - ctx: 上下文
//   - userID: 发送命令的用户 ID
//   - roomID: 命令所在的房间 ID
//   - args: 命令参数
//
// 返回值:
//   - error: 处理过程中发生的错误
func (c *AICommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	return c.service.handleAICommand(ctx, userID, roomID, c.service.modelRegistry.GetDefault(), args)
}

// MultiModelAICommand 处理指定模型的 AI 聊天命令。
type MultiModelAICommand struct {
	service   *Service
	modelName string
}

// NewMultiModelAICommand 创建一个新的多模型 AI 命令处理器。
//
// 参数:
//   - service: AI 服务实例
//   - modelName: 要使用的 AI 模型名称
//
// 返回值:
//   - *MultiModelAICommand: 创建的命令处理器
func NewMultiModelAICommand(service *Service, modelName string) *MultiModelAICommand {
	return &MultiModelAICommand{
		service:   service,
		modelName: modelName,
	}
}

// Handle 处理指定模型的 AI 聊天命令。
//
// 参数:
//   - ctx: 上下文
//   - userID: 发送命令的用户 ID
//   - roomID: 命令所在的房间 ID
//   - args: 命令参数
//
// 返回值:
//   - error: 处理过程中发生的错误
func (c *MultiModelAICommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	return c.service.handleAICommand(ctx, userID, roomID, c.modelName, args)
}

// ClearContextCommand 处理清除对话上下文的命令。
type ClearContextCommand struct {
	service *Service
}

// NewClearContextCommand 创建一个新的清除上下文命令处理器。
//
// 参数:
//   - service: AI 服务实例
//
// 返回值:
//   - *ClearContextCommand: 创建的命令处理器
func NewClearContextCommand(service *Service) *ClearContextCommand {
	return &ClearContextCommand{service: service}
}

// Handle 处理清除对话上下文命令。
//
// 参数:
//   - ctx: 上下文
//   - userID: 发送命令的用户 ID
//   - roomID: 命令所在的房间 ID
//   - args: 命令参数（未使用）
//
// 返回值:
//   - error: 处理过程中发生的错误
func (c *ClearContextCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if c.service.contextManager == nil {
		return c.service.matrixService.SendText(ctx, roomID, "上下文管理未启用")
	}

	c.service.contextManager.ClearContext(roomID)

	html := "<strong>✅ 对话上下文已清除</strong>"
	plain := "✅ 对话上下文已清除"
	return c.service.matrixService.SendFormattedText(ctx, roomID, html, plain)
}

// ContextInfoCommand 处理查询对话上下文信息的命令。
type ContextInfoCommand struct {
	service *Service
}

// NewContextInfoCommand 创建一个新的上下文信息查询命令处理器。
//
// 参数:
//   - service: AI 服务实例
//
// 返回值:
//   - *ContextInfoCommand: 创建的命令处理器
func NewContextInfoCommand(service *Service) *ContextInfoCommand {
	return &ContextInfoCommand{service: service}
}

// Handle 处理查询对话上下文信息命令。
//
// 参数:
//   - ctx: 上下文
//   - userID: 发送命令的用户 ID
//   - roomID: 命令所在的房间 ID
//   - args: 命令参数（未使用）
//
// 返回值:
//   - error: 处理过程中发生的错误
func (c *ContextInfoCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if c.service.contextManager == nil {
		return c.service.matrixService.SendText(ctx, roomID, "上下文管理未启用")
	}

	msgCount, tokenCount := c.service.contextManager.GetContextSize(roomID)

	html := fmt.Sprintf(`<table>
<thead><tr><th colspan="2">📊 对话上下文信息</th></tr></thead>
<tbody>
<tr><td>消息数量</td><td><strong>%d</strong></td></tr>
<tr><td>估算令牌数</td><td><strong>%d</strong></td></tr>
</tbody></table>`, msgCount, tokenCount)

	plain := fmt.Sprintf("📊 对话上下文信息\n- 消息数量：%d\n- 估算令牌数：%d", msgCount, tokenCount)

	return c.service.matrixService.SendFormattedText(ctx, roomID, html, plain)
}
