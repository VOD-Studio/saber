package config

import (
	"fmt"
	"net/url"
	"strings"
)

// BrowserConfig 控制真实浏览器来源；宿主进程与私网访问必须单独明确授权。
type BrowserConfig struct {
	Enabled                 bool           `yaml:"enabled"`
	Provider                string         `yaml:"provider"`
	Engine                  string         `yaml:"engine"`
	ExecutablePath          string         `yaml:"executable_path"`
	CDPURL                  string         `yaml:"cdp_url"`
	AttachExistingTabs      bool           `yaml:"attach_existing_tabs"`
	APIKey                  string         `yaml:"api_key"`
	APIKeyEnv               string         `yaml:"api_key_env"`
	ProjectID               string         `yaml:"project_id"`
	CloudProfileID          string         `yaml:"cloud_profile_id"`
	APIURL                  string         `yaml:"api_url"`
	CloudOptions            map[string]any `yaml:"cloud_options"`
	Headed                  bool           `yaml:"headed"`
	AllowHostProcess        bool           `yaml:"allow_host_process"`
	AllowPrivateURLs        bool           `yaml:"allow_private_urls"`
	AutoLocalForPrivateURLs bool           `yaml:"auto_local_for_private_urls"`
	UserDataDir             string         `yaml:"user_data_dir"`
	UseRealProfile          bool           `yaml:"use_real_profile"`
	RealProfileSource       string         `yaml:"real_profile_source"`
	RealProfilePin          string         `yaml:"real_profile_pin"`
	ChromiumArgs            []string       `yaml:"chromium_args"`
	CommandTimeout          int            `yaml:"command_timeout"`
	InactivityTimeout       int            `yaml:"inactivity_timeout"`
	MaxSessions             int            `yaml:"max_sessions"`
	DialogPolicy            string         `yaml:"dialog_policy"`
	DialogTimeout           int            `yaml:"dialog_timeout_s"`
}

// DefaultBrowserConfig 默认关闭宿主能力，不隐式安装浏览器或调用云服务。
func DefaultBrowserConfig() BrowserConfig {
	return BrowserConfig{Provider: "local", Engine: "chromium", CommandTimeout: 30, InactivityTimeout: 120, MaxSessions: 8, DialogPolicy: "must_respond", DialogTimeout: 300}
}

// Validate 拒绝缺少明确来源、危险启动覆盖与不完整的真实凭据配置。
func (c BrowserConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	switch c.Provider {
	case "local", "browserbase", "browser-use", "firecrawl", "nous", "cdp":
	default:
		name := strings.TrimPrefix(c.Provider, "plugin:")
		if name == c.Provider || name == "" || strings.ContainsAny(name, "/\\\x00\r\n") {
			return fmt.Errorf("browser.provider: unsupported provider %q", c.Provider)
		}
	}
	if c.Engine != "" && c.Engine != "chromium" && c.Engine != "lightpanda" {
		return fmt.Errorf("browser.engine: expected chromium or lightpanda")
	}
	if ((c.Provider == "local" && c.CDPURL == "") || c.UseRealProfile || c.AutoLocalForPrivateURLs) && !c.AllowHostProcess {
		return fmt.Errorf("browser.allow_host_process must explicitly permit host browser launch; it does not inherit sandbox network isolation")
	}
	if (c.Provider == "cdp" || c.Provider == "nous") && c.CDPURL == "" {
		return fmt.Errorf("browser.cdp_url is required; Nous must use an authenticated gateway-issued CDP endpoint")
	}
	for _, value := range []string{c.CDPURL, c.APIURL} {
		if value != "" {
			u, e := url.Parse(value)
			if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ws" && u.Scheme != "wss") {
				return fmt.Errorf("browser endpoint must be an absolute HTTP(S)/WS(S) URL without userinfo")
			}
		}
	}
	if c.UseRealProfile && (c.RealProfileSource == "" || c.ExecutablePath == "") {
		return fmt.Errorf("browser.use_real_profile requires real_profile_source and executable_path of the actual browser")
	}
	if c.AttachExistingTabs && c.CDPURL == "" {
		return fmt.Errorf("browser.attach_existing_tabs requires a configured cdp_url and a trusted administrator")
	}
	if c.UseRealProfile && c.CDPURL != "" {
		return fmt.Errorf("browser.use_real_profile and cdp_url are mutually exclusive browser sources")
	}
	if c.RealProfilePin != "" && (strings.ContainsAny(c.RealProfilePin, "/\\") || c.RealProfilePin == "." || c.RealProfilePin == "..") {
		return fmt.Errorf("browser.real_profile_pin must name one profile directory")
	}
	if c.CommandTimeout < 1 || c.CommandTimeout > 1800 || c.InactivityTimeout < 1 || c.MaxSessions < 1 || c.MaxSessions > 128 || c.DialogTimeout < 1 {
		return fmt.Errorf("browser timeouts/session limits must be positive (command_timeout <= 1800, max_sessions <= 128)")
	}
	switch c.DialogPolicy {
	case "must_respond", "auto_dismiss", "auto_accept":
	default:
		return fmt.Errorf("browser.dialog_policy: expected must_respond, auto_dismiss or auto_accept")
	}
	for _, arg := range c.ChromiumArgs {
		if !strings.HasPrefix(arg, "--") || strings.HasPrefix(arg, "--remote-debugging") || strings.HasPrefix(arg, "--user-data-dir") || arg == "--no-sandbox" || arg == "--disable-web-security" {
			return fmt.Errorf("browser.chromium_args contains unsafe override %q", arg)
		}
	}
	return nil
}
