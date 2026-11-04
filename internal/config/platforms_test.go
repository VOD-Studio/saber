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
	cfg, err := Load(writeFileConfig(t, "platforms:\n  terminal:\n    enabled: false\n  matrix:\n    enabled: true\n  violet:\n    enabled: true\n    endpoint: https://blog.example.com\n    bot_token: violet_bot_xxx\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Platforms.Terminal.Enabled {
		t.Fatal("terminal.enabled 应为 false")
	}
	if !cfg.Platforms.Matrix.Enabled || !cfg.PlatformEnabled("matrix") {
		t.Fatal("matrix.enabled 应为 true")
	}
	if cfg.Matrix.Homeserver != DefaultMatrixConfig().Homeserver || cfg.Matrix.DeviceName != DefaultMatrixConfig().DeviceName {
		t.Fatalf("matrix 明细默认值应保留: %+v", cfg.Matrix)
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
	cfg.Platforms.Matrix.Enabled = true
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

// TestVioletTweetsConfig_Defaults 固定推文互动的默认值与有效值回退规则。
func TestVioletTweetsConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg := DefaultVioletTweetsConfig()
	if cfg.Enabled {
		t.Fatal("推文互动默认应关闭")
	}
	if !cfg.ReplyToComments || !cfg.ReplyToQuotes {
		t.Fatalf("两种回复开关默认应开启: %+v", cfg)
	}
	if cfg.ReplyTimeoutSeconds != 120 || cfg.MaxContentRunes != 480 || cfg.MaxImagesPerTweet != 4 ||
		cfg.MaxPostsPerDay != 8 || cfg.MinPostIntervalMinutes != 30 {
		t.Fatalf("推文默认值不符: %+v", cfg)
	}
	if got := cfg.EffectiveReplyTimeout(); got.Seconds() != 120 {
		t.Fatalf("EffectiveReplyTimeout = %v", got)
	}
	if got := cfg.EffectiveMaxContentRunes(); got != 480 {
		t.Fatalf("EffectiveMaxContentRunes = %d", got)
	}
	zero := VioletTweetsConfig{}
	if zero.EffectiveReplyTimeout().Seconds() != 120 || zero.EffectiveMaxContentRunes() != 480 {
		t.Fatalf("零值未回退默认: %v / %d", zero.EffectiveReplyTimeout(), zero.EffectiveMaxContentRunes())
	}
	custom := VioletTweetsConfig{ReplyTimeoutSeconds: 30, MaxContentRunes: 200}
	if custom.EffectiveReplyTimeout().Seconds() != 30 || custom.EffectiveMaxContentRunes() != 200 {
		t.Fatalf("显式值被覆盖: %v / %d", custom.EffectiveReplyTimeout(), custom.EffectiveMaxContentRunes())
	}
}

// TestVioletTweetsConfig_Validate 验证负数始终拒绝，启用时再校验完整范围。
func TestVioletTweetsConfig_Validate(t *testing.T) {
	t.Parallel()
	defaults := DefaultVioletTweetsConfig()
	if err := defaults.Validate(); err != nil {
		t.Fatalf("默认推文配置应合法: %v", err)
	}
	zero := VioletTweetsConfig{}
	if err := zero.Validate(); err != nil {
		t.Fatalf("未启用时零值应合法: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*VioletTweetsConfig)
	}{
		{"负超时", func(c *VioletTweetsConfig) { c.ReplyTimeoutSeconds = -1 }},
		{"负正文上限", func(c *VioletTweetsConfig) { c.MaxContentRunes = -1 }},
		{"负图片上限", func(c *VioletTweetsConfig) { c.MaxImagesPerTweet = -1 }},
		{"负发帖上限", func(c *VioletTweetsConfig) { c.MaxPostsPerDay = -1 }},
		{"负发帖间隔", func(c *VioletTweetsConfig) { c.MinPostIntervalMinutes = -1 }},
	} {
		t.Run("未启用/"+tc.name, func(t *testing.T) {
			cfg := DefaultVioletTweetsConfig()
			cfg.Enabled = false
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("负数应被拒绝: %+v", cfg)
			}
		})
	}
	for _, tc := range []struct {
		name    string
		mutate  func(*VioletTweetsConfig)
		wantErr string
	}{
		{"超时为零", func(c *VioletTweetsConfig) { c.ReplyTimeoutSeconds = 0 }, "reply_timeout_seconds"},
		{"超时过大", func(c *VioletTweetsConfig) { c.ReplyTimeoutSeconds = 3601 }, "reply_timeout_seconds"},
		{"正文为零", func(c *VioletTweetsConfig) { c.MaxContentRunes = 0 }, "max_content_runes"},
		{"正文超上限", func(c *VioletTweetsConfig) { c.MaxContentRunes = 501 }, "max_content_runes"},
		{"图片超上限", func(c *VioletTweetsConfig) { c.MaxImagesPerTweet = 5 }, "max_images_per_tweet"},
		{"发帖上限为零", func(c *VioletTweetsConfig) { c.MaxPostsPerDay = 0 }, "max_posts_per_day"},
	} {
		t.Run("启用/"+tc.name, func(t *testing.T) {
			cfg := DefaultVioletTweetsConfig()
			cfg.Enabled = true
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("越界配置应被拒绝: %+v", cfg)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误 %q 未包含 %q", err, tc.wantErr)
			}
		})
	}
	noImages := DefaultVioletTweetsConfig()
	noImages.Enabled = true
	noImages.MaxImagesPerTweet = 0
	if err := noImages.Validate(); err != nil {
		t.Fatalf("max_images_per_tweet: 0 表示不允许配图，应合法: %v", err)
	}
	bounds := DefaultVioletTweetsConfig()
	bounds.Enabled = true
	bounds.ReplyTimeoutSeconds = 1
	bounds.MaxContentRunes = 500
	bounds.MaxImagesPerTweet = 4
	if err := bounds.Validate(); err != nil {
		t.Fatalf("边界值应合法: %v", err)
	}
}

// TestLoad_VioletTweetsSection 验证 tweets: 子节按字段覆盖默认值，
// 并且未出现的字段保留默认（解码目标来自 DefaultConfig）。
func TestLoad_VioletTweetsSection(t *testing.T) {
	t.Parallel()
	cfg, err := Load(writeFileConfig(t, "platforms:\n  violet:\n    enabled: true\n    endpoint: https://blog.example.com\n    bot_token: violet_bot_xxx\n    tweets:\n      enabled: true\n      reply_to_quotes: false\n      max_content_runes: 320\n"))
	if err != nil {
		t.Fatal(err)
	}
	tweets := cfg.Platforms.Violet.Tweets
	if !tweets.Enabled || tweets.ReplyToQuotes || !tweets.ReplyToComments {
		t.Fatalf("推文开关未按 YAML 生效: %+v", tweets)
	}
	if tweets.MaxContentRunes != 320 {
		t.Fatalf("max_content_runes = %d, want 320", tweets.MaxContentRunes)
	}
	if tweets.ReplyTimeoutSeconds != 120 || tweets.MaxImagesPerTweet != 4 || tweets.MaxPostsPerDay != 8 || tweets.MinPostIntervalMinutes != 30 {
		t.Fatalf("缺省推文字段应保留默认值: %+v", tweets)
	}
	if err := cfg.Platforms.Violet.Validate(); err != nil {
		t.Fatalf("合法推文配置校验失败: %v", err)
	}
}
