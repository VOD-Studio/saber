// Package ai_test 包含流式编辑器的单元测试。
package ai

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
)

// mockMessageSender 实现 MessageSender 接口用于测试。
type mockMessageSender struct {
	mu                         sync.Mutex
	sendTextCalls              []mockSendTextCall
	sendTextWithRelatesToCalls []mockSendTextWithRelatesToCall
	sendTextErr                error
	sendTextWithRelatesToErr   error
	nextEventID                id.EventID
}

type mockSendTextCall struct {
	roomID id.RoomID
	body   string
}

type mockSendTextWithRelatesToCall struct {
	roomID    id.RoomID
	body      string
	relatesTo *event.RelatesTo
}

func (m *mockMessageSender) SendText(ctx context.Context, roomID id.RoomID, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendTextCalls = append(m.sendTextCalls, mockSendTextCall{roomID, body})
	return m.sendTextErr
}

func (m *mockMessageSender) SendTextWithRelatesTo(ctx context.Context, roomID id.RoomID, body string, relatesTo *event.RelatesTo) (id.EventID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendTextWithRelatesToCalls = append(m.sendTextWithRelatesToCalls, mockSendTextWithRelatesToCall{roomID, body, relatesTo})
	if m.nextEventID == "" {
		m.nextEventID = "$event_id_1"
	}
	eventID := m.nextEventID
	m.nextEventID = ""
	return eventID, m.sendTextWithRelatesToErr
}

func (m *mockMessageSender) getSendTextCalls() []mockSendTextCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]mockSendTextCall, len(m.sendTextCalls))
	copy(result, m.sendTextCalls)
	return result
}

func (m *mockMessageSender) getSendTextWithRelatesToCalls() []mockSendTextWithRelatesToCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]mockSendTextWithRelatesToCall, len(m.sendTextWithRelatesToCalls))
	copy(result, m.sendTextWithRelatesToCalls)
	return result
}

// TestNewStreamEditor 测试 NewStreamEditor 构造函数。
func TestNewStreamEditor(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.DefaultStreamEditConfig()

	editor := NewStreamEditor(mock, roomID, "initial", cfg, "")

	if editor == nil {
		t.Fatal("NewStreamEditor returned nil")
		return
	}
	if editor.matrixService != mock {
		t.Error("matrixService not set correctly")
	}
	if editor.roomID != roomID {
		t.Error("roomID not set correctly")
	}
	if editor.initialMsg != "initial" {
		t.Error("initialMsg not set correctly")
	}
	if editor.stopped {
		t.Error("stopped should be false initially")
	}
	if editor.finalSent {
		t.Error("finalSent should be false initially")
	}
}

// TestStreamEditor_Start 测试 Start 方法。
func TestStreamEditor_Start(t *testing.T) {
	roomID := id.RoomID("!test:example.com")

	tests := []struct {
		name       string
		config     config.StreamEditConfig
		initialMsg string
		wantCalls  int
		wantErr    bool
		mockErr    error
	}{
		{
			name: "启用时有初始消息",
			config: config.StreamEditConfig{
				Enabled: true,
			},
			initialMsg: "Hello",
			wantCalls:  1,
			wantErr:    false,
		},
		{
			name: "启用时无初始消息",
			config: config.StreamEditConfig{
				Enabled: true,
			},
			initialMsg: "",
			wantCalls:  1,
			wantErr:    false,
		},
		{
			name: "禁用状态",
			config: config.StreamEditConfig{
				Enabled: false,
			},
			initialMsg: "Hello",
			wantCalls:  0,
			wantErr:    false,
		},
		{
			name: "发送错误",
			config: config.StreamEditConfig{
				Enabled: true,
			},
			initialMsg: "Hello",
			wantCalls:  1,
			wantErr:    true,
			mockErr:    errors.New("send failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockMessageSender{sendTextWithRelatesToErr: tt.mockErr}
			editor := NewStreamEditor(mock, roomID, tt.initialMsg, tt.config, "")

			ctx := context.Background()
			err := editor.Start(ctx)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			calls := mock.getSendTextWithRelatesToCalls()
			if len(calls) != tt.wantCalls {
				t.Errorf("expected %d calls, got %d", tt.wantCalls, len(calls))
			}

			if tt.wantCalls > 0 && tt.config.Enabled {
				expectedContent := tt.initialMsg
				if expectedContent == "" {
					expectedContent = "..."
				}
				if calls[0].body != expectedContent {
					t.Errorf("expected body %q, got %q", expectedContent, calls[0].body)
				}
			}
		})
	}
}

