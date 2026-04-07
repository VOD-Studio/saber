//go:build goolm

package ai

import (
	"context"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/config"
)

// TestNewSimpleService_NilConfig 测试空配置错误。
func TestNewSimpleService_NilConfig(t *testing.T) {
	_, err := NewSimpleService(nil)
	if err == nil {
		t.Error("expected error for nil config")
	}
}

// TestNewSimpleService_DisabledConfig 测试禁用配置。
func TestNewSimpleService_DisabledConfig(t *testing.T) {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = false

	service, err := NewSimpleService(&cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if service.IsEnabled() {
		t.Error("service should be disabled")
	}
}

// TestNewSimpleService_ValidConfig 测试有效配置。
func TestNewSimpleService_ValidConfig(t *testing.T) {
	cfg := createTestAIConfig()

	service, err := NewSimpleService(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if service == nil {
		t.Fatal("service is nil")
	}
	if !service.IsEnabled() {
		t.Error("service should be enabled")
	}
}

// TestSimpleService_GetModelRegistry 测试 GetModelRegistry 方法。
func TestSimpleService_GetModelRegistry(t *testing.T) {
	cfg := createTestAIConfig()
	service, _ := NewSimpleService(cfg)

	registry := service.GetModelRegistry()
	if registry == nil {
		t.Error("GetModelRegistry returned nil")
	}
}

// TestSimpleService_Chat_Disabled 测试禁用时的 Chat 方法。
func TestSimpleService_Chat_Disabled(t *testing.T) {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = false

	service, err := NewSimpleService(&cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = service.Chat(context.Background(), "test-user", "hello")
	if err == nil {
		t.Error("expected error when service is disabled")
	}
}

// TestSimpleService_ChatWithSystem_Disabled 测试禁用时的 ChatWithSystem 方法。
func TestSimpleService_ChatWithSystem_Disabled(t *testing.T) {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = false

	service, err := NewSimpleService(&cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = service.ChatWithSystem(context.Background(), "test-user", "system prompt", "hello")
	if err == nil {
		t.Error("expected error when service is disabled")
	}
}

// TestSimpleService_ChatWithModel_Disabled 测试禁用时的 ChatWithModel 方法。
func TestSimpleService_ChatWithModel_Disabled(t *testing.T) {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = false

	service, err := NewSimpleService(&cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = service.ChatWithModel(context.Background(), "test-user", "gpt-4", 0.7, "system", "hello")
	if err == nil {
		t.Error("expected error when service is disabled")
	}
}

// TestSimpleService_ChatWithModel_EmptyModel 测试空模型名。
func TestSimpleService_ChatWithModel_EmptyModel(t *testing.T) {
	cfg := createTestAIConfig()
	service, _ := NewSimpleService(cfg)

	// 空模型名会使用默认模型
	// 由于需要实际 API 调用，这里只测试上下文取消的情况
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	cancel()

	_, err := service.ChatWithModel(ctx, "test-user", "", 0, "", "hello")
	if err == nil {
		t.Error("expected error due to context cancellation")
	}
}

// TestCore_NewCore_NilConfig 测试空配置。
func TestCore_NewCore_NilConfig(t *testing.T) {
	_, err := NewCore(nil)
	if err == nil {
		t.Error("expected error for nil config")
	}
}

// TestCore_NewCore_InvalidConfig 测试无效配置。
func TestCore_NewCore_InvalidConfig(t *testing.T) {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = true
	cfg.Provider = "" // 无效配置

	_, err := NewCore(&cfg)
	if err == nil {
		t.Error("expected error for invalid config")
	}
}

// TestCore_NewCore_ValidConfig 测试有效配置。
func TestCore_NewCore_ValidConfig(t *testing.T) {
	cfg := createTestAIConfig()

	core, err := NewCore(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if core == nil {
		t.Fatal("core is nil")
	}
}

// TestCore_WaitForRateLimit_NoLimiter 测试无速率限制器。
func TestCore_WaitForRateLimit_NoLimiter(t *testing.T) {
	cfg := createTestAIConfig()
	cfg.RateLimitPerMinute = 0 // 无速率限制

	core, _ := NewCore(cfg)

	err := core.WaitForRateLimit(context.Background())
	if err != nil {
		t.Errorf("expected no error when rate limiter is nil, got: %v", err)
	}
}

// TestCore_WaitForRateLimit_WithLimiter 测试有速率限制器。
func TestCore_WaitForRateLimit_WithLimiter(t *testing.T) {
	cfg := createTestAIConfig()
	cfg.RateLimitPerMinute = 60 // 每分钟 60 个请求

	core, _ := NewCore(cfg)

	// 第一次应该立即通过
	err := core.WaitForRateLimit(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestCore_WaitForRateLimit_ContextCancel 测试上下文取消。
func TestCore_WaitForRateLimit_ContextCancel(t *testing.T) {
	cfg := createTestAIConfig()
	cfg.RateLimitPerMinute = 1 // 很低的限制，会让后续请求等待

	core, _ := NewCore(cfg)

	// 用完 burst 配额
	_ = core.WaitForRateLimit(context.Background())

	// 创建一个已取消的上下文
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := core.WaitForRateLimit(ctx)
	if err == nil {
		t.Error("expected error due to cancelled context")
	}
}

// TestCore_GetClient_Caching 测试客户端缓存。
func TestCore_GetClient_Caching(t *testing.T) {
	cfg := createTestAIConfig()
	core, _ := NewCore(cfg)

	client1, err := core.GetClient("gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	client2, err := core.GetClient("gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client1 != client2 {
		t.Error("clients should be cached and return same instance")
	}
}

// TestCore_GetConfig 测试 GetConfig 方法。
func TestCore_GetConfig(t *testing.T) {
	cfg := createTestAIConfig()
	core, _ := NewCore(cfg)

	gotCfg := core.GetConfig()
	if gotCfg == nil {
		t.Error("GetConfig returned nil")
	}
	if gotCfg != cfg {
		t.Error("GetConfig should return the original config")
	}
}

// TestCore_CreateChatCompletion_ContextCancel 测试上下文取消。
func TestCore_CreateChatCompletion_ContextCancel(t *testing.T) {
	cfg := createTestAIConfig()
	core, _ := NewCore(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	messages := []openai.ChatCompletionMessage{
		{Role: "user", Content: "hello"},
	}

	_, err := core.CreateChatCompletion(ctx, "gpt-4", messages, 100, 0.7)
	if err == nil {
		t.Error("expected error due to cancelled context")
	}
}
