package telegramplatform

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// maxMessageRunes 是 Telegram 每段消息上限；长回复继续发送后续分段。
const maxMessageRunes = 4000

// apiEnvelope 是 Telegram Bot API 的统一响应信封。
type apiEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// apiError 保留 Telegram 的错误码与描述。
//
// 401（token 失效）与 429（限流）的处置完全不同：前者重试无意义，
// 后者要按 retry_after 退避，因此错误里必须带上状态码和重试提示。
type apiError struct {
	Method     string
	Status     int
	Code       int
	Message    string
	retryDelay time.Duration
}

// Error 实现 error；只输出方法名与描述，不包含带 token 的 URL。
func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("telegram API %s 返回状态 %d", e.Method, e.Status)
	}
	return fmt.Sprintf("telegram API %s 失败（%d/%d）: %s", e.Method, e.Status, e.Code, e.Message)
}

// RetryDelay 返回服务端 retry_after 提示，供任务回复的重试节奏使用。
func (e *apiError) RetryDelay() time.Duration { return e.retryDelay }

// isNotModified 识别 Telegram 对「正文没变」的编辑拒绝。
//
// 流式编辑最后几帧经常与上一帧相同，这不是故障，按成功处理才能让收尾逻辑继续。
func isNotModified(err error) bool {
	apiErr, ok := err.(*apiError)
	return ok && strings.Contains(strings.ToLower(apiErr.Message), "message is not modified")
}

// botUser 是 getMe 返回的机器人身份。
type botUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// update 是 getUpdates 返回的单条更新；只关心新消息，编辑等事件解析后跳过。
type update struct {
	UpdateID      int64    `json:"update_id"`
	Message       *message `json:"message"`
	EditedMessage *message `json:"edited_message"`
	ChannelPost   *message `json:"channel_post"`
}

// message 是 Telegram 消息的快照，只保留规范化需要的字段。
type message struct {
	MessageID       int64       `json:"message_id"`
	From            *user       `json:"from"`
	Chat            chatRef     `json:"chat"`
	Date            int64       `json:"date"`
	Text            string      `json:"text"`
	ReplyToMessage  *message    `json:"reply_to_message"`
	MessageThreadID int         `json:"message_thread_id"`
	Entities        []entity    `json:"entities"`
	Caption         string      `json:"caption"`
	CaptionEntities []entity    `json:"caption_entities"`
	Photo           []mediaFile `json:"photo"`
	Voice           *mediaFile  `json:"voice"`
	Audio           *mediaFile  `json:"audio"`
	Video           *mediaFile  `json:"video"`
	Document        *mediaFile  `json:"document"`
	Animation       *mediaFile  `json:"animation"`
}

// user 是消息发送者。
type user struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// chatRef 是消息所属会话。
type chatRef struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
}

// entity 是正文实体，text_mention 能定位到没有用户名的机器人。
type entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	User   *user  `json:"user"`
}
