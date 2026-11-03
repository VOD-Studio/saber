package slackplatform

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// maxContentRunes 是 Slack 消息正文上限（约 40000）；留 1000 字符余量给截断提示。
const maxContentRunes = 39000

// contentTruncatedSuffix 在超长正文被截断时追加，让读者知道后面还有内容没送达。
const contentTruncatedSuffix = "\n\n…（内容过长，已截断）"

// apiError 保留 Slack 错误码与限流提示。
//
// Slack 的限流信息在 HTTP Retry-After 头里，错误体只有 "ratelimited"；
// 不把两者一起保留，上层就只能统一按最长退避重试。
type apiError struct {
	Method     string
	Status     int
	Code       string
	retryDelay time.Duration
}

// Error 实现 error；不包含带 token 的 URL。
func (e *apiError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("slack API %s 返回状态 %d", e.Method, e.Status)
	}
	return fmt.Sprintf("slack API %s 失败（%d）: %s", e.Method, e.Status, e.Code)
}

// RetryDelay 返回 Retry-After 提示，供任务回复的重试节奏使用。
func (e *apiError) RetryDelay() time.Duration { return e.retryDelay }

// slackResponse 是 Web API 与 Socket Mode 共用的响应信封。
type slackResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	URL   string `json:"url"`
}

// authIdentity 是 auth.test 的关键字段：mention 检测要用 bot 的 user_id。
type authIdentity struct {
	UserID string `json:"user_id"`
	BotID  string `json:"bot_id"`
	User   string `json:"user"`
	Team   string `json:"team"`
}

// socketEnvelope 是 Socket Mode 推送的外层信封。
type socketEnvelope struct {
	Type       string `json:"type"`
	EnvelopeID string `json:"envelope_id"`
	Reason     string `json:"reason"`
	Payload    struct {
		Event slackEvent `json:"event"`
	} `json:"payload"`
}

// slackEvent 是 message 事件的字段子集。
type slackEvent struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	User        string `json:"user"`
	BotID       string `json:"bot_id"`
	Text        string `json:"text"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	Edited      *struct {
		User string `json:"user"`
		TS   string `json:"ts"`
	} `json:"edited"`
}

// truncateContent 把出站正文压进 Slack 的字符上限。
func truncateContent(content string) (string, bool) {
	runes := []rune(content)
	if len(runes) <= maxContentRunes {
		return content, false
	}
	keep := maxContentRunes - utf8.RuneCountInString(contentTruncatedSuffix)
	if keep < 0 {
		keep = 0
	}
	return string(runes[:keep]) + contentTruncatedSuffix, true
}

// escapeText 转义 Slack 控制字符 &、<、>。
//
// Slack 把 <@U123>、<!everyone> 当提及、把 <http://x|text> 当链接；
// 模型输出里出现这些形状时不应该真的 ping 人或被渲染成别的东西。
func escapeText(text string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(text)
}
