package violetplatform

import "encoding/json"

// Violet Bot API 的读模型。
//
// 字段只声明 Saber 真正用到的部分：Violet 的 DTO 是给人和 bot 共用的读模型，
// 会随前端需求增删，多余字段在此处忽略即可，反向要求字段稳定只会让接入端先坏。
// 命名与 JSON 键一一对应，避免映射层再解释一遍。

// botProfileDTO 对应 GET /chat/bot/profile。
type botProfileDTO struct {
	// ID 是 bot 凭证 ID。
	ID string `json:"id"`
	// UserID 是 bot 对应的虚拟用户 ID，入站消息的 sender.id 即此值。
	UserID string `json:"user_id"`
	// Username 是虚拟用户名，@提及 token 里的寻址值。
	Username string `json:"username"`
	// Name 是显示名。
	Name string `json:"name"`
}

// userDTO 是消息作者与成员的公开资料。
type userDTO struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

// messageDTO 对应消息快照：SSE 的 message.created 与历史接口共用同一形状。
type messageDTO struct {
	ID             string         `json:"id"`
	ConversationID string         `json:"conversation_id"`
	Sender         userDTO        `json:"sender"`
	Type           string         `json:"type"`
	Content        string         `json:"content"`
	Mentions       map[string]any `json:"mentions"`
	ReplyTo        *messageRefDTO `json:"reply_to"`
	IsDeleted      bool           `json:"is_deleted"`
	EditedAt       string         `json:"edited_at,omitempty"`
	CreatedAt      string         `json:"created_at"`
}

// messageRefDTO 是被引用消息的紧凑预览，这里只取定位所需的 ID。
type messageRefDTO struct {
	ID string `json:"id"`
}

// conversationDTO 对应会话详情，kind 决定私聊与群聊的响应策略。
type conversationDTO struct {
	ID      string      `json:"id"`
	Kind    string      `json:"kind"`
	Title   string      `json:"title"`
	Members []memberDTO `json:"members"`
}

// memberDTO 是会话成员。
type memberDTO struct {
	User userDTO `json:"user"`
}

// 会话形态取值，与 Violet domain 的 ConversationKind 一致。
const (
	kindDirect = "direct"
	kindRoom   = "room"
)

// envelope 是 Violet 的统一响应信封：成功时数据在 data，游标在 meta.pagination。
type envelope struct {
	Data envelopeData  `json:"data"`
	Meta *envelopeMeta `json:"meta"`
}

// envelopeData 用原始 JSON 承载，让调用方按端点决定解成对象还是数组。
type envelopeData = json.RawMessage

// envelopeMeta 只解析接入端需要的分页信息。
type envelopeMeta struct {
	Message    string      `json:"message"`
	Pagination *pagination `json:"pagination"`
}

type pagination struct {
	Limit      int    `json:"limit"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

// errorBody 是失败响应：错误码与人类可读消息分开，日志里两者都留才查得出原因。
type errorBody struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// eventFrame 是 SSE 事件信封。ID 恒为空：bot 流不写 id: 行，也不支持续传。
type eventFrame struct {
	Type       string       `json:"type"`
	Version    int          `json:"version"`
	OccurredAt string       `json:"occurred_at"`
	Data       eventPayload `json:"data"`
}

// eventPayload 合并两类事件的字段：message.created 用 conversation_id + message，
// typing.updated 用 conversation_id + user_id + is_typing。
type eventPayload struct {
	ConversationID string     `json:"conversation_id"`
	Message        messageDTO `json:"message"`
	UserID         string     `json:"user_id"`
	IsTyping       bool       `json:"is_typing"`
}

// SSE 事件名，与 Violet 写帧用的事件类型一致。
const (
	eventMessageCreated = "message.created"
	eventTypingUpdated  = "typing.updated"
)