// TestStreamEditor_Update 测试 Update 方法。
func TestStreamEditor_Update(t *testing.T) {
	roomID := id.RoomID("!test:example.com")

	tests := []struct {
		name            string
		config          config.StreamEditConfig
		setupEditor     func(*StreamEditor)
		content         string
		wantErr         bool
		wantSendText    int
		wantSendWithRel int
	}{
		{
			name: "禁用状态 - 直接发送",
			config: config.StreamEditConfig{
				Enabled: false,
			},
			content:         "Hello",
			wantSendText:    1,
			wantSendWithRel: 0,
		},
		{
			name: "已停止 - 无操作",
			config: config.StreamEditConfig{
				Enabled: true,
			},
			setupEditor: func(e *StreamEditor) {
				e.stopped = true
			},
			content:         "Hello",
			wantSendText:    0,
			wantSendWithRel: 0,
		},
		{
			name: "已发送最终消息 - 无操作",
			config: config.StreamEditConfig{
				Enabled: true,
			},
			setupEditor: func(e *StreamEditor) {
				e.finalSent = true
			},
			content:         "Hello",
			wantSendText:    0,
			wantSendWithRel: 0,
		},
		{
			name: "无消息 ID - 发送新消息",
			config: config.StreamEditConfig{
				Enabled:  true,
				MaxEdits: 5,
			},
			content:         "Hello",
			wantSendText:    0,
			wantSendWithRel: 1,
		},
		{
			name: "达到最大编辑次数 - 无操作",
			config: config.StreamEditConfig{
				Enabled:  true,
				MaxEdits: 2,
			},
			setupEditor: func(e *StreamEditor) {
				e.messageID = "$existing"
				e.editCount = 2
			},
			content:         "Hello",
			wantSendText:    0,
			wantSendWithRel: 0,
		},
		{
			name: "间隔时间过短 - 无操作",
			config: config.StreamEditConfig{
				Enabled:        true,
				MaxEdits:       5,
				EditIntervalMs: 1000,
			},
			setupEditor: func(e *StreamEditor) {
				e.messageID = "$existing"
				e.editCount = 1
				e.lastEditTime = time.Now()
			},
			content:         "Hello",
			wantSendText:    0,
			wantSendWithRel: 0,
		},
		{
			name: "成功编辑",
			config: config.StreamEditConfig{
				Enabled:        true,
				MaxEdits:       5,
				EditIntervalMs: 0,
			},
			setupEditor: func(e *StreamEditor) {
				e.messageID = "$existing"
				e.editCount = 0
			},
			content:         "Hello",
			wantSendText:    0,
			wantSendWithRel: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockMessageSender{}
			editor := NewStreamEditor(mock, roomID, "", tt.config, "")

			if tt.setupEditor != nil {
				tt.setupEditor(editor)
			}

			ctx := context.Background()
			err := editor.Update(ctx, tt.content)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			sendTextCalls := mock.getSendTextCalls()
			if len(sendTextCalls) != tt.wantSendText {
				t.Errorf("expected %d SendText calls, got %d", tt.wantSendText, len(sendTextCalls))
			}

			sendWithRelCalls := mock.getSendTextWithRelatesToCalls()
			if len(sendWithRelCalls) != tt.wantSendWithRel {
				t.Errorf("expected %d SendTextWithRelatesTo calls, got %d", tt.wantSendWithRel, len(sendWithRelCalls))
			}
		})
	}
}

