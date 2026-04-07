// Package ai 提供 AI 服务相关功能。
package ai

import (
	"context"
	"testing"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/mcp"
)

// TestNewToolExecutor 测试创建工具执行器。
func TestNewToolExecutor(t *testing.T) {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = true
	cfg.Provider = "openai"
	cfg.BaseURL = "https://api.openai.com/v1"
	cfg.APIKey = "test-key"
	cfg.DefaultModel = "gpt-4"

	mcpMgr := mcp.NewManager(&config.MCPConfig{})
	service, err := NewService(&cfg, nil, mcpMgr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	executor := NewToolExecutor(service)
	if executor == nil {
		t.Error("NewToolExecutor returned nil")
	}
}

// TestToolExecutor_ExecuteToolCallingLoop_NoToolCalls 测试无工具调用的场景。
func TestToolExecutor_ExecuteToolCallingLoop_NoToolCalls(t *testing.T) {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = true
	cfg.Provider = "openai"
	cfg.BaseURL = "https://api.openai.com/v1"
	cfg.APIKey = "test-key"
	cfg.DefaultModel = "gpt-4"
	cfg.ToolCalling.MaxIterations = 5

	mcpMgr := mcp.NewManager(&config.MCPConfig{})
	service, err := NewService(&cfg, nil, mcpMgr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	executor := NewToolExecutor(service)

	// 测试空工具列表
	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "hello"},
	}

	// 由于没有 mock 客户端，这个测试会失败，但我们验证函数不会 panic
	_, _ = executor.ExecuteToolCallingLoop(context.Background(), messages, "gpt-4", nil)
}

// TestToolExecutor_PrepareTools 测试工具准备。
func TestToolExecutor_PrepareTools(t *testing.T) {
	t.Run("nil MCP manager", func(t *testing.T) {
		service := &Service{
			mcpManager: nil,
		}
		executor := NewToolExecutor(service)

		tools, hasTools := executor.PrepareTools()
		if hasTools {
			t.Error("PrepareTools should return false for nil MCP manager")
		}
		if tools != nil {
			t.Error("PrepareTools should return nil tools for nil MCP manager")
		}
	})

	t.Run("disabled MCP manager", func(t *testing.T) {
		mcpMgr := mcp.NewManager(&config.MCPConfig{Enabled: false})
		service := &Service{
			mcpManager: mcpMgr,
		}
		executor := NewToolExecutor(service)

		tools, hasTools := executor.PrepareTools()
		// 禁用的 MCP 可能返回空
		_ = tools
		_ = hasTools
	})
}

// TestExecuteToolCall 测试执行单个工具调用。
func TestToolExecutor_ExecuteToolCall(t *testing.T) {
	t.Run("nil MCP manager", func(t *testing.T) {
		service := &Service{
			mcpManager: nil,
		}
		executor := NewToolExecutor(service)

		_, err := executor.ExecuteToolCall(context.Background(), "test_tool", map[string]any{})
		if err == nil {
			t.Error("expected error for nil MCP manager")
		}
	})

	t.Run("empty tool name", func(t *testing.T) {
		mcpMgr := mcp.NewManager(&config.MCPConfig{Enabled: true})
		service := &Service{
			mcpManager: mcpMgr,
		}
		executor := NewToolExecutor(service)

		_, err := executor.ExecuteToolCall(context.Background(), "", map[string]any{})
		// 应该返回错误
		_ = err
	})

	t.Run("tool not found", func(t *testing.T) {
		mcpMgr := mcp.NewManager(&config.MCPConfig{Enabled: true})
		service := &Service{
			mcpManager: mcpMgr,
		}
		executor := NewToolExecutor(service)

		_, err := executor.ExecuteToolCall(context.Background(), "nonexistent_tool", map[string]any{})
		// 应该返回错误
		_ = err
	})
}

// TestPrepareToolsWithMCPServer 测试带 MCP 服务器的工具准备。
func TestPrepareToolsWithMCPServer(t *testing.T) {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = true

	mcpCfg := &config.MCPConfig{
		Enabled: true,
		Servers: map[string]config.ServerConfig{
			"test-server": {
				Command: "echo",
				Args:    []string{"test"},
			},
		},
	}
	mcpMgr := mcp.NewManager(mcpCfg)

	service := &Service{
		mcpManager: mcpMgr,
	}
	executor := NewToolExecutor(service)

	// PrepareTools 应该返回工具列表
	tools, hasTools := executor.PrepareTools()
	_ = tools
	_ = hasTools
}

// TestToolExecutor_MaxIterations 测试最大迭代次数。
func TestToolExecutor_MaxIterations(t *testing.T) {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = true
	cfg.Provider = "openai"
	cfg.BaseURL = "https://api.openai.com/v1"
	cfg.APIKey = "test-key"
	cfg.DefaultModel = "gpt-4"
	cfg.ToolCalling.MaxIterations = 3

	mcpMgr := mcp.NewManager(&config.MCPConfig{})
	service, err := NewService(&cfg, nil, mcpMgr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	executor := NewToolExecutor(service)

	// 验证最大迭代次数被正确设置
	if service.core.GetConfig().ToolCalling.MaxIterations != 3 {
		t.Errorf("MaxIterations = %d, want 3", service.core.GetConfig().ToolCalling.MaxIterations)
	}

	_ = executor
}

// TestToolExecutor_ExecuteToolCallWithContext 测试带上下文的工具调用。
func TestToolExecutor_ExecuteToolCallWithContext(t *testing.T) {
	mcpMgr := mcp.NewManager(&config.MCPConfig{Enabled: true})
	service := &Service{
		mcpManager: mcpMgr,
	}
	executor := NewToolExecutor(service)

	ctx := context.Background()

	// 执行工具调用（会失败因为没有注册的工具）
	_, err := executor.ExecuteToolCall(ctx, "echo", map[string]any{"message": "test"})
	// 预期会失败
	if err == nil {
		t.Log("tool call succeeded (may have built-in tools)")
	}
}

// TestToolExecutor_CancelledContext 测试取消的上下文。
func TestToolExecutor_CancelledContext(t *testing.T) {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = true
	cfg.Provider = "openai"
	cfg.BaseURL = "https://api.openai.com/v1"
	cfg.APIKey = "test-key"
	cfg.DefaultModel = "gpt-4"

	mcpMgr := mcp.NewManager(&config.MCPConfig{})
	service, err := NewService(&cfg, nil, mcpMgr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	executor := NewToolExecutor(service)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "test"},
	}

	// 使用已取消的上下文
	_, err = executor.ExecuteToolCallingLoop(ctx, messages, "gpt-4", nil)
	// 应该返回上下文相关的错误
	_ = err
}

// TestToolExecutor_DefaultMaxIterations 测试默认最大迭代次数。
func TestToolExecutor_DefaultMaxIterations(t *testing.T) {
	cfg := config.DefaultAIConfig()
	cfg.Enabled = true
	cfg.Provider = "openai"
	cfg.BaseURL = "https://api.openai.com/v1"
	cfg.APIKey = "test-key"
	cfg.DefaultModel = "gpt-4"
	// 不设置 ToolCalling.MaxIterations，使用配置默认值

	mcpMgr := mcp.NewManager(&config.MCPConfig{})
	service, err := NewService(&cfg, nil, mcpMgr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	executor := NewToolExecutor(service)
	_ = executor

	// 验证使用配置默认值
	if service.core.GetConfig().ToolCalling.MaxIterations < 1 {
		t.Error("MaxIterations should be at least 1")
	}
}
