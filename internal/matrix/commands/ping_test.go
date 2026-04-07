// Package commands 提供 Matrix 机器人的命令处理实现。
package commands

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"
)

// mockSender 是测试用的 Mock Sender。
type mockSender struct {
	lastRoomID id.RoomID
	lastHTML   string
	lastPlain  string
	err        error
}

func (m *mockSender) SendFormattedText(_ context.Context, roomID id.RoomID, html, plain string) error {
	m.lastRoomID = roomID
	m.lastHTML = html
	m.lastPlain = plain
	return m.err
}

// TestNewPingCommand 测试 PingCommand 构造函数。
func TestNewPingCommand(t *testing.T) {
	sender := &mockSender{}
	cmd := NewPingCommand(sender)

	if cmd == nil {
		t.Fatal("NewPingCommand() returned nil")
	}
	if cmd.sender != sender {
		t.Error("sender mismatch")
	}
}

// TestPingCommand_Handle 测试 PingCommand.Handle。
func TestPingCommand_Handle(t *testing.T) {
	sender := &mockSender{}
	cmd := NewPingCommand(sender)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}

	// 验证发送的房间 ID
	if sender.lastRoomID != roomID {
		t.Errorf("roomID = %q, want %q", sender.lastRoomID, roomID)
	}

	// 验证 HTML 内容
	expectedHTML := "<strong>🏓 Pong!</strong>"
	if sender.lastHTML != expectedHTML {
		t.Errorf("HTML = %q, want %q", sender.lastHTML, expectedHTML)
	}

	// 验证纯文本内容
	expectedPlain := "🏓 Pong!"
	if sender.lastPlain != expectedPlain {
		t.Errorf("plain = %q, want %q", sender.lastPlain, expectedPlain)
	}
}

// TestPingCommand_Handle_WithError 测试发送失败场景。
func TestPingCommand_Handle_WithError(t *testing.T) {
	expectedErr := context.Canceled
	sender := &mockSender{err: expectedErr}
	cmd := NewPingCommand(sender)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, []string{"ignored"})
	if err != expectedErr {
		t.Errorf("Handle() error = %v, want %v", err, expectedErr)
	}
}

// TestPingCommand_Handle_IgnoresArgs 测试 PingCommand 忽略参数。
func TestPingCommand_Handle_IgnoresArgs(t *testing.T) {
	sender := &mockSender{}
	cmd := NewPingCommand(sender)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	// 传递参数，但 ping 命令应该忽略它们
	err := cmd.Handle(ctx, userID, roomID, []string{"extra", "args"})
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}

	// 验证消息内容不变
	if sender.lastPlain != "🏓 Pong!" {
		t.Errorf("plain = %q, want %q", sender.lastPlain, "🏓 Pong!")
	}
}
