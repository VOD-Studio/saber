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

// TestNewAICommand_RealService 测试创建真实的 AICommand。
func TestNewAICommand_RealService(t *testing.T) {
	cfg := &config.AIConfig{
		Enabled:        true,
		Provider:       "openai",
		DefaultModel:   "test.default-model",
		TimeoutSeconds: 30,
		ToolCalling: config.ToolCallingConfig{
			MaxIterations: 5,
		},
		Providers: map[string]config.ProviderConfig{
			"test": {
				Type:    "openai",
				BaseURL: "https://api.openai.com/v1",
				Models: map[string]config.ModelConfig{
					"default-model": {Model: "test-default-model"},
				},
			},
		},
	}

	simpleService, err := ai.NewSimpleService(cfg)
	if err != nil {
		t.Fatalf("NewSimpleService error: %v", err)
	}

	contextMgr := NewContextManager(config.ContextConfig{})
	cmd := NewAICommand(simpleService, contextMgr)

	if cmd == nil {
		t.Error("NewAICommand should not return nil")
	}
	if cmd.aiService == nil {
		t.Error("aiService should be set")
	}
	if cmd.contextMgr == nil {
		t.Error("contextMgr should be set")
	}
	if cmd.modelRegistry == nil {
		t.Error("modelRegistry should be set")
	}
}

// TestAICommand_Handle_Switch_MissingModelID 测试缺少模型 ID 的切换。
func TestAICommand_Handle_Switch_MissingModelID(t *testing.T) {
	contextMgr := NewContextManager(config.ContextConfig{})
	mockService := &mockSimpleService{}

	cmd := &AICommand{
		contextMgr:    contextMgr,
		modelRegistry: mockService.GetModelRegistry(),
	}

	mock := &MockCommandSender{}
	err := cmd.Handle(context.Background(), "user1", "", []string{"switch"}, mock)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if mock.LastMessage != "用法: !ai switch <模型ID>" {
		t.Errorf("LastMessage = %q, want %q", mock.LastMessage, "用法: !ai switch <模型ID>")
	}
}

// TestAICommand_handleChat 测试 handleChat 相关逻辑。
// 注意: handleChat 需要 *ai.SimpleService 具体类型，无法使用 mock。
// 完整的聊天测试需要真实的 AI 服务或使用 httptest 模拟 API。
func TestAICommand_handleChat_RequiresRealService(t *testing.T) {
	// 由于 AICommand.aiService 是 *ai.SimpleService 具体类型而非接口，
	// 无法直接注入 mock。handleChat 的测试需要:
	// 1. 创建真实的 ai.SimpleService
	// 2. 使用 httptest.Server 模拟 OpenAI API
	// 这里标记为需要进一步基础设施支持
	t.Skip("handleChat requires *ai.SimpleService concrete type - needs integration test setup")
}