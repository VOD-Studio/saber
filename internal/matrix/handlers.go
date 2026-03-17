// Package matrix 提供 Matrix 事件处理和命令处理功能。
package matrix

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// CommandHandler 定义处理机器人命令的接口。
type CommandHandler interface {
	// Handle 处理带有给定参数的命令。
	// ctx 提供取消和超时控制。
	// userID 是发送命令用户的 Matrix ID。
	// roomID 是发送命令的 Matrix 房间 ID。
	// args 是解析后的命令参数（不包括命令本身）。
	Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error
}

// CommandInfo 包含已注册命令的元数据。
type CommandInfo struct {
	Name        string
	Description string
	Handler     CommandHandler
}

// CommandService 管理命令注册和分发。
type CommandService struct {
	mu             sync.RWMutex
	commands       map[string]CommandInfo
	client         *mautrix.Client
	botID          id.UserID
	directChatAI   CommandHandler
	mentionAI      CommandHandler  // 群聊 mention AI 处理器
	replyAI        CommandHandler  // 回复消息 AI 处理器
	mentionService *MentionService // Mention 服务
}

// NewCommandService 创建一个新的命令服务。
func NewCommandService(client *mautrix.Client, botID id.UserID) *CommandService {
	return &CommandService{
		commands: make(map[string]CommandInfo),
		client:   client,
		botID:    botID,
	}
}

// RegisterCommand 注册一个不带描述的命令处理器。
// 命令名称不应包含前缀 (!)。
func (s *CommandService) RegisterCommand(cmd string, handler CommandHandler) {
	s.RegisterCommandWithDesc(cmd, "", handler)
}

// RegisterCommandWithDesc 注册带有描述的命令处理器。
// 命令名称不应包含前缀 (!)。
func (s *CommandService) RegisterCommandWithDesc(cmd, desc string, handler CommandHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.commands[strings.ToLower(cmd)] = CommandInfo{
		Name:        cmd,
		Description: desc,
		Handler:     handler,
	}

	slog.Debug("Registered command",
		"command", cmd,
		"description", desc)
}

// UnregisterCommand 从注册表中移除一个命令。
func (s *CommandService) UnregisterCommand(cmd string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.commands, strings.ToLower(cmd))
}

// GetCommand 按名称检索命令信息。
func (s *CommandService) GetCommand(cmd string) (CommandInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	info, ok := s.commands[strings.ToLower(cmd)]
	return info, ok
}

// SetDirectChatAIHandler 设置私聊自动回复的 AI 处理器。
func (s *CommandService) SetDirectChatAIHandler(handler CommandHandler) {
	s.directChatAI = handler
	slog.Debug("Set direct chat AI handler")
}

// SetMentionAIHandler 设置群聊 mention 的 AI 处理器。
func (s *CommandService) SetMentionAIHandler(handler CommandHandler) {
	s.mentionAI = handler
	slog.Debug("Set mention AI handler")
}

// SetReplyAIHandler 设置回复消息的 AI 处理器。
func (s *CommandService) SetReplyAIHandler(handler CommandHandler) {
	s.replyAI = handler
	slog.Debug("Set reply AI handler")
}

// SetMentionService 设置 mention 服务。
func (s *CommandService) SetMentionService(service *MentionService) {
	s.mentionService = service
	slog.Debug("Set mention service")
}

// isDirectChat 检查房间是否为私聊（只有2个成员）。
func (s *CommandService) isDirectChat(ctx context.Context, roomID id.RoomID) bool {
	stateMap, err := s.client.State(ctx, roomID)
	if err != nil {
		slog.Debug("Failed to get room state for direct chat check", "room", roomID, "error", err)
		return false
	}

	memberEvents, ok := stateMap[event.StateMember]
	if !ok {
		return false
	}

	joinedCount := 0
	for _, evt := range memberEvents {
		if evt == nil {
			continue
		}
		memberContent, ok := evt.Content.Parsed.(*event.MemberEventContent)
		if !ok {
			continue
		}
		if memberContent.Membership == event.MembershipJoin {
			joinedCount++
		}
	}

	return joinedCount == 2
}

// isReplyToBot 检查回复的目标消息是否是 bot 发送的。
func (s *CommandService) isReplyToBot(ctx context.Context, roomID id.RoomID, eventID id.EventID) bool {
	evt, err := s.client.GetEvent(ctx, roomID, eventID)
	if err != nil {
		slog.Debug("获取被回复消息失败", "room", roomID, "event_id", eventID, "error", err)
		return false
	}

	isBot := evt.Sender == s.botID
	slog.Debug("检查回复目标", "sender", evt.Sender, "botID", s.botID, "isBot", isBot)
	return isBot
}

