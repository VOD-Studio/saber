package ai

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/config"
)

// StreamToolHandler 实现了支持工具调用的流式响应处理器。
//
// 它扩展了 SmartStreamHandler 的功能，增加了工具调用累积能力。
// 当模型在流式响应中调用工具时，会累积工具调用参数，
// 并在流结束后返回完整的工具调用列表供上层处理。
type StreamToolHandler struct {
	editor        *StreamEditor
	charThreshold int
	timeThreshold time.Duration
	startTime     time.Time
	config        config.StreamEditConfig

	// 文本内容累积
	contentBuilder    strings.Builder
	hasStartedEditing bool

	// 工具调用状态
	toolCalls    map[int]*StreamingToolCallState
	hasToolCalls bool
	finishReason string

	mu sync.Mutex
}

// NewStreamToolHandler 创建一个新的支持工具调用的流式处理器。
//
// 参数:
//   - editor: 流编辑器实例
//   - cfg: 流编辑配置
//
// 返回值:
//   - *StreamToolHandler: 创建的处理器实例
func NewStreamToolHandler(editor *StreamEditor, cfg config.StreamEditConfig) *StreamToolHandler {
	return &StreamToolHandler{
		editor:        editor,
		charThreshold: cfg.CharThreshold,
		timeThreshold: time.Duration(cfg.TimeThresholdMs) * time.Millisecond,
		config:        cfg,
		startTime:     time.Now(),
		toolCalls:     make(map[int]*StreamingToolCallState),
	}
}

// OnChunk 处理接收到的文本数据块。
//
// 它累积文本内容并根据阈值决定是否开始编辑消息。
func (h *StreamToolHandler) OnChunk(ctx context.Context, chunk string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if chunk == "" {
		return
	}

	h.contentBuilder.WriteString(chunk)
	h.handleContentUpdateLocked(ctx)
}

// OnToolCallChunk 处理工具调用的增量数据块。
//
// 第一个 chunk 通常包含 id 和 function.name，
// 后续 chunks 包含 arguments 的增量片段。
func (h *StreamToolHandler) OnToolCallChunk(ctx context.Context, toolCallIndex int, id string, name string, argumentsChunk string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.hasToolCalls = true

	state, exists := h.toolCalls[toolCallIndex]
	if !exists {
		state = &StreamingToolCallState{
			Index: toolCallIndex,
		}
		h.toolCalls[toolCallIndex] = state
	}

	if id != "" {
		state.ID = id
	}
	if name != "" {
		state.Name = name
	}
	if argumentsChunk != "" {
		state.Arguments.WriteString(argumentsChunk)
	}

	slog.Debug("累积工具调用",
		"index", toolCallIndex,
		"id", state.ID,
		"name", state.Name,
		"arguments_length", state.Arguments.Len())
}

// OnFinishReason 处理流的结束原因。
//
// 可能的值包括 "stop"（正常结束）和 "tool_calls"（需要调用工具）。
func (h *StreamToolHandler) OnFinishReason(ctx context.Context, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.finishReason = reason
	slog.Debug("流式响应结束原因", "reason", reason)
}

// OnComplete 在流完成时调用。
//
// 根据结束原因处理不同情况：
//   - "stop": 发送最终消息内容
//   - "tool_calls": 不发送消息，等待上层处理工具调用
func (h *StreamToolHandler) OnComplete(ctx context.Context, finalContent string, usage openai.Usage, model string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	slog.Debug("流式响应完成",
		"content_length", len(finalContent),
		"has_tool_calls", h.hasToolCalls,
		"finish_reason", h.finishReason,
		"tool_call_count", len(h.toolCalls))

	// 如果有工具调用且结束原因是 tool_calls，不发送消息
	// 上层会处理工具调用然后继续请求
	if h.finishReason == "tool_calls" && h.hasToolCalls {
		slog.Debug("检测到工具调用，等待工具执行", "tool_count", len(h.toolCalls))
		return
	}

	// 正常结束，发送最终内容
	if !h.hasStartedEditing {
		if err := h.editor.Start(ctx); err != nil {
			slog.Error("启动流编辑失败", "error", err)
			return
		}
		h.hasStartedEditing = true
	}

	if err := h.editor.SendFinal(ctx, finalContent); err != nil {
		slog.Error("发送最终内容失败", "error", err)
	}
}

// OnError 在发生错误时调用。
func (h *StreamToolHandler) OnError(ctx context.Context, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	slog.Error("AI流式响应错误", "error", err)
	h.editor.Stop()

	errorMsg := "❌ AI 服务出错：" + err.Error()
	if sendErr := h.editor.matrixService.SendText(ctx, h.editor.roomID, errorMsg); sendErr != nil {
		slog.Error("发送错误消息失败", "error", sendErr)
	}
}

// GetAccumulatedToolCalls 返回累积的工具调用列表。
//
// 将累积的工具调用状态转换为 openai.ToolCall 格式。
func (h *StreamToolHandler) GetAccumulatedToolCalls() []openai.ToolCall {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.toolCalls) == 0 {
		return nil
	}

	calls := make([]openai.ToolCall, 0, len(h.toolCalls))
	for _, state := range h.toolCalls {
		calls = append(calls, openai.ToolCall{
			ID:   state.ID,
			Type: openai.ToolTypeFunction,
			Function: openai.FunctionCall{
				Name:      state.Name,
				Arguments: state.Arguments.String(),
			},
		})
	}

	return calls
}

// HasToolCalls 返回是否存在工具调用。
func (h *StreamToolHandler) HasToolCalls() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hasToolCalls
}

// GetFinishReason 返回流的结束原因。
func (h *StreamToolHandler) GetFinishReason() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.finishReason
}

// GetAccumulatedContent 返回累积的文本内容。
func (h *StreamToolHandler) GetAccumulatedContent() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.contentBuilder.String()
}

// HasStartedEditing 返回是否已开始编辑消息。
func (h *StreamToolHandler) HasStartedEditing() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hasStartedEditing
}

// handleContentUpdateLocked 处理内容更新（调用时必须持有锁）。
func (h *StreamToolHandler) handleContentUpdateLocked(ctx context.Context) {
	content := h.contentBuilder.String()

	if !h.hasStartedEditing {
		elapsed := time.Since(h.startTime)
		if len(content) >= h.charThreshold || elapsed >= h.timeThreshold {
			if err := h.editor.Start(ctx); err != nil {
				slog.Error("启动流编辑失败", "error", err)
				return
			}
			h.hasStartedEditing = true
			slog.Debug("流编辑已启动",
				"accumulated_length", len(content),
				"elapsed_ms", elapsed.Milliseconds())
		}
	}

	if h.hasStartedEditing {
		if err := h.editor.Update(ctx, content); err != nil {
			slog.Debug("更新流编辑失败", "error", err)
		}
	}
}
