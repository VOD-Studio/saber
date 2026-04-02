// Package commands 提供 Matrix 机器人命令测试。
package commands

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"
)

// mockMemeService 实现 MemeService 接口用于测试。
type mockMemeService struct {
	enabled  bool
	searchFn func(ctx context.Context, query string, contentType int) (*MemeResult, error)
}

func (m *mockMemeService) IsEnabled() bool {
	return m.enabled
}

func (m *mockMemeService) Search(ctx context.Context, query string, contentType int) (*MemeResult, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, query, contentType)
	}
	return &MemeResult{
		ID:       "test-id",
		Title:    "Test Meme",
		URL:      "https://example.com/meme.gif",
		MimeType: "image/gif",
		Width:    200,
		Height:   200,
	}, nil
}

func (m *mockMemeService) Download(ctx context.Context, meme *MemeResult) ([]byte, error) {
	return []byte("fake-image-data"), nil
}

// TestNewMemeCommand 测试 NewMemeCommand 构造函数。
func TestNewMemeCommand(t *testing.T) {
	sender := &mockSender{}
	text := &mockTextSender{}
	svc := &mockMemeService{enabled: true}

	cmd := NewMemeCommand(sender, text, svc)
	if cmd == nil {
		t.Error("NewMemeCommand returned nil")
	}
}

// TestMemeCommand_Handle_ServiceDisabled 测试服务未启用的情况。
func TestMemeCommand_Handle_ServiceDisabled(t *testing.T) {
	sender := &mockSender{}
	text := &mockTextSender{}
	svc := &mockMemeService{enabled: false}

	cmd := NewMemeCommand(sender, text, svc)

	ctx := context.Background()
	userID := id.UserID("@user:example.com")
	roomID := id.RoomID("!room:example.com")

	err := cmd.Handle(ctx, userID, roomID, []string{"happy"})
	if err != nil {
		t.Errorf("Handle error: %v", err)
	}

	expected := "Meme 服务未启用"
	if text.lastBody == "" || len(text.lastBody) < len(expected) {
		t.Errorf("expected text containing %q, got %q", expected, text.lastBody)
	}
}

// TestMemeCommand_Handle_NilService 测试服务为 nil 的情况。
func TestMemeCommand_Handle_NilService(t *testing.T) {
	sender := &mockSender{}
	text := &mockTextSender{}

	cmd := NewMemeCommand(sender, text, nil)

	ctx := context.Background()
	userID := id.UserID("@user:example.com")
	roomID := id.RoomID("!room:example.com")

	err := cmd.Handle(ctx, userID, roomID, []string{"happy"})
	if err != nil {
		t.Errorf("Handle error: %v", err)
	}

	expected := "Meme 服务未启用"
	if text.lastBody == "" || len(text.lastBody) < len(expected) {
		t.Errorf("expected text containing %q, got %q", expected, text.lastBody)
	}
}

// TestMemeCommand_Handle_EmptyArgs 测试空参数的情况。
func TestMemeCommand_Handle_EmptyArgs(t *testing.T) {
	sender := &mockSender{}
	text := &mockTextSender{}
	svc := &mockMemeService{enabled: true}

	cmd := NewMemeCommand(sender, text, svc)

	ctx := context.Background()
	userID := id.UserID("@user:example.com")
	roomID := id.RoomID("!room:example.com")

	err := cmd.Handle(ctx, userID, roomID, []string{})
	if err != nil {
		t.Errorf("Handle error: %v", err)
	}

	expected := "请提供搜索关键词"
	if text.lastBody == "" || len(text.lastBody) < len(expected) {
		t.Errorf("expected text containing %q, got %q", expected, text.lastBody)
	}
}

// TestMemeCommand_Handle_WithArgs 测试有参数的情况。
func TestMemeCommand_Handle_WithArgs(t *testing.T) {
	sender := &mockSender{}
	text := &mockTextSender{}
	svc := &mockMemeService{enabled: true}

	cmd := NewMemeCommand(sender, text, svc)

	ctx := context.Background()
	userID := id.UserID("@user:example.com")
	roomID := id.RoomID("!room:example.com")

	err := cmd.Handle(ctx, userID, roomID, []string{"happy", "cat"})
	if err != nil {
		t.Errorf("Handle error: %v", err)
	}

	if text.lastBody == "" {
		t.Error("expected response text")
	}
}