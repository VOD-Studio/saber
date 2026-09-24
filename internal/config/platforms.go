package config

import (
	"fmt"
	"net/url"
	"strings"
)

// PlatformsConfig 集中管理可插拔聊天平台接入端的开关与平台专属参数。
//
// Matrix 的明细配置仍在顶层 matrix: 节（见 MatrixConfig），启用开关在此处。
type PlatformsConfig struct {
	Terminal TerminalPlatformConfig `yaml:"terminal"` // 本机 HTTP 聊天入口（saber chat / TUI）
	Matrix   MatrixPlatformConfig   `yaml:"matrix"`   // Matrix 聊天入口开关
	Violet   VioletConfig           `yaml:"violet"`   // Violet 博客平台 Bot API 接入
}

// MatrixPlatformConfig 存储 Matrix 聊天入口的开关。
type MatrixPlatformConfig struct {
	Enabled bool `yaml:"enabled"` // 默认关闭
}

// TerminalPlatformConfig 存储本机终端入口的开关。
//
// 它不依赖任何外部平台，因此默认开启；关闭后 saber chat 与 TUI 不再受理消息。
type TerminalPlatformConfig struct {
	Enabled bool `yaml:"enabled"` // 是否启用本机 HTTP 聊天入口（默认 true）
}

// VioletConfig 存储 Violet 博客平台 Bot API 接入参数。
//
// Violet 以 bot 虚拟用户身份收发站内聊天，凭据由 Violet 管理端
// （/admin/chat-bots）签发，Saber 侧只持有 endpoint 与 bot_token。
type VioletConfig struct {
	Enabled               bool   `yaml:"enabled"`                  // 是否启用 Violet 接入
	Endpoint              string `yaml:"endpoint"`                 // Violet 站点根地址，如 https://blog.example.com
	BotToken              string `yaml:"bot_token"`                // Bot API token，形如 violet_bot_xxx
	Account               string `yaml:"account"`                  // 历史与限流作用域的账号标识，留空时取 endpoint 主机名
	DirectChatAutoReply   bool   `yaml:"direct_chat_auto_reply"`   // 私聊自动回复（无需 !ai 前缀）
	GroupChatMentionReply bool   `yaml:"group_chat_mention_reply"` // 群聊被 @ 时自动回复
	EditIntervalMs        int    `yaml:"edit_interval_ms"`         // 出站消息两次编辑的最小间隔（毫秒，默认 200）
	HTTPTimeoutSeconds    int    `yaml:"http_timeout_seconds"`     // 单次 API 请求超时（秒，默认 15），不含 SSE 长连接
}

// DefaultPlatformsConfig 返回带有合理默认值的平台配置。
func DefaultPlatformsConfig() PlatformsConfig {
	return PlatformsConfig{
		Terminal: TerminalPlatformConfig{Enabled: true},
		Matrix:   MatrixPlatformConfig{Enabled: false},
		Violet:   DefaultVioletConfig(),
	}
}

// DefaultVioletConfig 返回带有合理默认值的 Violet 配置；默认不启用。
func DefaultVioletConfig() VioletConfig {
	return VioletConfig{
		Enabled:               false,
		DirectChatAutoReply:   true,
		GroupChatMentionReply: true,
		EditIntervalMs:        200,
		HTTPTimeoutSeconds:    15,
	}
}

// Validate 检查 Violet 接入所需的必填项。未启用时直接通过，避免无关配置阻塞启动。
func (v *VioletConfig) Validate() error {
	if !v.Enabled {
		return nil
	}
	if strings.TrimSpace(v.Endpoint) == "" {
		return fmt.Errorf("platforms.violet.endpoint 不能为空")
	}
	if strings.TrimSpace(v.BotToken) == "" {
		return fmt.Errorf("platforms.violet.bot_token 不能为空")
	}
	if !strings.HasPrefix(v.Endpoint, "http://") && !strings.HasPrefix(v.Endpoint, "https://") {
		return fmt.Errorf("platforms.violet.endpoint 必须以 http:// 或 https:// 开头")
	}
	if _, err := url.Parse(v.Endpoint); err != nil {
		return fmt.Errorf("platforms.violet.endpoint 不是合法 URL: %w", err)
	}
	if v.EditIntervalMs <= 0 {
		return fmt.Errorf("platforms.violet.edit_interval_ms 必须大于 0")
	}
	if v.HTTPTimeoutSeconds <= 0 {
		return fmt.Errorf("platforms.violet.http_timeout_seconds 必须大于 0")
	}
	return nil
}

// EffectiveAccount 返回会话作用域用的账号标识。
//
// 账号参与 chat.Session.Key，改动它等于换一份历史，因此显式配置优先，
// 未配置时回退到 endpoint 主机名，保证同一站点的标识稳定。
func (v *VioletConfig) EffectiveAccount() string {
	if account := strings.TrimSpace(v.Account); account != "" {
		return account
	}
	if host, err := url.Parse(strings.TrimSpace(v.Endpoint)); err == nil && host.Host != "" {
		return host.Host
	}
	return "violet"
}

// PlatformEnabled 返回指定平台是否应当启动。
//
// 未知平台一律返回 false：新增接入端必须同时在这里登记开关，
// 避免「注册了就自动上线」这种没人配置的默认行为。
func (c *Config) PlatformEnabled(name string) bool {
	switch name {
	case "terminal":
		return c.Platforms.Terminal.Enabled
	case "matrix":
		return c.Platforms.Matrix.Enabled
	case "violet":
		return c.Platforms.Violet.Enabled
	default:
		return false
	}
}
