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
			name:           "nil config",
			cfg:            nil,
			wantModelCount: 0,
			wantDefault:    "",
		},
		{
			name: "empty config",
			cfg: &config.AIConfig{
				DefaultModel: "",
				Models:       make(map[string]config.ModelConfig),
			},
			wantModelCount: 0,
			wantDefault:    "",
		},
		{
			name: "only default model",
			cfg: &config.AIConfig{
				DefaultModel: "gpt-4o-mini",
				Models:       make(map[string]config.ModelConfig),
			},
			wantModelCount: 1,
			wantDefault:    "gpt-4o-mini",
		},
		{
			name: "default model and custom models",
			cfg: &config.AIConfig{
				DefaultModel: "gpt-4o-mini",
				Models: map[string]config.ModelConfig{
					"fast":   {Model: "gpt-4o-mini"},
					"smart":  {Model: "gpt-4o"},
					"claude": {Model: "claude-3-opus"},
				},
			},
			wantModelCount: 4, // default + 3 custom
			wantDefault:    "gpt-4o-mini",
		},
		{
			name: "default model not in models map",
			cfg: &config.AIConfig{
				DefaultModel: "default-model",
				Models: map[string]config.ModelConfig{
					"fast": {Model: "gpt-4o-mini"},
				},
			},
			wantModelCount: 2,
			wantDefault:    "default-model",
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
		DefaultModel: "gpt-4o-mini",
		Models: map[string]config.ModelConfig{
			"fast":  {Model: "gpt-4o-mini"},
			"smart": {Model: "gpt-4o"},
		},
	}

	registry := NewModelRegistry(cfg)

	// 初始默认模型
	if got := registry.GetDefault(); got != "gpt-4o-mini" {
		t.Fatalf("initial default = %q, want %q", got, "gpt-4o-mini")
	}

	// 切换到已存在的模型
	if err := registry.SetDefault("smart"); err != nil {
		t.Errorf("SetDefault(\"smart\") error = %v", err)
	}
	if got := registry.GetDefault(); got != "smart" {
		t.Errorf("after SetDefault(\"smart\") = %q, want %q", got, "smart")
	}

	// 切换到不存在的模型（应该允许）
	if err := registry.SetDefault("custom-model"); err != nil {
		t.Errorf("SetDefault(\"custom-model\") error = %v", err)
	}
	if got := registry.GetDefault(); got != "custom-model" {
		t.Errorf("after SetDefault(\"custom-model\") = %q, want %q", got, "custom-model")
	}
}

func TestModelRegistry_ResetDefault(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "gpt-4o-mini",
		Models: map[string]config.ModelConfig{
			"fast": {Model: "gpt-4o-mini"},
		},
	}

	registry := NewModelRegistry(cfg)

	// 切换默认模型
	_ = registry.SetDefault("fast")
	if got := registry.GetDefault(); got != "fast" {
		t.Fatalf("after SetDefault = %q, want %q", got, "fast")
	}

	// 重置
	registry.ResetDefault()
	if got := registry.GetDefault(); got != "gpt-4o-mini" {
		t.Errorf("after ResetDefault() = %q, want %q", got, "gpt-4o-mini")
	}
}

func TestModelRegistry_ListModels(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "default",
		Models: map[string]config.ModelConfig{
			"zebra": {Model: "model-z"},
			"apple": {Model: "model-a"},
			"mango": {Model: "model-m"},
		},
	}

	registry := NewModelRegistry(cfg)
	models := registry.ListModels()

	// 检查排序
	if len(models) != 4 { // default + 3 models
		t.Fatalf("ListModels() count = %d, want 4", len(models))
	}

	// 验证按 ID 字母顺序排序
	expectedOrder := []string{"apple", "default", "mango", "zebra"}
	for i, m := range models {
		if m.ID != expectedOrder[i] {
			t.Errorf("models[%d].ID = %q, want %q", i, m.ID, expectedOrder[i])
		}
	}
}

