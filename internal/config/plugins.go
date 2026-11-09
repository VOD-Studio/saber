package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// PluginsConfig 配置经过管理员明确启用的进程插件和连接器；插件代码不继承宿主凭据环境。
type PluginsConfig struct {
	Enabled             bool                             `yaml:"enabled"`
	Directory           string                           `yaml:"directory"`
	EnabledPlugins      []string                         `yaml:"enabled_plugins"`
	DisabledPlugins     []string                         `yaml:"disabled_plugins"`
	Sources             map[string]string                `yaml:"sources"`
	Launcher            []string                         `yaml:"launcher"`
	Environment         map[string]string                `yaml:"environment"`
	LoadTimeoutSeconds  int                              `yaml:"load_timeout_seconds"`
	CallTimeoutSeconds  int                              `yaml:"call_timeout_seconds"`
	CloneTimeoutSeconds int                              `yaml:"clone_timeout_seconds"`
	MaxOutputBytes      int                              `yaml:"max_output_bytes"`
	Connectors          map[string]PluginConnectorConfig `yaml:"connectors"`
}

// PluginConnectorConfig 定义固定的远程服务地址；凭据只引用宿主环境或加密 vault。
type PluginConnectorConfig struct {
	Enabled       bool                       `yaml:"enabled"`
	Type          string                     `yaml:"type"`
	URL           string                     `yaml:"url"`
	TokenEnv      string                     `yaml:"token_env"`
	OAuth         PluginConnectorOAuthConfig `yaml:"oauth"`
	AllowedTools  []string                   `yaml:"allowed_tools"`
	Accounts      map[string]string          `yaml:"accounts"`
	NoTraining    bool                       `yaml:"no_training"`
	ZeroRetention bool                       `yaml:"zero_retention"`
}

// PluginConnectorOAuthConfig 配置标准授权码加 PKCE 流程，未填写端点时使用服务端元数据发现。
type PluginConnectorOAuthConfig struct {
	ClientID         string   `yaml:"client_id"`
	ClientSecretEnv  string   `yaml:"client_secret_env"`
	AuthorizationURL string   `yaml:"authorization_url"`
	TokenURL         string   `yaml:"token_url"`
	RegistrationURL  string   `yaml:"registration_url"`
	RevocationURL    string   `yaml:"revocation_url"`
	Scopes           []string `yaml:"scopes"`
	RedirectPort     int      `yaml:"redirect_port"`
}

// DefaultPluginsConfig 返回默认关闭第三方代码执行的插件配置。
func DefaultPluginsConfig() PluginsConfig {
	return PluginsConfig{LoadTimeoutSeconds: 10, CallTimeoutSeconds: 60, CloneTimeoutSeconds: 300, MaxOutputBytes: 8 << 20}
}

// Validate 拒绝非法地址、无界输出及包含凭据的插件来源。
func (c PluginsConfig) Validate() error {
	if c.LoadTimeoutSeconds < 1 || c.LoadTimeoutSeconds > 600 || c.CallTimeoutSeconds < 1 || c.CallTimeoutSeconds > 3600 || c.CloneTimeoutSeconds < 1 || c.CloneTimeoutSeconds > 3600 {
		return errors.New("plugins timeouts must be positive and bounded")
	}
	if c.MaxOutputBytes < 1024 || c.MaxOutputBytes > 64<<20 {
		return errors.New("plugins.max_output_bytes must be 1024..67108864")
	}
	for name, source := range c.Sources {
		if name == "" || source == "" {
			return errors.New("plugins sources require name and URL/path")
		}
		if strings.Contains(source, "\n") {
			return errors.New("invalid plugin source")
		}
		u, err := url.Parse(source)
		if err != nil || u.User != nil {
			return fmt.Errorf("plugin source %s must not contain credentials", name)
		}
	}
	for name, v := range c.Connectors {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("connector %s: %w", name, err)
		}
	}
	for name := range c.Connectors {
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return errors.New("connector names must be safe lowercase slugs")
			}
		}
		if name == "" {
			return errors.New("connector name required")
		}
	}
	return nil
}

// Validate 只允许 HTTPS 或显式回环 HTTP，避免认证被重定向到任意目的地。
func (c PluginConnectorConfig) Validate() error {
	if c.Type != "mcp" && c.Type != "managed" {
		return errors.New("type must be mcp or managed")
	}
	for _, value := range []string{c.URL, c.OAuth.AuthorizationURL, c.OAuth.TokenURL, c.OAuth.RevocationURL, c.OAuth.RegistrationURL} {
		if value == "" {
			continue
		}
		u, err := url.Parse(value)
		if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
			return errors.New("connector endpoints require HTTPS or loopback HTTP without embedded credentials")
		}
		for key := range u.Query() {
			if strings.Contains(strings.ToLower(key), "token") || strings.Contains(strings.ToLower(key), "password") || strings.Contains(strings.ToLower(key), "api_key") {
				return errors.New("connector credentials must not appear in endpoint URL")
			}
		}
	}
	if c.URL == "" {
		return errors.New("connector URL is required")
	}
	if c.OAuth.RedirectPort < 0 || c.OAuth.RedirectPort > 65535 {
		return errors.New("OAuth redirect port out of range")
	}
	return nil
}
