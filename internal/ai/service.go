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
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/mcp"
)

type contextKey string

const (
	userIDKey contextKey = "userID"
	roomIDKey contextKey = "roomID"
)

func WithUserContext(ctx context.Context, userID id.UserID, roomID id.RoomID) context.Context {
	ctx = context.WithValue(ctx, userIDKey, userID)
	return context.WithValue(ctx, roomIDKey, roomID)
}

func GetUserFromContext(ctx context.Context) (id.UserID, bool) {
	userID, ok := ctx.Value(userIDKey).(id.UserID)
	return userID, ok
}

func GetRoomFromContext(ctx context.Context) (id.RoomID, bool) {
	roomID, ok := ctx.Value(roomIDKey).(id.RoomID)
	return roomID, ok
}

const maxToolIterations = 5

// executeToolCallingLoop 执行工具调用循环，处理 AI 响应中的工具调用。
//
// 它最多执行 maxToolIterations 次迭代，每次迭代都会：
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

	for i := 0; i < maxToolIterations; i++ {
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

	return "", fmt.Errorf("max tool iterations (%d) exceeded", maxToolIterations)
}

// Service 是 AI 服务的核心结构体。
type Service struct {
	globalConfig   *config.AIConfig
	matrixService  *matrix.CommandService
	contextManager *ContextManager
	mcpManager     *mcp.Manager
	clients        map[string]*Client
	clientsMu      sync.RWMutex
	rateLimiter    *rate.Limiter
}

func NewService(cfg *config.AIConfig, matrixService *matrix.CommandService, mcpManager *mcp.Manager) (*Service, error) {
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
		clients:        make(map[string]*Client),
		rateLimiter:    rateLimiter,
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
	s.clientsMu.RLock()
	client, exists := s.clients[modelName]
	s.clientsMu.RUnlock()

	if exists {
		return client, nil
	}

	modelConfig, _ := s.globalConfig.GetModelConfig(modelName)
	cfg := &modelConfig

	newClient, err := NewClientWithModel(cfg)
	if err != nil {
		return nil, fmt.Errorf("创建AI客户端失败 (模型: %s): %w", modelName, err)
	}

	s.clientsMu.Lock()
	s.clients[modelName] = newClient
	s.clientsMu.Unlock()

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

	modelName := s.globalConfig.DefaultModel
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
		modelName = s.globalConfig.DefaultModel
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

func (s *Service) handleAICommand(ctx context.Context, userID id.UserID, roomID id.RoomID, modelName string, args []string) error {
	ctx = mcp.WithUserContext(ctx, userID, roomID)

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

	req := ChatCompletionRequest{
		Messages:    messages,
		Stream:      s.globalConfig.StreamEnabled,
		MaxTokens:   s.globalConfig.MaxTokens,
		Temperature: s.globalConfig.Temperature,
		Model:       modelName,
	}

	slog.Debug("AI请求准备完成",
		"model", modelName,
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
		MainModel:   modelName,
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

		// 检查是否应该使用工具调用
		var tools []openai.Tool
		useToolCalling := false
		if s.mcpManager != nil && s.mcpManager.IsEnabled() {
			mcpTools := s.mcpManager.ListTools()
			if len(mcpTools) > 0 {
				// Convert MCP tools to OpenAI tools
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
				useToolCalling = true
				slog.Debug("启用工具调用", "tool_count", len(tools))
			}
		}

		// If using tool calling, we need to add tools to the request
		if useToolCalling {
			req.Tools = tools
			// For tool calling, we currently don't support streaming
			// TODO: Add streaming support for tool calls in the future
			req.Stream = false
		}

		if s.globalConfig.StreamEnabled && s.globalConfig.StreamEdit.Enabled && !useToolCalling {
			slog.Debug("使用流式响应模式", "char_threshold", s.globalConfig.StreamEdit.CharThreshold)
			// Extract EventID for reply-to functionality
			eventID := matrix.GetEventID(ctx)
			editor := NewStreamEditor(s.matrixService, roomID, "", s.globalConfig.StreamEdit, eventID)
			handler := NewSmartStreamHandler(editor, s.globalConfig.StreamEdit.CharThreshold, s.globalConfig.StreamEdit.TimeThresholdMs)

			streamErr := client.CreateStreamingChatCompletion(ctx, req, handler)
			if streamErr != nil {
				slog.Error("流式AI请求失败", "model", model, "error", streamErr)
				return nil, streamErr
			}

			slog.Debug("流式AI请求完成", "model", model)
			return nil, nil
		} else {
			// 非流式编辑模式：显示 typing indicator
			if err := s.matrixService.StartTyping(ctx, roomID, 30000); err != nil {
				slog.Warn("无法启动 typing indicator", "error", err)
			}

			var finalContent string
			var usage openai.Usage
			var chatErr error

			if useToolCalling {
				// 使用工具调用循环
				finalContent, chatErr = s.executeToolCallingLoop(ctx, messages, model, tools)
				if chatErr == nil {
					// 为工具调用响应创建空的 usage
					usage = openai.Usage{}
				}
			} else {
				// 使用直接聊天完成
				resp, err := client.CreateChatCompletion(ctx, req)
				if err != nil {
					chatErr = err
				} else {
					finalContent = resp.Content
					usage = resp.Usage
				}
			}

			// 停止 typing indicator
			if stopErr := s.matrixService.StopTyping(ctx, roomID); stopErr != nil {
				slog.Warn("无法停止 typing indicator", "error", stopErr)
			}

			if chatErr != nil {
				slog.Error("AI请求失败", "model", model, "error", chatErr)
				return nil, chatErr
			}

			slog.Debug("AI响应成功",
				"model", model,
				"content_length", len(finalContent),
				"content", finalContent,
				"prompt_tokens", usage.PromptTokens,
				"completion_tokens", usage.CompletionTokens,
				"total_tokens", usage.TotalTokens)

			slog.Info("AI 响应", "model", model, "content", finalContent)

			// 如果上下文中有 EventID，使用回复模式（群聊场景）
			eventID := matrix.GetEventID(ctx)
			var sendErr error
			if eventID != "" {
				_, sendErr = s.matrixService.SendReply(ctx, roomID, finalContent, eventID)
			} else {
				sendErr = s.matrixService.SendText(ctx, roomID, finalContent)
			}

			if sendErr != nil {
				slog.Error("发送 AI 响应失败", "error", sendErr)
				return nil, fmt.Errorf("发送响应失败：%w", sendErr)
			}

			if s.contextManager != nil {
				s.contextManager.AddMessage(roomID, RoleAssistant, finalContent, s.matrixService.BotID())
			}

			// Return a dummy response for the fallback handler
			return &ChatCompletionResponse{
				Content: finalContent,
				Usage:   usage,
				Model:   model,
			}, nil
		}
	})

	if err != nil {
		slog.Error("AI命令执行失败", "error", err)
	} else {
		slog.Debug("AI命令执行成功")
	}

	return err
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
	return c.service.handleAICommand(ctx, userID, roomID, c.service.globalConfig.DefaultModel, args)
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
