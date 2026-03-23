package ai

import (
	"testing"

	"rua.plus/saber/internal/config"
)

func TestNewModelRegistry(t *testing.T) {
	tests := []struct {
		name           string
		cfg            *config.AIConfig
		wantModelCount int
		wantDefault    string
	}{
		{
			name:           "空配置",
			cfg:            nil,
			wantModelCount: 0,
			wantDefault:    "",
		},
		{
			name: "空配置对象",
			cfg: &config.AIConfig{
				DefaultModel: "",
				Providers:    make(map[string]config.ProviderConfig),
			},
			wantModelCount: 0,
			wantDefault:    "",
		},
		{
			name: "仅默认模型（完全限定名称）",
			cfg: &config.AIConfig{
				DefaultModel: "openai.gpt-4o-mini",
				Providers:    make(map[string]config.ProviderConfig),
			},
			wantModelCount: 1,
			wantDefault:    "openai.gpt-4o-mini",
		},
		{
			name: "多提供商配置",
			cfg: &config.AIConfig{
				DefaultModel: "openai.gpt-4o-mini",
				Providers: map[string]config.ProviderConfig{
					"openai": {
						BaseURL: "https://api.openai.com/v1",
						Models: map[string]config.ModelConfig{
							"gpt-4o-mini": {Model: "gpt-4o-mini"},
							"gpt-4o":      {Model: "gpt-4o"},
						},
					},
					"ollama": {
						BaseURL: "http://localhost:11434/v1",
						Models: map[string]config.ModelConfig{
							"llama3": {Model: "llama3"},
						},
					},
				},
			},
			wantModelCount: 3, // gpt-4o-mini, gpt-4o, llama3
			wantDefault:    "openai.gpt-4o-mini",
		},
		{
			name: "默认模型不在提供商模型列表中",
			cfg: &config.AIConfig{
				DefaultModel: "openai.custom-model",
				Providers: map[string]config.ProviderConfig{
					"openai": {
						BaseURL: "https://api.openai.com/v1",
						Models: map[string]config.ModelConfig{
							"gpt-4o-mini": {Model: "gpt-4o-mini"},
						},
					},
				},
			},
			wantModelCount: 2, // custom-model + gpt-4o-mini
			wantDefault:    "openai.custom-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewModelRegistry(tt.cfg)
			if registry == nil {
				t.Fatal("NewModelRegistry returned nil")
			}

			models := registry.ListModels()
			if len(models) != tt.wantModelCount {
				t.Errorf("ListModels() count = %d, want %d", len(models), tt.wantModelCount)
			}

			if got := registry.GetDefault(); got != tt.wantDefault {
				t.Errorf("GetDefault() = %q, want %q", got, tt.wantDefault)
			}

			if got := registry.GetConfigDefault(); got != tt.wantDefault {
				t.Errorf("GetConfigDefault() = %q, want %q", got, tt.wantDefault)
			}
		})
	}
}

func TestModelRegistry_SetDefault(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "openai.gpt-4o-mini",
		Providers: map[string]config.ProviderConfig{
			"openai": {
				BaseURL: "https://api.openai.com/v1",
				Models: map[string]config.ModelConfig{
					"gpt-4o-mini": {Model: "gpt-4o-mini"},
					"gpt-4o":      {Model: "gpt-4o"},
				},
			},
		},
	}

	registry := NewModelRegistry(cfg)

	// 初始默认模型
	if got := registry.GetDefault(); got != "openai.gpt-4o-mini" {
		t.Fatalf("initial default = %q, want %q", got, "openai.gpt-4o-mini")
	}

	// 切换到已存在的模型
	if err := registry.SetDefault("openai.gpt-4o"); err != nil {
		t.Errorf("SetDefault(\"openai.gpt-4o\") error = %v", err)
	}
	if got := registry.GetDefault(); got != "openai.gpt-4o" {
		t.Errorf("after SetDefault(\"openai.gpt-4o\") = %q, want %q", got, "openai.gpt-4o")
	}

	// 切换到不存在的模型（应该允许）
	if err := registry.SetDefault("openai.custom-model"); err != nil {
		t.Errorf("SetDefault(\"openai.custom-model\") error = %v", err)
	}
	if got := registry.GetDefault(); got != "openai.custom-model" {
		t.Errorf("after SetDefault(\"openai.custom-model\") = %q, want %q", got, "openai.custom-model")
	}
}

