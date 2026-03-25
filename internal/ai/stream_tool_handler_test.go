// Package ai 包含流式工具处理器的单元测试。
package ai

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
)

// TestNewStreamToolHandler 测试 NewStreamToolHandler 构造函数。
func TestNewStreamToolHandler(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.DefaultStreamEditConfig()
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewStreamToolHandler(editor, cfg)

	if handler == nil {
		t.Fatal("NewStreamToolHandler returned nil")
	}
	if handler.editor != editor {
		t.Error("editor not set correctly")
	}
	if handler.charThreshold != cfg.CharThreshold {
		t.Errorf("charThreshold = %d, want %d", handler.charThreshold, cfg.CharThreshold)
	}
	if handler.toolCalls == nil {
		t.Error("toolCalls map should be initialized")
	}
}

// TestStreamToolHandler_OnChunk 测试文本内容累积。
func TestStreamToolHandler_OnChunk(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{Enabled: true, MaxEdits: 10, EditIntervalMs: 0, CharThreshold: 5}
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewStreamToolHandler(editor, cfg)
	ctx := context.Background()

	chunks := []string{"Hello", " ", "World", "!"}
	for _, chunk := range chunks {
		handler.OnChunk(ctx, chunk)
	}

	content := handler.GetAccumulatedContent()
	if content != "Hello World!" {
		t.Errorf("accumulated content = %q, want %q", content, "Hello World!")
	}
}

// TestStreamToolHandler_OnToolCallChunk 测试工具调用累积。
func TestStreamToolHandler_OnToolCallChunk(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.DefaultStreamEditConfig()
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewStreamToolHandler(editor, cfg)
	ctx := context.Background()

	// 第一个 chunk：设置 ID 和 name
	handler.OnToolCallChunk(ctx, 0, "call_123", "get_weather", "")

	// 后续 chunks：累积 arguments
	handler.OnToolCallChunk(ctx, 0, "", "", "{\"lo")
	handler.OnToolCallChunk(ctx, 0, "", "", "cation")
	handler.OnToolCallChunk(ctx, 0, "", "", "\":\"Paris\"}")

	if !handler.HasToolCalls() {
		t.Error("HasToolCalls should return true")
	}

	toolCalls := handler.GetAccumulatedToolCalls()
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}

	tc := toolCalls[0]
	if tc.ID != "call_123" {
		t.Errorf("tool call ID = %q, want %q", tc.ID, "call_123")
	}
	if tc.Function.Name != "get_weather" {
		t.Errorf("tool call name = %q, want %q", tc.Function.Name, "get_weather")
	}
	expectedArgs := "{\"location\":\"Paris\"}"
	if tc.Function.Arguments != expectedArgs {
		t.Errorf("tool call arguments = %q, want %q", tc.Function.Arguments, expectedArgs)
	}
}

// TestStreamToolHandler_MultipleToolCalls 测试多个工具调用累积。
func TestStreamToolHandler_MultipleToolCalls(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.DefaultStreamEditConfig()
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewStreamToolHandler(editor, cfg)
	ctx := context.Background()

	// 第一个工具调用
	handler.OnToolCallChunk(ctx, 0, "call_1", "get_weather", "{\"city\":\"Paris\"}")
	// 第二个工具调用
	handler.OnToolCallChunk(ctx, 1, "call_2", "get_time", "{\"tz\":\"UTC\"}")

	toolCalls := handler.GetAccumulatedToolCalls()
	if len(toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(toolCalls))
	}

	// 验证工具调用内容 - 由于 map 遍历顺序不确定，使用 map 来查找
	callMap := make(map[string]openai.ToolCall)
	for _, tc := range toolCalls {
		callMap[tc.ID] = tc
	}

	// 验证第一个工具调用
	if tc, ok := callMap["call_1"]; ok {
		if tc.Function.Name != "get_weather" {
			t.Errorf("call_1 function name mismatch: got %s", tc.Function.Name)
		}
	} else {
		t.Error("call_1 not found")
	}

	// 验证第二个工具调用
	if tc, ok := callMap["call_2"]; ok {
		if tc.Function.Name != "get_time" {
			t.Errorf("call_2 function name mismatch: got %s", tc.Function.Name)
		}
	} else {
		t.Error("call_2 not found")
	}
}

// TestStreamToolHandler_OnFinishReason 测试结束原因处理。
func TestStreamToolHandler_OnFinishReason(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.DefaultStreamEditConfig()
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewStreamToolHandler(editor, cfg)
	ctx := context.Background()

	handler.OnFinishReason(ctx, "tool_calls")

	if handler.GetFinishReason() != "tool_calls" {
		t.Errorf("finish reason = %q, want %q", handler.GetFinishReason(), "tool_calls")
	}
}

