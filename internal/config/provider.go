// Package config 提供 YAML 配置文件的加载、验证和默认值管理。
package config

import (
	"fmt"
	"strings"
)

// ProviderConfig 存储单个 AI 提供商的配置。
// 每个提供商可以有自己的 API 端点、认证信息和模型配置。
type ProviderConfig struct {
	Type    string                 `yaml:"type"`    // 提供商类型（如 openai, azure），默认使用配置键名
	BaseURL string                 `yaml:"base_url"` // API 基础 URL
	APIKey  string                 `yaml:"api_key"` // API 密钥
	Models  map[string]ModelConfig `yaml:"models"`  // 该提供商下的模型配置
	Extra   map[string]any         `yaml:",inline"` // 提供商特有配置（如 Azure deployment）
}

// Validate 验证提供商配置是否有效。
// providerName 用于错误消息中标识配置位置。
func (p *ProviderConfig) Validate(providerName string) error {
	// Type 可选，默认使用 providerName
	if p.Type == "" {
		p.Type = providerName
	}

	if p.BaseURL == "" {
		return fmt.Errorf("base_url is required")
	}

	// APIKey 可以为空（某些本地服务如 Ollama 不需要）

	// 验证所有模型配置
	for modelName, modelCfg := range p.Models {
		if err := modelCfg.Validate(); err != nil {
			return fmt.Errorf("models[%s]: %w", modelName, err)
		}
	}

	return nil
}

// GetModelConfig 获取指定模型的配置。
// 如果模型未显式配置，返回基于提供商默认值的配置。
// 返回值 bool 表示是否找到了显式配置。
func (p *ProviderConfig) GetModelConfig(modelName string) (ModelConfig, bool) {
	if cfg, ok := p.Models[modelName]; ok {
		// 补充未设置的字段
		if cfg.Model == "" {
			cfg.Model = modelName
		}
		return cfg, true
	}

	// 返回默认配置
	return ModelConfig{
		Model: modelName,
	}, false
}

// ParseModelID 解析完全限定模型标识符。
// 格式: provider.model（如 openai.gpt-4o-mini）
//
// 返回值:
//   - provider: 提供商名称
//   - model: 模型名称
//   - error: 格式错误时返回错误
func ParseModelID(id string) (provider, model string, err error) {
	parts := strings.SplitN(id, ".", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid model id format: %q (expected provider.model)", id)
	}
	if parts[0] == "" {
		return "", "", fmt.Errorf("provider name is empty in model id: %q", id)
	}
	if parts[1] == "" {
		return "", "", fmt.Errorf("model name is empty in model id: %q", id)
	}
	return parts[0], parts[1], nil
}

// FormatModelID 格式化模型标识符为完全限定名称。
func FormatModelID(provider, model string) string {
	return provider + "." + model
}