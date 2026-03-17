package ai

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// Service 是 AI 服务的核心结构体。
//
// 它管理 AI 客户端、上下文和命令处理。
type Service struct {
	globalConfig   *config.AIConfig
	matrixService  *matrix.CommandService
	contextManager *ContextManager
	clients        map[string]*Client
	clientsMu      sync.RWMutex
}

// NewService 创建并初始化一个新的 AI 服务实例。
//
// 参数:
//   - cfg: AI 配置，必须提供且验证通过
//   - matrixService: Matrix 命令服务，用于发送消息
//
// 返回值:
//   - *Service: 创建的 AI 服务实例
//   - error: 初始化过程中发生的错误
func NewService(cfg *config.AIConfig, matrixService *matrix.CommandService) (*Service, error) {
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

	service := &Service{
		globalConfig:   cfg,
		matrixService:  matrixService,
		contextManager: contextManager,
		clients:        make(map[string]*Client),
	}

	slog.Info("AI服务初始化完成",
		"enabled", cfg.Enabled,
		"provider", cfg.Provider,
		"default_model", cfg.DefaultModel,
		"context_enabled", cfg.Context.Enabled)

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

func (s *Service) handleAICommand(ctx context.Context, userID id.UserID, roomID id.RoomID, modelName string, args []string) error {
	if !s.IsEnabled() {
		return fmt.Errorf("AI功能未启用")
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

		if s.globalConfig.StreamEnabled && s.globalConfig.StreamEdit.Enabled {
			slog.Debug("使用流式响应模式", "char_threshold", s.globalConfig.StreamEdit.CharThreshold)
			editor := NewStreamEditor(s.matrixService, roomID, "", s.globalConfig.StreamEdit)
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

			resp, chatErr := client.CreateChatCompletion(ctx, req)

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
				"content_length", len(resp.Content),
				"content", resp.Content,
				"prompt_tokens", resp.Usage.PromptTokens,
				"completion_tokens", resp.Usage.CompletionTokens,
				"total_tokens", resp.Usage.TotalTokens)

			slog.Info("AI 响应", "model", model, "content", resp.Content)

			// 如果上下文中有 EventID，使用回复模式（群聊场景）
			eventID := matrix.GetEventID(ctx)
			var sendErr error
			if eventID != "" {
				_, sendErr = s.matrixService.SendReply(ctx, roomID, resp.Content, eventID)
			} else {
				sendErr = s.matrixService.SendText(ctx, roomID, resp.Content)
			}

			if sendErr != nil {
				slog.Error("发送 AI 响应失败", "error", sendErr)
				return nil, fmt.Errorf("发送响应失败：%w", sendErr)
			}

			if s.contextManager != nil {
				s.contextManager.AddMessage(roomID, RoleAssistant, resp.Content, s.matrixService.BotID())
			}

			return resp, nil
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