// ListCommands 返回所有已注册的命令。
func (s *CommandService) ListCommands() []CommandInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]CommandInfo, 0, len(s.commands))
	for _, info := range s.commands {
		list = append(list, info)
	}
	return list
}

// ParsedCommand 表示从消息中解析的命令。
type ParsedCommand struct {
	Command string
	Args    []string
}

// ParseCommand 从消息体中提取命令和参数。
// 支持基于前缀的命令 (!command args) 和提及 (@bot:command args)。
// 如果消息不是有效命令则返回 nil。
func (s *CommandService) ParseCommand(body string) *ParsedCommand {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}

	// 检查基于前缀的命令 (!command)
	if strings.HasPrefix(body, "!") {
		return s.parsePrefixedCommand(body[1:])
	}

	// 检查基于提及的命令 (@bot:command)
	if strings.HasPrefix(body, "@") {
		return s.parseMentionCommand(body)
	}

	return nil
}

func (s *CommandService) parsePrefixedCommand(body string) *ParsedCommand {
	parts := strings.Fields(body)
	if len(parts) == 0 {
		return nil
	}

	return &ParsedCommand{
		Command: strings.ToLower(parts[0]),
		Args:    parts[1:],
	}
}

func (s *CommandService) parseMentionCommand(body string) *ParsedCommand {
	// 格式: @bot:server.com command args
	// 或: @bot:server.com: command args
	parts := strings.Fields(body)
	if len(parts) == 0 {
		return nil
	}

	// 第一部分应该是提及
	mention := parts[0]

	// 验证是否是对本机器人的提及
	expectedMention := string(s.botID)
	if mention != expectedMention {
		// 检查带有尾随冒号的提及
		if strings.TrimSuffix(mention, ":") != expectedMention {
			return nil
		}
	}

	// 剩余部分是命令和参数
	if len(parts) < 2 {
		return nil
	}

	return &ParsedCommand{
		Command: strings.ToLower(parts[1]),
		Args:    parts[2:],
	}
}

// HandleEvent 处理 Matrix 事件并分发命令。
// 它只处理消息事件，忽略来自机器人自身的事件。
func (s *CommandService) HandleEvent(ctx context.Context, evt *event.Event) error {
	// 只处理房间消息
	if evt.Type != event.EventMessage {
		return nil
	}

	// 解析消息内容
	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		return nil
	}

	// 忽略编辑消息
	if content.RelatesTo != nil && content.RelatesTo.Type == event.RelReplace {
		slog.Debug("Ignoring edited message", "event_id", evt.ID.String())
		return nil
	}

	// 忽略自身消息
	sender := evt.Sender
	if sender == s.botID {
		return nil
	}

	roomID := evt.RoomID

	// 记录接收到的消息
	slog.Info("Received message",
		"sender", sender.String(),
		"room", roomID.String(),
		"event_id", evt.ID.String(),
		"body", content.Body)

	// 注入 EventID 到上下文，用于回复消息功能
	ctx = WithEventID(ctx, evt.ID)

	// 解析命令
	parsed := s.ParseCommand(content.Body)

	// 如果不是命令，检查是否需要私聊自动回复
	if parsed == nil {
		if s.directChatAI != nil && s.isDirectChat(ctx, roomID) {
			slog.Info("Direct chat auto-reply triggered",
				"sender", sender.String(),
				"room", roomID.String())

			args := []string{content.Body}
			err := s.directChatAI.Handle(ctx, sender, roomID, args)
			if err != nil {
				slog.Error("Direct chat AI handler failed",
					"sender", sender.String(),
					"room", roomID.String(),
					"error", err)
				return s.reportError(ctx, roomID, "ai", err)
			}
		}

		// 回复消息响应（优先于 mention 检测，避免回复引用中的 mention 误触发）
		if s.replyAI != nil && content.RelatesTo != nil && content.RelatesTo.GetReplyTo() != "" {
			replyToEventID := content.RelatesTo.GetReplyTo()
			slog.Debug("检测到回复消息", "reply_to", replyToEventID.String(), "replyAI", s.replyAI != nil)
			if s.isReplyToBot(ctx, roomID, replyToEventID) {
				cleanedBody := event.TrimReplyFallbackText(content.Body)

				replyContext := ""
				if evt, err := s.client.GetEvent(ctx, roomID, replyToEventID); err == nil {
					if msgContent, ok := evt.Content.Parsed.(*event.MessageEventContent); ok {
						replyContext = msgContent.Body
					}
				}

				slog.Info("回复消息触发 AI 回复",
					"sender", sender.String(),
					"room", roomID.String(),
					"reply_to", replyToEventID.String(),
					"reply_context", replyContext,
					"message", cleanedBody)

				args := []string{cleanedBody}
				if replyContext != "" {
					args = []string{fmt.Sprintf("[引用消息]\n%s\n\n[回复]\n%s", replyContext, cleanedBody)}
				}

				if err := s.replyAI.Handle(ctx, sender, roomID, args); err != nil {
					slog.Error("回复消息处理失败", "error", err)
					return s.reportError(ctx, roomID, "ai", err)
				}
				return nil
			}
		} else if content.RelatesTo != nil {
			slog.Debug("回复消息条件不满足",
				"replyAI", s.replyAI != nil,
				"relatesTo", content.RelatesTo != nil,
				"replyTo", content.RelatesTo.GetReplyTo())
		}

		// 群聊 mention 响应
		if s.mentionAI != nil && s.mentionService != nil && !s.isDirectChat(ctx, roomID) {
			if msg, ok := s.mentionService.ParseMention(content.Body, content); ok {
				slog.Info("群聊 mention 触发 AI 回复",
					"sender", sender.String(),
					"room", roomID.String(),
					"message", msg)

				args := []string{msg}
				if err := s.mentionAI.Handle(ctx, sender, roomID, args); err != nil {
					slog.Error("群聊 mention 处理失败", "error", err)
					return s.reportError(ctx, roomID, "ai", err)
				}
			}
		}
		return nil
	}

	// 查找命令
	cmdInfo, ok := s.GetCommand(parsed.Command)
	if !ok {
		slog.Debug("Unknown command", "command", parsed.Command)
		return nil
	}

	// 记录命令执行
	slog.Info("Executing command",
		"command", parsed.Command,
		"sender", sender.String(),
		"room", roomID.String(),
		"args", parsed.Args)

	// 执行命令
	err := cmdInfo.Handler.Handle(ctx, sender, roomID, parsed.Args)
	if err != nil {
		slog.Error("Command execution failed",
			"command", parsed.Command,
			"sender", sender.String(),
			"error", err)

		// 向房间报告错误
		return s.reportError(ctx, roomID, parsed.Command, err)
	}

	return nil
}