// TestStreamEditor_SendFinal 测试 SendFinal 方法。
func TestStreamEditor_SendFinal(t *testing.T) {
	roomID := id.RoomID("!test:example.com")

	tests := []struct {
		name        string
		config      config.StreamEditConfig
		setupEditor func(*StreamEditor)
		content     string
		wantCalls   int
		wantErr     bool
	}{
		{
			name: "首次调用 - 发送最终消息",
			config: config.StreamEditConfig{
				Enabled: true,
			},
			setupEditor: func(e *StreamEditor) {
				e.messageID = "$existing"
			},
			content:   "Final content",
			wantCalls: 1,
		},
		{
			name: "第二次调用 - 幂等",
			config: config.StreamEditConfig{
				Enabled: true,
			},
			setupEditor: func(e *StreamEditor) {
				e.messageID = "$existing"
				e.finalSent = true
			},
			content:   "Final content",
			wantCalls: 0,
		},
		{
			name: "禁用状态 - 直接发送",
			config: config.StreamEditConfig{
				Enabled: false,
			},
			content:   "Final content",
			wantCalls: 0,
		},
		{
			name: "无消息 ID - 发送新消息",
			config: config.StreamEditConfig{
				Enabled: true,
			},
			content:   "Final content",
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockMessageSender{}
			editor := NewStreamEditor(mock, roomID, "", tt.config, "")

			if tt.setupEditor != nil {
				tt.setupEditor(editor)
			}

			ctx := context.Background()
			err := editor.SendFinal(ctx, tt.content)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			calls := mock.getSendTextWithRelatesToCalls()
			if len(calls) != tt.wantCalls {
				t.Errorf("expected %d calls, got %d", tt.wantCalls, len(calls))
			}
		})
	}
}

// TestStreamEditor_SendFinal_Idempotency 测试 SendFinal 的幂等性。
func TestStreamEditor_SendFinal_Idempotency(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{Enabled: true}

	editor := NewStreamEditor(mock, roomID, "", cfg, "")
	ctx := context.Background()

	for i := range 5 {
		err := editor.SendFinal(ctx, "Final content")
		if err != nil {
			t.Errorf("call %d: unexpected error: %v", i, err)
		}
	}

	calls := mock.getSendTextWithRelatesToCalls()
	if len(calls) != 1 {
		t.Errorf("expected 1 call (idempotent), got %d", len(calls))
	}
}

// TestStreamEditor_Stop 测试 Stop 和 IsStopped 方法。
func TestStreamEditor_Stop(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{Enabled: true}

	editor := NewStreamEditor(mock, roomID, "", cfg, "")

	if editor.IsStopped() {
		t.Error("editor should not be stopped initially")
	}

	editor.Stop()

	if !editor.IsStopped() {
		t.Error("editor should be stopped after Stop()")
	}
}

// TestStreamEditor_MaxEdits 测试最大编辑次数限制。
func TestStreamEditor_MaxEdits(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{
		Enabled:        true,
		MaxEdits:       3,
		EditIntervalMs: 0,
	}

	editor := NewStreamEditor(mock, roomID, "", cfg, "")
	ctx := context.Background()

	_ = editor.Start(ctx)

	for range 10 {
		_ = editor.Update(ctx, "content %d")
	}

	calls := mock.getSendTextWithRelatesToCalls()
	expectedCalls := 4 // 1 start + 3 edits max
	if len(calls) != expectedCalls {
		t.Errorf("expected %d calls, got %d", expectedCalls, len(calls))
	}
}

// TestStreamEditor_EditInterval 测试编辑间隔限制。
func TestStreamEditor_EditInterval(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{
		Enabled:        true,
		MaxEdits:       10,
		EditIntervalMs: 100,
	}

	editor := NewStreamEditor(mock, roomID, "", cfg, "")
	ctx := context.Background()

	_ = editor.Start(ctx)

	_ = editor.Update(ctx, "content 1")
	time.Sleep(150 * time.Millisecond)
	_ = editor.Update(ctx, "content 2")
	_ = editor.Update(ctx, "content 3")

	calls := mock.getSendTextWithRelatesToCalls()
	expectedCalls := 3 // 启动 + 2 次编辑（第三次因间隔跳过）
	if len(calls) != expectedCalls {
		t.Errorf("expected %d calls, got %d", expectedCalls, len(calls))
	}
}

// TestStreamEditor_Concurrency 测试并发安全性。
func TestStreamEditor_Concurrency(t *testing.T) {
	mock := &mockMessageSender{}
	roomID := id.RoomID("!test:example.com")
	cfg := config.StreamEditConfig{
		Enabled:        true,
		MaxEdits:       100,
		EditIntervalMs: 0,
	}

	editor := NewStreamEditor(mock, roomID, "", cfg, "")
	ctx := context.Background()

	_ = editor.Start(ctx)

	const goroutines = 50
	var wg sync.WaitGroup

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = editor.Update(ctx, "concurrent update")
		}()
	}

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = editor.SendFinal(ctx, "final")
		}()
	}

	wg.Wait()

	_ = editor.IsStopped()
}