func TestModelRegistry_ResetDefault(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "openai.gpt-4o-mini",
		Providers: map[string]config.ProviderConfig{
			"openai": {
				BaseURL: "https://api.openai.com/v1",
				Models: map[string]config.ModelConfig{
					"gpt-4o-mini": {Model: "gpt-4o-mini"},
				},
			},
		},
	}

	registry := NewModelRegistry(cfg)

	// 切换默认模型
	_ = registry.SetDefault("openai.gpt-4o-mini")
	if got := registry.GetDefault(); got != "openai.gpt-4o-mini" {
		t.Fatalf("after SetDefault = %q, want %q", got, "openai.gpt-4o-mini")
	}

	// 重置
	registry.ResetDefault()
	if got := registry.GetDefault(); got != "openai.gpt-4o-mini" {
		t.Errorf("after ResetDefault() = %q, want %q", got, "openai.gpt-4o-mini")
	}
}

func TestModelRegistry_ListModels(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "openai.default",
		Providers: map[string]config.ProviderConfig{
			"openai": {
				BaseURL: "https://api.openai.com/v1",
				Models: map[string]config.ModelConfig{
					"apple":   {Model: "model-a"},
					"mango":   {Model: "model-m"},
					"zebra":   {Model: "model-z"},
					"default": {Model: "default-model"},
				},
			},
		},
	}

	registry := NewModelRegistry(cfg)
	models := registry.ListModels()

	// 检查排序
	if len(models) != 4 {
		t.Fatalf("ListModels() count = %d, want 4", len(models))
	}

	// 验证按 ID 字母顺序排序
	expectedOrder := []string{"openai.apple", "openai.default", "openai.mango", "openai.zebra"}
	for i, m := range models {
		if m.ID != expectedOrder[i] {
			t.Errorf("models[%d].ID = %q, want %q", i, m.ID, expectedOrder[i])
		}
	}
}

func TestModelRegistry_GetModelInfo(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "openai.default",
		Providers: map[string]config.ProviderConfig{
			"openai": {
				BaseURL: "https://api.openai.com/v1",
				Models: map[string]config.ModelConfig{
					"fast":    {Model: "gpt-4o-mini"},
					"default": {Model: "default-model"},
				},
			},
		},
	}

	registry := NewModelRegistry(cfg)

	// 获取存在的模型
	info, exists := registry.GetModelInfo("openai.fast")
	if !exists {
		t.Fatal("GetModelInfo(\"openai.fast\") exists = false, want true")
	}
	if info.ID != "openai.fast" {
		t.Errorf("info.ID = %q, want %q", info.ID, "openai.fast")
	}
	if info.Model != "gpt-4o-mini" {
		t.Errorf("info.Model = %q, want %q", info.Model, "gpt-4o-mini")
	}

	// 获取不存在的模型
	_, exists = registry.GetModelInfo("openai.nonexistent")
	if exists {
		t.Error("GetModelInfo(\"openai.nonexistent\") exists = true, want false")
	}
}

func TestModelRegistry_IsCurrentDefault(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "openai.default",
		Providers: map[string]config.ProviderConfig{
			"openai": {
				BaseURL: "https://api.openai.com/v1",
				Models: map[string]config.ModelConfig{
					"fast":    {Model: "gpt-4o-mini"},
					"default": {Model: "default-model"},
				},
			},
		},
	}

	registry := NewModelRegistry(cfg)

	// 检查初始默认模型
	if !registry.IsCurrentDefault("openai.default") {
		t.Error("IsCurrentDefault(\"openai.default\") = false, want true")
	}
	if registry.IsCurrentDefault("openai.fast") {
		t.Error("IsCurrentDefault(\"openai.fast\") = true, want false")
	}

	// 切换后检查
	_ = registry.SetDefault("openai.fast")
	if registry.IsCurrentDefault("openai.default") {
		t.Error("after switch, IsCurrentDefault(\"openai.default\") = true, want false")
	}
	if !registry.IsCurrentDefault("openai.fast") {
		t.Error("after switch, IsCurrentDefault(\"openai.fast\") = false, want true")
	}
}

