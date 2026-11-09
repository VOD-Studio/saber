package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
)

// DesktopConfig 配置独立桌面客户端，不授予任何执行权限。
type DesktopConfig struct {
	Enabled            bool   `yaml:"enabled"`
	AppDir             string `yaml:"app_dir"`
	StateDir           string `yaml:"state_dir"`
	Endpoint           string `yaml:"endpoint"`
	TokenFile          string `yaml:"token_file"`
	Theme              string `yaml:"theme"`
	Language           string `yaml:"language"`
	FontFamily         string `yaml:"font_family"`
	TerminalFontFamily string `yaml:"terminal_font_family"`
	QuickEntryShortcut string `yaml:"quick_entry_shortcut"`
}

// DefaultDesktopConfig 返回本机连接与保守显示默认值。
func DefaultDesktopConfig() DesktopConfig {
	return DesktopConfig{Endpoint: "http://127.0.0.1:8320", Theme: "system", Language: "zh", QuickEntryShortcut: "CommandOrControl+Shift+Space"}
}

// Validate 拒绝向远程地址发送本机令牌。
func (c DesktopConfig) Validate() error {
	for _, p := range []string{c.AppDir, c.StateDir, c.TokenFile} {
		if p != "" && !filepath.IsAbs(p) {
			return fmt.Errorf("desktop 路径必须为绝对路径: %s", p)
		}
	}
	if c.Endpoint != "" {
		u, e := url.Parse(c.Endpoint)
		if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("desktop.endpoint 无效")
		}
		ip := net.ParseIP(u.Hostname())
		local := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
		if u.Scheme != "https" && !(u.Scheme == "http" && local) {
			return fmt.Errorf("远程桌面连接必须使用 HTTPS")
		}
		if c.TokenFile != "" && !local {
			return fmt.Errorf("本机 token_file 不能用于远程连接")
		}
	}
	if c.Theme != "" && c.Theme != "system" && c.Theme != "light" && c.Theme != "dark" {
		return fmt.Errorf("desktop.theme 必须是 system/light/dark")
	}
	if c.Language != "" && c.Language != "zh" && c.Language != "en" {
		return fmt.Errorf("desktop.language 必须是 zh/en")
	}
	return nil
}
