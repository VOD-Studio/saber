// Package ai_test 包含流式处理器的单元测试。
package ai

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
)

// TestNewSmartStreamHandler 测试 NewSmartStreamHandler 构造函数。
func TestNewSmartStreamHandler(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.DefaultStreamEditConfig()
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewSmartStreamHandler(editor, 10, 1000)

	if handler == nil {
		t.Fatal("NewSmartStreamHandler returned nil")
	}
	if handler.editor != editor {
		t.Error("editor not set correctly")
	}
	if handler.charThreshold != 10 {
		t.Errorf("charThreshold = %d, want 10", handler.charThreshold)
	}
	if handler.timeThreshold != 1000*time.Millisecond {
		t.Errorf("timeThreshold = %v, want 1000ms", handler.timeThreshold)
	}
}

// TestSmartStreamHandler_OnChunk_CharThreshold 测试字符阈值触发。
func TestSmartStreamHandler_OnChunk_CharThreshold(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{Enabled: true, MaxEdits: 10, EditIntervalMs: 0}
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewSmartStreamHandler(editor, 10, 10000)
	ctx := context.Background()

	handler.OnChunk(ctx, "12345")
	if handler.hasStartedEditing {
		t.Error("should not start editing before char threshold")
	}

	handler.OnChunk(ctx, "67890")
	if !handler.hasStartedEditing {
		t.Error("should start editing after char threshold")
	}

	handler.OnChunk(ctx, "more content")

	calls := mock.getSendTextWithRelatesToCalls()
	if len(calls) < 2 {
		t.Errorf("expected at least 2 calls, got %d", len(calls))
	}
}

// TestSmartStreamHandler_OnChunk_TimeThreshold 测试时间阈值触发。
func TestSmartStreamHandler_OnChunk_TimeThreshold(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{Enabled: true, MaxEdits: 10, EditIntervalMs: 0}
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewSmartStreamHandler(editor, 1000, 100)
	ctx := context.Background()

	handler.OnChunk(ctx, "small")
	if handler.hasStartedEditing {
		t.Error("should not start editing immediately")
	}

	time.Sleep(150 * time.Millisecond)

	handler.OnChunk(ctx, "more")
	if !handler.hasStartedEditing {
		t.Error("should start editing after time threshold")
	}
}

// TestSmartStreamHandler_OnComplete 测试 OnComplete 方法。
func TestSmartStreamHandler_OnComplete(t *testing.T) {
	tests := []struct {
		name         string
		setupHandler func(*SmartStreamHandler)
		finalContent string
		wantCalls    int
	}{
		{
			name: "not started - starts and sends final",
			setupHandler: func(h *SmartStreamHandler) {
				h.hasStartedEditing = false
			},
			finalContent: "Final content",
			wantCalls:    2, // Start sends one, SendFinal sends another
		},
		{
			name: "already started - just sends final",
			setupHandler: func(h *SmartStreamHandler) {
				h.hasStartedEditing = true
				h.editor.messageID = "$existing"
			},
			finalContent: "Final content",
			wantCalls:    1, // Only SendFinal
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockMessageSender{}
			roomID := id.RoomID("!test:example.com")
			cfg := config.StreamEditConfig{Enabled: true, MaxEdits: 10}
			editor := NewStreamEditor(mock, roomID, "", cfg, "")

			handler := NewSmartStreamHandler(editor, 10, 1000)
			if tt.setupHandler != nil {
				tt.setupHandler(handler)
			}

			ctx := context.Background()
			handler.OnComplete(ctx, tt.finalContent, openai.Usage{}, "gpt-4")

			calls := mock.getSendTextWithRelatesToCalls()
			if len(calls) != tt.wantCalls {
				t.Errorf("expected %d calls, got %d", tt.wantCalls, len(calls))
			}
		})
	}
}

// TestSmartStreamHandler_OnError 测试 OnError 方法。
func TestSmartStreamHandler_OnError(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{Enabled: true}
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewSmartStreamHandler(editor, 10, 1000)
	ctx := context.Background()

	testErr := errors.New("test error")
	handler.OnError(ctx, testErr)

	if !editor.IsStopped() {
		t.Error("editor should be stopped after OnError")
	}

	calls := mock.getSendTextCalls()
	if len(calls) != 1 {
		t.Errorf("expected 1 SendText call for error message, got %d", len(calls))
	}

	if len(calls) > 0 {
		expectedPrefix := "❌ AI 服务出错："
		if len(calls[0].body) < len(expectedPrefix) {
			t.Errorf("error message too short: %s", calls[0].body)
		}
	}
}

