package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// createMockMatrixServer 创建一个模拟 Matrix API 的测试服务器。
func createMockMatrixServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 返回简单的成功响应
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"event_id":"$test_event_id:example.com"}`))
	}))
}

// createTestMatrixClient 创建一个测试用的 Matrix 客户端。
func createTestMatrixClient(server *httptest.Server) *mautrix.Client {
	client, _ := mautrix.NewClient(server.URL, id.UserID("@test:example.com"), "test_token")
	return client
}

func TestModelsCommand_Registry(t *testing.T) {
	tests := []struct {
		name            string
		cfg             *config.AIConfig
		wantModelCount  int
		wantDefault     string
		wantModelsExist []string
	}{
		{
			name: "无模型配置",
			cfg: func() *config.AIConfig {
				cfg := createTestMultiProviderAIConfig()
				cfg.DefaultModel = "openai.gpt-4"
				return cfg
			}(),
			wantModelCount:  3, // gpt-4, gpt-4o-mini, gpt-4o
			wantDefault:     "openai.gpt-4",
			wantModelsExist: []string{"openai.gpt-4", "openai.gpt-4o-mini", "openai.gpt-4o"},
		},
		{
			name: "有模型配置",
			cfg: func() *config.AIConfig {
				cfg := createTestMultiProviderAIConfig()
				cfg.DefaultModel = "openai.gpt-4o-mini"
				return cfg
			}(),
			wantModelCount:  3, // gpt-4, gpt-4o-mini, gpt-4o
			wantDefault:     "openai.gpt-4o-mini",
			wantModelsExist: []string{"openai.gpt-4o-mini", "openai.gpt-4o"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, err := NewService(tt.cfg, nil, nil, nil)
			if err != nil {
				t.Fatalf("NewService error: %v", err)
			}

			cmd := NewModelsCommand(service)
			registry := cmd.service.GetModelRegistry()

			if registry.GetDefault() != tt.wantDefault {
				t.Errorf("GetDefault() = %q, want %q", registry.GetDefault(), tt.wantDefault)
			}

			models := registry.ListModels()
			if len(models) != tt.wantModelCount {
				t.Errorf("ListModels() count = %d, want %d", len(models), tt.wantModelCount)
			}

			for _, modelID := range tt.wantModelsExist {
				if _, exists := registry.GetModelInfo(modelID); !exists {
					t.Errorf("Model %q should exist", modelID)
				}
			}
		})
	}
}

func TestSwitchModelCommand_Registry(t *testing.T) {
	cfg := createTestMultiProviderAIConfig()
	cfg.DefaultModel = "openai.gpt-4o-mini"

	t.Run("switch to existing model", func(t *testing.T) {
		service, err := NewService(cfg, nil, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewSwitchModelCommand(service)
		registry := cmd.service.GetModelRegistry()

		// 切换模型
		if err := registry.SetDefault("openai.gpt-4o"); err != nil {
			t.Errorf("SetDefault error: %v", err)
		}

		if registry.GetDefault() != "openai.gpt-4o" {
			t.Errorf("GetDefault() = %q, want %q", registry.GetDefault(), "openai.gpt-4o")
		}
	})

	t.Run("switch to any model", func(t *testing.T) {
		service, err := NewService(cfg, nil, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewSwitchModelCommand(service)
		registry := cmd.service.GetModelRegistry()

		// 切换到任意模型（即使不在配置中）
		if err := registry.SetDefault("openai.custom-model"); err != nil {
			t.Errorf("SetDefault error: %v", err)
		}

		if registry.GetDefault() != "openai.custom-model" {
			t.Errorf("GetDefault() = %q, want %q", registry.GetDefault(), "openai.custom-model")
		}
	})

	t.Run("config default preserved", func(t *testing.T) {
		service, err := NewService(cfg, nil, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewSwitchModelCommand(service)
		registry := cmd.service.GetModelRegistry()

		// 切换模型
		_ = registry.SetDefault("openai.gpt-4o")

		// 配置默认应该保持不变
		if registry.GetConfigDefault() != "openai.gpt-4o-mini" {
			t.Errorf("GetConfigDefault() = %q, want %q", registry.GetConfigDefault(), "openai.gpt-4o-mini")
		}

		// 重置
		registry.ResetDefault()
		if registry.GetDefault() != "openai.gpt-4o-mini" {
			t.Errorf("after ResetDefault, GetDefault() = %q, want %q", registry.GetDefault(), "openai.gpt-4o-mini")
		}
	})
}

func TestCurrentModelCommand_Registry(t *testing.T) {
	cfg := createTestMultiProviderAIConfig()
	cfg.DefaultModel = "openai.gpt-4o-mini"

	t.Run("initial state", func(t *testing.T) {
		service, err := NewService(cfg, nil, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewCurrentModelCommand(service)
		registry := cmd.service.GetModelRegistry()

		// 初始状态：当前默认和配置默认相同
		if registry.GetDefault() != registry.GetConfigDefault() {
			t.Error("Initial state: GetDefault should equal GetConfigDefault")
		}

		if !registry.IsCurrentDefault("openai.gpt-4o-mini") {
			t.Error("openai.gpt-4o-mini should be current default")
		}
	})

	t.Run("after switch", func(t *testing.T) {
		service, err := NewService(cfg, nil, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewCurrentModelCommand(service)
		registry := cmd.service.GetModelRegistry()

		// 切换模型
		_ = registry.SetDefault("openai.gpt-4o")

		// 当前默认应该是 openai.gpt-4o
		if registry.GetDefault() != "openai.gpt-4o" {
			t.Errorf("GetDefault() = %q, want %q", registry.GetDefault(), "openai.gpt-4o")
		}

		// 配置默认仍然应该是 openai.gpt-4o-mini
		if registry.GetConfigDefault() != "openai.gpt-4o-mini" {
			t.Errorf("GetConfigDefault() = %q, want %q", registry.GetConfigDefault(), "openai.gpt-4o-mini")
		}

		// IsCurrentDefault 应该正确反映状态
		if registry.IsCurrentDefault("openai.gpt-4o-mini") {
			t.Error("openai.gpt-4o-mini should NOT be current default after switch")
		}
		if !registry.IsCurrentDefault("openai.gpt-4o") {
			t.Error("openai.gpt-4o should be current default after switch")
		}
	})
}

func TestModelCommands_NilService(t *testing.T) {
	// 测试命令处理器在 nil service 下的行为
	t.Run("ModelsCommand with nil service", func(t *testing.T) {
		var service *Service
		cmd := NewModelsCommand(service)
		if cmd == nil {
			t.Error("NewModelsCommand should not return nil")
		}
	})

	t.Run("SwitchModelCommand with nil service", func(t *testing.T) {
		var service *Service
		cmd := NewSwitchModelCommand(service)
		if cmd == nil {
			t.Error("NewSwitchModelCommand should not return nil")
		}
	})

	t.Run("CurrentModelCommand with nil service", func(t *testing.T) {
		var service *Service
		cmd := NewCurrentModelCommand(service)
		if cmd == nil {
			t.Error("NewCurrentModelCommand should not return nil")
		}
	})
}

// TestModelsCommand_Handle 测试 ModelsCommand.Handle 方法。
func TestModelsCommand_Handle(t *testing.T) {
	t.Run("with mock matrix service", func(t *testing.T) {
		server := createMockMatrixServer()
		defer server.Close()

		client := createTestMatrixClient(server)
		matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

		cfg := createTestMultiProviderAIConfig()
		cfg.DefaultModel = "openai.gpt-4"

		service, err := NewService(cfg, matrixSvc, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewModelsCommand(service)

		ctx := context.Background()
		userID := id.UserID("@user:example.com")
		roomID := id.RoomID("!room:example.com")

		err = cmd.Handle(ctx, userID, roomID, nil)
		if err != nil {
			t.Errorf("Handle error: %v", err)
		}
	})

	t.Run("empty models list", func(t *testing.T) {
		server := createMockMatrixServer()
		defer server.Close()

		client := createTestMatrixClient(server)
		matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

		cfg := createTestMultiProviderAIConfig()
		cfg.DefaultModel = "openai.gpt-4"
		cfg.Models = map[string]config.ModelConfig{} // 空模型列表

		service, err := NewService(cfg, matrixSvc, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewModelsCommand(service)

		ctx := context.Background()
		userID := id.UserID("@user:example.com")
		roomID := id.RoomID("!room:example.com")

		err = cmd.Handle(ctx, userID, roomID, nil)
		if err != nil {
			t.Errorf("Handle error: %v", err)
		}
	})
}

// TestSwitchModelCommand_Handle 测试 SwitchModelCommand.Handle 方法。
func TestSwitchModelCommand_Handle(t *testing.T) {
	t.Run("no args", func(t *testing.T) {
		server := createMockMatrixServer()
		defer server.Close()

		client := createTestMatrixClient(server)
		matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

		cfg := createTestMultiProviderAIConfig()
		cfg.DefaultModel = "openai.gpt-4"

		service, err := NewService(cfg, matrixSvc, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewSwitchModelCommand(service)

		ctx := context.Background()
		userID := id.UserID("@user:example.com")
		roomID := id.RoomID("!room:example.com")

		err = cmd.Handle(ctx, userID, roomID, []string{})
		if err != nil {
			t.Errorf("Handle error: %v", err)
		}
	})

	t.Run("empty model id", func(t *testing.T) {
		server := createMockMatrixServer()
		defer server.Close()

		client := createTestMatrixClient(server)
		matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

		cfg := createTestMultiProviderAIConfig()
		cfg.DefaultModel = "openai.gpt-4"

		service, err := NewService(cfg, matrixSvc, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewSwitchModelCommand(service)

		ctx := context.Background()
		userID := id.UserID("@user:example.com")
		roomID := id.RoomID("!room:example.com")

		err = cmd.Handle(ctx, userID, roomID, []string{""})
		if err != nil {
			t.Errorf("Handle error: %v", err)
		}
	})

	t.Run("valid model switch", func(t *testing.T) {
		server := createMockMatrixServer()
		defer server.Close()

		client := createTestMatrixClient(server)
		matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

		cfg := createTestMultiProviderAIConfig()
		cfg.DefaultModel = "openai.gpt-4"

		service, err := NewService(cfg, matrixSvc, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewSwitchModelCommand(service)

		ctx := context.Background()
		userID := id.UserID("@user:example.com")
		roomID := id.RoomID("!room:example.com")

		err = cmd.Handle(ctx, userID, roomID, []string{"openai.gpt-4o-mini"})
		if err != nil {
			t.Errorf("Handle error: %v", err)
		}

		// 验证模型已切换
		registry := service.GetModelRegistry()
		if registry.GetDefault() != "openai.gpt-4o-mini" {
			t.Errorf("default model = %q, want %q", registry.GetDefault(), "openai.gpt-4o-mini")
		}
	})

	t.Run("invalid model switch", func(t *testing.T) {
		server := createMockMatrixServer()
		defer server.Close()

		client := createTestMatrixClient(server)
		matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

		cfg := createTestMultiProviderAIConfig()
		cfg.DefaultModel = "openai.gpt-4"

		service, err := NewService(cfg, matrixSvc, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewSwitchModelCommand(service)

		ctx := context.Background()
		userID := id.UserID("@user:example.com")
		roomID := id.RoomID("!room:example.com")

		// 切换到不存在的模型，Handle 会返回错误信息但不应该 panic
		err = cmd.Handle(ctx, userID, roomID, []string{"invalid-model"})
		if err != nil {
			t.Errorf("Handle error: %v", err)
		}
	})
}

// TestCurrentModelCommand_Handle 测试 CurrentModelCommand.Handle 方法。
func TestCurrentModelCommand_Handle(t *testing.T) {
	t.Run("initial state", func(t *testing.T) {
		server := createMockMatrixServer()
		defer server.Close()

		client := createTestMatrixClient(server)
		matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

		cfg := createTestMultiProviderAIConfig()
		cfg.DefaultModel = "openai.gpt-4"

		service, err := NewService(cfg, matrixSvc, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewCurrentModelCommand(service)

		ctx := context.Background()
		userID := id.UserID("@user:example.com")
		roomID := id.RoomID("!room:example.com")

		err = cmd.Handle(ctx, userID, roomID, nil)
		if err != nil {
			t.Errorf("Handle error: %v", err)
		}
	})

	t.Run("after model switch", func(t *testing.T) {
		server := createMockMatrixServer()
		defer server.Close()

		client := createTestMatrixClient(server)
		matrixSvc := matrix.NewCommandService(client, id.UserID("@bot:example.com"), nil)

		cfg := createTestMultiProviderAIConfig()
		cfg.DefaultModel = "openai.gpt-4"

		service, err := NewService(cfg, matrixSvc, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		// 先切换模型
		registry := service.GetModelRegistry()
		require.NoError(t, registry.SetDefault("openai.gpt-4o"))

		cmd := NewCurrentModelCommand(service)

		ctx := context.Background()
		userID := id.UserID("@user:example.com")
		roomID := id.RoomID("!room:example.com")

		err = cmd.Handle(ctx, userID, roomID, nil)
		if err != nil {
			t.Errorf("Handle error: %v", err)
		}
	})
}
