// Package ai 提供 AI 服务相关功能，包括对话管理、流式响应和工具调用。
package ai

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/matrix"
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

// ResponseHandler 负责处理 AI 响应。
//
// 它提供多种方法来处理不同类型的响应：
//   - 直接响应（非流式）
//   - 流式响应
//   - 带工具调用的响应
type ResponseHandler struct {
	service *Service
}

// NewResponseHandler 创建一个新的响应处理器。
//
// 参数:
//   - service: AI 服务实例
//
// 返回值:
//   - *ResponseHandler: 新创建的响应处理器实例
func NewResponseHandler(service *Service) *ResponseHandler {
	return &ResponseHandler{service: service}
}

// DetermineResponseMode 根据配置和工具调用状态决定响应模式。
//
// 参数:
//   - streamEnabled: 是否启用流式传输
//   - streamEdit: 是否启用流式编辑
//   - useToolCall: 是否使用工具调用
//
// 返回值:
//   - ResponseMode: 决定的响应模式
func (rh *ResponseHandler) DetermineResponseMode(streamEnabled, streamEdit, useToolCall bool) ResponseMode {
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

// SendResponse 根据上下文中的 EventID 发送响应消息。
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
func (rh *ResponseHandler) SendResponse(ctx context.Context, roomID id.RoomID, content string) error {
	eventID := matrix.GetEventID(ctx)
	if eventID != "" {
		_, err := rh.service.matrixService.SendReply(ctx, roomID, content, eventID)
		return err
	}
	return rh.service.matrixService.SendText(ctx, roomID, content)
}

// ExecuteDirectResponse 执行直接（非流式）AI 响应。
//
// 参数:
//   - ctx: 上下文
//   - client: AI 客户端
//   - req: 聊天完成请求
//   - respCtx: 响应上下文
//
// 返回值:
//   - *ChatCompletionResponse: 响应结果
//   - error: 错误信息
func (rh *ResponseHandler) ExecuteDirectResponse(
	ctx context.Context,
	client *Client,
	req ChatCompletionRequest,
	respCtx *ResponseContext,
) (*ChatCompletionResponse, error) {
	roomID := respCtx.RoomID

	if err := rh.service.matrixService.StartTyping(ctx, roomID, 30000); err != nil {
		slog.Warn("无法启动 typing indicator", "error", err)
	}

	resp, err := client.CreateChatCompletion(ctx, req)

	if stopErr := rh.service.matrixService.StopTyping(ctx, roomID); stopErr != nil {
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

	if err := rh.SendResponse(ctx, roomID, resp.Content); err != nil {
		slog.Error("发送 AI 响应失败", "error", err)
		return nil, fmt.Errorf("发送响应失败：%w", err)
	}

	if rh.service.contextManager != nil {
		rh.service.contextManager.AddMessage(roomID, RoleAssistant, resp.Content, rh.service.matrixService.BotID())
	}

	return resp, nil
}

// ExecuteStreamingResponse 执行流式 AI 响应。
//
// 参数:
//   - ctx: 上下文
//   - client: AI 客户端
//   - req: 聊天完成请求
//   - respCtx: 响应上下文
//
// 返回值:
//   - error: 错误信息
func (rh *ResponseHandler) ExecuteStreamingResponse(
	ctx context.Context,
	client *Client,
	req ChatCompletionRequest,
	respCtx *ResponseContext,
) error {
	roomID := respCtx.RoomID

	cfg := rh.service.core.GetConfig()
	slog.Debug("使用流式响应模式", "char_threshold", cfg.StreamEdit.CharThreshold)

	eventID := matrix.GetEventID(ctx)
	editor := NewStreamEditor(rh.service.matrixService, roomID, "", cfg.StreamEdit, eventID)
	handler := NewSmartStreamHandler(editor, cfg.StreamEdit.CharThreshold, cfg.StreamEdit.TimeThresholdMs)

	streamErr := client.CreateStreamingChatCompletion(ctx, req, handler)
	if streamErr != nil {
		slog.Error("流式AI请求失败", "model", req.Model, "error", streamErr)
		return streamErr
	}

	slog.Debug("流式AI请求完成", "model", req.Model)
	return nil
}

// ExecuteDirectResponseWithTools 执行带工具调用的直接响应。
//
// 参数:
//   - ctx: 上下文
//   - client: AI 客户端（未使用，仅为接口兼容性）
//   - req: 聊天完成请求（未使用，仅为接口兼容性）
//   - respCtx: 响应上下文
//
// 返回值:
//   - *ChatCompletionResponse: 响应结果
//   - error: 错误信息
func (rh *ResponseHandler) ExecuteDirectResponseWithTools(
	ctx context.Context,
	_ *Client,
	_ ChatCompletionRequest,
	respCtx *ResponseContext,
) (*ChatCompletionResponse, error) {
	roomID := respCtx.RoomID

	if err := rh.service.matrixService.StartTyping(ctx, roomID, 30000); err != nil {
		slog.Warn("无法启动 typing indicator", "error", err)
	}

	toolExecutor := NewToolExecutor(rh.service)
	finalContent, chatErr := toolExecutor.ExecuteToolCallingLoop(ctx, respCtx.Messages, respCtx.Model, respCtx.Tools)

	if stopErr := rh.service.matrixService.StopTyping(ctx, roomID); stopErr != nil {
		slog.Warn("无法停止 typing indicator", "error", stopErr)
	}

	if chatErr != nil {
		slog.Error("AI请求失败", "model", respCtx.Model, "error", chatErr)
		return nil, chatErr
	}

	slog.Info("AI 响应", "model", respCtx.Model, "content_length", len(finalContent))

	if err := rh.SendResponse(ctx, roomID, finalContent); err != nil {
		slog.Error("发送 AI 响应失败", "error", err)
		return nil, fmt.Errorf("发送响应失败：%w", err)
	}

	if rh.service.contextManager != nil {
		rh.service.contextManager.AddMessage(roomID, RoleAssistant, finalContent, rh.service.matrixService.BotID())
	}

	return &ChatCompletionResponse{
		Content: finalContent,
		Model:   respCtx.Model,
	}, nil
}
