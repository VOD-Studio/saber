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
		{"MaxConcurrentEvents 为负数", MatrixConfig{Homeserver: "https://matrix.org", UserID: "@bot:matrix.org", AccessToken: "token", MaxConcurrentEvents: -1}, true, "max_concurrent_events must be non-negative"},
		{"MaxConcurrentEvents 为 0 (有效)", MatrixConfig{Homeserver: "https://matrix.org", UserID: "@bot:matrix.org", AccessToken: "token", MaxConcurrentEvents: 0}, false, ""},
		{"MaxConcurrentEvents 为 10 (有效)", MatrixConfig{Homeserver: "https://matrix.org", UserID: "@bot:matrix.org", AccessToken: "token", MaxConcurrentEvents: 10}, false, ""},
		{"MaxConcurrentEvents 为 100 (有效)", MatrixConfig{Homeserver: "https://matrix.org", UserID: "@bot:matrix.org", AccessToken: "token", MaxConcurrentEvents: 100}, false, ""},
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
		// 基本测试
		{"禁用时不验证", AIConfig{Enabled: false}, false, ""},

		// 旧格式配置（向后兼容）
		{"旧格式-有效配置", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", TimeoutSeconds: 30, ToolCalling: ToolCallingConfig{MaxIterations: 5}}, false, ""},
		{"旧格式-缺少 base_url", AIConfig{Enabled: true, Provider: "openai", APIKey: "key", DefaultModel: "gpt-4"}, true, "base_url is required"},
		{"旧格式-缺少 default_model", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key"}, true, "default_model is required"},
		{"旧格式-api_key 可为空", AIConfig{Enabled: true, Provider: "ollama", BaseURL: "http://localhost:11434/v1", DefaultModel: "llama3", TimeoutSeconds: 30, ToolCalling: ToolCallingConfig{MaxIterations: 5}}, false, ""},

		// 新格式配置（多提供商）
		{"新格式-有效配置", AIConfig{Enabled: true, DefaultModel: "openai.gpt-4", Providers: map[string]ProviderConfig{"openai": {BaseURL: "https://api.openai.com/v1", APIKey: "key"}}, TimeoutSeconds: 30, ToolCalling: ToolCallingConfig{MaxIterations: 5}}, false, ""},
		{"新格式-提供商不存在", AIConfig{Enabled: true, DefaultModel: "anthropic.claude", Providers: map[string]ProviderConfig{"openai": {BaseURL: "https://api.openai.com/v1"}}, TimeoutSeconds: 30}, true, "provider \"anthropic\" not found"},
		{"新格式-default_model 格式错误", AIConfig{Enabled: true, DefaultModel: "gpt-4", Providers: map[string]ProviderConfig{"openai": {BaseURL: "https://api.openai.com/v1"}}, TimeoutSeconds: 30}, true, "invalid model id format"},

		// 参数验证
		{"温度过低", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", Temperature: -0.1}, true, "temperature must be between 0 and 2"},
		{"温度过高", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", Temperature: 2.1}, true, "temperature must be between 0 and 2"},
		{"温度边界 0", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", Temperature: 0, TimeoutSeconds: 30, ToolCalling: ToolCallingConfig{MaxIterations: 5}}, false, ""},
		{"温度边界 2", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", Temperature: 2, TimeoutSeconds: 30, ToolCalling: ToolCallingConfig{MaxIterations: 5}}, false, ""},
		{"timeout 无效", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", TimeoutSeconds: 0}, true, "timeout_seconds must be positive"},
		{"timeout 负数", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", TimeoutSeconds: -1}, true, "timeout_seconds must be positive"},
		{"tool_calling 迭代次数为0", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", TimeoutSeconds: 30, ToolCalling: ToolCallingConfig{MaxIterations: 0}}, true, "max_iterations must be at least 1"},
		{"tool_calling 迭代次数为负数", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", TimeoutSeconds: 30, ToolCalling: ToolCallingConfig{MaxIterations: -1}}, true, "max_iterations must be at least 1"},
		{"tool_calling 迭代次数边界 1", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", TimeoutSeconds: 30, ToolCalling: ToolCallingConfig{MaxIterations: 1}}, false, ""},
		{"tool_calling 迭代次数边界 20", AIConfig{Enabled: true, Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key", DefaultModel: "gpt-4", TimeoutSeconds: 30, ToolCalling: ToolCallingConfig{MaxIterations: 20}}, false, ""},
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
		if cfg.MaxTokens != 256000 {
			t.Errorf("Default MaxTokens = %d, want 256000", cfg.MaxTokens)
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
		if !cfg.Enabled {
			t.Error("Default stream edit should be enabled")
		}
		if cfg.CharThreshold != 300 {
			t.Errorf("Default CharThreshold = %d, want 300", cfg.CharThreshold)
		}
		if cfg.TimeThresholdMs != 3000 {
			t.Errorf("Default TimeThresholdMs = %d, want 3000", cfg.TimeThresholdMs)
		}
		if cfg.EditIntervalMs != 500 {
			t.Errorf("Default EditIntervalMs = %d, want 500", cfg.EditIntervalMs)
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
	if !contains(content, "providers:") {
		t.Error("示例配置应包含 providers 多提供商配置")
	}
	if !contains(content, "provider:") {
		t.Error("示例配置应包含 provider 说明")
	}
	if !contains(content, "proactive:") {
		t.Error("示例配置应包含 proactive 说明")
	}
}

func TestProactiveConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  ProactiveConfig
		wantErr bool
		errMsg  string
	}{
		{
			"禁用时不验证",
			ProactiveConfig{Enabled: false},
			false,
			"",
		},
		{
			"有效配置",
			ProactiveConfig{
				Enabled:            true,
				MaxMessagesPerDay:  5,
				MinIntervalMinutes: 60,
				Silence:            SilenceConfig{Enabled: false},
				Schedule:           ScheduleConfig{Enabled: false},
				NewMember:          NewMemberConfig{Enabled: false},
				Decision:           DecisionConfig{Temperature: 0.8},
			},
			false,
			"",
		},
		{
			"负的 max_messages_per_day",
			ProactiveConfig{
				Enabled:           true,
				MaxMessagesPerDay: -1,
				Silence:           SilenceConfig{Enabled: false},
				Schedule:          ScheduleConfig{Enabled: false},
			},
			true,
			"max_messages_per_day must be non-negative",
		},
		{
			"负的 min_interval_minutes",
			ProactiveConfig{
				Enabled:            true,
				MinIntervalMinutes: -1,
				Silence:            SilenceConfig{Enabled: false},
				Schedule:           ScheduleConfig{Enabled: false},
			},
			true,
			"min_interval_minutes must be non-negative",
		},
		{
			"Silence 配置无效",
			ProactiveConfig{
				Enabled:  true,
				Silence:  SilenceConfig{Enabled: true, ThresholdMinutes: -1, CheckIntervalMinutes: 15},
				Schedule: ScheduleConfig{Enabled: false},
			},
			true,
			"silence config: threshold_minutes must be positive",
		},
		{
			"Schedule 配置无效",
			ProactiveConfig{
				Enabled:  true,
				Silence:  SilenceConfig{Enabled: false},
				Schedule: ScheduleConfig{Enabled: true, Times: []string{}},
			},
			true,
			"schedule config: times must not be empty when schedule is enabled",
		},
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

func TestSilenceConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  SilenceConfig
		wantErr bool
		errMsg  string
	}{
		{"禁用时不验证", SilenceConfig{Enabled: false}, false, ""},
		{"有效配置", SilenceConfig{Enabled: true, ThresholdMinutes: 60, CheckIntervalMinutes: 15}, false, ""},
		{"threshold_minutes 为零", SilenceConfig{Enabled: true, ThresholdMinutes: 0, CheckIntervalMinutes: 15}, true, "threshold_minutes must be positive"},
		{"threshold_minutes 负数", SilenceConfig{Enabled: true, ThresholdMinutes: -10, CheckIntervalMinutes: 15}, true, "threshold_minutes must be positive"},
		{"check_interval_minutes 为零", SilenceConfig{Enabled: true, ThresholdMinutes: 60, CheckIntervalMinutes: 0}, true, "check_interval_minutes must be positive"},
		{"check_interval_minutes 负数", SilenceConfig{Enabled: true, ThresholdMinutes: 60, CheckIntervalMinutes: -5}, true, "check_interval_minutes must be positive"},
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

func TestScheduleConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  ScheduleConfig
		wantErr bool
		errMsg  string
	}{
		{"禁用时不验证", ScheduleConfig{Enabled: false}, false, ""},
		{"有效配置", ScheduleConfig{Enabled: true, Times: []string{"09:00", "18:00"}}, false, ""},
		{"times 为空", ScheduleConfig{Enabled: true, Times: []string{}}, true, "times must not be empty when schedule is enabled"},
		{"times 为 nil", ScheduleConfig{Enabled: true, Times: nil}, true, "times must not be empty when schedule is enabled"},
		{"时间格式无效", ScheduleConfig{Enabled: true, Times: []string{"9:00"}}, true, "times[0] invalid format"},
		{"时间格式错误", ScheduleConfig{Enabled: true, Times: []string{"25:00"}}, true, "times[0] invalid format"},
		{"时间格式缺少分钟", ScheduleConfig{Enabled: true, Times: []string{"09"}}, true, "times[0] invalid format"},
		{"第二个时间格式错误", ScheduleConfig{Enabled: true, Times: []string{"09:00", "invalid"}}, true, "times[1] invalid format"},
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

func TestNewMemberConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  NewMemberConfig
		wantErr bool
		errMsg  string
	}{
		{"禁用时不验证", NewMemberConfig{Enabled: false}, false, ""},
		{"有效配置", NewMemberConfig{Enabled: true, WelcomePrompt: "欢迎新成员"}, false, ""},
		{"缺少 welcome_prompt", NewMemberConfig{Enabled: true, WelcomePrompt: ""}, true, "welcome_prompt is required when new_member is enabled"},
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

func TestDecisionConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  DecisionConfig
		wantErr bool
		errMsg  string
	}{
		{"有效配置", DecisionConfig{Model: "gpt-4", Temperature: 0.8}, false, ""},
		{"空配置", DecisionConfig{}, false, ""},
		{"温度过低", DecisionConfig{Temperature: -0.1}, true, "temperature must be between 0 and 2"},
		{"温度过高", DecisionConfig{Temperature: 2.1}, true, "temperature must be between 0 and 2"},
		{"温度边界 0", DecisionConfig{Temperature: 0}, false, ""},
		{"温度边界 2", DecisionConfig{Temperature: 2}, false, ""},
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

func TestDefaultProactiveConfigs(t *testing.T) {
	t.Run("DefaultProactiveConfig", func(t *testing.T) {
		cfg := DefaultProactiveConfig()
		if cfg.Enabled {
			t.Error("Default proactive should be disabled")
		}
		if cfg.MaxMessagesPerDay != 5 {
			t.Errorf("Default MaxMessagesPerDay = %d, want 5", cfg.MaxMessagesPerDay)
		}
		if cfg.MinIntervalMinutes != 60 {
			t.Errorf("Default MinIntervalMinutes = %d, want 60", cfg.MinIntervalMinutes)
		}
	})

	t.Run("DefaultSilenceConfig", func(t *testing.T) {
		cfg := DefaultSilenceConfig()
		if !cfg.Enabled {
			t.Error("Default silence should be enabled")
		}
		if cfg.ThresholdMinutes != 60 {
			t.Errorf("Default ThresholdMinutes = %d, want 60", cfg.ThresholdMinutes)
		}
		if cfg.CheckIntervalMinutes != 15 {
			t.Errorf("Default CheckIntervalMinutes = %d, want 15", cfg.CheckIntervalMinutes)
		}
	})

	t.Run("DefaultScheduleConfig", func(t *testing.T) {
		cfg := DefaultScheduleConfig()
		if !cfg.Enabled {
			t.Error("Default schedule should be enabled")
		}
		if len(cfg.Times) != 3 {
			t.Errorf("Default Times length = %d, want 3", len(cfg.Times))
		}
		expectedTimes := []string{"09:00", "12:00", "18:00"}
		for i, expected := range expectedTimes {
			if i < len(cfg.Times) && cfg.Times[i] != expected {
				t.Errorf("Default Times[%d] = %s, want %s", i, cfg.Times[i], expected)
			}
		}
	})

	t.Run("DefaultNewMemberConfig", func(t *testing.T) {
		cfg := DefaultNewMemberConfig()
		if !cfg.Enabled {
			t.Error("Default new_member should be enabled")
		}
		if cfg.WelcomePrompt == "" {
			t.Error("Default WelcomePrompt should not be empty")
		}
	})

	t.Run("DefaultDecisionConfig", func(t *testing.T) {
		cfg := DefaultDecisionConfig()
		if cfg.Temperature != 0.8 {
			t.Errorf("Default Temperature = %f, want 0.8", cfg.Temperature)
		}
	})
}

func TestMediaConfig(t *testing.T) {
	t.Run("DefaultMediaConfig", func(t *testing.T) {
		cfg := DefaultMediaConfig()
		if !cfg.Enabled {
			t.Error("Default media should be enabled")
		}
		if cfg.MaxSizeMB != 10 {
			t.Errorf("Default MaxSizeMB = %d, want 10", cfg.MaxSizeMB)
		}
		if cfg.TimeoutSec != 30 {
			t.Errorf("Default TimeoutSec = %d, want 30", cfg.TimeoutSec)
		}
	})

	t.Run("AIConfig 包含 Media 字段", func(t *testing.T) {
		cfg := DefaultAIConfig()
		if cfg.Media.Enabled != true {
			t.Error("Default AI Media.Enabled should be true")
		}
		if cfg.Media.MaxSizeMB != 10 {
			t.Errorf("Default AI Media.MaxSizeMB = %d, want 10", cfg.Media.MaxSizeMB)
		}
		if cfg.Media.TimeoutSec != 30 {
			t.Errorf("Default AI Media.TimeoutSec = %d, want 30", cfg.Media.TimeoutSec)
		}
	})

	t.Run("Media 配置验证 - 禁用时不验证", func(t *testing.T) {
		cfg := AIConfig{
			Enabled:        true,
			Provider:       "openai",
			BaseURL:        "https://api.openai.com/v1",
			APIKey:         "key",
			DefaultModel:   "gpt-4",
			TimeoutSeconds: 30,
			ToolCalling:    ToolCallingConfig{MaxIterations: 5},
			Media:          MediaConfig{Enabled: false, MaxSizeMB: -1, TimeoutSec: -1},
		}
		err := cfg.Validate()
		if err != nil {
			t.Errorf("Validate() should not error when media is disabled, got %v", err)
		}
	})

	t.Run("Media 配置验证 - 有效配置", func(t *testing.T) {
		cfg := AIConfig{
			Enabled:        true,
			Provider:       "openai",
			BaseURL:        "https://api.openai.com/v1",
			APIKey:         "key",
			DefaultModel:   "gpt-4",
			TimeoutSeconds: 30,
			ToolCalling:    ToolCallingConfig{MaxIterations: 5},
			Media:          MediaConfig{Enabled: true, MaxSizeMB: 10, TimeoutSec: 30},
		}
		err := cfg.Validate()
		if err != nil {
			t.Errorf("Validate() error = %v", err)
		}
	})

	t.Run("Media 配置验证 - MaxSizeMB 为负数", func(t *testing.T) {
		cfg := AIConfig{
			Enabled:        true,
			Provider:       "openai",
			BaseURL:        "https://api.openai.com/v1",
			APIKey:         "key",
			DefaultModel:   "gpt-4",
			TimeoutSeconds: 30,
			ToolCalling:    ToolCallingConfig{MaxIterations: 5},
			Media:          MediaConfig{Enabled: true, MaxSizeMB: -1, TimeoutSec: 30},
		}
		err := cfg.Validate()
		if err == nil {
			t.Error("Validate() should error when MaxSizeMB is negative")
		}
		if err != nil && !contains(err.Error(), "media.max_size_mb must be positive") {
			t.Errorf("Validate() error = %v, want error containing 'media.max_size_mb must be positive'", err)
		}
	})

	t.Run("Media 配置验证 - TimeoutSec 为负数", func(t *testing.T) {
		cfg := AIConfig{
			Enabled:        true,
			Provider:       "openai",
			BaseURL:        "https://api.openai.com/v1",
			APIKey:         "key",
			DefaultModel:   "gpt-4",
			TimeoutSeconds: 30,
			ToolCalling:    ToolCallingConfig{MaxIterations: 5},
			Media:          MediaConfig{Enabled: true, MaxSizeMB: 10, TimeoutSec: -1},
		}
		err := cfg.Validate()
		if err == nil {
			t.Error("Validate() should error when TimeoutSec is negative")
		}
		if err != nil && !contains(err.Error(), "media.timeout_sec must be positive") {
			t.Errorf("Validate() error = %v, want error containing 'media.timeout_sec must be positive'", err)
		}
	})
}

func TestShutdownConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  ShutdownConfig
		wantErr bool
		errMsg  string
	}{
		{"有效配置 - 默认值", ShutdownConfig{TimeoutSeconds: 30}, false, ""},
		{"有效配置 - 最小值", ShutdownConfig{TimeoutSeconds: 5}, false, ""},
		{"有效配置 - 最大值", ShutdownConfig{TimeoutSeconds: 300}, false, ""},
		{"超时时间过短", ShutdownConfig{TimeoutSeconds: 4}, true, "timeout_seconds must be at least 5 seconds"},
		{"超时时间为 0", ShutdownConfig{TimeoutSeconds: 0}, true, "timeout_seconds must be at least 5 seconds"},
		{"超时时间为负数", ShutdownConfig{TimeoutSeconds: -1}, true, "timeout_seconds must be at least 5 seconds"},
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

func TestToolCallingConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  ToolCallingConfig
		wantErr bool
		errMsg  string
	}{
		{"有效配置 - 默认值", ToolCallingConfig{MaxIterations: 5}, false, ""},
		{"有效配置 - 最小值", ToolCallingConfig{MaxIterations: 1}, false, ""},
		{"有效配置 - 最大值", ToolCallingConfig{MaxIterations: 20}, false, ""},
		{"迭代次数为 0", ToolCallingConfig{MaxIterations: 0}, true, "max_iterations must be at least 1"},
		{"迭代次数为负数", ToolCallingConfig{MaxIterations: -1}, true, "max_iterations must be at least 1"},
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

func TestModelConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  ModelConfig
		wantErr bool
		errMsg  string
	}{
		{"有效配置", ModelConfig{Model: "gpt-4o", Temperature: 0.7, MaxTokens: 4096}, false, ""},
		{"缺少模型名称", ModelConfig{Temperature: 0.7}, true, "model is required"},
		{"温度过低", ModelConfig{Model: "gpt-4o", Temperature: -0.1}, true, "temperature must be between 0 and 2"},
		{"温度过高", ModelConfig{Model: "gpt-4o", Temperature: 2.1}, true, "temperature must be between 0 and 2"},
		{"温度边界 0", ModelConfig{Model: "gpt-4o", Temperature: 0}, false, ""},
		{"温度边界 2", ModelConfig{Model: "gpt-4o", Temperature: 2}, false, ""},
		{"MaxTokens 为负数", ModelConfig{Model: "gpt-4o", MaxTokens: -1}, true, "max_tokens must be non-negative"},
		{"MaxTokens 为 0 (有效)", ModelConfig{Model: "gpt-4o", MaxTokens: 0}, false, ""},
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
