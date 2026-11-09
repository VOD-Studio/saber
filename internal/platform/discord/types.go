package discordplatform

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// maxContentRunes 是 Discord 每段消息上限；长回复继续发送后续分段。
const maxContentRunes = 1900

// gatewayIntents 是接入所需的最小 intent 集合：
// GUILDS、GUILD_MESSAGES、DIRECT_MESSAGES 与 MESSAGE_CONTENT。
//
// MESSAGE_CONTENT 属于特权 intent，必须在 Discord 开发者后台为 bot 显式开启，
// 否则频道与私聊的 content 字段会是空串。
const gatewayIntents = 1<<0 | 1<<9 | 1<<12 | 1<<15

// apiError 保留 Discord 的状态码与错误消息。
//
// 429 带 retry_after（秒，浮点），401 说明 token 无效重试无意义，二者要分开处理。
type apiError struct {
	Method     string
	Status     int
	Message    string
	retryDelay time.Duration
}

// Error 实现 error；不包含请求 URL，避免把 token 带进日志。
func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("discord API %s 返回状态 %d", e.Method, e.Status)
	}
	return fmt.Sprintf("discord API %s 失败（%d）: %s", e.Method, e.Status, e.Message)
}

// RetryDelay 返回服务端 retry_after 提示，供任务回复的重试节奏使用。
func (e *apiError) RetryDelay() time.Duration { return e.retryDelay }

// gatewayPayload 是 Gateway 的通用信封。
type gatewayPayload struct {
	Op int             `json:"op"`
	T  string          `json:"t"`
	S  *int64          `json:"s"`
	D  json.RawMessage `json:"d"`
}

// gatewayHello 是 op 10 的负载。
type gatewayHello struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

// gatewayReady 是 READY 事件里规范化需要的字段。
type gatewayReady struct {
	SessionID string `json:"session_id"`
	User      struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Bot      bool   `json:"bot"`
	} `json:"user"`
}

// gatewayMessage 是 MESSAGE_CREATE 里规范化需要的字段。
type gatewayMessage struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
	Content   string `json:"content"`
	Type      int    `json:"type"`
	Author    struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Bot      bool   `json:"bot"`
	} `json:"author"`
	MessageReference *struct {
		MessageID string `json:"message_id"`
	} `json:"message_reference"`
	ReferencedMessage *gatewayMessage `json:"referenced_message"`
	Mentions          []struct {
		ID string `json:"id"`
	} `json:"mentions"`
	Attachments []mediaAttachment `json:"attachments"`
}

// gatewayBot 是 GET /gateway/bot 的响应。
type gatewayBot struct {
	URL string `json:"url"`
}

// stripMentionTokens 移除正文里对本 bot 的提及，保留其余文本。
func stripMentionTokens(content, botID string) string {
	content = strings.ReplaceAll(content, "<@!"+botID+">", "")
	content = strings.ReplaceAll(content, "<@"+botID+">", "")
	return strings.TrimSpace(content)
}