func (s *CommandService) reportError(ctx context.Context, roomID id.RoomID, cmd string, err error) error {
	msg := fmt.Sprintf("Error executing command '%s': %v", cmd, err)

	// 如果上下文中有 EventID，则作为回复发送
	if eventID := GetEventID(ctx); eventID != "" {
		_, sendErr := s.SendReply(ctx, roomID, msg, eventID)
		if sendErr != nil {
			slog.Error("Failed to send error message to room",
				"room", roomID.String(),
				"error", sendErr)
			return fmt.Errorf("command error: %v, send error: %w", err, sendErr)
		}
		return err
	}

	// 否则，作为普通消息发送
	_, sendErr := s.client.SendMessageEvent(
		ctx,
		roomID,
		event.EventMessage,
		&event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    msg,
		},
	)

	if sendErr != nil {
		slog.Error("Failed to send error message to room",
			"room", roomID.String(),
			"error", sendErr)
		return fmt.Errorf("command error: %v, send error: %w", err, sendErr)
	}

	return err
}

// SendText 向房间发送文本消息。
func (s *CommandService) SendText(ctx context.Context, roomID id.RoomID, body string) error {
	_, err := s.client.SendMessageEvent(
		ctx,
		roomID,
		event.EventMessage,
		&event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    body,
		},
	)
	if err != nil {
		slog.Error("Failed to send message",
			"room", roomID.String(),
			"error", err)
	}

	return err
}

// SendFormattedText 向房间发送格式化消息（支持 HTML）。
//
// 参数:
//   - ctx: 上下文
//   - roomID: 目标房间 ID
//   - html: HTML 格式的消息内容
//   - plain: 纯文本格式的消息内容（用于不支持 HTML 的客户端）
//
// 返回值:
//   - error: 发送过程中发生的错误
func (s *CommandService) SendFormattedText(ctx context.Context, roomID id.RoomID, html, plain string) error {
	_, err := s.client.SendMessageEvent(
		ctx,
		roomID,
		event.EventMessage,
		&event.MessageEventContent{
			MsgType:       event.MsgText,
			Body:          plain,
			Format:        event.FormatHTML,
			FormattedBody: html,
		},
	)
	if err != nil {
		slog.Error("Failed to send formatted message",
			"room", roomID.String(),
			"error", err)
	}

	return err
}

