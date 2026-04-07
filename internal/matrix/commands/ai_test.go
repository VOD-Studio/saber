// Package commands 提供 Matrix 机器人的命令处理实现。
package commands

import (
	"context"
	"errors"
	"testing"

	"maunium.net/go/mautrix/id"
)

// mockAIService 是测试用的 Mock AIService。
type mockAIService struct {
	chatResponse   string
	chatError      error
	contextInfo    string
	models         []string
	currentModel   string
	setModelError  error
	clearedContext bool
	clearedRoomID  id.RoomID
	clearedUserID  id.UserID
}

func (m *mockAIService) Chat(_ context.Context, _ id.RoomID, _ id.UserID, _ string) (string, error) {
	return m.chatResponse, m.chatError
}

func (m *mockAIService) ClearContext(roomID id.RoomID, userID id.UserID) {
	m.clearedContext = true
	m.clearedRoomID = roomID
	m.clearedUserID = userID
}

func (m *mockAIService) GetContextInfo(_ id.RoomID, _ id.UserID) string {
	return m.contextInfo
}

func (m *mockAIService) ListModels() []string {
	return m.models
}

func (m *mockAIService) GetCurrentModel() string {
	return m.currentModel
}

func (m *mockAIService) SetModel(model string) error {
	m.currentModel = model
	return m.setModelError
}

// mockTextSender 是测试用的 Mock TextOnlySender。
type mockTextSender struct {
	lastRoomID id.RoomID
	lastBody   string
	err        error
}

func (m *mockTextSender) SendText(_ context.Context, roomID id.RoomID, body string) error {
	m.lastRoomID = roomID
	m.lastBody = body
	return m.err
}

// TestNewAICommand 测试 AICommand 构造函数。
func TestNewAICommand(t *testing.T) {
	sender := &mockSender{}
	text := &mockTextSender{}
	svc := &mockAIService{}

	cmd := NewAICommand(sender, text, svc)

	if cmd == nil {
		t.Fatal("NewAICommand() returned nil")
	}
}

// TestAICommand_Handle_NoService 测试 AI 服务未启用。
func TestAICommand_Handle_NoService(t *testing.T) {
	text := &mockTextSender{}
	cmd := NewAICommand(nil, text, nil)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, []string{"hello"})
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if text.lastBody != "AI 服务未启用" {
		t.Errorf("body = %q, want %q", text.lastBody, "AI 服务未启用")
	}
}

// TestAICommand_Handle_NoArgs 测试无参数。
func TestAICommand_Handle_NoArgs(t *testing.T) {
	text := &mockTextSender{}
	svc := &mockAIService{}
	cmd := NewAICommand(nil, text, svc)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if text.lastBody != "请提供消息内容，例如：!ai 你好" {
		t.Errorf("body = %q, want %q", text.lastBody, "请提供消息内容，例如：!ai 你好")
	}
}

// TestAICommand_Handle_WithArgs 测试有参数（占位符实现）。
func TestAICommand_Handle_WithArgs(t *testing.T) {
	text := &mockTextSender{}
	svc := &mockAIService{}
	cmd := NewAICommand(nil, text, svc)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, []string{"hello", "world"})
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if text.lastBody != "AI 服务接口占位符" {
		t.Errorf("body = %q, want %q", text.lastBody, "AI 服务接口占位符")
	}
}

// TestAIClearContextCommand_Handle 测试清除上下文。
func TestAIClearContextCommand_Handle(t *testing.T) {
	text := &mockTextSender{}
	svc := &mockAIService{}
	cmd := NewAIClearContextCommand(text, svc)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if !svc.clearedContext {
		t.Error("ClearContext should have been called")
	}
	if svc.clearedRoomID != roomID {
		t.Errorf("roomID = %q, want %q", svc.clearedRoomID, roomID)
	}
	if text.lastBody != "对话上下文已清除" {
		t.Errorf("body = %q, want %q", text.lastBody, "对话上下文已清除")
	}
}

// TestAIClearContextCommand_Handle_NoService 测试清除上下文服务未启用。
func TestAIClearContextCommand_Handle_NoService(t *testing.T) {
	text := &mockTextSender{}
	cmd := NewAIClearContextCommand(text, nil)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if text.lastBody != "AI 服务未启用" {
		t.Errorf("body = %q, want %q", text.lastBody, "AI 服务未启用")
	}
}

// TestAIContextInfoCommand_Handle 测试获取上下文信息。
func TestAIContextInfoCommand_Handle(t *testing.T) {
	text := &mockTextSender{}
	svc := &mockAIService{contextInfo: "上下文消息数: 5"}
	cmd := NewAIContextInfoCommand(text, svc)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if text.lastBody != "上下文消息数: 5" {
		t.Errorf("body = %q, want %q", text.lastBody, "上下文消息数: 5")
	}
}

// TestAIModelsCommand_Handle 测试列出模型。
func TestAIModelsCommand_Handle(t *testing.T) {
	text := &mockTextSender{}
	svc := &mockAIService{models: []string{"gpt-4", "gpt-3.5-turbo"}}
	cmd := NewAIModelsCommand(text, svc)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if text.lastBody == "" {
		t.Error("body should not be empty")
	}
}

// TestAIModelsCommand_Handle_Empty 测试无可用模型。
func TestAIModelsCommand_Handle_Empty(t *testing.T) {
	text := &mockTextSender{}
	svc := &mockAIService{models: []string{}}
	cmd := NewAIModelsCommand(text, svc)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if text.lastBody != "暂无可用模型" {
		t.Errorf("body = %q, want %q", text.lastBody, "暂无可用模型")
	}
}

// TestAICurrentModelCommand_Handle 测试获取当前模型。
func TestAICurrentModelCommand_Handle(t *testing.T) {
	text := &mockTextSender{}
	svc := &mockAIService{currentModel: "gpt-4"}
	cmd := NewAICurrentModelCommand(text, svc)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if text.lastBody != "当前模型：gpt-4" {
		t.Errorf("body = %q, want %q", text.lastBody, "当前模型：gpt-4")
	}
}

// TestAISwitchModelCommand_Handle 测试切换模型。
func TestAISwitchModelCommand_Handle(t *testing.T) {
	text := &mockTextSender{}
	svc := &mockAIService{}
	cmd := NewAISwitchModelCommand(text, svc)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, []string{"gpt-4"})
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if svc.currentModel != "gpt-4" {
		t.Errorf("currentModel = %q, want %q", svc.currentModel, "gpt-4")
	}
	if text.lastBody != "已切换到模型：gpt-4" {
		t.Errorf("body = %q, want %q", text.lastBody, "已切换到模型：gpt-4")
	}
}

// TestAISwitchModelCommand_Handle_NoArgs 测试切换模型无参数。
func TestAISwitchModelCommand_Handle_NoArgs(t *testing.T) {
	text := &mockTextSender{}
	svc := &mockAIService{}
	cmd := NewAISwitchModelCommand(text, svc)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if text.lastBody != "请指定模型名称，例如：!ai-switch gpt-4" {
		t.Errorf("body = %q, want %q", text.lastBody, "请指定模型名称，例如：!ai-switch gpt-4")
	}
}

// TestAISwitchModelCommand_Handle_Error 测试切换模型失败。
func TestAISwitchModelCommand_Handle_Error(t *testing.T) {
	text := &mockTextSender{}
	svc := &mockAIService{setModelError: errors.New("invalid model")}
	cmd := NewAISwitchModelCommand(text, svc)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, []string{"invalid"})
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if text.lastBody == "" {
		t.Error("body should contain error message")
	}
}
