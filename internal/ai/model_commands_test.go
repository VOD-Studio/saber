package ai

import (
	"testing"

	"rua.plus/saber/internal/config"
)

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
				cfg := createTestAIConfig()
				cfg.DefaultModel = "default"
				return cfg
			}(),
			wantModelCount:  1, // just default
			wantDefault:     "default",
			wantModelsExist: nil,
		},
		{
			name: "有模型配置",
			cfg: func() *config.AIConfig {
				cfg := createTestAIConfig()
				cfg.DefaultModel = "fast"
				cfg.Models = map[string]config.ModelConfig{
					"fast":  {Model: "gpt-4o-mini"},
					"smart": {Model: "gpt-4o"},
				}
				return cfg
			}(),
			wantModelCount:  2, // fast + smart
			wantDefault:     "fast",
			wantModelsExist: []string{"fast", "smart"},
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
	cfg := createTestAIConfig()
	cfg.DefaultModel = "fast"
	cfg.Models = map[string]config.ModelConfig{
		"fast":  {Model: "gpt-4o-mini"},
		"smart": {Model: "gpt-4o"},
	}

	t.Run("switch to existing model", func(t *testing.T) {
		service, err := NewService(cfg, nil, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewSwitchModelCommand(service)
		registry := cmd.service.GetModelRegistry()

		// 切换模型
		if err := registry.SetDefault("smart"); err != nil {
			t.Errorf("SetDefault error: %v", err)
		}

		if registry.GetDefault() != "smart" {
			t.Errorf("GetDefault() = %q, want %q", registry.GetDefault(), "smart")
		}
	})

	t.Run("switch to any model", func(t *testing.T) {
		service, err := NewService(cfg, nil, nil, nil)
		if err != nil {
			t.Fatalf("NewService error: %v", err)
		}

		cmd := NewSwitchModelCommand(service)
		registry := cmd.service.GetModelRegistry()

		// 切换到任意模型
		if err := registry.SetDefault("custom-model"); err != nil {
			t.Errorf("SetDefault error: %v", err)
		}

		if registry.GetDefault() != "custom-model" {
			t.Errorf("GetDefault() = %q, want %q", registry.GetDefault(), "custom-model")
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
		_ = registry.SetDefault("smart")

		// 配置默认应该保持不变
		if registry.GetConfigDefault() != "fast" {
			t.Errorf("GetConfigDefault() = %q, want %q", registry.GetConfigDefault(), "fast")
		}

		// 重置
		registry.ResetDefault()
		if registry.GetDefault() != "fast" {
			t.Errorf("after ResetDefault, GetDefault() = %q, want %q", registry.GetDefault(), "fast")
		}
	})
}

func TestCurrentModelCommand_Registry(t *testing.T) {
	cfg := createTestAIConfig()
	cfg.DefaultModel = "fast"
	cfg.Models = map[string]config.ModelConfig{
		"fast":  {Model: "gpt-4o-mini"},
		"smart": {Model: "gpt-4o"},
	}

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

		if !registry.IsCurrentDefault("fast") {
			t.Error("fast should be current default")
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
		_ = registry.SetDefault("smart")

		// 当前默认应该是 smart
		if registry.GetDefault() != "smart" {
			t.Errorf("GetDefault() = %q, want %q", registry.GetDefault(), "smart")
		}

		// 配置默认仍然应该是 fast
		if registry.GetConfigDefault() != "fast" {
			t.Errorf("GetConfigDefault() = %q, want %q", registry.GetConfigDefault(), "fast")
		}

		// IsCurrentDefault 应该正确反映状态
		if registry.IsCurrentDefault("fast") {
			t.Error("fast should NOT be current default after switch")
		}
		if !registry.IsCurrentDefault("smart") {
			t.Error("smart should be current default after switch")
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
