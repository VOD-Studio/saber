package config

import (
	"fmt"
	"net/url"
	"strings"
)

// ModelRuntimeConfig 控制可信模型路由、响应缓存与协作模型；所有目标必须在 AI 模型白名单中。
type ModelRuntimeConfig struct {
	StateDir string                      `yaml:"state_dir"`
	Routes   map[string]ModelRouteConfig `yaml:"routes"`
	Cache    ModelCacheConfig            `yaml:"cache"`
	MoA      MoAConfig                   `yaml:"moa"`
}

// ModelRouteConfig 将一个已配置模型映射到有序目标；失败不改写用户选择。
type ModelRouteConfig struct {
	Targets       []string `yaml:"targets"`
	Fallbacks     []string `yaml:"fallbacks"`
	Strategy      string   `yaml:"strategy"`
	StickySeconds int      `yaml:"sticky_seconds"`
}

// ModelCacheConfig 只缓存无工具且非流式的确定性辅助请求，按可信会话隔离。
type ModelCacheConfig struct {
	Enabled    bool     `yaml:"enabled"`
	TTLSeconds int      `yaml:"ttl_seconds"`
	MaxEntries int      `yaml:"max_entries"`
	Purposes   []string `yaml:"purposes"`
}

// MoASlot 指定实际计费的白名单模型及该槽的推理等级。
type MoASlot struct {
	Model           string `yaml:"model"`
	ReasoningEffort string `yaml:"reasoning_effort"`
}

// MoAPresetConfig 的聚合器是实际执行工具的模型，参考模型只提供建议。
type MoAPresetConfig struct {
	Enabled       bool      `yaml:"enabled"`
	References    []MoASlot `yaml:"references"`
	Aggregator    MoASlot   `yaml:"aggregator"`
	Fanout        string    `yaml:"fanout"`
	MinSuccessful int       `yaml:"min_successful"`
}

// MoAConfig 保存命名协作预设及建议的隐私过滤策略。
type MoAConfig struct {
	Enabled        bool                       `yaml:"enabled"`
	DefaultPreset  string                     `yaml:"default_preset"`
	Presets        map[string]MoAPresetConfig `yaml:"presets"`
	PrivacyFilter  string                     `yaml:"privacy_filter"`
	MaxParallel    int                        `yaml:"max_parallel"`
	TimeoutSeconds int                        `yaml:"timeout_seconds"`
}

// OAuthConfig 只由管理员配置授权端点，禁止使用模型或令牌响应提供的推理端点。
type OAuthConfig struct {
	Enabled          bool              `yaml:"enabled"`
	Flow             string            `yaml:"flow"`
	TokenEncoding    string            `yaml:"token_encoding"`
	TokenAuthMethod  string            `yaml:"token_auth_method"`
	RefreshHeader    string            `yaml:"refresh_header"`
	ClientID         string            `yaml:"client_id"`
	ClientSecret     string            `yaml:"client_secret"`
	AuthorizationURL string            `yaml:"authorization_url"`
	TokenURL         string            `yaml:"token_url"`
	DeviceURL        string            `yaml:"device_url"`
	RedirectURL      string            `yaml:"redirect_url"`
	Scopes           []string          `yaml:"scopes"`
	AuthParams       map[string]string `yaml:"auth_params"`
	TokenFile        string            `yaml:"token_file"`
}

// ProviderCredential 定义独立账号，令牌由私有文件保存，状态不暴露给模型。
type ProviderCredential struct {
	ID        string `yaml:"id"`
	APIKey    string `yaml:"api_key"`
	TokenFile string `yaml:"token_file"`
	Priority  int    `yaml:"priority"`
	Disabled  bool   `yaml:"disabled"`
}

// AWSProviderConfig 指定 AWS 的可信凭据来源与 Converse 参数。
type AWSProviderConfig struct {
	Region           string `yaml:"region"`
	AccessKeyID      string `yaml:"access_key_id"`
	SecretAccessKey  string `yaml:"secret_access_key"`
	SessionToken     string `yaml:"session_token"`
	Profile          string `yaml:"profile"`
	GuardrailID      string `yaml:"guardrail_id"`
	GuardrailVersion string `yaml:"guardrail_version"`
}

// GoogleProviderConfig 只读取显式服务账号文件或显式 OAuth 凭据，不借用其他租户的 ADC。
type GoogleProviderConfig struct {
	CredentialsFile string `yaml:"credentials_file"`
	Project         string `yaml:"project"`
	Region          string `yaml:"region"`
}

