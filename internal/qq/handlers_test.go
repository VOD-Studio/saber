// Package qq 提供 QQ 机器人的适配器实现。
package qq

import (
	"context"
	"errors"
	"testing"

	"github.com/tencent-connect/botgo/dto"

	"rua.plus/saber/internal/config"
)

// mockSimpleAIService 模拟 AI 服务。
type mockSimpleAIService struct {
	enabled      bool
	chatResponse string
	chatError    error
}

func (m *mockSimpleAIService) IsEnabled() bool {
	return m.enabled
}

func (m *mockSimpleAIService) ChatWithSystem(ctx context.Context, userID, systemPrompt, message string) (string, error) {
	if m.chatError != nil {
		return "", m.chatError
	}
	return m.chatResponse, nil
}

// TestNewDefaultHandler 测试创建 Handler。
func TestNewDefaultHandler(t *testing.T) {
	registry := NewCommandRegistry()
	aiConfig := &config.AIConfig{
		DirectChatAutoReply:   true,
		GroupChatMentionReply: true,
	}

	tests := []struct {
		name      string
		client    *Client
		aiService SimpleAIService
		registry  *CommandRegistry
		wantNil   bool
	}{
		{
			name:      "完整参数",
			client:    &Client{config: &config.QQConfig{}},
			aiService: &mockSimpleAIService{enabled: true},
			registry:  registry,
			wantNil:   false,
		},
		{
			name:      "nil AI服务",
			client:    &Client{config: &config.QQConfig{}},
			aiService: nil,
			registry:  registry,
			wantNil:   false,
		},
		{
			name:      "nil registry",
			client:    &Client{config: &config.QQConfig{}},
			aiService: &mockSimpleAIService{},
			registry:  nil,
			wantNil:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewDefaultHandler(tt.client, tt.aiService, aiConfig, tt.registry, nil, nil)
			if (handler == nil) != tt.wantNil {
				t.Errorf("NewDefaultHandler() = %v, wantNil %v", handler, tt.wantNil)
			}
		})
	}
}

// TestHandleReady 测试 Ready 事件处理。
func TestHandleReady(t *testing.T) {
	handler := NewDefaultHandler(nil, nil, &config.AIConfig{}, nil, nil, nil)

	event := &dto.WSPayload{}
	data := &dto.WSReadyData{
		Version:   1,
		SessionID: "test-session",
		Shard:     []uint32{0, 1},
	}

	// HandleReady 只记录日志，不应 panic
	handler.HandleReady(event, data)
}

// TestHandleC2CMessage_EmptyContent 测试空消息内容。
func TestHandleC2CMessage_EmptyContent(t *testing.T) {
	handler := NewDefaultHandler(nil, nil, &config.AIConfig{}, nil, nil, nil)

	event := &dto.WSPayload{}
	data := &dto.WSC2CMessageData{
		Author:  &dto.User{ID: "test-user"},
		Content: "",
	}

	// 空消息应该直接返回 nil
	err := handler.HandleC2CMessage(event, data)
	if err != nil {
		t.Errorf("expected nil for empty content, got: %v", err)
	}
}

// TestHandleC2CMessage_DisabledAutoReply 测试禁用自动回复。
func TestHandleC2CMessage_DisabledAutoReply(t *testing.T) {
	registry := NewCommandRegistry()
	// 不注册任何命令

	handler := NewDefaultHandler(
		nil,
		nil,
		&config.AIConfig{DirectChatAutoReply: false},
		registry,
		nil,
		nil,
	)

	event := &dto.WSPayload{}
	data := &dto.WSC2CMessageData{
		Author:  &dto.User{ID: "test-user"},
		Content: "hello",
	}

	// 自动回复禁用且无命令，应该返回 nil
	err := handler.HandleC2CMessage(event, data)
	if err != nil {
		t.Errorf("expected nil for disabled auto reply, got: %v", err)
	}
}

// TestHandleC2CMessage_AINotEnabled 测试 AI 服务未启用。
func TestHandleC2CMessage_AINotEnabled(t *testing.T) {
	registry := NewCommandRegistry()

	handler := NewDefaultHandler(
		nil,
		&mockSimpleAIService{enabled: false},
		&config.AIConfig{DirectChatAutoReply: true},
		registry,
		nil,
		nil,
	)

	event := &dto.WSPayload{}
	data := &dto.WSC2CMessageData{
		Author:  &dto.User{ID: "test-user"},
		Content: "hello",
	}

	// AI 未启用，应该返回 nil
	err := handler.HandleC2CMessage(event, data)
	if err != nil {
		t.Errorf("expected nil for AI not enabled, got: %v", err)
	}
}