// TestSmartStreamHandler_ContentAccumulation 测试内容累积。
func TestSmartStreamHandler_ContentAccumulation(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{Enabled: true, MaxEdits: 10, EditIntervalMs: 0}
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewSmartStreamHandler(editor, 5, 10000)
	ctx := context.Background()

	chunks := []string{"Hello", " ", "World", "!"}
	for _, chunk := range chunks {
		handler.OnChunk(ctx, chunk)
	}

	if handler.accumulatedContent != "Hello World!" {
		t.Errorf("accumulatedContent = %q, want %q", handler.accumulatedContent, "Hello World!")
	}
}

// TestSmartStreamHandler_Concurrency 测试并发安全性。
func TestSmartStreamHandler_Concurrency(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{Enabled: true, MaxEdits: 100, EditIntervalMs: 0}
	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	handler := NewSmartStreamHandler(editor, 1, 1)
	ctx := context.Background()

	const goroutines = 50
	var wg sync.WaitGroup

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler.OnChunk(ctx, "chunk")
		}()
	}

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler.OnComplete(ctx, "final", openai.Usage{}, "model")
		}()
	}

	wg.Wait()
}

// TestSmartStreamHandler_DualThreshold 测试双阈值机制。
func TestSmartStreamHandler_DualThreshold(t *testing.T) {
	t.Run("char threshold triggers first", func(t *testing.T) {
		mock := &mockMessageSender{}
		roomID := id.RoomID("!test:example.com")
		cfg := config.StreamEditConfig{Enabled: true, MaxEdits: 10, EditIntervalMs: 0}
		editor := NewStreamEditor(mock, roomID, "", cfg, "")

		handler := NewSmartStreamHandler(editor, 5, 10000)
		ctx := context.Background()

		startTime := time.Now()
		handler.OnChunk(ctx, "12345")

		if !handler.hasStartedEditing {
			t.Error("should start editing by char threshold")
		}

		elapsed := time.Since(startTime)
		if elapsed > 100*time.Millisecond {
			t.Error("char threshold should trigger quickly, not wait for time threshold")
		}
	})

	t.Run("time threshold triggers when char not reached", func(t *testing.T) {
		mock := &mockMessageSender{}
		roomID := id.RoomID("!test:example.com")
		cfg := config.StreamEditConfig{Enabled: true, MaxEdits: 10, EditIntervalMs: 0}
		editor := NewStreamEditor(mock, roomID, "", cfg, "")

		handler := NewSmartStreamHandler(editor, 1000, 50)
		ctx := context.Background()

		handler.OnChunk(ctx, "small")

		if handler.hasStartedEditing {
			t.Error("should not start by char threshold")
		}

		time.Sleep(60 * time.Millisecond)

		handler.OnChunk(ctx, "more")

		if !handler.hasStartedEditing {
			t.Error("should start by time threshold")
		}
	})
}

// TestSmartStreamHandler_EdgeCases 测试边界情况。
func TestSmartStreamHandler_EdgeCases(t *testing.T) {
	t.Run("empty chunks", func(t *testing.T) {
		mock := &mockMessageSender{}
		roomID := id.RoomID("!test:example.com")
		cfg := config.StreamEditConfig{Enabled: true}
		editor := NewStreamEditor(mock, roomID, "", cfg, "")

		handler := NewSmartStreamHandler(editor, 1, 1)
		ctx := context.Background()

		handler.OnChunk(ctx, "")
		handler.OnChunk(ctx, "")

		if handler.accumulatedContent != "" {
			t.Errorf("accumulatedContent should be empty, got %q", handler.accumulatedContent)
		}
	})

	t.Run("nil context", func(t *testing.T) {
		mock := &mockMessageSender{}
		roomID := id.RoomID("!test:example.com")
		cfg := config.StreamEditConfig{Enabled: true}
		editor := NewStreamEditor(mock, roomID, "", cfg, "")

		handler := NewSmartStreamHandler(editor, 1, 1)

		handler.OnChunk(context.Background(), "test")
		handler.OnComplete(context.Background(), "final", openai.Usage{}, "model")
		handler.OnError(context.Background(), errors.New("test"))
	})
}