// DefaultModelRuntimeConfig 不自动启用收费调用、缓存或协作。
func DefaultModelRuntimeConfig() ModelRuntimeConfig {
	return ModelRuntimeConfig{Cache: ModelCacheConfig{TTLSeconds: 300, MaxEntries: 128, Purposes: []string{"summary", "evaluation"}}, MoA: MoAConfig{MaxParallel: 8, TimeoutSeconds: 120}}
}

// Validate 拒绝循环之外的无效参数；目标白名单与循环检查由 Core 完成。
func (c ModelRuntimeConfig) Validate() error {
	if c.Cache.Enabled && (c.Cache.TTLSeconds <= 0 || c.Cache.MaxEntries <= 0) {
		return fmt.Errorf("model_runtime.cache requires positive ttl_seconds and max_entries")
	}
	for name, route := range c.Routes {
		if name == "" || len(route.Targets) == 0 || route.StickySeconds < 0 {
			return fmt.Errorf("invalid model route %q", name)
		}
		if route.Strategy != "" && route.Strategy != "ordered" && route.Strategy != "round_robin" {
			return fmt.Errorf("unsupported route strategy %q", route.Strategy)
		}
	}
	return c.MoA.Validate()
}

// Validate 检查协作预设，参考模型的任何故障均按最少成功数处理。
func (c MoAConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.MaxParallel < 1 || c.MaxParallel > 32 || c.TimeoutSeconds < 1 {
		return fmt.Errorf("moa requires max_parallel 1..32 and positive timeout_seconds")
	}
	if c.PrivacyFilter != "" && c.PrivacyFilter != "display" && c.PrivacyFilter != "full" {
		return fmt.Errorf("unsupported moa privacy_filter")
	}
	if p, ok := c.Presets[c.DefaultPreset]; !ok || !p.Enabled {
		return fmt.Errorf("moa default preset is missing or disabled")
	}
	for name, p := range c.Presets {
		if !p.Enabled {
			continue
		}
		if name == "" || p.Aggregator.Model == "" || len(p.References) == 0 || p.MinSuccessful < 0 || p.MinSuccessful > len(p.References) {
			return fmt.Errorf("invalid moa preset %q", name)
		}
		if p.Fanout != "" && p.Fanout != "user_turn" && p.Fanout != "per_iteration" {
			var n int
			if _, err := fmt.Sscanf(p.Fanout, "every_n:%d", &n); err != nil || n < 2 || p.Fanout != fmt.Sprintf("every_n:%d", n) {
				return fmt.Errorf("invalid moa fanout %q", p.Fanout)
			}
		}
		for _, slot := range append(append([]MoASlot{}, p.References...), p.Aggregator) {
			if slot.Model == "" {
				return fmt.Errorf("empty moa model")
			}
			switch slot.ReasoningEffort {
			case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
			default:
				return fmt.Errorf("unsupported moa reasoning_effort %q", slot.ReasoningEffort)
			}
		}
	}
	return nil
}

func validateAuthURL(raw string, loopback bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid OAuth URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && loopback && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1") {
		return nil
	}
	return fmt.Errorf("OAuth URL requires HTTPS (HTTP allowed only on loopback)")
}

// Validate 限制 OAuth 标准流程与可信端点，允许回环开发服务器。
func (o OAuthConfig) Validate() error {
	if !o.Enabled {
		return nil
	}
	if (o.ClientID == "" && o.Flow != "openrouter") || o.TokenURL == "" {
		return fmt.Errorf("OAuth requires client_id and token_url")
	}
	for _, raw := range []string{o.TokenURL, o.AuthorizationURL, o.DeviceURL} {
		if raw != "" {
			if err := validateAuthURL(raw, true); err != nil {
				return err
			}
		}
	}
	if o.TokenEncoding != "" && o.TokenEncoding != "form" && o.TokenEncoding != "json" {
		return fmt.Errorf("unsupported OAuth token_encoding")
	}
	if o.TokenAuthMethod != "" && o.TokenAuthMethod != "none" && o.TokenAuthMethod != "client_secret_post" && o.TokenAuthMethod != "client_secret_basic" {
		return fmt.Errorf("unsupported OAuth token_auth_method")
	}
	if o.TokenAuthMethod == "client_secret_basic" && o.ClientSecret == "" {
		return fmt.Errorf("OAuth client_secret_basic requires client_secret")
	}
	if strings.ContainsAny(o.RefreshHeader, "\r\n") {
		return fmt.Errorf("invalid OAuth refresh_header")
	}
	switch o.Flow {
	case "", "pkce", "anthropic", "openrouter":
		if o.AuthorizationURL == "" {
			return fmt.Errorf("PKCE requires authorization_url")
		}
		if o.RedirectURL != "" {
			if err := validateAuthURL(o.RedirectURL, true); err != nil {
				return err
			}
		}
	case "device", "minimax", "qwen":
		if o.DeviceURL == "" {
			return fmt.Errorf("device OAuth requires device_url")
		}
	case "client_credentials":
	default:
		return fmt.Errorf("unsupported OAuth flow %q", o.Flow)
	}
	for key := range o.AuthParams {
		if strings.EqualFold(key, "state") || strings.EqualFold(key, "redirect_uri") || strings.HasPrefix(key, "code_challenge") {
			return fmt.Errorf("OAuth auth_params cannot replace security parameter %q", key)
		}
	}
	return nil
}