// SendTextWithRelatesTo 向房间发送文本消息，并指定关系。
// 返回发送的消息事件 ID 和错误。
func (s *CommandService) SendTextWithRelatesTo(ctx context.Context, roomID id.RoomID, body string, relatesTo *event.RelatesTo) (id.EventID, error) {
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    body,
	}

	if relatesTo != nil {
		content.RelatesTo = relatesTo
		if relatesTo.Type == event.RelReplace {
			content.NewContent = &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    body,
			}
		}
		// 如果是回复消息，自动添加 fallback 文本
		if relatesTo.InReplyTo != nil {
			// Matrix 客户端需要 fallback 文本来显示回复关系
			// fallback 格式：> <@user:example.com> Original message\n\nbody
			content.Body = fmt.Sprintf("> <%s> %s\n\n%s", relatesTo.InReplyTo.EventID, relatesTo.InReplyTo.EventID, body)
		}
	}

	resp, err := s.client.SendMessageEvent(
		ctx,
		roomID,
		event.EventMessage,
		content,
	)
	if err != nil {
		slog.Error("Failed to send message with relatesTo",
			"room", roomID.String(),
			"error", err)
		return "", err
	}

	return resp.EventID, nil
}

// SendReply 发送回复消息到指定的事件。
//
// 参数:
//   - ctx: 上下文
//   - roomID: 房间 ID
//   - body: 消息内容
//   - replyTo: 要回复的事件 ID
//
// 返回值:
//   - id.EventID: 发送的消息事件 ID
//   - error: 操作过程中发生的错误
func (s *CommandService) SendReply(ctx context.Context, roomID id.RoomID, body string, replyTo id.EventID) (id.EventID, error) {
	relatesTo := &event.RelatesTo{
		InReplyTo: &event.InReplyTo{
			EventID: replyTo,
		},
	}
	return s.SendTextWithRelatesTo(ctx, roomID, body, relatesTo)
}

// BotID 返回机器人的用户 ID。
func (s *CommandService) BotID() id.UserID {
	return s.botID
}

// StartTyping 在房间中显示"正在输入"指示器。
//
// 参数:
//   - ctx: 上下文
//   - roomID: 房间 ID
//   - timeout: 超时时间（毫秒），默认 30000ms
//
// 返回值:
//   - error: 操作过程中发生的错误
func (s *CommandService) StartTyping(ctx context.Context, roomID id.RoomID, timeout int) error {
	if timeout <= 0 {
		timeout = 30000
	}

	slog.Debug("Starting typing indicator", "room", roomID.String(), "timeout", timeout)

	_, err := s.client.UserTyping(ctx, roomID, true, time.Duration(timeout)*time.Millisecond)
	if err != nil {
		slog.Error("Failed to start typing indicator", "room", roomID.String(), "error", err)
		return fmt.Errorf("failed to start typing: %w", err)
	}

	return nil
}

// StopTyping 停止房间中的"正在输入"指示器。
//
// 参数:
//   - ctx: 上下文
//   - roomID: 房间 ID
//
// 返回值:
//   - error: 操作过程中发生的错误
func (s *CommandService) StopTyping(ctx context.Context, roomID id.RoomID) error {
	slog.Debug("Stopping typing indicator", "room", roomID.String())

	_, err := s.client.UserTyping(ctx, roomID, false, 0)
	if err != nil {
		slog.Error("Failed to stop typing indicator", "room", roomID.String(), "error", err)
		return fmt.Errorf("failed to stop typing: %w", err)
	}

	return nil
}

// EventHandler 封装 CommandService 并实现 mautrix 事件处理。
type EventHandler struct {
	service *CommandService
	logger  *slog.Logger
}

// NewEventHandler 创建一个新的事件处理器。
func NewEventHandler(service *CommandService) *EventHandler {
	return &EventHandler{
		service: service,
		logger:  slog.With("component", "event_handler"),
	}
}

// OnMessage 处理传入的消息事件。
// 这设计用于作为 Syncer.OnEvent 回调使用。
func (h *EventHandler) OnMessage(ctx context.Context, evt *event.Event) {
	logger := h.logger.With(
		"event_id", evt.ID.String(),
		"type", evt.Type.String(),
		"sender", evt.Sender.String())

	logger.Debug("Processing event")

	// 为每个消息创建独立的 goroutine 进行并发处理
	go func() {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				logger.Error("Panic recovered in message handler",
					"panic", r,
					"stack_trace", string(stack))
			}
		}()

		// 创建独立上下文，带有 5 分钟超时
		msgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// 复制 SyncTokenContextKey 到新上下文（如果存在）
		if token := ctx.Value(mautrix.SyncTokenContextKey); token != nil {
			msgCtx = context.WithValue(msgCtx, mautrix.SyncTokenContextKey, token)
		}

		if err := h.service.HandleEvent(msgCtx, evt); err != nil {
			logger.Error("Event handling failed", "error", err)
		}
	}()
}

