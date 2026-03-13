// Package ai_test 包含 AI 服务的单元测试。
package ai

import (
	"sync"
	"testing"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
)

// createTestAIConfig 创建测试用的 AI 配置。
func createTestAIConfig() *config.AIConfig {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = true
	cfg.Provider = "openai"
	cfg.BaseURL = "https://api.openai.com/v1"
	cfg.APIKey = "test-key"
	cfg.DefaultModel = "gpt-4"
	return &cfg
}

// TestNewService_NilConfig 测试空配置错误。
func TestNewService_NilConfig(t *testing.T) {
	_, err := NewService(nil, nil)
	if err == nil {
		t.Error("expected error for nil config")
	}
}

// TestNewService_InvalidConfig 测试无效配置错误。
func TestNewService_InvalidConfig(t *testing.T) {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = true
	cfg.Provider = ""

	_, err := NewService(&cfg, nil)
	if err == nil {
		t.Error("expected error for invalid config")
	}
}

// TestNewService_DisabledConfig 测试禁用配置。
func TestNewService_DisabledConfig(t *testing.T) {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = false

	service, err := NewService(&cfg, nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if service.IsEnabled() {
		t.Error("service should be disabled")
	}
}

// TestNewService_ValidConfig 测试有效配置。
func TestNewService_ValidConfig(t *testing.T) {
	cfg := createTestAIConfig()

	service, err := NewService(cfg, nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if service == nil {
		t.Error("service is nil")
	}
	if !service.IsEnabled() {
		t.Error("service should be enabled")
	}
}

// TestNewService_ContextEnabled 测试上下文管理器初始化。
func TestNewService_ContextEnabled(t *testing.T) {
	cfg := createTestAIConfig()
	cfg.Context.Enabled = true

	service, err := NewService(cfg, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if service.contextManager == nil {
		t.Error("contextManager should be initialized when context is enabled")
	}
}

// TestNewService_ContextDisabled 测试上下文管理器未初始化。
func TestNewService_ContextDisabled(t *testing.T) {
	cfg := createTestAIConfig()
	cfg.Context.Enabled = false

	service, err := NewService(cfg, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if service.contextManager != nil {
		t.Error("contextManager should be nil when context is disabled")
	}
}

// TestService_GetClient_Caching 测试客户端缓存。
func TestService_GetClient_Caching(t *testing.T) {
	cfg := createTestAIConfig()
	service, _ := NewService(cfg, nil)

	client1, err := service.getClient("gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	client2, err := service.getClient("gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client1 != client2 {
		t.Error("clients should be cached and return same instance")
	}
}

// TestService_GetClient_DifferentModels 测试不同模型返回不同客户端。
func TestService_GetClient_DifferentModels(t *testing.T) {
	cfg := createTestAIConfig()
	service, _ := NewService(cfg, nil)

	client1, err := service.getClient("gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	client2, err := service.getClient("gpt-3.5-turbo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client1 == client2 {
		t.Error("different models should return different clients")
	}
}

// TestService_GetClient_Concurrency 测试并发客户端创建。
func TestService_GetClient_Concurrency(t *testing.T) {
	cfg := createTestAIConfig()
	service, _ := NewService(cfg, nil)

	const goroutines = 50
	var wg sync.WaitGroup
	errChan := make(chan error, goroutines)

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := service.getClient("gpt-4")
			errChan <- err
		}()
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}
}

// TestService_IsEnabled 测试 IsEnabled 方法。
func TestService_IsEnabled(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		cfg := createTestAIConfig()
		cfg.Enabled = true
		service, _ := NewService(cfg, nil)

		if !service.IsEnabled() {
			t.Error("service should be enabled")
		}
	})

	t.Run("disabled", func(t *testing.T) {
		cfg := createTestAIConfig()
		cfg.Enabled = false
		service, _ := NewService(cfg, nil)

		if service.IsEnabled() {
			t.Error("service should be disabled")
		}
	})
}

// TestAICommand_New 测试 AICommand 创建。
func TestAICommand_New(t *testing.T) {
	cfg := createTestAIConfig()
	service, _ := NewService(cfg, nil)

	cmd := NewAICommand(service)
	if cmd == nil {
		t.Error("NewAICommand returned nil")
	}
}

// TestMultiModelAICommand_New 测试 MultiModelAICommand 创建。
func TestMultiModelAICommand_New(t *testing.T) {
	cfg := createTestAIConfig()
	service, _ := NewService(cfg, nil)

	cmd := NewMultiModelAICommand(service, "gpt-3.5-turbo")
	if cmd == nil {
		t.Error("NewMultiModelAICommand returned nil")
	}
}

// TestClearContextCommand_New 测试 ClearContextCommand 创建。
func TestClearContextCommand_New(t *testing.T) {
	cfg := createTestAIConfig()
	service, _ := NewService(cfg, nil)

	cmd := NewClearContextCommand(service)
	if cmd == nil {
		t.Error("NewClearContextCommand returned nil")
	}
}

// TestContextInfoCommand_New 测试 ContextInfoCommand 创建。
func TestContextInfoCommand_New(t *testing.T) {
	cfg := createTestAIConfig()
	service, _ := NewService(cfg, nil)

	cmd := NewContextInfoCommand(service)
	if cmd == nil {
		t.Error("NewContextInfoCommand returned nil")
	}
}

// TestService_ContextIntegration 测试上下文集成。
func TestService_ContextIntegration(t *testing.T) {
	cfg := createTestAIConfig()
	cfg.Context.Enabled = true
	service, _ := NewService(cfg, nil)

	roomID := id.RoomID("!room:example.com")
	userID := id.UserID("@user:example.com")

	msgCount, _ := service.contextManager.GetContextSize(roomID)
	if msgCount != 0 {
		t.Errorf("expected 0 messages initially, got %d", msgCount)
	}

	service.contextManager.AddMessage(roomID, RoleUser, "Hello", userID)

	msgCount, _ = service.contextManager.GetContextSize(roomID)
	if msgCount != 1 {
		t.Errorf("expected 1 message after add, got %d", msgCount)
	}

	service.contextManager.ClearContext(roomID)

	msgCount, _ = service.contextManager.GetContextSize(roomID)
	if msgCount != 0 {
		t.Errorf("expected 0 messages after clear, got %d", msgCount)
	}
}

// TestService_Concurrency 测试服务的并发安全性。
func TestService_Concurrency(t *testing.T) {
	cfg := createTestAIConfig()
	cfg.Context.Enabled = true
	service, _ := NewService(cfg, nil)

	const goroutines = 50
	var wg sync.WaitGroup

	roomID := id.RoomID("!room:example.com")

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			service.contextManager.AddMessage(roomID, RoleUser, "message", id.UserID("@user:example.com"))
		}()
	}

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			service.IsEnabled()
		}()
	}

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = service.getClient("gpt-4")
		}()
	}

	wg.Wait()
}
