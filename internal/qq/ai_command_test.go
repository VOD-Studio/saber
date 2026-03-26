package qq

import (
	"context"
	"testing"

	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/config"
)

// mockSimpleService 是测试用的 Mock AI 服务。
type mockSimpleService struct {
	response      string
	err           error
	modelRegistry *ai.ModelRegistry
}

func (m *mockSimpleService) IsEnabled() bool {
	return true
}

func (m *mockSimpleService) Chat(ctx context.Context, userID, message string) (string, error) {
	return m.response, m.err
}

func (m *mockSimpleService) ChatWithSystem(ctx context.Context, userID, systemPrompt, message string) (string, error) {
	return m.response, m.err
}

func (m *mockSimpleService) GetModelRegistry() *ai.ModelRegistry {
	if m.modelRegistry != nil {
		return m.modelRegistry
	}
	return ai.NewModelRegistry(&config.AIConfig{
		DefaultModel: "test.default-model",
		Providers: map[string]config.ProviderConfig{
			"test": {
				Type: "openai",
				Models: map[string]config.ModelConfig{
					"default-model": {Model: "test-default-model"},
					"another-model": {Model: "test-another-model"},
				},
			},
		},
	})
}

func TestAICommand_Handle_Clear(t *testing.T) {
	contextMgr := NewContextManager(config.ContextConfig{})
	mockService := &mockSimpleService{}
	cmd := &AICommand{
		contextMgr:    contextMgr,
		modelRegistry: mockService.GetModelRegistry(),
	}

	contextMgr.AddMessage("user1", "user", "test")
	mock := &MockCommandSender{}

	err := cmd.Handle(context.Background(), "user1", "", []string{"clear"}, mock)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if mock.LastMessage != "已清除对话上下文" {
		t.Errorf("LastMessage = %q, want %q", mock.LastMessage, "已清除对话上下文")
	}
	if contextMgr.HasContext("user1") {
		t.Error("Context should be cleared")
	}
}

func TestAICommand_Handle_Context(t *testing.T) {
	contextMgr := NewContextManager(config.ContextConfig{})
	mockService := &mockSimpleService{}
	cmd := &AICommand{
		contextMgr:    contextMgr,
		modelRegistry: mockService.GetModelRegistry(),
	}

	mock := &MockCommandSender{}
	err := cmd.Handle(context.Background(), "user1", "", []string{"context"}, mock)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if mock.LastMessage == "" {
		t.Error("Context message should not be empty")
	}
}

func TestAICommand_Handle_Current(t *testing.T) {
	contextMgr := NewContextManager(config.ContextConfig{})
	mockService := &mockSimpleService{}
	cmd := &AICommand{
		contextMgr:    contextMgr,
		modelRegistry: mockService.GetModelRegistry(),
	}

	mock := &MockCommandSender{}
	err := cmd.Handle(context.Background(), "user1", "", []string{"current"}, mock)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if mock.LastMessage == "" {
		t.Error("Current model message should not be empty")
	}
}

func TestAICommand_Handle_Models(t *testing.T) {
	contextMgr := NewContextManager(config.ContextConfig{})
	mockService := &mockSimpleService{}
	cmd := &AICommand{
		contextMgr:    contextMgr,
		modelRegistry: mockService.GetModelRegistry(),
	}

	mock := &MockCommandSender{}
	err := cmd.Handle(context.Background(), "user1", "", []string{"models"}, mock)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if mock.LastMessage == "" {
		t.Error("Models list should not be empty")
	}
}

func TestAICommand_Handle_Switch(t *testing.T) {
	contextMgr := NewContextManager(config.ContextConfig{})
	mockService := &mockSimpleService{}
	cmd := &AICommand{
		contextMgr:    contextMgr,
		modelRegistry: mockService.GetModelRegistry(),
	}

	mock := &MockCommandSender{}
	err := cmd.Handle(context.Background(), "user1", "", []string{"switch", "test.another-model"}, mock)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if mock.LastMessage == "" {
		t.Error("Switch message should not be empty")
	}

	// 验证默认模型已切换
	current := cmd.modelRegistry.GetDefault()
	if current != "test.another-model" {
		t.Errorf("Default model = %q, want %q", current, "test.another-model")
	}
}

func TestAICommand_Handle_Switch_InvalidModel(t *testing.T) {
	contextMgr := NewContextManager(config.ContextConfig{})
	mockService := &mockSimpleService{}
	cmd := &AICommand{
		contextMgr:    contextMgr,
		modelRegistry: mockService.GetModelRegistry(),
	}

	mock := &MockCommandSender{}
	err := cmd.Handle(context.Background(), "user1", "", []string{"switch", "nonexistent-model"}, mock)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if mock.LastMessage == "" {
		t.Error("Error message should not be empty")
	}
}

func TestAICommand_Handle_Help(t *testing.T) {
	contextMgr := NewContextManager(config.ContextConfig{})
	mockService := &mockSimpleService{}
	cmd := &AICommand{
		contextMgr:    contextMgr,
		modelRegistry: mockService.GetModelRegistry(),
	}

	mock := &MockCommandSender{}
	err := cmd.Handle(context.Background(), "user1", "", []string{}, mock)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if mock.LastMessage == "" {
		t.Error("Help message should not be empty")
	}
}

func TestAICommand_Handle_Chat(t *testing.T) {
	// 跳过实际聊天测试，因为需要真实的 AI 服务
	// 该测试需要 mock ai.SimpleService，但 handleChat 直接调用 aiService.Chat
	t.Skip("需要真实的 AI 服务或更复杂的 mock")
}

func TestAICommand_NewAICommand(t *testing.T) {
	contextMgr := NewContextManager(config.ContextConfig{})
	// NewAICommand 需要 *ai.SimpleService，这里我们测试结构创建
	// 由于需要真实的 SimpleService，这里只验证结构体
	_ = &AICommand{
		contextMgr:    contextMgr,
		modelRegistry: (&mockSimpleService{}).GetModelRegistry(),
	}
}