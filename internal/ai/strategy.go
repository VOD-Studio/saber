package ai

import (
	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/config"
)

// ClientStrategy 定义 AI 客户端创建策略。
//
// 该接口抽象了不同 AI 提供商的客户端创建逻辑，
// 使得添加新提供商时只需实现新的策略，而无需修改现有代码。
// 这符合开闭原则（对扩展开放，对修改关闭）。
type ClientStrategy interface {
	// CreateClientConfig 创建并返回 OpenAI 客户端配置。
	//
	// 参数:
	//   - cfg: 模型配置，包含 API 密钥、基础 URL 等信息
	//
	// 返回值:
	//   - openai.ClientConfig: 配置好的客户端配置
	CreateClientConfig(cfg *config.ModelConfig) openai.ClientConfig

	// Name 返回策略名称。
	//
	// 返回值:
	//   - string: 策略标识符（如 "openai", "azure"）
	Name() string
}

// OpenAIStrategy 是 OpenAI 及其兼容 API 的客户端创建策略。
//
// 该策略适用于标准 OpenAI API 以及所有兼容 OpenAI API 格式的服务，
// 如 Ollama、vLLM、LocalAI 等。它支持自定义 BaseURL 以连接不同的 API 端点。
type OpenAIStrategy struct{}

// CreateClientConfig 创建 OpenAI 客户端配置。
//
// 它使用 API 密钥创建默认配置，并可选择设置自定义 BaseURL。
// 如果 BaseURL 为空，则使用 OpenAI 官方 API 地址。
func (s *OpenAIStrategy) CreateClientConfig(cfg *config.ModelConfig) openai.ClientConfig {
	clientConfig := openai.DefaultConfig(cfg.APIKey)
	if cfg.BaseURL != "" {
		clientConfig.BaseURL = cfg.BaseURL
	}
	return clientConfig
}

// Name 返回策略名称。
func (s *OpenAIStrategy) Name() string {
	return "openai"
}

// AzureStrategy 是 Azure OpenAI 服务的客户端创建策略。
//
// Azure OpenAI 使用不同的认证方式和 API 结构，
// 需要使用 Azure 特定的配置方法。
type AzureStrategy struct{}

// CreateClientConfig 创建 Azure OpenAI 客户端配置。
//
// 它使用 Azure 特定的配置方法，需要提供 API 密钥和 BaseURL
// （即 Azure 资源端点）。
func (s *AzureStrategy) CreateClientConfig(cfg *config.ModelConfig) openai.ClientConfig {
	return openai.DefaultAzureConfig(cfg.APIKey, cfg.BaseURL)
}

// Name 返回策略名称。
func (s *AzureStrategy) Name() string {
	return "azure"
}

// ClientFactory 是 AI 客户端的工厂。
//
// 它管理所有已注册的策略，并根据提供商名称选择合适的策略创建客户端配置。
// 使用工厂模式可以轻松扩展支持新的 AI 提供商。
type ClientFactory struct {
	strategies map[string]ClientStrategy
}

// NewClientFactory 创建一个新的客户端工厂。
//
// 工厂会自动注册内置的策略（OpenAI 和 Azure）。
// 如需添加新策略，可使用 RegisterStrategy 方法。
//
// 返回值:
//   - *ClientFactory: 创建的工厂实例
func NewClientFactory() *ClientFactory {
	factory := &ClientFactory{
		strategies: make(map[string]ClientStrategy),
	}

	// 注册内置策略
	factory.RegisterStrategy(&OpenAIStrategy{})
	factory.RegisterStrategy(&AzureStrategy{})

	return factory
}

// RegisterStrategy 注册一个新的客户端创建策略。
//
// 如果同名的策略已存在，新策略将覆盖旧策略。
// 这允许用户自定义或替换内置策略。
//
// 参数:
//   - strategy: 要注册的策略实例
func (f *ClientFactory) RegisterStrategy(strategy ClientStrategy) {
	f.strategies[strategy.Name()] = strategy
}

// CreateClientConfig 根据配置创建客户端配置。
//
// 它首先尝试匹配精确的提供商名称，然后尝试已知的别名。
// 如果找不到匹配的策略，默认使用 OpenAI 策略（因为大多数 API 都兼容）。
//
// 参数:
//   - cfg: 模型配置
//
// 返回值:
//   - openai.ClientConfig: 创建的客户端配置
func (f *ClientFactory) CreateClientConfig(cfg *config.ModelConfig) openai.ClientConfig {
	// 尝试精确匹配
	if strategy, ok := f.strategies[cfg.Provider]; ok {
		return strategy.CreateClientConfig(cfg)
	}

	// 处理已知的别名
	provider := normalizeProvider(cfg.Provider)
	if strategy, ok := f.strategies[provider]; ok {
		return strategy.CreateClientConfig(cfg)
	}

	// 默认使用 OpenAI 策略（大多数 API 都兼容）
	if strategy, ok := f.strategies["openai"]; ok {
		return strategy.CreateClientConfig(cfg)
	}

	// 兜底：直接创建默认配置
	return openai.DefaultConfig(cfg.APIKey)
}

// normalizeProvider 将提供商名称标准化。
//
// 不同的配置可能使用不同的名称来表示同一个提供商，
// 此函数将它们统一为标准名称。
//
// 参数:
//   - provider: 原始提供商名称
//
// 返回值:
//   - string: 标准化后的提供商名称
func normalizeProvider(provider string) string {
	switch provider {
	case "azure-openai":
		return "azure"
	case "openai-compatible", "ollama", "vllm", "localai":
		return "openai"
	default:
		return provider
	}
}

// GetStrategy 获取指定名称的策略。
//
// 参数:
//   - name: 策略名称
//
// 返回值:
//   - ClientStrategy: 策略实例
//   - bool: 是否找到该策略
func (f *ClientFactory) GetStrategy(name string) (ClientStrategy, bool) {
	strategy, ok := f.strategies[name]
	return strategy, ok
}

// ListStrategies 列出所有已注册的策略名称。
//
// 返回值:
//   - []string: 策略名称列表
func (f *ClientFactory) ListStrategies() []string {
	names := make([]string, 0, len(f.strategies))
	for name := range f.strategies {
		names = append(names, name)
	}
	return names
}

// defaultFactory 是默认的全局工厂实例。
//
// 使用全局实例可以避免重复创建工厂，提高效率。
var defaultFactory = NewClientFactory()

// GetDefaultFactory 获取默认的客户端工厂。
//
// 返回值:
//   - *ClientFactory: 默认工厂实例
func GetDefaultFactory() *ClientFactory {
	return defaultFactory
}
