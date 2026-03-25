// Package ai_test 包含 AI 响应模式的单元测试。
package ai

import (
	"testing"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"
)

// TestResponseMode_String 测试 ResponseMode.String 方法。
func TestResponseMode_String(t *testing.T) {
	tests := []struct {
		mode     ResponseMode
		expected string
	}{
		{ResponseModeDirect, "direct"},
		{ResponseModeStreaming, "streaming"},
		{ResponseModeToolCalling, "tool_calling"},
		{ResponseModeStreamingWithTools, "streaming_with_tools"},
		{ResponseMode(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.expected {
				t.Errorf("ResponseMode(%d).String() = %q, want %q", tt.mode, got, tt.expected)
			}
		})
	}
}

// TestDetermineResponseMode 测试响应模式判断逻辑。
func TestDetermineResponseMode(t *testing.T) {
	tests := []struct {
		name         string
		streamEnable bool
		streamEdit   bool
		useToolCall  bool
		expected     ResponseMode
	}{
		{
			name:         "direct_no_stream_no_tools",
			streamEnable: false,
			streamEdit:   false,
			useToolCall:  false,
			expected:     ResponseModeDirect,
		},
		{
			name:         "streaming_enabled",
			streamEnable: true,
			streamEdit:   true,
			useToolCall:  false,
			expected:     ResponseModeStreaming,
		},
		{
			name:         "tool_calling_no_stream",
			streamEnable: false,
			streamEdit:   false,
			useToolCall:  true,
			expected:     ResponseModeToolCalling,
		},
		{
			name:         "streaming_with_tools",
			streamEnable: true,
			streamEdit:   true,
			useToolCall:  true,
			expected:     ResponseModeStreamingWithTools,
		},
		{
			name:         "stream_enabled_but_no_edit",
			streamEnable: true,
			streamEdit:   false,
			useToolCall:  false,
			expected:     ResponseModeDirect,
		},
		{
			name:         "stream_enabled_but_no_edit_with_tools",
			streamEnable: true,
			streamEdit:   false,
			useToolCall:  true,
			expected:     ResponseModeToolCalling,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rh := NewResponseHandler(nil)
			got := rh.DetermineResponseMode(tt.streamEnable, tt.streamEdit, tt.useToolCall)
			if got != tt.expected {
				t.Errorf("DetermineResponseMode(%v, %v, %v) = %v, want %v",
					tt.streamEnable, tt.streamEdit, tt.useToolCall, got, tt.expected)
			}
		})
	}
}

// TestResponseContext 测试 ResponseContext 结构体。
func TestResponseContext(t *testing.T) {
	ctx := ResponseContext{
		UserID:   id.UserID("@user:example.com"),
		RoomID:   id.RoomID("!room:example.com"),
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "test"}},
		Model:    "gpt-4",
	}

	if ctx.UserID != "@user:example.com" {
		t.Errorf("UserID = %q, want @user:example.com", ctx.UserID)
	}
	if ctx.RoomID != "!room:example.com" {
		t.Errorf("RoomID = %q, want !room:example.com", ctx.RoomID)
	}
	if len(ctx.Messages) != 1 {
		t.Errorf("Messages length = %d, want 1", len(ctx.Messages))
	}
	if ctx.Model != "gpt-4" {
		t.Errorf("Model = %q, want gpt-4", ctx.Model)
	}
}
