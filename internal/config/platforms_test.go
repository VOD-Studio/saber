package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFileConfig 写入临时配置文件并返回路径。
func writeFileConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoad_PlatformsSection 验证 platforms: 节按字段覆盖默认值，未出现的字段保持默认。
func TestLoad_PlatformsSection(t *testing.T) {
	t.Parallel()
	cfg, err := Load(writeFileConfig(t, "platforms:\n  terminal:\n    enabled: false\n  violet:\n    enabled: true\n    endpoint: https://blog.example.com\n    bot_token: violet_bot_xxx\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Platforms.Terminal.Enabled {
		t.Fatal("terminal.enabled 应为 false")
	}
	if !cfg.Platforms.Violet.Enabled {
		t.Fatal("violet.enabled 应为 true")
	}
	if cfg.Platforms.Violet.Endpoint != "https://blog.example.com" || cfg.Platforms.Violet.BotToken != "violet_bot_xxx" {
		t.Fatalf("violet 接入参数未生效: %+v", cfg.Platforms.Violet)
	}
	if cfg.Platforms.Violet.EditIntervalMs != 200 || cfg.Platforms.Violet.HTTPTimeoutSeconds != 15 {
		t.Fatalf("violet 缺省字段应保留默认值: %+v", cfg.Platforms.Violet)
	}
	if !cfg.Platforms.Violet.DirectChatAutoReply || !cfg.Platforms.Violet.GroupChatMentionReply {
		t.Fatalf("violet 自动回复开关默认应开启: %+v", cfg.Platforms.Violet)
	}
	if err := cfg.Platforms.Violet.Validate(); err != nil {
		t.Fatalf("合法 violet 配置校验失败: %v", err)
	}
}

// TestPlatformEnabled 验证平台开关按名称解析，且未知平台不会自动上线。
func TestPlatformEnabled(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.Matrix.Enabled = true
	cfg.Platforms.Violet.Enabled = true
	cfg.Platforms.Terminal.Enabled = false

	for _, tc := range []struct {
		name, want string
		enabled    bool
	}{
		{name: "matrix", enabled: true},
		{name: "violet", enabled: true},
		{name: "terminal", enabled: false},
		{name: "discord", enabled: false},
		{name: "", enabled: false},
	} {
		if got := cfg.PlatformEnabled(tc.name); got != tc.enabled {
			t.Fatalf("PlatformEnabled(%q) = %v, want %v", tc.name, got, tc.enabled)
		}
	}
}

// TestVioletConfig_Validate 验证启用后的必填项与取值边界。
func TestVioletConfig_Validate(t *testing.T) {
	t.Parallel()
	base := func(mutate func(*VioletConfig)) *VioletConfig {
		cfg := DefaultVioletConfig()
		cfg.Enabled = true
		cfg.Endpoint = "https://blog.example.com"
		cfg.BotToken = "violet_bot_xxx"
		mutate(&cfg)
		return &cfg
	}
	disabled := DefaultVioletConfig()
	if err := disabled.Validate(); err != nil {
		t.Fatalf("未启用的配置不应阻塞启动: %v", err)
	}
	if err := base(func(*VioletConfig) {}).Validate(); err != nil {
		t.Fatalf("合法配置校验失败: %v", err)
	}
	for _, tc := range []struct {
		name    string
		mutate  func(*VioletConfig)
		wantErr string
	}{
		{"缺少 endpoint", func(c *VioletConfig) { c.Endpoint = "" }, "endpoint"},
		{"缺少 token", func(c *VioletConfig) { c.BotToken = "" }, "bot_token"},
		{"非 http 协议", func(c *VioletConfig) { c.Endpoint = "blog.example.com" }, "http:// 或 https://"},
		{"编辑间隔为零", func(c *VioletConfig) { c.EditIntervalMs = 0 }, "edit_interval_ms"},
		{"超时为零", func(c *VioletConfig) { c.HTTPTimeoutSeconds = 0 }, "http_timeout_seconds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := base(tc.mutate).Validate()
			if err == nil {
				t.Fatal("期望校验失败")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误 %q 未包含 %q", err, tc.wantErr)
			}
		})
	}
}

// TestVioletConfig_EffectiveAccount 验证账号回退顺序：显式配置 > endpoint 主机名 > 平台名。
func TestVioletConfig_EffectiveAccount(t *testing.T) {
	t.Parallel()
	explicit := DefaultVioletConfig()
	explicit.Account = " primary "
	explicit.Endpoint = "https://blog.example.com"
	if got := explicit.EffectiveAccount(); got != "primary" {
		t.Fatalf("EffectiveAccount = %q, want primary", got)
	}
	fallback := DefaultVioletConfig()
	fallback.Endpoint = "https://blog.example.com:8443/path"
	if got := fallback.EffectiveAccount(); got != "blog.example.com:8443" {
		t.Fatalf("EffectiveAccount = %q, want 站点主机名", got)
	}
	empty := DefaultVioletConfig()
	if got := empty.EffectiveAccount(); got != "violet" {
		t.Fatalf("EffectiveAccount = %q, want violet", got)
	}
}
