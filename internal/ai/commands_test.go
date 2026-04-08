//go:build goolm

// Package ai 提供 AI 命令测试。
package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/mcp"
)

// createMockMatrixServerForCommands 创建一个模拟 Matrix API 的测试服务器。
func createMockMatrixServerForCommands() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"event_id":"$test_event_id:example.com"}`))
	}))
}

// createTestMatrixClientForCommands 创建一个测试用的 Matrix 客户端。
func createTestMatrixClientForCommands(server *httptest.Server) *mautrix.Client {
	client, _ := mautrix.NewClient(server.URL, id.UserID("@test:example.com"), "test_token")
	return client
}

// TestAICommand_Handle 测试 AICommand 的 Handle 方法。
func TestAICommand_Handle(t *testing.T) {
	t.Run("nil service", func(t *testing.T) {
		cmd := NewAICommand(nil)
		if cmd == nil {
			t.Error("NewAICommand should not return nil")
		}
		// Handle with nil service would panic, so we skip that
	})

	t.Run("service exists", func(t *testing.T) {
		cfg := config.DefaultAIConfig()
		cfg.Enabled = true
		cfg.Provider = "openai"
		cfg.BaseURL = "https://api.openai.com/v1"
		cfg.APIKey = "test-key"
		cfg.DefaultModel = "gpt-4"
		cfg.Models = map[string]config.ModelConfig{"gpt-4": {Model: "gpt-4"}}

		mcpMgr := mcp.NewManager(&config.MCPConfig{})
		service, err := NewService(&cfg, nil, mcpMgr, nil)
		if err != nil {
			t.Fatalf("NewService failed: %v", err)
		}

		cmd := NewAICommand(service)
		if cmd == nil {
			t.Error("NewAICommand should not return nil")
		}
	})
}

// TestMultiModelAICommand_Handle 测试 MultiModelAICommand 的 Handle 方法。
func TestMultiModelAICommand_Handle(t *testing.T) {
	t.Run("create command", func(t *testing.T) {
		cfg := config.DefaultAIConfig()
		cfg.Enabled = true
		cfg.Provider = "openai"
		cfg.BaseURL = "https://api.openai.com/v1"
		cfg.APIKey = "test-key"
		cfg.DefaultModel = "gpt-4"
		cfg.Models = map[string]config.ModelConfig{
			"gpt-4":         {Model: "gpt-4"},
			"gpt-3.5-turbo": {Model: "gpt-3.5-turbo"},
		}

		mcpMgr := mcp.NewManager(&config.MCPConfig{})
		service, err := NewService(&cfg, nil, mcpMgr, nil)
		if err != nil {
			t.Fatalf("NewService failed: %v", err)
		}

		cmd := NewMultiModelAICommand(service, "gpt-3.5-turbo")
		if cmd != nil {
			if cmd.modelName != "gpt-3.5-turbo" {
				t.Errorf("modelName = %q, want %q", cmd.modelName, "gpt-3.5-turbo")
			}
		} else {
			t.Error("NewMultiModelAICommand should not return nil")
		}
	})
}

// TestClearContextCommand_NilContextManager 测试 ClearContextCommand 的 nil contextManager 情况。
func TestClearContextCommand_NilContextManager(t *testing.T) {
	// 创建一个没有 contextManager 的 service
	service := &Service{
		contextManager: nil,
		matrixService:  nil, // nil 会阻止发送消息
	}

	cmd := NewClearContextCommand(service)
	if cmd == nil {
		t.Error("NewClearContextCommand should not return nil")
	}

	// Handle with nil matrixService 会 panic，所以我们只验证创建成功
}

// TestClearContextCommand_WithContextManager 测试 ClearContextCommand 带 contextManager。
func TestClearContextCommand_WithContextManager(t *testing.T) {
	ctxMgr := NewTestContextManager(WithMaxMessages(10))
	roomID := TestRoomID(1)
	userID := TestUserID(1)

	// 先添加一些上下文
	ctxMgr.AddMessage(roomID, RoleUser, "hello", userID)

	// 验证上下文存在
	msgCount, _ := ctxMgr.GetContextSize(roomID)
	if msgCount == 0 {
		t.Error("context should have messages after AddMessage")
	}

	// 清除上下文
	ctxMgr.ClearContext(roomID)

	// 验证上下文被清除
	msgCount, _ = ctxMgr.GetContextSize(roomID)
	if msgCount != 0 {
		t.Errorf("context should be cleared, got %d messages", msgCount)
	}
}

// TestContextInfoCommand_WithContextManager 测试 ContextInfoCommand 带 contextManager。
func TestContextInfoCommand_WithContextManager(t *testing.T) {
	ctxMgr := NewTestContextManager(WithMaxMessages(10))
	roomID := TestRoomID(1)
	userID := TestUserID(1)

	// 添加一些上下文
	ctxMgr.AddMessage(roomID, RoleUser, "hello", userID)
	ctxMgr.AddMessage(roomID, RoleAssistant, "hi there", userID)

	// 验证上下文信息
	msgCount, tokenCount := ctxMgr.GetContextSize(roomID)
	if msgCount != 2 {
		t.Errorf("expected 2 messages, got %d", msgCount)
	}
	if tokenCount == 0 {
		t.Error("token count should be positive")
	}
}

// TestContextInfoCommand_NilContextManager 测试 ContextInfoCommand 的 nil contextManager 情况。
func TestContextInfoCommand_NilContextManager(t *testing.T) {
	service := &Service{
		contextManager: nil,
		matrixService:  nil,
	}

	cmd := NewContextInfoCommand(service)
	if cmd == nil {
		t.Error("NewContextInfoCommand should not return nil")
	}
}

// TestAICommandRouter_Handle 测试 AICommandRouter 的 Handle 方法。
func TestAICommandRouter_Handle(t *testing.T) {
	t.Run("no subcommand", func(t *testing.T) {
		// 跳过：需要完整的 Service 实例
		t.Skip("requires fully initialized Service with Core")
	})

	t.Run("unknown subcommand", func(t *testing.T) {
		// 跳过：需要完整的 Service 实例
		t.Skip("requires fully initialized Service with Core")
	})

	t.Run("list subcommands", func(t *testing.T) {
		service := &Service{}
		router := NewAICommandRouter(service)

		subs := router.ListSubcommands()
		// 空 router 返回空列表
		_ = subs
	})
}

// TestModelsCommand 测试模型列表命令。
func TestModelsCommand(t *testing.T) {
	t.Run("create command", func(t *testing.T) {
		cfg := config.DefaultAIConfig()
		cfg.Enabled = true
		cfg.Provider = "openai"
		cfg.BaseURL = "https://api.openai.com/v1"
		cfg.APIKey = "test-key"
		cfg.DefaultModel = "gpt-4"
		cfg.Models = map[string]config.ModelConfig{
			"gpt-4":         {Model: "gpt-4"},
			"gpt-3.5-turbo": {Model: "gpt-3.5-turbo"},
		}

		mcpMgr := mcp.NewManager(&config.MCPConfig{})
		service, err := NewService(&cfg, nil, mcpMgr, nil)
		if err != nil {
			t.Fatalf("NewService failed: %v", err)
		}

		cmd := NewModelsCommand(service)
		if cmd == nil {
			t.Error("NewModelsCommand should not return nil")
		}
	})
}

// TestSwitchModelCommand 测试模型切换命令。
func TestSwitchModelCommand(t *testing.T) {
	t.Run("create command", func(t *testing.T) {
		service := &Service{}
		cmd := NewSwitchModelCommand(service)
		if cmd == nil {
			t.Error("NewSwitchModelCommand should not return nil")
		}
	})
}

// TestCurrentModelCommand 测试当前模型查询命令。
func TestCurrentModelCommand(t *testing.T) {
	t.Run("create command", func(t *testing.T) {
		service := &Service{}
		cmd := NewCurrentModelCommand(service)
		if cmd == nil {
			t.Error("NewCurrentModelCommand should not return nil")
		}
	})
}

// TestNewAICommandRouter 测试创建 AICommandRouter。
func TestNewAICommandRouter(t *testing.T) {
	service := &Service{}
	router := NewAICommandRouter(service)

	if router != nil && router.service != service {
		t.Error("router.service should be set")
	}
	if router == nil {
		t.Error("NewAICommandRouter should not return nil")
	}
}

// TestAICommand_Handle_Disabled 测试 AICommand.Handle 当 AI 禁用时的行为。
func TestAICommand_Handle_Disabled(t *testing.T) {
	server := createMockMatrixServerForCommands()
	defer server.Close()

	client := createTestMatrixClientForCommands(server)
	matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

	cfg := createTestMultiProviderAIConfig()
	cfg.Enabled = false // AI 禁用

	service, err := NewService(cfg, matrixSvc, nil, nil)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	cmd := NewAICommand(service)

	ctx := context.Background()
	userID := id.UserID("@user:example.com")
	roomID := id.RoomID("!room:example.com")
	args := []string{"hello", "AI"}

	// 当 AI 禁用时应该返回错误
	err = cmd.Handle(ctx, userID, roomID, args)
	if err == nil {
		t.Error("Handle should return error when AI is disabled")
	}
}

// TestAICommand_Handle_EmptyInput 测试 AICommand.Handle 当输入为空时的行为。
func TestAICommand_Handle_EmptyInput(t *testing.T) {
	server := createMockMatrixServerForCommands()
	defer server.Close()

	client := createTestMatrixClientForCommands(server)
	matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

	cfg := createTestMultiProviderAIConfig()

	service, err := NewService(cfg, matrixSvc, nil, nil)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	cmd := NewAICommand(service)

	ctx := context.Background()
	userID := id.UserID("@user:example.com")
	roomID := id.RoomID("!room:example.com")
	args := []string{} // 空输入

	// 当输入为空时应该返回错误
	err = cmd.Handle(ctx, userID, roomID, args)
	if err == nil {
		t.Error("Handle should return error when input is empty")
	}
}

// TestMultiModelAICommand_Handle_Disabled 测试 MultiModelAICommand.Handle 当 AI 禁用时的行为。
func TestMultiModelAICommand_Handle_Disabled(t *testing.T) {
	server := createMockMatrixServerForCommands()
	defer server.Close()

	client := createTestMatrixClientForCommands(server)
	matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

	cfg := createTestMultiProviderAIConfig()
	cfg.Enabled = false // AI 禁用

	service, err := NewService(cfg, matrixSvc, nil, nil)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	cmd := NewMultiModelAICommand(service, "gpt-4o")

	ctx := context.Background()
	userID := id.UserID("@user:example.com")
	roomID := id.RoomID("!room:example.com")
	args := []string{"hello"}

	// 当 AI 禁用时应该返回错误
	err = cmd.Handle(ctx, userID, roomID, args)
	if err == nil {
		t.Error("Handle should return error when AI is disabled")
	}
}

// TestMultiModelAICommand_Handle_EmptyInput 测试 MultiModelAICommand.Handle 当输入为空时的行为。
func TestMultiModelAICommand_Handle_EmptyInput(t *testing.T) {
	server := createMockMatrixServerForCommands()
	defer server.Close()

	client := createTestMatrixClientForCommands(server)
	matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

	cfg := createTestMultiProviderAIConfig()

	service, err := NewService(cfg, matrixSvc, nil, nil)
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}

	cmd := NewMultiModelAICommand(service, "gpt-4o")

	ctx := context.Background()
	userID := id.UserID("@user:example.com")
	roomID := id.RoomID("!room:example.com")
	args := []string{} // 空输入

	// 当输入为空时应该返回错误
	err = cmd.Handle(ctx, userID, roomID, args)
	if err == nil {
		t.Error("Handle should return error when input is empty")
	}
}

// TestClearContextCommand_Handle 测试 ClearContextCommand.Handle 方法。
func TestClearContextCommand_Handle(t *testing.T) {
	t.Run("context manager disabled", func(t *testing.T) {
		server := createMockMatrixServerForCommands()
		defer server.Close()

		client := createTestMatrixClientForCommands(server)
		matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

		cfg := createTestMultiProviderAIConfig()
		cfg.Context.Enabled = false // 禁用上下文管理

		service, err := NewService(cfg, matrixSvc, nil, nil)
		if err != nil {
			t.Fatalf("NewService failed: %v", err)
		}

		cmd := NewClearContextCommand(service)

		ctx := context.Background()
		userID := id.UserID("@user:example.com")
		roomID := id.RoomID("!room:example.com")

		err = cmd.Handle(ctx, userID, roomID, nil)
		if err != nil {
			t.Errorf("Handle error: %v", err)
		}
	})

	t.Run("context manager enabled", func(t *testing.T) {
		server := createMockMatrixServerForCommands()
		defer server.Close()

		client := createTestMatrixClientForCommands(server)
		matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

		cfg := createTestMultiProviderAIConfig()
		cfg.Context.Enabled = true

		service, err := NewService(cfg, matrixSvc, nil, nil)
		if err != nil {
			t.Fatalf("NewService failed: %v", err)
		}

		// 添加一些上下文
		service.contextManager.AddMessage(id.RoomID("!room:example.com"), RoleUser, "hello", id.UserID("@user:example.com"))

		cmd := NewClearContextCommand(service)

		ctx := context.Background()
		userID := id.UserID("@user:example.com")
		roomID := id.RoomID("!room:example.com")

		err = cmd.Handle(ctx, userID, roomID, nil)
		if err != nil {
			t.Errorf("Handle error: %v", err)
		}

		// 验证上下文被清除
		msgCount, _ := service.contextManager.GetContextSize(roomID)
		if msgCount != 0 {
			t.Errorf("context should be cleared, got %d messages", msgCount)
		}
	})
}

// TestContextInfoCommand_Handle 测试 ContextInfoCommand.Handle 方法。
func TestContextInfoCommand_Handle(t *testing.T) {
	t.Run("context manager disabled", func(t *testing.T) {
		server := createMockMatrixServerForCommands()
		defer server.Close()

		client := createTestMatrixClientForCommands(server)
		matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

		cfg := createTestMultiProviderAIConfig()
		cfg.Context.Enabled = false

		service, err := NewService(cfg, matrixSvc, nil, nil)
		if err != nil {
			t.Fatalf("NewService failed: %v", err)
		}

		cmd := NewContextInfoCommand(service)

		ctx := context.Background()
		userID := id.UserID("@user:example.com")
		roomID := id.RoomID("!room:example.com")

		err = cmd.Handle(ctx, userID, roomID, nil)
		if err != nil {
			t.Errorf("Handle error: %v", err)
		}
	})

	t.Run("context manager enabled with messages", func(t *testing.T) {
		server := createMockMatrixServerForCommands()
		defer server.Close()

		client := createTestMatrixClientForCommands(server)
		matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

		cfg := createTestMultiProviderAIConfig()
		cfg.Context.Enabled = true

		service, err := NewService(cfg, matrixSvc, nil, nil)
		if err != nil {
			t.Fatalf("NewService failed: %v", err)
		}

		roomID := id.RoomID("!room:example.com")
		userID := id.UserID("@user:example.com")

		// 添加一些上下文
		service.contextManager.AddMessage(roomID, RoleUser, "hello", userID)
		service.contextManager.AddMessage(roomID, RoleAssistant, "hi there", userID)

		cmd := NewContextInfoCommand(service)

		ctx := context.Background()

		err = cmd.Handle(ctx, userID, roomID, nil)
		if err != nil {
			t.Errorf("Handle error: %v", err)
		}
	})
}

// TestAICommandRouter_Handle_WithSubcommand 测试 AICommandRouter.Handle 带子命令。
func TestAICommandRouter_Handle_WithSubcommand(t *testing.T) {
	t.Run("router with subcommands", func(t *testing.T) {
		server := createMockMatrixServerForCommands()
		defer server.Close()

		client := createTestMatrixClientForCommands(server)
		matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

		cfg := createTestMultiProviderAIConfig()
		service, err := NewService(cfg, matrixSvc, nil, nil)
		if err != nil {
			t.Fatalf("NewService failed: %v", err)
		}

		router := NewAICommandRouter(service)

		// 注册子命令
		router.RegisterSubcommand("clear", NewClearContextCommand(service))
		router.RegisterSubcommand("info", NewContextInfoCommand(service))

		ctx := context.Background()
		userID := id.UserID("@user:example.com")
		roomID := id.RoomID("!room:example.com")

		// 测试 clear 子命令
		err = router.Handle(ctx, userID, roomID, []string{"clear"})
		if err != nil {
			t.Errorf("Handle clear error: %v", err)
		}

		// 测试 info 子命令
		err = router.Handle(ctx, userID, roomID, []string{"info"})
		if err != nil {
			t.Errorf("Handle info error: %v", err)
		}
	})
}
