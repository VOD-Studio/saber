package model

import (
	"context"
	"sort"
	"strings"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
)

// agentStreamHandler 只拼接模型协议并发出运行事件，不操作聊天平台。
// SDK 在单个请求内串行调用 handler。
type agentStreamHandler struct {
	emit     func(agent.Event)
	content  strings.Builder
	thinking strings.Builder
	calls    map[int]*StreamingToolCallState
	result   agent.Response
}

func newAgentStreamHandler(emit func(agent.Event)) *agentStreamHandler {
	return &agentStreamHandler{emit: emit, calls: make(map[int]*StreamingToolCallState)}
}

// OnChunk 记录文本并通知展示消费者。
func (h *agentStreamHandler) OnChunk(_ context.Context, chunk string) {
	h.content.WriteString(chunk)
	h.emit(agent.Event{Kind: agent.TextDelta, Text: chunk})
}

// OnThinkingChunk 只转发上游明确标为公开摘要的内容。
func (h *agentStreamHandler) OnThinkingChunk(_ context.Context, chunk string) {
	h.thinking.WriteString(chunk)
	h.emit(agent.Event{Kind: agent.ThinkingDelta, Text: chunk})
}

// OnToolCallChunk 按索引拼接工具调用，完成前不执行。
func (h *agentStreamHandler) OnToolCallChunk(_ context.Context, index int, id, name, args string) {
	state := h.calls[index]
	if state == nil {
		state = &StreamingToolCallState{Index: index}
		h.calls[index] = state
	}
	if id != "" {
		state.ID = id
	}
	if name != "" {
		state.Name = name
	}
	state.Arguments.WriteString(args)
}

// OnFinishReason 保存协议结束原因供 Runtime 校验。
func (h *agentStreamHandler) OnFinishReason(_ context.Context, reason string) {
	h.result.FinishReason = reason
}

// OnComplete 保存模型报告的用量和实际模型。
func (h *agentStreamHandler) OnComplete(_ context.Context, _ string, usage openai.Usage, model string) {
	h.result.Usage = usage
	h.result.Model = model
}

// OnError 保留部分响应；请求方法返回的错误由模型 adapter 统一记录。
func (h *agentStreamHandler) OnError(context.Context, error) {}

// HasToolCalls 告知 SDK 日志是否收到工具片段。
func (h *agentStreamHandler) HasToolCalls() bool { return len(h.calls) > 0 }

// GetAccumulatedToolCalls 按模型索引返回工具，避免 map 遍历改变顺序。
func (h *agentStreamHandler) GetAccumulatedToolCalls() []openai.ToolCall {
	indices := make([]int, 0, len(h.calls))
	for index := range h.calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	calls := make([]openai.ToolCall, 0, len(indices))
	for _, index := range indices {
		state := h.calls[index]
		calls = append(calls, openai.ToolCall{ID: state.ID, Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: state.Name, Arguments: state.Arguments.String()}})
	}
	return calls
}
func (h *agentStreamHandler) response() agent.Response {
	response := h.result
	response.Content = h.content.String()
	response.Thinking = h.thinking.String()
	response.ToolCalls = h.GetAccumulatedToolCalls()
	return response
}
