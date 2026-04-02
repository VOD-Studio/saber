// Package commands 提供 Matrix 机器人的命令处理实现。
package commands

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"
)

// mockBuildInfoProvider 测试用的 BuildInfoProvider。
type mockBuildInfoProvider struct {
	info *BuildInfo
}

func (m *mockBuildInfoProvider) GetBuildInfo() *BuildInfo {
	return m.info
}

// TestBuildInfo_RuntimePlatform 测试 RuntimePlatform 方法。
func TestBuildInfo_RuntimePlatform(t *testing.T) {
	info := BuildInfo{
		Version:       "1.0.0",
		GitCommit:     "abc123",
		GitBranch:     "main",
		BuildTime:     "2024-01-01",
		GoVersion:     "go1.21.0",
		BuildPlatform: "linux/amd64",
	}

	platform := info.RuntimePlatform()
	if platform == "" {
		t.Error("RuntimePlatform should not be empty")
	}

	// 应该包含 GOOS/GOARCH 格式
	// 例如: darwin/arm64, linux/amd64
}

// TestNewVersionCommand 测试 VersionCommand 构造函数。
func TestNewVersionCommand(t *testing.T) {
	sender := &mockSender{}
	provider := &mockBuildInfoProvider{info: &BuildInfo{Version: "test"}}

	cmd := NewVersionCommand(sender, provider)
	if cmd == nil {
		t.Fatal("NewVersionCommand() returned nil")
	}
}

// TestVersionCommand_Handle 测试 VersionCommand.Handle。
func TestVersionCommand_Handle(t *testing.T) {
	sender := &mockSender{}
	info := &BuildInfo{
		Version:       "1.0.0",
		GitCommit:     "abc123",
		GitBranch:     "main",
		BuildTime:     "2024-01-01",
		GoVersion:     "go1.21.0",
		BuildPlatform: "linux/amd64",
	}
	provider := &mockBuildInfoProvider{info: info}

	cmd := NewVersionCommand(sender, provider)

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

	// 验证 HTML 包含版本信息
	if sender.lastHTML == "" {
		t.Error("HTML should not be empty")
	}

	// 验证纯文本包含版本信息
	if sender.lastPlain == "" {
		t.Error("plain should not be empty")
	}
}

// TestVersionCommand_Handle_NilInfo 测试 BuildInfo 为 nil 的情况。
func TestVersionCommand_Handle_NilInfo(t *testing.T) {
	sender := &mockSender{}
	provider := &mockBuildInfoProvider{info: nil}

	cmd := NewVersionCommand(sender, provider)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}

	// 验证发送了错误消息
	expectedPlain := "版本信息不可用"
	if sender.lastPlain != expectedPlain {
		t.Errorf("plain = %q, want %q", sender.lastPlain, expectedPlain)
	}
}

// TestVersionCommand_Handle_WithError 测试发送失败场景。
func TestVersionCommand_Handle_WithError(t *testing.T) {
	expectedErr := context.Canceled
	sender := &mockSender{err: expectedErr}
	provider := &mockBuildInfoProvider{info: &BuildInfo{Version: "test"}}

	cmd := NewVersionCommand(sender, provider)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != expectedErr {
		t.Errorf("Handle() error = %v, want %v", err, expectedErr)
	}
}