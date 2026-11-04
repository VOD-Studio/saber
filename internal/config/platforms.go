package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// PlatformsConfig 集中管理可插拔聊天平台接入端的开关与平台专属参数。
//
// Matrix 的明细配置仍在顶层 matrix: 节（见 MatrixConfig），启用开关在此处。
type PlatformsConfig struct {
	Terminal TerminalPlatformConfig `yaml:"terminal"` // 本机 HTTP 聊天入口（saber chat / TUI）
	Matrix   MatrixPlatformConfig   `yaml:"matrix"`   // Matrix 聊天入口开关
	Violet   VioletConfig           `yaml:"violet"`   // Violet 博客平台 Bot API 接入
	Telegram TelegramConfig         `yaml:"telegram"` // Telegram Bot API 接入
	Discord  DiscordConfig          `yaml:"discord"`  // Discord Bot Gateway 接入
	Slack    SlackConfig            `yaml:"slack"`    // Slack Socket Mode 接入
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
	Enabled               bool               `yaml:"enabled"`                  // 是否启用 Violet 接入
	Endpoint              string             `yaml:"endpoint"`                 // Violet 站点根地址，如 https://blog.example.com
	BotToken              string             `yaml:"bot_token"`                // Bot API token，形如 violet_bot_xxx
	Account               string             `yaml:"account"`                  // 历史与限流作用域的账号标识，留空时取 endpoint 主机名
	DirectChatAutoReply   bool               `yaml:"direct_chat_auto_reply"`   // 私聊自动回复（无需 !ai 前缀）
	GroupChatMentionReply bool               `yaml:"group_chat_mention_reply"` // 群聊被 @ 时自动回复
	EditIntervalMs        int                `yaml:"edit_interval_ms"`         // 出站消息两次编辑的最小间隔（毫秒，默认 200）
	HTTPTimeoutSeconds    int                `yaml:"http_timeout_seconds"`     // 单次 API 请求超时（秒，默认 15），不含 SSE 长连接
	Tweets                VioletTweetsConfig `yaml:"tweets"`                   // 推文互动接入参数
}

// VioletTweetsConfig 存储推文互动参数。
//
// 推文是聊天之外的第二条入口：有人评论或引用 bot 的推文时，Violet 推送事件，
// Saber 组织一次模型回合，再把结果作为公开评论发回推文。默认关闭，避免
// 上线即自动回评。
type VioletTweetsConfig struct {
	Enabled                bool `yaml:"enabled"`                   // 是否处理推文互动事件（默认 false）
	ReplyToComments        bool `yaml:"reply_to_comments"`         // 回应推文评论与评论下的追问（默认 true）
	ReplyToQuotes          bool `yaml:"reply_to_quotes"`           // 回应对本人推文的引用（默认 true）
	ReplyTimeoutSeconds    int  `yaml:"reply_timeout_seconds"`     // 单次推文回合超时（秒，默认 120，1..3600）
	MaxContentRunes        int  `yaml:"max_content_runes"`         // 评论正文上限（Unicode 字符，默认 480，1..500）
	MaxImagesPerTweet      int  `yaml:"max_images_per_tweet"`      // 单条推文图片上限（默认 4，0..4）
	MaxPostsPerDay         int  `yaml:"max_posts_per_day"`         // 每日主动发帖上限（默认 8，≥1）
	MinPostIntervalMinutes int  `yaml:"min_post_interval_minutes"` // 两次主动发帖最小间隔（分钟，默认 30，≥0）
}

// 推文配置的默认值与硬边界。Violet 服务端评论正文上限 500 字符，超出会被直接拒绝。
const (
	// DefaultVioletTweetReplyTimeoutSeconds 是单次推文回合的默认超时。
	DefaultVioletTweetReplyTimeoutSeconds = 120
	// DefaultVioletTweetMaxContentRunes 是评论正文的默认上限。
	DefaultVioletTweetMaxContentRunes = 480
	// DefaultVioletTweetMaxImages 是单条推文的默认配图上限。
	DefaultVioletTweetMaxImages = 4
	// DefaultVioletTweetMaxPostsPerDay 是每日主动发帖的默认上限。
	DefaultVioletTweetMaxPostsPerDay = 8
	// DefaultVioletTweetMinPostIntervalMinutes 是两次主动发帖的默认最小间隔。
	DefaultVioletTweetMinPostIntervalMinutes = 30
	// maxVioletTweetContentRunes 是 Violet 服务端接受的评论正文上限。
	maxVioletTweetContentRunes = 500
)