func TestModelRegistry_GetModelInfo(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "default",
		Models: map[string]config.ModelConfig{
			"fast": {Model: "gpt-4o-mini"},
		},
	}

	registry := NewModelRegistry(cfg)

	// 获取存在的模型
	info, exists := registry.GetModelInfo("fast")
	if !exists {
		t.Fatal("GetModelInfo(\"fast\") exists = false, want true")
	}
	if info.ID != "fast" {
		t.Errorf("info.ID = %q, want %q", info.ID, "fast")
	}
	if info.Model != "gpt-4o-mini" {
		t.Errorf("info.Model = %q, want %q", info.Model, "gpt-4o-mini")
	}

	// 获取不存在的模型
	_, exists = registry.GetModelInfo("nonexistent")
	if exists {
		t.Error("GetModelInfo(\"nonexistent\") exists = true, want false")
	}
}

func TestModelRegistry_IsCurrentDefault(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "default",
		Models: map[string]config.ModelConfig{
			"fast": {Model: "gpt-4o-mini"},
		},
	}

	registry := NewModelRegistry(cfg)

	// 检查初始默认模型
	if !registry.IsCurrentDefault("default") {
		t.Error("IsCurrentDefault(\"default\") = false, want true")
	}
	if registry.IsCurrentDefault("fast") {
		t.Error("IsCurrentDefault(\"fast\") = true, want false")
	}

	// 切换后检查
	_ = registry.SetDefault("fast")
	if registry.IsCurrentDefault("default") {
		t.Error("after switch, IsCurrentDefault(\"default\") = true, want false")
	}
	if !registry.IsCurrentDefault("fast") {
		t.Error("after switch, IsCurrentDefault(\"fast\") = false, want true")
	}
}

func TestModelRegistry_RegisterModel(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "default",
		Models:       make(map[string]config.ModelConfig),
	}

	registry := NewModelRegistry(cfg)
	initialCount := len(registry.ListModels())

	// 注册新模型
	registry.RegisterModel("new-model", "gpt-4")

	models := registry.ListModels()
	if len(models) != initialCount+1 {
		t.Errorf("after RegisterModel, count = %d, want %d", len(models), initialCount+1)
	}

	info, exists := registry.GetModelInfo("new-model")
	if !exists {
		t.Fatal("GetModelInfo(\"new-model\") exists = false, want true")
	}
	if info.Model != "gpt-4" {
		t.Errorf("info.Model = %q, want %q", info.Model, "gpt-4")
	}
}

func TestModelRegistry_ValidateModel(t *testing.T) {
	cfg := &config.AIConfig{
		DefaultModel: "default",
		Models: map[string]config.ModelConfig{
			"fast": {Model: "gpt-4o-mini"},
		},
	}

	registry := NewModelRegistry(cfg)

	// 验证存在的模型
	if err := registry.ValidateModel("fast"); err != nil {
		t.Errorf("ValidateModel(\"fast\") error = %v, want nil", err)
	}

	// 验证默认模型
	if err := registry.ValidateModel("default"); err != nil {
		t.Errorf("ValidateModel(\"default\") error = %v, want nil", err)
	}

	// 验证不存在的模型（全局配置存在，应该允许）
	if err := registry.ValidateModel("any-model"); err != nil {
		t.Errorf("ValidateModel(\"any-model\") error = %v, want nil (global config allows)", err)
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
		DefaultModel: "default",
		Models: map[string]config.ModelConfig{
			"fast":  {Model: "gpt-4o-mini"},
			"smart": {Model: "gpt-4o"},
		},
	}

	registry := NewModelRegistry(cfg)

	// 并发读写测试
	done := make(chan bool)

	// 并发 SetDefault
	for i := 0; i < 10; i++ {
		go func(idx int) {
			modelID := "model-" + string(rune('a'+idx))
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
		DefaultModel: "fast",
		Models: map[string]config.ModelConfig{
			"fast":  {Model: "gpt-4o-mini"},
			"smart": {Model: "gpt-4o"},
		},
	}

	registry := NewModelRegistry(cfg)
	models := registry.ListModels()

	for _, m := range models {
		if m.ID == "fast" && !m.IsConfigDefault {
			t.Error("fast model should have IsConfigDefault = true")
		}
		if m.ID == "smart" && m.IsConfigDefault {
			t.Error("smart model should have IsConfigDefault = false")
		}
	}
}
