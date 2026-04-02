package meme

import (
	"context"
	"strings"
	"testing"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// mockCommandService 是用于测试的模拟命令服务。
type mockCommandService struct {
	lastText   string
	lastRoomID id.RoomID
}

func (m *mockCommandService) SendText(ctx context.Context, roomID id.RoomID, body string) error {
	m.lastText = body
	m.lastRoomID = roomID
	return nil
}

func (m *mockCommandService) SendReply(ctx context.Context, roomID id.RoomID, body string, replyTo id.EventID) (id.EventID, error) {
	m.lastText = body
	m.lastRoomID = roomID
	return "event123", nil
}

// createTestService 创建测试用的 meme 服务。
func createTestService() *Service {
	return NewService(&config.MemeConfig{
		Enabled:        true,
		APIKey:         "test-key",
		MaxResults:     5,
		TimeoutSeconds: 10,
	})
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name            string
		args            []string
		wantContentType ContentType
		wantQuery       string
	}{
		{
			name:            "空参数",
			args:            []string{},
			wantContentType: ContentTypeGIF,
			wantQuery:       "",
		},
		{
			name:            "普通关键词（默认 GIF）",
			args:            []string{"happy"},
			wantContentType: ContentTypeGIF,
			wantQuery:       "happy",
		},
		{
			name:            "多个关键词（默认 GIF）",
			args:            []string{"happy", "cat"},
			wantContentType: ContentTypeGIF,
			wantQuery:       "happy cat",
		},
		{
			name:            "子命令 gif 正常解析",
			args:            []string{"gif", "funny"},
			wantContentType: ContentTypeGIF,
			wantQuery:       "funny",
		},
		{
			name:            "子命令 gif 多关键词",
			args:            []string{"gif", "funny", "cat"},
			wantContentType: ContentTypeGIF,
			wantQuery:       "funny cat",
		},
		{
			name:            "子命令 sticker 正常解析",
			args:            []string{"sticker", "hello"},
			wantContentType: ContentTypeSticker,
			wantQuery:       "hello",
		},
		{
			name:            "子命令 meme 正常解析",
			args:            []string{"meme", "tired"},
			wantContentType: ContentTypeMeme,
			wantQuery:       "tired",
		},
		{
			name:            "子命令 gif 无关键词",
			args:            []string{"gif"},
			wantContentType: ContentTypeGIF,
			wantQuery:       "",
		},
		{
			name:            "子命令 sticker 无关键词",
			args:            []string{"sticker"},
			wantContentType: ContentTypeSticker,
			wantQuery:       "",
		},
		{
			name:            "无子命令默认为 GIF",
			args:            []string{"default", "search"},
			wantContentType: ContentTypeGIF,
			wantQuery:       "default search",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contentType, query := parseArgs(tt.args)
			if contentType != tt.wantContentType {
				t.Errorf("parseArgs() contentType = %v, want %v", contentType, tt.wantContentType)
			}
			if query != tt.wantQuery {
				t.Errorf("parseArgs() query = %v, want %v", query, tt.wantQuery)
			}
		})
	}
}

func TestMemeCommand_Handle_ServiceNotEnabled(t *testing.T) {
	mockSvc := &mockCommandService{}
	svc := NewService(&config.MemeConfig{Enabled: false})

	cmd := &MemeCommand{
		service:    svc,
		cmdService: mockSvc,
	}

	ctx := context.Background()
	err := cmd.Handle(ctx, "@user:example.com", "!room:example.com", []string{"happy"})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if mockSvc.lastText == "" {
		t.Error("expected error message to be sent")
	}

	expected := "Meme 服务未启用"
	if !strings.Contains(mockSvc.lastText, expected) {
		t.Errorf("expected text containing %q, got %q", expected, mockSvc.lastText)
	}
}

func TestMemeCommand_Handle_EmptyQuery(t *testing.T) {
	mockSvc := &mockCommandService{}
	svc := createTestService()

	cmd := &MemeCommand{
		service:    svc,
		cmdService: mockSvc,
	}

	ctx := context.Background()
	err := cmd.Handle(ctx, "@user:example.com", "!room:example.com", []string{})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if mockSvc.lastText == "" {
		t.Error("expected help message to be sent")
	}

	expected := "Meme 命令用法"
	if !strings.Contains(mockSvc.lastText, expected) {
		t.Errorf("expected text containing %q, got %q", expected, mockSvc.lastText)
	}
}

func TestNewMemeCommand(t *testing.T) {
	// 测试构造函数返回非 nil
	cmd := NewMemeCommand(nil, nil, nil)
	if cmd == nil {
		t.Error("NewMemeCommand returned nil")
	}
}

func TestMemeCommand_WithEventContext(t *testing.T) {
	// 测试 EventID 上下文注入
	ctx := context.Background()
	eventID := id.EventID("$event123")
	ctx = matrix.WithEventID(ctx, eventID)

	// 验证上下文中的 EventID
	retrievedID := matrix.GetEventID(ctx)
	if retrievedID != eventID {
		t.Errorf("expected eventID %v, got %v", eventID, retrievedID)
	}
}

// mockMemeService 是用于测试的模拟 meme 服务。
type mockMemeService struct {
	enabled   bool
	gifResult *GIF
	err       error
}

func (m *mockMemeService) IsEnabled() bool {
	return m.enabled
}

func (m *mockMemeService) GetRandom(ctx context.Context, query string, contentType ContentType) (*GIF, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.gifResult, nil
}

func (m *mockMemeService) DownloadImage(ctx context.Context, gif *GIF) ([]byte, error) {
	return []byte("fake-image-data"), nil
}

// TestMemeCommand_Handle_NilService 测试服务为 nil 的情况。
func TestMemeCommand_Handle_NilService(t *testing.T) {
	mockSvc := &mockCommandService{}

	cmd := &MemeCommand{
		service:    nil,
		cmdService: mockSvc,
	}

	ctx := context.Background()
	err := cmd.Handle(ctx, "@user:example.com", "!room:example.com", []string{"happy"})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if mockSvc.lastText == "" {
		t.Error("expected error message to be sent")
	}

	expected := "Meme 服务未启用"
	if !strings.Contains(mockSvc.lastText, expected) {
		t.Errorf("expected text containing %q, got %q", expected, mockSvc.lastText)
	}
}