// TelegramConfig 存储 Telegram Bot API 接入参数。
//
// Telegram 走 getUpdates 长轮询：不需要公网地址、证书或 webhook 注册，
// 因此账号只需 bot_token；api_base 可指向自建反代或测试用假 API。
type TelegramConfig struct {
	Enabled               bool   `yaml:"enabled"`                  // 是否启用 Telegram 接入
	BotToken              string `yaml:"bot_token"`                // BotFather 签发的 token，形如 123456:ABC-DEF
	APIBase               string `yaml:"api_base"`                 // Bot API 根地址，留空用 https://api.telegram.org
	Account               string `yaml:"account"`                  // 历史与限流作用域的账号标识，留空用 telegram
	DirectChatAutoReply   bool   `yaml:"direct_chat_auto_reply"`   // 私聊自动回复
	GroupChatMentionReply bool   `yaml:"group_chat_mention_reply"` // 群聊被 @ 或被回复时自动回复
	EditIntervalMs        int    `yaml:"edit_interval_ms"`         // 出站消息两次编辑的最小间隔（毫秒，默认 200）
	HTTPTimeoutSeconds    int    `yaml:"http_timeout_seconds"`     // 单次 API 请求超时（秒，默认 15），不含长轮询
	PollTimeoutSeconds    int    `yaml:"poll_timeout_seconds"`     // getUpdates 长轮询挂起时长（秒，默认 30）
}

// DiscordConfig 存储 Discord Bot Gateway 接入参数。
//
// 入站走 Gateway WebSocket（无需公网回调），出站走 REST；MESSAGE_CONTENT intent
// 必须在 Discord 开发者后台显式开启，否则群聊正文为空。
type DiscordConfig struct {
	Enabled               bool   `yaml:"enabled"`                  // 是否启用 Discord 接入
	BotToken              string `yaml:"bot_token"`                // 开发者后台签发的 bot token
	APIBase               string `yaml:"api_base"`                 // Discord REST 根地址，留空用 https://discord.com/api/v10
	Account               string `yaml:"account"`                  // 历史与限流作用域的账号标识，留空用 discord
	DirectChatAutoReply   bool   `yaml:"direct_chat_auto_reply"`   // 私聊自动回复
	GroupChatMentionReply bool   `yaml:"group_chat_mention_reply"` // 频道内被 @ 或被回复时自动回复
	EditIntervalMs        int    `yaml:"edit_interval_ms"`         // 出站消息两次编辑的最小间隔（毫秒，默认 200）
	HTTPTimeoutSeconds    int    `yaml:"http_timeout_seconds"`     // 单次 REST 请求超时（秒，默认 15）
}

// SlackConfig 存储 Slack Socket Mode 接入参数。
//
// app_token（xapp-）只用于 apps.connections.open 建立 WebSocket，bot_token（xoxb-）
// 用于 Web API；两者权限相互独立，缺一个都无法收发消息。
type SlackConfig struct {
	Enabled               bool   `yaml:"enabled"`                  // 是否启用 Slack 接入
	BotToken              string `yaml:"bot_token"`                // Bot User OAuth Token，形如 xoxb-
	AppToken              string `yaml:"app_token"`                // App-Level Token，形如 xapp-，需要 connections:write
	APIBase               string `yaml:"api_base"`                 // Web API 根地址，留空用 https://slack.com/api
	Account               string `yaml:"account"`                  // 历史与限流作用域的账号标识，留空用 slack
	DirectChatAutoReply   bool   `yaml:"direct_chat_auto_reply"`   // 私聊（IM）自动回复
	GroupChatMentionReply bool   `yaml:"group_chat_mention_reply"` // 频道内被 @ 或被回复时自动回复
	EditIntervalMs        int    `yaml:"edit_interval_ms"`         // 出站消息两次编辑的最小间隔（毫秒，默认 200）
	HTTPTimeoutSeconds    int    `yaml:"http_timeout_seconds"`     // 单次 Web API 请求超时（秒，默认 15）
}

// DefaultPlatformsConfig 返回带有合理默认值的平台配置。
func DefaultPlatformsConfig() PlatformsConfig {
	return PlatformsConfig{
		Terminal: TerminalPlatformConfig{Enabled: true},
		Matrix:   MatrixPlatformConfig{Enabled: false},
		Violet:   DefaultVioletConfig(),
		Telegram: DefaultTelegramConfig(),
		Discord:  DefaultDiscordConfig(),
		Slack:    DefaultSlackConfig(),
	}
}

// DefaultTelegramConfig 返回 Telegram 的默认参数；默认不启用。
func DefaultTelegramConfig() TelegramConfig {
	return TelegramConfig{
		Enabled:               false,
		APIBase:               "https://api.telegram.org",
		Account:               "telegram",
		DirectChatAutoReply:   true,
		GroupChatMentionReply: true,
		EditIntervalMs:        200,
		HTTPTimeoutSeconds:    15,
		PollTimeoutSeconds:    30,
	}
}

