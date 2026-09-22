// Package chat 定义聊天平台与 Saber 之间的消息、会话和回复契约。
package chat

import (
	"context"
	"errors"
	"strconv"

	"rua.plus/saber/internal/agent"
)

// SessionID 是平台、账号、会话和线程共同构成的存储键，应通过 Session.Key 创建。
type SessionID string

// Session 标识一个平台账号内的会话；零值无效。
type Session struct {
	// Platform 是接入端名称，例如 matrix 或 memory。
	Platform string
	// Account 是接入账号的稳定标识。
	Account string
	// Conversation 是平台原生会话标识，不跨平台解释其格式。
	Conversation string
	// Thread 非空时将线程历史与主会话隔离。
	Thread string
}

// Validate 拒绝缺少作用域的会话，避免空键或跨账号历史混用。
func (s Session) Validate() error {
	if s.Platform == "" || s.Account == "" || s.Conversation == "" {
		return errors.New("chat session requires platform, account and conversation")
	}
	return nil
}

// Key 通过明确编码隔离字段，避免分隔符出现在原始 ID 中时产生碰撞。
func (s Session) Key() SessionID {
	return SessionID("[" + strconv.Quote(s.Platform) + "," + strconv.Quote(s.Account) + "," + strconv.Quote(s.Conversation) + "," + strconv.Quote(s.Thread) + "]")
}

// Attachment 是 adapter 已解析的附件，不包含平台 SDK 对象。
type Attachment struct {
	// Kind 当前支持 image；其他附件由上层能力决定是否接受。
	Kind string
	// Name 保留附件显示名。
	Name string
	// MIMEType 是附件媒体类型。
	MIMEType string
	// URL 是可供模型使用的图片 URL 或 Data URL，私有媒体由 adapter 先下载。
	URL string
}

// Message 是发送给 Agent 的规范化入站消息。
type Message struct {
	// Session 标识历史与回复的目标。
	Session Session
	// ID 是当前消息 ID，允许不提供；有值时回复指向此消息。
	ID string
	// SenderID 是当前平台账号作用域内的用户标识。
	SenderID string
	// Text 是去掉平台命令或提及前缀后的内容。
	Text string
	// ControlText 是接入端剥离平台引用回退后的用户原文，用于识别控制指令；
	// nil 表示该平台没有引用包装，按 Text 识别。指向空串表示用户只引用未发言。
	ControlText *string
	// ReplyTo 是入站消息所引用的消息 ID。
	ReplyTo string
	// Attachments 包含当前消息及 adapter 已解析的引用附件。
	Attachments []Attachment
}

// CommandText 返回识别控制指令时应使用的正文：优先接入端给出的用户原文，
// 避免把引用回退里的历史文本误判成本轮指令。
func (m Message) CommandText() string {
	if m.ControlText != nil {
		return *m.ControlText
	}
	return m.Text
}

// Validate 验证通用必填项，平台 ID 格式校验由 adapter 负责。
func (m Message) Validate() error {
	if err := m.Session.Validate(); err != nil {
		return err
	}
	if m.SenderID == "" {
		return errors.New("chat sender is required")
	}
	if m.Text == "" && len(m.Attachments) == 0 {
		return errors.New("chat message is empty")
	}
	for _, a := range m.Attachments {
		if a.Kind != "image" || a.URL == "" {
			return errors.New("unsupported or empty chat attachment")
		}
	}
	return nil
}

// Capabilities 决定如何展示响应，模型是否流式传输与展示能力相互独立。
type Capabilities struct {
	// Edit 表示可以原地更新已发送消息。
	Edit bool
	// Typing 表示支持输入状态。
	Typing bool
	// Reply 表示支持引用当前消息回复。
	Reply bool
}

// Reply 是发往原会话的文本回复。
type Reply struct {
	// TransactionID 在平台支持时用于幂等发送；为空时创建普通消息。
	TransactionID string
	// Session 是目标会话，必须由入站消息派生。
	Session Session
	// ReplyTo 是被回复的消息 ID。
	ReplyTo string
	// Text 是当前完整内容，编辑时也使用完整内容。
	Text string
}

// Adapter 实现平台发送能力；不支持的可选方法不会被调用。
type Adapter interface {
	// Capabilities 返回该账号的展示能力。
	Capabilities() Capabilities
	// Send 发送一条消息并返回可用于编辑的平台消息 ID。
	Send(context.Context, Reply) (string, error)
	// Edit 更新已发送消息的完整内容。
	Edit(context.Context, string, Reply) error
	// SetTyping 设置会话输入状态。
	SetTyping(context.Context, Session, bool) error
}

// Handler 是所有聊天 adapter 共用的消息处理入口。
type Handler func(context.Context, Message, Adapter) (agent.Result, error)

// Identity 是工具授权和限流使用的调用来源。
type Identity struct {
	// Session 包含平台和账号作用域。
	Session Session
	// SenderID 是平台原生发送者 ID。
	SenderID string
}

type identityKey struct{}

// WithIdentity 设置已由接入端确定的身份，运行时不会信任模型生成的身份字段。
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

// IdentityFromContext 读取完整身份，缺少任一必填项时返回 false。
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	return identity, ok && identity.Session.Validate() == nil && identity.SenderID != ""
}

// UserKey 在同一平台账号内跨会话识别同一用户，用于用户级限流。
func (i Identity) UserKey() string {
	return "[" + strconv.Quote(i.Session.Platform) + "," + strconv.Quote(i.Session.Account) + "," + strconv.Quote(i.SenderID) + "]"
}
