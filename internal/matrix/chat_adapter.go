package matrix

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// ChatAdapter 将 Matrix 命令和媒体转为通用消息，并实现平台回复能力。
// 登录、同步和 E2EE 仍由原 Matrix 客户端管理。
type ChatAdapter struct {
	service     *CommandService
	media       *MediaService
	mediaConfig config.MediaConfig
	handler     chat.Handler
	edit        bool
}

// NewChatAdapter 绑定 Matrix 账号和通用处理入口。
func NewChatAdapter(service *CommandService, media *MediaService, mediaConfig config.MediaConfig, edit bool, handler chat.Handler) *ChatAdapter {
	return &ChatAdapter{service: service, media: media, mediaConfig: mediaConfig, edit: edit, handler: handler}
}

// Session 将房间及线程转换为带账号作用域的会话。
func (a *ChatAdapter) Session(ctx context.Context, roomID id.RoomID) chat.Session {
	return chat.Session{Platform: "matrix", Account: string(a.service.BotID()), Conversation: string(roomID), Thread: string(GetThreadID(ctx))}
}

// Message 在接入层解析图片和引用关系，核心无需理解 MXC 或 Matrix SDK。
func (a *ChatAdapter) Message(ctx context.Context, userID id.UserID, roomID id.RoomID, text string) chat.Message {
	message := chat.Message{Session: a.Session(ctx, roomID), ID: string(GetEventID(ctx)), SenderID: string(userID), Text: text, ReplyTo: string(GetReplyToID(ctx))}
	// 引用机器人消息时，Matrix 会在正文前加上回退引用；控制指令只看用户自己写的那部分。
	if control, ok := a.controlText(ctx, text); ok {
		message.ControlText = &control
	}
	if a.media == nil || !a.mediaConfig.Enabled {
		return message
	}
	for _, info := range []*MediaInfo{GetMediaInfo(ctx), GetReferencedMediaInfo(ctx)} {
		if info == nil || info.Type != "image" {
			continue
		}
		data, err := a.media.DownloadImage(ctx, info)
		if err != nil {
			slog.Warn("下载聊天图片失败", "error", err)
			continue
		}
		message.Attachments = append(message.Attachments, chat.Attachment{Kind: "image", Name: info.Body, MIMEType: info.MimeType, URL: data})
	}
	return message
}

// Receive 将规范化消息送入与其他平台相同的 Handler。
func (a *ChatAdapter) Receive(ctx context.Context, userID id.UserID, roomID id.RoomID, text string) (agent.Result, error) {
	if a.service == nil || a.handler == nil {
		return agent.Result{}, errors.New("matrix chat adapter is not configured")
	}
	return a.handler(ctx, a.Message(ctx, userID, roomID, text), a)
}

// Handle 实现现有 Matrix 命令接口，仅负责去掉命令参数分隔。
func (a *ChatAdapter) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	_, err := a.Receive(ctx, userID, roomID, strings.Join(args, " "))
	return err
}

// controlText 返回剥离引用回退后的用户正文。handleReply 已解析时直接沿用，
// 私聊等未预解析的路径在这里就地剥离，避免核心链路读取 Matrix 上下文。
func (a *ChatAdapter) controlText(ctx context.Context, text string) (string, bool) {
	if body, ok := getReplyBody(ctx); ok {
		return body, true
	}
	if GetReplyToID(ctx) == "" {
		return "", false
	}
	if body := event.TrimReplyFallbackText(text); body != text {
		return body, true
	}
	return "", false
}

// Capabilities 声明当前 Matrix 展示配置。
func (a *ChatAdapter) Capabilities() chat.Capabilities {
	return chat.Capabilities{Edit: a.edit, Typing: true, Reply: true}
}

func (a *ChatAdapter) validate(ctx context.Context, session chat.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.Validate(); err != nil {
		return err
	}
	if a.service == nil || session.Platform != "matrix" || session.Account != string(a.service.BotID()) {
		return errors.New("reply does not belong to this Matrix account")
	}
	return nil
}

// Send 将通用回复的会话和引用关系转换为 Matrix 消息。
func (a *ChatAdapter) Send(ctx context.Context, reply chat.Reply) (string, error) {
	if err := a.validate(ctx, reply.Session); err != nil {
		return "", err
	}
	relates := chatReplyRelation(reply)
	messageID, err := a.service.sendTextWithOptions(ctx, id.RoomID(reply.Session.Conversation), reply.Text, relates, mautrix.ReqSendEvent{TransactionID: reply.TransactionID})
	return string(messageID), err
}

// Edit 只更新该运行此前创建的 Matrix 消息。
func (a *ChatAdapter) Edit(ctx context.Context, messageID string, reply chat.Reply) error {
	if err := a.validate(ctx, reply.Session); err != nil {
		return err
	}
	if !a.edit {
		return errors.New("matrix message editing is disabled")
	}
	if messageID == "" {
		return errors.New("matrix edit requires a message ID")
	}
	// 新内容保留原回复和线程关系，外层仅表示替换哪条消息。
	content := &event.MessageEventContent{
		MsgType: event.MsgText, Body: "* " + reply.Text,
		RelatesTo:  &event.RelatesTo{Type: event.RelReplace, EventID: id.EventID(messageID)},
		NewContent: &event.MessageEventContent{MsgType: event.MsgText, Body: reply.Text, RelatesTo: chatReplyRelation(reply)},
	}
	_, err := a.service.client.SendMessageEvent(ctx, id.RoomID(reply.Session.Conversation), event.EventMessage, content)
	return err
}

// SetTyping 将通用输入状态转换为 Matrix typing 请求。
func (a *ChatAdapter) SetTyping(ctx context.Context, session chat.Session, active bool) error {
	if err := a.validate(ctx, session); err != nil {
		return err
	}
	if active {
		return a.service.StartTyping(ctx, id.RoomID(session.Conversation), 30000)
	}
	return a.service.StopTyping(ctx, id.RoomID(session.Conversation))
}

func chatReplyRelation(reply chat.Reply) *event.RelatesTo {
	var relates *event.RelatesTo
	if reply.ReplyTo != "" || reply.Session.Thread != "" {
		relates = &event.RelatesTo{}
		if reply.ReplyTo != "" {
			relates.InReplyTo = &event.InReplyTo{EventID: id.EventID(reply.ReplyTo)}
		}
		if reply.Session.Thread != "" {
			relates.Type = event.RelThread
			relates.EventID = id.EventID(reply.Session.Thread)
		}
	}
	return relates
}