// TestStreamToolHandler_OnComplete_Stop 测试正常结束（无工具调用）。
func TestStreamToolHandler_OnComplete_Stop(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{Enabled: true, MaxEdits: 10}
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewStreamToolHandler(editor, cfg)
	ctx := context.Background()

	// 设置结束原因为 stop
	handler.OnFinishReason(ctx, "stop")

	// 完成流
	handler.OnComplete(ctx, "Final response", openai.Usage{}, "gpt-4")

	// 应该发送了最终消息
	calls := mock.getSendTextWithRelatesToCalls()
	if len(calls) < 1 {
		t.Errorf("expected at least 1 call, got %d", len(calls))
	}
}

// TestStreamToolHandler_OnComplete_ToolCalls 测试工具调用结束。
func TestStreamToolHandler_OnComplete_ToolCalls(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{Enabled: true}
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewStreamToolHandler(editor, cfg)
	ctx := context.Background()

	// 添加工具调用
	handler.OnToolCallChunk(ctx, 0, "call_1", "test_tool", "{}")
	handler.OnFinishReason(ctx, "tool_calls")

	// 完成流
	handler.OnComplete(ctx, "", openai.Usage{}, "gpt-4")

	// 不应该发送消息（等待工具执行）
	calls := mock.getSendTextWithRelatesToCalls()
	if len(calls) != 0 {
		t.Errorf("expected 0 calls (waiting for tool execution), got %d", len(calls))
	}
}

// TestStreamToolHandler_OnError 测试错误处理。
func TestStreamToolHandler_OnError(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{Enabled: true}
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewStreamToolHandler(editor, cfg)
	ctx := context.Background()

	testErr := errors.New("stream error")
	handler.OnError(ctx, testErr)

	// 编辑器应该停止
	if !editor.IsStopped() {
		t.Error("editor should be stopped after OnError")
	}

	// 应该发送错误消息
	calls := mock.getSendTextCalls()
	if len(calls) != 1 {
		t.Errorf("expected 1 SendText call for error, got %d", len(calls))
	}
}

// TestStreamToolHandler_Concurrency 测试并发安全性。
func TestStreamToolHandler_Concurrency(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{Enabled: true, MaxEdits: 100, EditIntervalMs: 0, CharThreshold: 1}
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewStreamToolHandler(editor, cfg)
	ctx := context.Background()

	const goroutines = 50
	var wg sync.WaitGroup

	// 并发调用 OnChunk
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			handler.OnChunk(ctx, "chunk")
		}(i)
	}

	// 并发调用 OnToolCallChunk
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			handler.OnToolCallChunk(ctx, idx%3, "", "", "arg")
		}(i)
	}

	// 并发调用读取方法
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = handler.HasToolCalls()
			_ = handler.GetAccumulatedToolCalls()
			_ = handler.GetAccumulatedContent()
			_ = handler.GetFinishReason()
		}()
	}

	wg.Wait()
}

// TestStreamToolHandler_EmptyChunks 测试空 chunk 处理。
func TestStreamToolHandler_EmptyChunks(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.DefaultStreamEditConfig()
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewStreamToolHandler(editor, cfg)
	ctx := context.Background()

	// 空字符串应该被忽略
	handler.OnChunk(ctx, "")
	handler.OnChunk(ctx, "")

	content := handler.GetAccumulatedContent()
	if content != "" {
		t.Errorf("accumulated content should be empty, got %q", content)
	}
}

// TestStreamToolHandler_ThresholdTrigger 测试阈值触发编辑。
func TestStreamToolHandler_ThresholdTrigger(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{Enabled: true, MaxEdits: 10, EditIntervalMs: 0, CharThreshold: 5, TimeThresholdMs: 60000}
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewStreamToolHandler(editor, cfg)
	ctx := context.Background()

	// 未达到阈值
	handler.OnChunk(ctx, "1234")
	if handler.HasStartedEditing() {
		t.Error("should not start editing before threshold")
	}

	// 达到阈值
	handler.OnChunk(ctx, "5")
	if !handler.HasStartedEditing() {
		t.Error("should start editing after threshold")
	}

	// 应该有消息发送
	calls := mock.getSendTextWithRelatesToCalls()
	if len(calls) < 1 {
		t.Errorf("expected at least 1 call, got %d", len(calls))
	}
}