// DefaultDiscordConfig 返回 Discord 的默认参数；默认不启用。
func DefaultDiscordConfig() DiscordConfig {
	return DiscordConfig{
		Enabled:               false,
		APIBase:               "https://discord.com/api/v10",
		Account:               "discord",
		DirectChatAutoReply:   true,
		GroupChatMentionReply: true,
		EditIntervalMs:        200,
		HTTPTimeoutSeconds:    15,
	}
}

// DefaultSlackConfig 返回 Slack 的默认参数；默认不启用。
func DefaultSlackConfig() SlackConfig {
	return SlackConfig{
		Enabled:               false,
		APIBase:               "https://slack.com/api",
		Account:               "slack",
		DirectChatAutoReply:   true,
		GroupChatMentionReply: true,
		EditIntervalMs:        200,
		HTTPTimeoutSeconds:    15,
	}
}

// Validate 检查 Telegram 接入所需的必填项。未启用时直接通过。
func (t *TelegramConfig) Validate() error {
	if !t.Enabled {
		return nil
	}
	if strings.TrimSpace(t.BotToken) == "" {
		return fmt.Errorf("platforms.telegram.bot_token 不能为空")
	}
	if err := validateEndpointURL("platforms.telegram.api_base", t.APIBase); err != nil {
		return err
	}
	if t.EditIntervalMs <= 0 {
		return fmt.Errorf("platforms.telegram.edit_interval_ms 必须大于 0")
	}
	if t.HTTPTimeoutSeconds <= 0 {
		return fmt.Errorf("platforms.telegram.http_timeout_seconds 必须大于 0")
	}
	if t.PollTimeoutSeconds <= 0 {
		return fmt.Errorf("platforms.telegram.poll_timeout_seconds 必须大于 0")
	}
	return nil
}

// Validate 检查 Discord 接入所需的必填项。未启用时直接通过。
func (d *DiscordConfig) Validate() error {
	if !d.Enabled {
		return nil
	}
	if strings.TrimSpace(d.BotToken) == "" {
		return fmt.Errorf("platforms.discord.bot_token 不能为空")
	}
	if d.EditIntervalMs <= 0 {
		return fmt.Errorf("platforms.discord.edit_interval_ms 必须大于 0")
	}
	if d.HTTPTimeoutSeconds <= 0 {
		return fmt.Errorf("platforms.discord.http_timeout_seconds 必须大于 0")
	}
	return validateEndpointURL("platforms.discord.api_base", d.APIBase)
}

// Validate 检查 Slack 接入所需的必填项。未启用时直接通过。
func (s *SlackConfig) Validate() error {
	if !s.Enabled {
		return nil
	}
	if strings.TrimSpace(s.BotToken) == "" {
		return fmt.Errorf("platforms.slack.bot_token 不能为空")
	}
	if !strings.HasPrefix(strings.TrimSpace(s.BotToken), "xoxb-") {
		return fmt.Errorf("platforms.slack.bot_token 必须以 xoxb- 开头")
	}
	if strings.TrimSpace(s.AppToken) == "" {
		return fmt.Errorf("platforms.slack.app_token 不能为空")
	}
	if !strings.HasPrefix(strings.TrimSpace(s.AppToken), "xapp-") {
		return fmt.Errorf("platforms.slack.app_token 必须以 xapp- 开头")
	}
	if s.EditIntervalMs <= 0 {
		return fmt.Errorf("platforms.slack.edit_interval_ms 必须大于 0")
	}
	if s.HTTPTimeoutSeconds <= 0 {
		return fmt.Errorf("platforms.slack.http_timeout_seconds 必须大于 0")
	}
	return validateEndpointURL("platforms.slack.api_base", s.APIBase)
}

// validateEndpointURL 拒绝缺少协议或无法解析的 API 根地址。
func validateEndpointURL(field, endpoint string) error {
	value := strings.TrimSpace(endpoint)
	if value == "" {
		return fmt.Errorf("%s 不能为空", field)
	}
	if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
		return fmt.Errorf("%s 必须以 http:// 或 https:// 开头", field)
	}
	if _, err := url.Parse(value); err != nil {
		return fmt.Errorf("%s 不是合法 URL: %w", field, err)
	}
	return nil
}

// EffectiveAccount 返回 Telegram 会话作用域用的账号标识。
func (t *TelegramConfig) EffectiveAccount() string {
	return effectiveAccount(t.Account, "telegram")
}

// EffectiveAccount 返回 Discord 会话作用域用的账号标识。
func (d *DiscordConfig) EffectiveAccount() string {
	return effectiveAccount(d.Account, "discord")
}

// EffectiveAccount 返回 Slack 会话作用域用的账号标识。
func (s *SlackConfig) EffectiveAccount() string {
	return effectiveAccount(s.Account, "slack")
}

