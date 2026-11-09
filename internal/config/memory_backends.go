package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// MemoryBackendsConfig 配置原生外部记忆适配器；未选为 active 时不会发送对话。
type MemoryBackendsConfig struct {
	Backends map[string]MemoryBackendConfig `yaml:"backends"`
}

// MemoryBackendConfig 只由管理员配置服务地址和凭据来源，模型不能覆盖这些字段。
type MemoryBackendConfig struct {
	Type           string         `yaml:"type"`
	Endpoint       string         `yaml:"endpoint"`
	APIKey         string         `yaml:"api_key"`
	APIKeyEnv      string         `yaml:"api_key_env"`
	Options        map[string]any `yaml:"options"`
	TimeoutSeconds int            `yaml:"timeout_seconds"`
	AllowPrivate   bool           `yaml:"allow_private"`
}

// DefaultMemoryBackendsConfig 默认不配置或启用任何第三方记忆服务。
func DefaultMemoryBackendsConfig() MemoryBackendsConfig { return MemoryBackendsConfig{} }

// Validate 拒绝未知适配器、携带凭据的 URL 和不受限超时。
func (c MemoryBackendsConfig) Validate() error {
	for name, backend := range c.Backends {
		if name == "" || strings.ContainsAny(name, ":/\\\x00") {
			return errors.New("invalid memory backend name")
		}
		switch backend.Type {
		case "hindsight", "supermemory", "holographic", "retaindb", "byterover", "memori", "honcho", "openviking", "mem0":
		default:
			return fmt.Errorf("memory backend %s has unsupported type %q", name, backend.Type)
		}
		if backend.TimeoutSeconds < 0 || backend.TimeoutSeconds > 300 {
			return fmt.Errorf("memory backend %s timeout must be 0..300", name)
		}
		if backend.Endpoint != "" {
			u, err := url.Parse(backend.Endpoint)
			if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(backend.AllowPrivate && u.Scheme == "http")) {
				return fmt.Errorf("memory backend %s requires a credential-free HTTPS endpoint", name)
			}
		}
	}
	return nil
}
