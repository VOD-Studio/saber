package config

import (
	"testing"
)

func TestProviderConfig_Validate(t *testing.T) {
	tests := []struct {
		name         string
		config       ProviderConfig
		providerName string
		wantErr      bool
		errContains  string
	}{
		{
			name: "valid config",
			config: ProviderConfig{
				BaseURL: "https://api.openai.com/v1",
				APIKey:  "sk-test",
				Models: map[string]ModelConfig{
					"gpt-4": {Model: "gpt-4", Temperature: 0.7},
				},
			},
			providerName: "openai",
			wantErr:      false,
		},
		{
			name: "type defaults to provider name",
			config: ProviderConfig{
				BaseURL: "https://api.openai.com/v1",
			},
			providerName: "openai",
			wantErr:      false,
		},
		{
			name: "missing base_url",
			config: ProviderConfig{
				APIKey: "sk-test",
			},
			providerName: "openai",
			wantErr:      true,
			errContains:  "base_url is required",
		},
		{
			name: "empty api_key allowed",
			config: ProviderConfig{
				BaseURL: "http://localhost:11434/v1",
				APIKey:  "",
			},
			providerName: "ollama",
			wantErr:      false,
		},
		{
			name: "invalid model config",
			config: ProviderConfig{
				BaseURL: "https://api.openai.com/v1",
				Models: map[string]ModelConfig{
					"bad": {Temperature: 3.0}, // invalid temperature
				},
			},
			providerName: "openai",
			wantErr:      true,
			errContains:  "models[bad]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate(tt.providerName)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Validate() expected error, got nil")
					return
				}
				if tt.errContains != "" && !containsString(err.Error(), tt.errContains) {
					t.Errorf("Validate() error = %v, want containing %q", err, tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
				// 验证 Type 被设置
				if tt.config.Type == "" {
					t.Errorf("Validate() did not set Type to provider name")
				}
			}
		})
	}
}

func TestProviderConfig_GetModelConfig(t *testing.T) {
	provider := ProviderConfig{
		Type:    "openai",
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-test",
		Models: map[string]ModelConfig{
			"gpt-4": {
				Model:       "gpt-4-turbo",
				Temperature: 0.7,
				MaxTokens:   4096,
			},
		},
	}

	t.Run("existing model", func(t *testing.T) {
		cfg, found := provider.GetModelConfig("gpt-4")
		if !found {
			t.Error("GetModelConfig() expected found=true for existing model")
		}
		if cfg.Model != "gpt-4-turbo" {
			t.Errorf("GetModelConfig() model = %q, want %q", cfg.Model, "gpt-4-turbo")
		}
		if cfg.Temperature != 0.7 {
			t.Errorf("GetModelConfig() temperature = %v, want %v", cfg.Temperature, 0.7)
		}
	})

	t.Run("non-existing model", func(t *testing.T) {
		cfg, found := provider.GetModelConfig("gpt-4o")
		if found {
			t.Error("GetModelConfig() expected found=false for non-existing model")
		}
		if cfg.Model != "gpt-4o" {
			t.Errorf("GetModelConfig() model = %q, want %q", cfg.Model, "gpt-4o")
		}
	})
}

func TestParseModelID(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantProvider string
		wantModel    string
		wantErr      bool
		errContains  string
	}{
		{
			name:         "valid format",
			input:        "openai.gpt-4o-mini",
			wantProvider: "openai",
			wantModel:    "gpt-4o-mini",
			wantErr:      false,
		},
		{
			name:         "valid with multiple dots",
			input:        "azure.gpt-4-32k",
			wantProvider: "azure",
			wantModel:    "gpt-4-32k",
			wantErr:      false,
		},
		{
			name:         "ollama model",
			input:        "ollama.llama3",
			wantProvider: "ollama",
			wantModel:    "llama3",
			wantErr:      false,
		},
		{
			name:        "missing dot",
			input:       "gpt-4o-mini",
			wantErr:     true,
			errContains: "invalid model id format",
		},
		{
			name:        "empty provider",
			input:       ".gpt-4o-mini",
			wantErr:     true,
			errContains: "provider name is empty",
		},
		{
			name:        "empty model",
			input:       "openai.",
			wantErr:     true,
			errContains: "model name is empty",
		},
		{
			name:        "empty string",
			input:       "",
			wantErr:     true,
			errContains: "invalid model id format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, model, err := ParseModelID(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseModelID() expected error, got nil")
					return
				}
				if tt.errContains != "" && !containsString(err.Error(), tt.errContains) {
					t.Errorf("ParseModelID() error = %v, want containing %q", err, tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("ParseModelID() unexpected error: %v", err)
					return
				}
				if provider != tt.wantProvider {
					t.Errorf("ParseModelID() provider = %q, want %q", provider, tt.wantProvider)
				}
				if model != tt.wantModel {
					t.Errorf("ParseModelID() model = %q, want %q", model, tt.wantModel)
				}
			}
		})
	}
}

func TestFormatModelID(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     string
	}{
		{"openai", "gpt-4o-mini", "openai.gpt-4o-mini"},
		{"azure", "gpt-4", "azure.gpt-4"},
		{"ollama", "llama3", "ollama.llama3"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := FormatModelID(tt.provider, tt.model)
			if got != tt.want {
				t.Errorf("FormatModelID() = %q, want %q", got, tt.want)
			}
		})
	}
}

// 辅助函数
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