// effectiveAccount 实现「显式配置优先，否则回退平台名」的账号标识规则。
func effectiveAccount(configured, fallback string) string {
	if account := strings.TrimSpace(configured); account != "" {
		return account
	}
	return fallback
}

// DefaultVioletConfig 返回带有合理默认值的 Violet 配置；默认不启用。
func DefaultVioletConfig() VioletConfig {
	return VioletConfig{
		Enabled:               false,
		DirectChatAutoReply:   true,
		GroupChatMentionReply: true,
		EditIntervalMs:        200,
		HTTPTimeoutSeconds:    15,
		Tweets:                DefaultVioletTweetsConfig(),
	}
}

// DefaultVioletTweetsConfig 返回推文互动的默认参数；默认不处理推文事件。
//
// 事件开关关闭但回复开关保持 true：用户打开 enabled 时拿到的是「评论与引用都回」，
// 而不是一个还需要二次确认的空壳配置。
func DefaultVioletTweetsConfig() VioletTweetsConfig {
	return VioletTweetsConfig{
		Enabled:                false,
		ReplyToComments:        true,
		ReplyToQuotes:          true,
		ReplyTimeoutSeconds:    DefaultVioletTweetReplyTimeoutSeconds,
		MaxContentRunes:        DefaultVioletTweetMaxContentRunes,
		MaxImagesPerTweet:      DefaultVioletTweetMaxImages,
		MaxPostsPerDay:         DefaultVioletTweetMaxPostsPerDay,
		MinPostIntervalMinutes: DefaultVioletTweetMinPostIntervalMinutes,
	}
}

// Validate 检查 Violet 接入所需的必填项。未启用时直接通过，避免无关配置阻塞启动。
//
// 推文子配置始终校验负值：负数是明显的笔误，与开关无关；只有推文接入启用时
// 才要求完整的取值范围。
func (v *VioletConfig) Validate() error {
	if err := v.Tweets.Validate(); err != nil {
		return err
	}
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

// Validate 校验推文互动配置：负值一律拒绝；启用时再检查各字段的完整范围。
func (t *VioletTweetsConfig) Validate() error {
	if t.ReplyTimeoutSeconds < 0 {
		return fmt.Errorf("platforms.violet.tweets.reply_timeout_seconds 不能为负数")
	}
	if t.MaxContentRunes < 0 {
		return fmt.Errorf("platforms.violet.tweets.max_content_runes 不能为负数")
	}
	if t.MaxImagesPerTweet < 0 {
		return fmt.Errorf("platforms.violet.tweets.max_images_per_tweet 不能为负数")
	}
	if t.MaxPostsPerDay < 0 {
		return fmt.Errorf("platforms.violet.tweets.max_posts_per_day 不能为负数")
	}
	if t.MinPostIntervalMinutes < 0 {
		return fmt.Errorf("platforms.violet.tweets.min_post_interval_minutes 不能为负数")
	}
	if !t.Enabled {
		return nil
	}
	if t.ReplyTimeoutSeconds < 1 || t.ReplyTimeoutSeconds > 3600 {
		return fmt.Errorf("platforms.violet.tweets.reply_timeout_seconds 必须在 1..3600 之间")
	}
	if t.MaxContentRunes < 1 || t.MaxContentRunes > maxVioletTweetContentRunes {
		return fmt.Errorf("platforms.violet.tweets.max_content_runes 必须在 1..%d 之间", maxVioletTweetContentRunes)
	}
	if t.MaxImagesPerTweet > DefaultVioletTweetMaxImages {
		return fmt.Errorf("platforms.violet.tweets.max_images_per_tweet 不能超过 %d", DefaultVioletTweetMaxImages)
	}
	if t.MaxPostsPerDay < 1 {
		return fmt.Errorf("platforms.violet.tweets.max_posts_per_day 必须大于 0")
	}
	return nil
}

// EffectiveReplyTimeout 返回单次推文回合的超时；零值取默认 120 秒。
func (t VioletTweetsConfig) EffectiveReplyTimeout() time.Duration {
	seconds := t.ReplyTimeoutSeconds
	if seconds <= 0 {
		seconds = DefaultVioletTweetReplyTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// EffectiveMaxContentRunes 返回评论正文上限；零值取默认 480。
func (t VioletTweetsConfig) EffectiveMaxContentRunes() int {
	if t.MaxContentRunes <= 0 {
		return DefaultVioletTweetMaxContentRunes
	}
	return t.MaxContentRunes
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
	case "telegram":
		return c.Platforms.Telegram.Enabled
	case "discord":
		return c.Platforms.Discord.Enabled
	case "slack":
		return c.Platforms.Slack.Enabled
	default:
		return false
	}
}
