// Package config_test 包含配置加载和验证的单元测试。
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatrixConfigUseTokenAuth(t *testing.T) {
	tests := []struct {
		name      string
		config    MatrixConfig
		wantToken bool
		wantPass  bool
	}{
		{"只有 access_token", MatrixConfig{AccessToken: "token123"}, true, false},
		{"只有 password", MatrixConfig{Password: "pass123"}, false, true},
		{"两者都有 (token 优先)", MatrixConfig{AccessToken: "token", Password: "pass"}, true, false},
		{"两者都没有", MatrixConfig{}, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.config.UseTokenAuth(); got != tt.wantToken {
				t.Errorf("UseTokenAuth() = %v, want %v", got, tt.wantToken)
			}
			if got := tt.config.UsePasswordAuth(); got != tt.wantPass {
				t.Errorf("UsePasswordAuth() = %v, want %v", got, tt.wantPass)
			}
		})
	}
}

func TestMatrixConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  MatrixConfig
		wantErr bool
		errMsg  string
	}{
		{"有效配置 (token)", MatrixConfig{Homeserver: "https://matrix.org", UserID: "@bot:matrix.org", AccessToken: "token"}, false, ""},
		{"有效配置 (password)", MatrixConfig{Homeserver: "https://matrix.org", UserID: "@bot:matrix.org", Password: "pass"}, false, ""},
		{"缺少 homeserver", MatrixConfig{UserID: "@bot:matrix.org", AccessToken: "token"}, true, "homeserver is required"},
		{"缺少 user_id", MatrixConfig{Homeserver: "https://matrix.org", AccessToken: "token"}, true, "user_id is required"},
		{"缺少认证", MatrixConfig{Homeserver: "https://matrix.org", UserID: "@bot:matrix.org"}, true, "either password or access_token"},
		{"E2EE 缺少 session path", MatrixConfig{Homeserver: "https://matrix.org", UserID: "@bot:matrix.org", AccessToken: "token", EnableE2EE: true}, true, "e2ee_session_path is required"},
		{"E2EE 有效", MatrixConfig{Homeserver: "https://matrix.org", UserID: "@bot:matrix.org", AccessToken: "token", EnableE2EE: true, E2EESessionPath: "./session.db"}, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
				t.Errorf("Validate() error = %v, want error containing %q", err, tt.errMsg)
			}
		})
	}
}

func TestAIConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  AIConfig
		wantErr bool
		errMsg  string
	}{
		{"禁用时不验证", AIConfig{Enabled: false}, false, ""},
		{"有效配置", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", TimeoutSeconds: 30}, false, ""},
		{"缺少 provider", AIConfig{Enabled: true, BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4"}, true, "provider is required"},
		{"缺少 base_url", AIConfig{Enabled: true, Provider: "openai", APIKey: "key", DefaultModel: "gpt-4"}, true, "base_url is required"},
		{"缺少 api_key", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", DefaultModel: "gpt-4"}, true, "api_key is required"},
		{"缺少 default_model", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key"}, true, "default_model is required"},
		{"温度过低", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", Temperature: -0.1}, true, "temperature must be between 0 and 2"},
		{"温度过高", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", Temperature: 2.1}, true, "temperature must be between 0 and 2"},
		{"温度边界 0", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", Temperature: 0, TimeoutSeconds: 30}, false, ""},
		{"温度边界 2", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", Temperature: 2, TimeoutSeconds: 30}, false, ""},
		{"timeout 无效", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", TimeoutSeconds: 0}, true, "timeout_seconds must be positive"},
		{"timeout 负数", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", TimeoutSeconds: -1}, true, "timeout_seconds must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
				t.Errorf("Validate() error = %v, want error containing %q", err, tt.errMsg)
			}
		})
	}
}

func TestAIConfigGetModelConfig(t *testing.T) {
	globalConfig := AIConfig{
		Provider:    "openai",
		BaseURL:     "https://api.openai.com/v1",
		APIKey:      "global-key",
		MaxTokens:   4096,
		Temperature: 0.7,
		Models: map[string]ModelConfig{
			"fast": {
				Model:       "gpt-4o-mini",
				Temperature: 0.3,
			},
			"custom": {
				Model:    "gpt-4",
				Provider: "azure",
				BaseURL:  "https://custom.azure.com",
				APIKey:   "custom-key",
			},
		},
	}

	tests := []struct {
		name          string
		modelID       string
		wantModel     string
		wantProvider  string
		wantTemp      float64
		wantFound     bool
		wantMaxTokens int
		wantAPIKey    string
		wantBaseURL   string
	}{
		{"未知模型使用全局配置", "unknown", "unknown", "openai", 0.7, false, 4096, "global-key", "https://api.openai.com/v1"},
		{"fast 模型部分覆盖", "fast", "gpt-4o-mini", "openai", 0.3, true, 4096, "global-key", "https://api.openai.com/v1"},
		{"custom 模型完全覆盖", "custom", "gpt-4", "azure", 0.7, true, 4096, "custom-key", "https://custom.azure.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := globalConfig.GetModelConfig(tt.modelID)

			if found != tt.wantFound {
				t.Errorf("GetModelConfig() found = %v, want %v", found, tt.wantFound)
			}
			if got.Model != tt.wantModel {
				t.Errorf("GetModelConfig() Model = %v, want %v", got.Model, tt.wantModel)
			}
			if got.Provider != tt.wantProvider {
				t.Errorf("GetModelConfig() Provider = %v, want %v", got.Provider, tt.wantProvider)
			}
			if got.Temperature != tt.wantTemp {
				t.Errorf("GetModelConfig() Temperature = %v, want %v", got.Temperature, tt.wantTemp)
			}
			if got.MaxTokens != tt.wantMaxTokens {
				t.Errorf("GetModelConfig() MaxTokens = %v, want %v", got.MaxTokens, tt.wantMaxTokens)
			}
			if got.APIKey != tt.wantAPIKey {
				t.Errorf("GetModelConfig() APIKey = %v, want %v", got.APIKey, tt.wantAPIKey)
			}
			if got.BaseURL != tt.wantBaseURL {
				t.Errorf("GetModelConfig() BaseURL = %v, want %v", got.BaseURL, tt.wantBaseURL)
			}
		})
	}
}

func TestDefaultConfigs(t *testing.T) {
	t.Run("DefaultAIConfig", func(t *testing.T) {
		cfg := DefaultAIConfig()
		if cfg.Enabled {
			t.Error("Default AI should be disabled")
		}
		if cfg.MaxTokens != 4096 {
			t.Errorf("Default MaxTokens = %d, want 4096", cfg.MaxTokens)
		}
		if cfg.Temperature != 0.7 {
			t.Errorf("Default Temperature = %f, want 0.7", cfg.Temperature)
		}
		if cfg.TimeoutSeconds != 30 {
			t.Errorf("Default TimeoutSeconds = %d, want 30", cfg.TimeoutSeconds)
		}
		if !cfg.DirectChatAutoReply {
			t.Error("Default DirectChatAutoReply should be true")
		}
		if !cfg.GroupChatMentionReply {
			t.Error("Default GroupChatMentionReply should be true")
		}
	})

	t.Run("DefaultContextConfig", func(t *testing.T) {
		cfg := DefaultContextConfig()
		if !cfg.Enabled {
			t.Error("Default context should be enabled")
		}
		if cfg.MaxMessages != 50 {
			t.Errorf("Default MaxMessages = %d, want 50", cfg.MaxMessages)
		}
		if cfg.MaxTokens != 8000 {
			t.Errorf("Default MaxTokens = %d, want 8000", cfg.MaxTokens)
		}
		if cfg.ExpiryMinutes != 60 {
			t.Errorf("Default ExpiryMinutes = %d, want 60", cfg.ExpiryMinutes)
		}
	})

	t.Run("DefaultStreamEditConfig", func(t *testing.T) {
		cfg := DefaultStreamEditConfig()
		if cfg.Enabled {
			t.Error("Default stream edit should be disabled")
		}
		if cfg.CharThreshold != 10 {
			t.Errorf("Default CharThreshold = %d, want 10", cfg.CharThreshold)
		}
		if cfg.MaxEdits != 5 {
			t.Errorf("Default MaxEdits = %d, want 5", cfg.MaxEdits)
		}
	})

	t.Run("DefaultRetryConfig", func(t *testing.T) {
		cfg := DefaultRetryConfig()
		if !cfg.Enabled {
			t.Error("Default retry should be enabled")
		}
		if cfg.MaxRetries != 3 {
			t.Errorf("Default MaxRetries = %d, want 3", cfg.MaxRetries)
		}
		if cfg.BackoffFactor != 2.0 {
			t.Errorf("Default BackoffFactor = %f, want 2.0", cfg.BackoffFactor)
		}
	})

	t.Run("DefaultConfig", func(t *testing.T) {
		cfg := DefaultConfig()
		if cfg.Matrix.Homeserver != "https://matrix.org" {
			t.Errorf("Default Homeserver = %s, want https://matrix.org", cfg.Matrix.Homeserver)
		}
		if cfg.Matrix.DeviceName != "Saber Bot" {
			t.Errorf("Default DeviceName = %s, want Saber Bot", cfg.Matrix.DeviceName)
		}
		if cfg.AI.Enabled {
			t.Error("Default AI should be disabled")
		}
	})
}

func TestLoad(t *testing.T) {
	t.Run("文件不存在", func(t *testing.T) {
		_, err := Load("/nonexistent/path/config.yaml")
		if err == nil {
			t.Error("期望加载不存在的文件返回错误")
		}
	})

	t.Run("有效配置文件", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		content := `
matrix:
  homeserver: "https://matrix.org"
  user_id: "@bot:matrix.org"
  access_token: "test-token"

ai:
  enabled: true
  provider: "openai"
  base_url: "https://api.openai.com/v1"
  api_key: "test-key"
  default_model: "gpt-4"
`
		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			t.Fatalf("写入测试配置文件失败: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		if cfg.Matrix.Homeserver != "https://matrix.org" {
			t.Errorf("Homeserver = %s, want https://matrix.org", cfg.Matrix.Homeserver)
		}
		if cfg.Matrix.UserID != "@bot:matrix.org" {
			t.Errorf("UserID = %s, want @bot:matrix.org", cfg.Matrix.UserID)
		}
		if !cfg.AI.Enabled {
			t.Error("AI should be enabled")
		}
		if cfg.AI.Provider != "openai" {
			t.Errorf("Provider = %s, want openai", cfg.AI.Provider)
		}
	})

	t.Run("无效 YAML", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "invalid.yaml")

		if err := os.WriteFile(configPath, []byte("invalid: [yaml: content"), 0o644); err != nil {
			t.Fatalf("写入测试配置文件失败: %v", err)
		}

		_, err := Load(configPath)
		if err == nil {
			t.Error("期望加载无效 YAML 返回错误")
		}
	})

	t.Run("空路径使用默认", func(t *testing.T) {
		originalWd, _ := os.Getwd()
		tmpDir := t.TempDir()
		_ = os.Chdir(tmpDir)
		defer func() { _ = os.Chdir(originalWd) }()

		content := `matrix:
  homeserver: "https://test.matrix.org"
  user_id: "@test:test.org"
  access_token: "token"
`
		if err := os.WriteFile("config.yaml", []byte(content), 0o644); err != nil {
			t.Fatalf("写入测试配置文件失败: %v", err)
		}

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Matrix.Homeserver != "https://test.matrix.org" {
			t.Errorf("Homeserver = %s, want https://test.matrix.org", cfg.Matrix.Homeserver)
		}
	})
}

func TestLoadOrDefault(t *testing.T) {
	t.Run("文件不存在返回默认配置", func(t *testing.T) {
		cfg, err := LoadOrDefault("/nonexistent/path/config.yaml")
		if err != nil {
			t.Fatalf("LoadOrDefault() error = %v", err)
		}
		if cfg == nil {
			t.Fatal("LoadOrDefault() returned nil")
			return
		}
		if cfg.Matrix.Homeserver != "https://matrix.org" {
			t.Errorf("Default Homeserver = %s, want https://matrix.org", cfg.Matrix.Homeserver)
		}
	})

	t.Run("文件存在加载配置", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		content := `
matrix:
  homeserver: "https://custom.matrix.org"
  user_id: "@custom:matrix.org"
  access_token: "token"
`
		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			t.Fatalf("写入测试配置文件失败: %v", err)
		}

		cfg, err := LoadOrDefault(configPath)
		if err != nil {
			t.Fatalf("LoadOrDefault() error = %v", err)
		}
		if cfg.Matrix.Homeserver != "https://custom.matrix.org" {
			t.Errorf("Homeserver = %s, want https://custom.matrix.org", cfg.Matrix.Homeserver)
		}
	})
}

func TestGenerateExample(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "example.yaml")

	if err := GenerateExample(configPath); err != nil {
		t.Fatalf("GenerateExample() error = %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("读取生成的配置文件失败: %v", err)
	}

	content := string(data)
	if !contains(content, "homeserver:") {
		t.Error("示例配置应包含 homeserver")
	}
	if !contains(content, "user_id:") {
		t.Error("示例配置应包含 user_id")
	}
	if !contains(content, "ai:") {
		t.Error("示例配置应包含 ai 配置")
	}
}

func TestExampleConfig(t *testing.T) {
	content := ExampleConfig()

	if !contains(content, "matrix:") {
		t.Error("示例配置应包含 matrix 部分")
	}
	if !contains(content, "ai:") {
		t.Error("示例配置应包含 ai 部分")
	}
	if !contains(content, "homeserver:") {
		t.Error("示例配置应包含 homeserver 说明")
	}
	if !contains(content, "provider:") {
		t.Error("示例配置应包含 provider 说明")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