// WithDefaults 补齐已知提供商的真实 OAuth 端点；显式配置仍有最高优先级。
func (o OAuthConfig) WithDefaults(provider string) OAuthConfig {
	if !o.Enabled {
		return o
	}
	defaults := OAuthConfig{}
	switch provider {
	case "openai-codex":
		defaults = OAuthConfig{Flow: "pkce", ClientID: "app_EMoamEEZ73f0CkXaXp7hrann", AuthorizationURL: "https://auth.openai.com/oauth/authorize", TokenURL: "https://auth.openai.com/oauth/token", RedirectURL: "http://localhost:1455/auth/callback", Scopes: []string{"openid", "profile", "email", "offline_access"}}
	case "anthropic", "claude", "claude-code":
		defaults = OAuthConfig{Flow: "anthropic", TokenEncoding: "json", ClientID: "9d1c250a-e61b-44d9-88ed-5944d1962f5e", AuthorizationURL: "https://claude.ai/oauth/authorize", TokenURL: "https://platform.claude.com/v1/oauth/token", RedirectURL: "https://console.anthropic.com/oauth/code/callback", Scopes: []string{"org:create_api_key", "user:profile", "user:inference"}}
	case "nous":
		defaults = OAuthConfig{Flow: "device", RefreshHeader: "x-nous-refresh-token", ClientID: "hermes-cli", DeviceURL: "https://portal.nousresearch.com/api/oauth/device/code", TokenURL: "https://portal.nousresearch.com/api/oauth/token", Scopes: []string{"inference:invoke"}}
	case "xai-oauth":
		defaults = OAuthConfig{Flow: "device", ClientID: "b1a00492-073a-47ea-816f-4c329264a828", DeviceURL: "https://auth.x.ai/oauth2/device/code", TokenURL: "https://auth.x.ai/oauth2/token", Scopes: []string{"openid", "profile", "email", "offline_access", "grok-cli:access", "api:access"}}
	case "qwen-oauth":
		defaults = OAuthConfig{Flow: "qwen", ClientID: "f0304373b74a44d2b584a3fb70ca9e56", DeviceURL: "https://chat.qwen.ai/api/v1/oauth2/device/code", TokenURL: "https://chat.qwen.ai/api/v1/oauth2/token", Scopes: []string{"openid", "profile", "email", "model.completion"}}
	case "openrouter":
		defaults = OAuthConfig{Flow: "openrouter", TokenEncoding: "json", AuthorizationURL: "https://openrouter.ai/auth", TokenURL: "https://openrouter.ai/api/v1/auth/keys", RedirectURL: "http://127.0.0.1:1455/callback"}
	case "minimax-oauth":
		defaults = OAuthConfig{Flow: "minimax", ClientID: "78257093-7e40-4613-99e0-527b14b39113", DeviceURL: "https://api.minimax.io/oauth/code", TokenURL: "https://api.minimax.io/oauth/token", Scopes: []string{"group_id", "profile", "model.completion"}}
	}
	if o.Flow == "" {
		o.Flow = defaults.Flow
	}
	if o.TokenEncoding == "" {
		o.TokenEncoding = defaults.TokenEncoding
	}
	if o.ClientID == "" {
		o.ClientID = defaults.ClientID
	}
	if o.AuthorizationURL == "" {
		o.AuthorizationURL = defaults.AuthorizationURL
	}
	if o.TokenURL == "" {
		o.TokenURL = defaults.TokenURL
	}
	if o.DeviceURL == "" {
		o.DeviceURL = defaults.DeviceURL
	}
	if o.RedirectURL == "" {
		o.RedirectURL = defaults.RedirectURL
	}
	if len(o.Scopes) == 0 {
		o.Scopes = defaults.Scopes
	}
	if o.RefreshHeader == "" {
		o.RefreshHeader = defaults.RefreshHeader
	}
	return o
}
