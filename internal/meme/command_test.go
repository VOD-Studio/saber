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
			name:            "普通关键词",
			args:            []string{"happy"},
			wantContentType: ContentTypeGIF,
			wantQuery:       "happy",
		},
		{
			name:            "多个关键词",
			args:            []string{"happy", "cat"},
			wantContentType: ContentTypeGIF,
			wantQuery:       "happy cat",
		},
		{
			name:            "--gif 标志",
			args:            []string{"--gif", "funny"},
			wantContentType: ContentTypeGIF,
			wantQuery:       "funny",
		},
		{
			name:            "--sticker 标志",
			args:            []string{"--sticker", "hello"},
			wantContentType: ContentTypeSticker,
			wantQuery:       "hello",
		},
		{
			name:            "--meme 标志",
			args:            []string{"--meme", "tired"},
			wantContentType: ContentTypeMeme,
			wantQuery:       "tired",
		},
		{
			name:            "只有标志无关键词",
			args:            []string{"--sticker"},
			wantContentType: ContentTypeSticker,
			wantQuery:       "",
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
		t.Error("expected error message to be sent")
	}

	expected := "请提供搜索关键词"
	if !strings.Contains(mockSvc.lastText, expected) {
		t.Errorf("expected text containing %q, got %q", expected, mockSvc.lastText)
	}
}

func TestTypedMemeCommand_Handle_EmptyQuery(t *testing.T) {
	mockSvc := &mockCommandService{}
	svc := createTestService()

	cmd := &TypedMemeCommand{
		service:     svc,
		cmdService:  mockSvc,
		contentType: ContentTypeGIF,
	}

	ctx := context.Background()
	err := cmd.Handle(ctx, "@user:example.com", "!room:example.com", []string{})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if mockSvc.lastText == "" {
		t.Error("expected error message to be sent")
	}

	expected := "请提供搜索关键词"
	if !strings.Contains(mockSvc.lastText, expected) {
		t.Errorf("expected text containing %q, got %q", expected, mockSvc.lastText)
	}
}

func TestTypedMemeCommand_Handle_ServiceNotEnabled(t *testing.T) {
	mockSvc := &mockCommandService{}
	svc := NewService(&config.MemeConfig{Enabled: false})

	cmd := &TypedMemeCommand{
		service:     svc,
		cmdService:  mockSvc,
		contentType: ContentTypeGIF,
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

func TestNewMemeCommand(t *testing.T) {
	// 测试构造函数返回非 nil
	cmd := NewMemeCommand(nil, nil, nil)
	if cmd == nil {
		t.Error("NewMemeCommand returned nil")
	}
}

func TestNewTypedMemeCommand(t *testing.T) {
	svc := createTestService()

	cmd := NewTypedMemeCommand(nil, nil, svc, ContentTypeSticker)
	if cmd == nil {
		t.Fatal("NewTypedMemeCommand returned nil")
	}

	if cmd.contentType != ContentTypeSticker {
		t.Errorf("expected contentType %v, got %v", ContentTypeSticker, cmd.contentType)
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