func TestModelRegistry_RegisterModel(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "openai.default",
		Providers:    make(map[string]config.ProviderConfig),
	}

	registry := NewModelRegistry(cfg)
	initialCount := len(registry.ListModels())

	// 注册新模型
	registry.RegisterModel("openai.new-model", "gpt-4")

	models := registry.ListModels()
	if len(models) != initialCount+1 {
		t.Errorf("after RegisterModel, count = %d, want %d", len(models), initialCount+1)
	}

	info, exists := registry.GetModelInfo("openai.new-model")
	if !exists {
		t.Fatal("GetModelInfo(\"openai.new-model\") exists = false, want true")
	}
	if info.Model != "gpt-4" {
		t.Errorf("info.Model = %q, want %q", info.Model, "gpt-4")
	}
}

func TestModelRegistry_ValidateModel(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "openai.default",
		Providers: map[string]config.ProviderConfig{
			"openai": {
				BaseURL: "https://api.openai.com/v1",
				Models: map[string]config.ModelConfig{
					"fast":    {Model: "gpt-4o-mini"},
					"default": {Model: "default-model"},
				},
			},
		},
	}

	registry := NewModelRegistry(cfg)

	// 验证存在的模型
	if err := registry.ValidateModel("openai.fast"); err != nil {
		t.Errorf("ValidateModel(\"openai.fast\") error = %v, want nil", err)
	}

	// 验证默认模型
	if err := registry.ValidateModel("openai.default"); err != nil {
		t.Errorf("ValidateModel(\"openai.default\") error = %v, want nil", err)
	}

	// 验证不存在的模型（全局配置存在，应该允许）
	if err := registry.ValidateModel("openai.any-model"); err != nil {
		t.Errorf("ValidateModel(\"openai.any-model\") error = %v, want nil (global config allows)", err)
	}
}

func TestModelRegistry_ValidateModel_NilConfig(t *testing.T) {
	registry := NewModelRegistry(nil)

	// 无全局配置时，不存在的模型应该报错
	if err := registry.ValidateModel("any-model"); err == nil {
		t.Error("ValidateModel with nil config should fail for unknown model")
	}
}

func TestModelRegistry_Concurrency(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "openai.default",
		Providers: map[string]config.ProviderConfig{
			"openai": {
				BaseURL: "https://api.openai.com/v1",
				Models: map[string]config.ModelConfig{
					"fast":    {Model: "gpt-4o-mini"},
					"smart":   {Model: "gpt-4o"},
					"default": {Model: "default"},
				},
			},
		},
	}

	registry := NewModelRegistry(cfg)

	// 并发读写测试
	done := make(chan bool)

	// 并发 SetDefault
	for i := 0; i < 10; i++ {
		go func(idx int) {
			modelID := "openai.model-" + string(rune('a'+idx))
			_ = registry.SetDefault(modelID)
			done <- true
		}(i)
	}

	// 并发 GetDefault
	for i := 0; i < 10; i++ {
		go func() {
			_ = registry.GetDefault()
			done <- true
		}()
	}

	// 并发 ListModels
	for i := 0; i < 10; i++ {
		go func() {
			_ = registry.ListModels()
			done <- true
		}()
	}

	// 等待所有 goroutine 完成
	for i := 0; i < 30; i++ {
		<-done
	}
}

func TestModelInfo_IsConfigDefault(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "openai.fast",
		Providers: map[string]config.ProviderConfig{
			"openai": {
				BaseURL: "https://api.openai.com/v1",
				Models: map[string]config.ModelConfig{
					"fast":  {Model: "gpt-4o-mini"},
					"smart": {Model: "gpt-4o"},
				},
			},
		},
	}

	registry := NewModelRegistry(cfg)
	models := registry.ListModels()

	for _, m := range models {
		if m.ID == "openai.fast" && !m.IsConfigDefault {
			t.Error("openai.fast model should have IsConfigDefault = true")
		}
		if m.ID == "openai.smart" && m.IsConfigDefault {
			t.Error("openai.smart model should have IsConfigDefault = false")
		}
	}
}
