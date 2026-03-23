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