// OnMember 处理成员事件（包括邀请）。
func (h *EventHandler) OnMember(ctx context.Context, evt *event.Event) {
	logger := h.logger.With(
		"event_id", evt.ID.String(),
		"room", evt.RoomID.String(),
		"sender", evt.Sender.String())

	// 解析成员事件内容
	content, ok := evt.Content.Parsed.(*event.MemberEventContent)
	if !ok {
		logger.Debug("无法解析成员事件内容")
		return
	}

	// 只处理邀请事件
	if content.Membership != event.MembershipInvite {
		logger.Debug("忽略非邀请成员事件", "membership", content.Membership)
		return
	}

	// 检查是否邀请机器人自己
	if evt.StateKey == nil {
		logger.Debug("成员事件没有 state key")
		return
	}

	targetUserID := id.UserID(*evt.StateKey)
	if targetUserID != h.service.botID {
		logger.Debug("邀请目标不是本机器人", "target", targetUserID)
		return
	}

	// 接受邀请
	logger.Info("接受房间邀请", "inviter", evt.Sender.String())
	_, err := h.service.client.JoinRoom(ctx, evt.RoomID.String(), nil)
	if err != nil {
		logger.Error("接受邀请失败", "error", err)
		return
	}

	logger.Info("成功接受邀请")
}

// OnEvent 是通用事件处理器，分发到适当的处理器。
func (h *EventHandler) OnEvent(ctx context.Context, evt *event.Event) {
	switch evt.Type {
	case event.EventMessage:
		h.OnMessage(ctx, evt)
	case event.StateMember:
		h.OnMember(ctx, evt)
	default:
		h.logger.Debug("Ignoring non-message event", "type", evt.Type.String())
	}
}

// Service 返回底层的 CommandService。
func (h *EventHandler) Service() *CommandService {
	return h.service
}

// 内置命令

// PingCommand 响应 "Pong!"。
type PingCommand struct {
	service *CommandService
}

// NewPingCommand 创建一个新的 ping 命令处理器。
func NewPingCommand(service *CommandService) *PingCommand {
	return &PingCommand{service: service}
}

// Handle 实现 CommandHandler。
func (c *PingCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	html := "<strong>🏓 Pong!</strong>"
	plain := "🏓 Pong!"
	return c.service.SendFormattedText(ctx, roomID, html, plain)
}

// HelpCommand 列出可用命令。
type HelpCommand struct {
	service *CommandService
}

// NewHelpCommand 创建一个新的帮助命令处理器。
func NewHelpCommand(service *CommandService) *HelpCommand {
	return &HelpCommand{service: service}
}

// Handle 实现 CommandHandler，生成 HTML 表格格式的帮助信息。
func (c *HelpCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	commands := c.service.ListCommands()

	if len(commands) == 0 {
		return c.service.SendText(ctx, roomID, "暂无可用命令")
	}

	var htmlBuilder strings.Builder
	htmlBuilder.WriteString("<table>")
	htmlBuilder.WriteString("<thead><tr><th>命令</th><th>描述</th></tr></thead>")
	htmlBuilder.WriteString("<tbody>")
	for _, cmd := range commands {
		desc := cmd.Description
		if desc == "" {
			desc = "-"
		}
		fmt.Fprintf(&htmlBuilder, "<tr><td><code>!%s</code></td><td>%s</td></tr>",
			cmd.Name, desc)
	}
	htmlBuilder.WriteString("</tbody></table>")

	var plainBuilder strings.Builder
	plainBuilder.WriteString("可用命令：\n\n")
	for _, cmd := range commands {
		fmt.Fprintf(&plainBuilder, "  !%s", cmd.Name)
		if cmd.Description != "" {
			fmt.Fprintf(&plainBuilder, " - %s", cmd.Description)
		}
		plainBuilder.WriteString("\n")
	}

	return c.service.SendFormattedText(ctx, roomID, htmlBuilder.String(), plainBuilder.String())
}

// RegisterBuiltinCommands 注册默认命令（!ping, !help）。
func RegisterBuiltinCommands(service *CommandService) {
	service.RegisterCommandWithDesc("ping", "检查机器人是否在线", NewPingCommand(service))
	service.RegisterCommandWithDesc("help", "列出可用命令", NewHelpCommand(service))
}
