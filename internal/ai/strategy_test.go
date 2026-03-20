package ai

import (
	"testing"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/config"
)

// TestClientStrategy_Interface 测试策略接口实现。
func TestClientStrategy_Interface(t *testing.T) {
	// 确保所有策略都实现了接口
	var _ ClientStrategy = (*OpenAIStrategy)(nil)
	var _ ClientStrategy = (*AzureStrategy)(nil)
}

// TestOpenAIStrategy_CreateClientConfig 测试 OpenAI 策略创建客户端配置。
func TestOpenAIStrategy_CreateClientConfig(t *testing.T) {
	strategy := &OpenAIStrategy{}

	tests := []struct {
		name     string
		config   *config.ModelConfig
		wantURL  string
		wantName string
	}{
		{
			name: "使用自定义 BaseURL",
			config: &config.ModelConfig{
				APIKey:  "test-key",
				BaseURL: "https://custom.api.com/v1",
			},
			wantURL:  "https://custom.api.com/v1",
			wantName: "openai",
		},
		{
			name: "使用默认 BaseURL",
			config: &config.ModelConfig{
				APIKey: "test-key",
			},
			wantURL:  "https://api.openai.com/v1",
			wantName: "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := strategy.CreateClientConfig(tt.config)

			if cfg.BaseURL != tt.wantURL {
				t.Errorf("expected BaseURL %q, got %q", tt.wantURL, cfg.BaseURL)
			}

			if strategy.Name() != tt.wantName {
				t.Errorf("expected Name %q, got %q", tt.wantName, strategy.Name())
			}
		})
	}
}

// TestAzureStrategy_CreateClientConfig 测试 Azure 策略创建客户端配置。
func TestAzureStrategy_CreateClientConfig(t *testing.T) {
	strategy := &AzureStrategy{}

	cfg := &config.ModelConfig{
		APIKey:  "test-key",
		BaseURL: "https://test.openai.azure.com",
	}

	clientConfig := strategy.CreateClientConfig(cfg)

	if clientConfig.BaseURL == "" {
		t.Error("expected non-empty BaseURL for Azure config")
	}

	if strategy.Name() != "azure" {
		t.Errorf("expected Name %q, got %q", "azure", strategy.Name())
	}
}

// TestClientFactory_RegisterStrategy 测试策略注册。
func TestClientFactory_RegisterStrategy(t *testing.T) {
	factory := NewClientFactory()

	// 测试内置策略已注册
	if _, ok := factory.GetStrategy("openai"); !ok {
		t.Error("expected openai strategy to be registered")
	}

	if _, ok := factory.GetStrategy("azure"); !ok {
		t.Error("expected azure strategy to be registered")
	}

	// 测试注册自定义策略
	customStrategy := &mockStrategy{name: "custom"}
	factory.RegisterStrategy(customStrategy)

	if _, ok := factory.GetStrategy("custom"); !ok {
		t.Error("expected custom strategy to be registered after registration")
	}
}

// TestClientFactory_CreateClientConfig 测试工厂创建客户端配置。
func TestClientFactory_CreateClientConfig(t *testing.T) {
	factory := NewClientFactory()

	tests := []struct {
		name     string
		config   *config.ModelConfig
		wantType string // "openai" or "azure"
	}{
		{
			name: "OpenAI 提供商",
			config: &config.ModelConfig{
				Provider: "openai",
				APIKey:   "test-key",
				BaseURL:  "https://api.openai.com/v1",
			},
			wantType: "openai",
		},
		{
			name: "Azure 提供商",
			config: &config.ModelConfig{
				Provider: "azure",
				APIKey:   "test-key",
				BaseURL:  "https://test.openai.azure.com",
			},
			wantType: "azure",
		},
		{
			name: "Azure-OpenAI 别名",
			config: &config.ModelConfig{
				Provider: "azure-openai",
				APIKey:   "test-key",
				BaseURL:  "https://test.openai.azure.com",
			},
			wantType: "azure",
		},
		{
			name: "未知提供商默认使用 OpenAI",
			config: &config.ModelConfig{
				Provider: "unknown",
				APIKey:   "test-key",
				BaseURL:  "https://custom.api.com/v1",
			},
			wantType: "openai",
		},
		{
			name: "Ollama 使用 OpenAI 策略",
			config: &config.ModelConfig{
				Provider: "ollama",
				APIKey:   "dummy",
				BaseURL:  "http://localhost:11434/v1",
			},
			wantType: "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := factory.CreateClientConfig(tt.config)

			if cfg.BaseURL == "" {
				t.Error("expected non-empty BaseURL")
			}
		})
	}
}

// TestClientFactory_ListStrategies 测试列出所有策略。
func TestClientFactory_ListStrategies(t *testing.T) {
	factory := NewClientFactory()

	strategies := factory.ListStrategies()

	if len(strategies) < 2 {
		t.Errorf("expected at least 2 strategies, got %d", len(strategies))
	}

	// 检查内置策略是否存在
	found := make(map[string]bool)
	for _, s := range strategies {
		found[s] = true
	}

	if !found["openai"] {
		t.Error("expected openai strategy in list")
	}
	if !found["azure"] {
		t.Error("expected azure strategy in list")
	}
}

// TestNormalizeProvider 测试提供商名称标准化。
func TestNormalizeProvider(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"azure-openai", "azure"},
		{"openai-compatible", "openai"},
		{"ollama", "openai"},
		{"vllm", "openai"},
		{"localai", "openai"},
		{"openai", "openai"},
		{"azure", "azure"},
		{"anthropic", "anthropic"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeProvider(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeProvider(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestGetDefaultFactory 测试获取默认工厂。
func TestGetDefaultFactory(t *testing.T) {
	factory1 := GetDefaultFactory()
	factory2 := GetDefaultFactory()

	if factory1 != factory2 {
		t.Error("expected same factory instance from GetDefaultFactory")
	}
}

// mockStrategy 是用于测试的模拟策略。
type mockStrategy struct {
	name string
}

func (m *mockStrategy) CreateClientConfig(cfg *config.ModelConfig) openai.ClientConfig {
	return openai.DefaultConfig(cfg.APIKey)
}

func (m *mockStrategy) Name() string {
	return m.name
}