// TestHandleGroupATMessage_EmptyContent 测试群@空消息。
func TestHandleGroupATMessage_EmptyContent(t *testing.T) {
	handler := NewDefaultHandler(nil, nil, &config.AIConfig{}, nil, nil, nil)

	event := &dto.WSPayload{}
	data := &dto.WSGroupATMessageData{
		GroupID: "test-group",
		Author:  &dto.User{ID: "test-user"},
		Content: "",
	}

	// 空消息应该直接返回 nil
	err := handler.HandleGroupATMessage(event, data)
	if err != nil {
		t.Errorf("expected nil for empty content, got: %v", err)
	}
}

// TestHandleGroupATMessage_OnlyMention 测试只有@没有内容。
func TestHandleGroupATMessage_OnlyMention(t *testing.T) {
	handler := NewDefaultHandler(nil, nil, &config.AIConfig{}, nil, nil, nil)

	event := &dto.WSPayload{}
	data := &dto.WSGroupATMessageData{
		GroupID: "test-group",
		Author:  &dto.User{ID: "test-user"},
		Content: "<@!123456>",
	}

	// 只有@没有内容，应该返回 nil
	err := handler.HandleGroupATMessage(event, data)
	if err != nil {
		t.Errorf("expected nil for only mention, got: %v", err)
	}
}

// TestHandleGroupATMessage_DisabledMentionReply 测试禁用群@回复。
func TestHandleGroupATMessage_DisabledMentionReply(t *testing.T) {
	registry := NewCommandRegistry()

	handler := NewDefaultHandler(
		nil,
		nil,
		&config.AIConfig{GroupChatMentionReply: false},
		registry,
		nil,
		nil,
	)

	event := &dto.WSPayload{}
	data := &dto.WSGroupATMessageData{
		GroupID: "test-group",
		Author:  &dto.User{ID: "test-user"},
		Content: "<@!123> hello",
	}

	// 群@回复禁用且无命令，应该返回 nil
	err := handler.HandleGroupATMessage(event, data)
	if err != nil {
		t.Errorf("expected nil for disabled mention reply, got: %v", err)
	}
}

// TestHandleC2CMessage_Command 测试私聊命令处理逻辑。
func TestHandleC2CMessage_Command(t *testing.T) {
	registry := NewCommandRegistry()
	registry.Register("ping", &PingCommand{}, "测试在线状态")

	// 创建一个记录发送内容的 mock sender
	var sentMsg string
	mockSender := &mockCommandSender{
		sendFunc: func(ctx context.Context, userID, groupID, message string) error {
			sentMsg = message
			return nil
		},
	}

	tests := []struct {
		name     string
		content  string
		wantMsg  string
		wantSent bool
	}{
		{
			name:     "ping命令",
			content:  "!ping",
			wantMsg:  "pong",
			wantSent: true,
		},
		{
			name:     "带空格的ping",
			content:  "  !ping  ",
			wantMsg:  "pong",
			wantSent: true,
		},
		{
			name:     "未注册命令",
			content:  "!unknown",
			wantSent: false,
		},
		{
			name:     "普通文本",
			content:  "hello world",
			wantSent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sentMsg = ""

			// 直接测试命令解析和分发
			parsed := registry.Parse(tt.content)
			if parsed == nil {
				if tt.wantSent {
					t.Error("expected command to be parsed")
				}
				return
			}

			found, err := registry.Dispatch(context.Background(), "test-user", "", parsed, mockSender)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if tt.wantSent && !found {
				t.Error("expected command to be found")
			}
			if tt.wantSent && sentMsg != tt.wantMsg {
				t.Errorf("sent message = %q, want %q", sentMsg, tt.wantMsg)
			}
		})
	}
}

// mockCommandSender 用于测试的命令发送器。
type mockCommandSender struct {
	sendFunc func(ctx context.Context, userID, groupID, message string) error
}

func (m *mockCommandSender) Send(ctx context.Context, userID, groupID, message string) error {
	if m.sendFunc != nil {
		return m.sendFunc(ctx, userID, groupID, message)
	}
	return nil
}

// TestSimpleAIServiceInterface 测试 SimpleAIService 接口实现。
func TestSimpleAIServiceInterface(t *testing.T) {
	var _ SimpleAIService = &mockSimpleAIService{}

	ai := &mockSimpleAIService{
		enabled:      true,
		chatResponse: "test response",
	}

	if !ai.IsEnabled() {
		t.Error("expected IsEnabled to be true")
	}

	resp, err := ai.ChatWithSystem(context.Background(), "user1", "system", "message")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if resp != "test response" {
		t.Errorf("response = %q, want %q", resp, "test response")
	}
}

// TestMockSimpleAIService_Error 测试 AI 服务返回错误。
func TestMockSimpleAIService_Error(t *testing.T) {
	testError := errors.New("test error")
	ai := &mockSimpleAIService{
		enabled:   true,
		chatError: testError,
	}

	_, err := ai.ChatWithSystem(context.Background(), "user1", "system", "message")
	if !errors.Is(err, testError) {
		t.Errorf("expected test error, got: %v", err)
	}
}