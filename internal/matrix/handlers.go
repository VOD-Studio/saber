// Package matrix 提供 Matrix 事件处理和命令处理功能。
package matrix

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/mcp"
)

// BuildInfo 包含构建时的版本信息。
type BuildInfo struct {
	Version       string
	GitCommit     string
	GitBranch     string
	BuildTime     string
	GoVersion     string
	BuildPlatform string
}

// RuntimePlatform 返回运行时平台信息 (GOOS/GOARCH)。
func (b *BuildInfo) RuntimePlatform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

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
	mentionAI      CommandHandler
	replyAI        CommandHandler
	mentionService *MentionService
	buildInfo      *BuildInfo
}

// NewCommandService 创建一个新的命令服务。
func NewCommandService(client *mautrix.Client, botID id.UserID, info *BuildInfo) *CommandService {
	return &CommandService{
		commands:  make(map[string]CommandInfo),
		client:    client,
		botID:     botID,
		buildInfo: info,
	}
}

// GetBuildInfo 返回构建信息。
func (s *CommandService) GetBuildInfo() *BuildInfo {
	return s.buildInfo
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

// ParsedCommand 表示从消息中解析的命令。
type ParsedCommand struct {
	Command string
	Args    []string
}

func (s *CommandService) parsePrefixedCommand(body string) *ParsedCommand {
	parts := splitArgs(body)
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
	parts := splitArgs(body)
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

func splitArgs(s string) []string {
	var args []string
	var current string
	inQuote := false

	for _, r := range s {
		switch r {
		case ' ', '\t':
			if inQuote {
				current += string(r)
			} else if current != "" {
				args = append(args, current)
				current = ""
			}
		case '"':
			inQuote = !inQuote
		default:
			current += string(r)
		}
	}

	if current != "" {
		args = append(args, current)
	}

	return args
}

// handleDirectChat 处理私聊消息自动回复。
func (s *CommandService) handleDirectChat(ctx context.Context, sender id.UserID, roomID id.RoomID, body string) (handled bool, err error) {
	if s.directChatAI == nil || !s.isDirectChat(ctx, roomID) {
		return false, nil
	}

	slog.Info("Direct chat auto-reply triggered",
		"sender", sender.String(),
		"room", roomID.String())

	args := []string{body}
	if err := s.directChatAI.Handle(ctx, sender, roomID, args); err != nil {
		slog.Error("Direct chat AI handler failed",
			"sender", sender.String(),
			"room", roomID.String(),
			"error", err)
		return true, s.reportError(ctx, roomID, "ai", err)
	}

	return true, nil
}

// handleReply 处理回复消息。
func (s *CommandService) handleReply(ctx context.Context, sender id.UserID, roomID id.RoomID, content *event.MessageEventContent) (handled bool, err error) {
	if s.replyAI == nil || content.RelatesTo == nil || content.RelatesTo.GetReplyTo() == "" {
		return false, nil
	}

	replyToEventID := content.RelatesTo.GetReplyTo()
	slog.Debug("检测到回复消息", "reply_to", replyToEventID.String())

	if !s.isReplyToBot(ctx, roomID, replyToEventID) {
		return false, nil
	}

	cleanedBody := event.TrimReplyFallbackText(content.Body)

	replyContext := ""
	if evt, err := s.client.GetEvent(ctx, roomID, replyToEventID); err == nil {
		if evt.Content.Parsed == nil {
			if parseErr := evt.Content.ParseRaw(event.EventMessage); parseErr != nil {
				slog.Debug("解析被引用消息内容失败", "error", parseErr)
			}
		}
		if msgContent, ok := evt.Content.Parsed.(*event.MessageEventContent); ok {
			if msgContent.MsgType == event.MsgImage {
				mediaInfo := ExtractMediaInfo(msgContent)
				if mediaInfo != nil {
					ctx = WithReferencedMediaInfo(ctx, mediaInfo)
					slog.Debug("检测到引用图片消息",
						"reply_to", replyToEventID.String(),
						"media_type", mediaInfo.Type,
						"mime_type", mediaInfo.MimeType)
				}
			}
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
		return true, s.reportError(ctx, roomID, "ai", err)
	}

	return true, nil
}

// handleGroupMention 处理群聊中的提及消息。
func (s *CommandService) handleGroupMention(ctx context.Context, sender id.UserID, roomID id.RoomID, content *event.MessageEventContent) (handled bool, err error) {
	if s.mentionAI == nil || s.mentionService == nil || s.isDirectChat(ctx, roomID) {
		return false, nil
	}

	msg, ok := s.mentionService.ParseMention(content.Body, content)
	if !ok {
		return false, nil
	}

	slog.Info("群聊 mention 触发 AI 回复",
		"sender", sender.String(),
		"room", roomID.String(),
		"message", msg)

	args := []string{msg}
	if err := s.mentionAI.Handle(ctx, sender, roomID, args); err != nil {
		slog.Error("群聊 mention 处理失败", "error", err)
		return true, s.reportError(ctx, roomID, "ai", err)
	}

	return true, nil
}

// handleCommand 处理命令消息。
func (s *CommandService) handleCommand(ctx context.Context, sender id.UserID, roomID id.RoomID, parsed *ParsedCommand) (handled bool, err error) {
	cmdInfo, ok := s.GetCommand(parsed.Command)
	if !ok {
		slog.Debug("Unknown command", "command", parsed.Command)
		return false, nil
	}

	slog.Info("Executing command",
		"command", parsed.Command,
		"sender", sender.String(),
		"room", roomID.String(),
		"args", parsed.Args)

	if err := cmdInfo.Handler.Handle(ctx, sender, roomID, parsed.Args); err != nil {
		slog.Error("Command execution failed",
			"command", parsed.Command,
			"sender", sender.String(),
			"error", err)
		return true, s.reportError(ctx, roomID, parsed.Command, err)
	}

	return true, nil
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

	if content.MsgType.IsMedia() {
		mediaInfo := ExtractMediaInfo(content)
		if mediaInfo != nil {
			slog.Info("收到媒体消息",
				"type", mediaInfo.Type,
				"sender", sender.String(),
				"room", roomID.String())
			ctx = WithMediaInfo(ctx, mediaInfo)
		}
	}

	// 解析命令
	parsed := s.ParseCommand(content.Body)

	// 命令处理
	if parsed != nil {
		_, err := s.handleCommand(ctx, sender, roomID, parsed)
		return err
	}

	// 私聊自动回复
	if handled, err := s.handleDirectChat(ctx, sender, roomID, content.Body); handled {
		return err
	}

	// 回复消息处理（优先于 mention 检测，避免回复引用中的 mention 误触发）
	if handled, err := s.handleReply(ctx, sender, roomID, content); handled {
		return err
	}

	// 群聊 mention 响应
	if handled, err := s.handleGroupMention(ctx, sender, roomID, content); handled {
		return err
	}

	return nil
}

// reportError 报告命令执行错误。
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
// 如果 context 中包含 EventID，则使用引用回复。
func (s *CommandService) SendText(ctx context.Context, roomID id.RoomID, body string) error {
	if eventID := GetEventID(ctx); eventID != "" {
		_, err := s.SendReply(ctx, roomID, body, eventID)
		return err
	}

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
// 如果 context 中包含 EventID，则使用引用回复。
func (s *CommandService) SendFormattedText(ctx context.Context, roomID id.RoomID, html, plain string) error {
	if eventID := GetEventID(ctx); eventID != "" {
		_, err := s.SendFormattedReply(ctx, roomID, html, plain, eventID)
		return err
	}

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

// SendFormattedReply 发送格式化回复消息。
func (s *CommandService) SendFormattedReply(ctx context.Context, roomID id.RoomID, html, plain string, replyTo id.EventID) (id.EventID, error) {
	relatesTo := &event.RelatesTo{
		InReplyTo: &event.InReplyTo{
			EventID: replyTo,
		},
	}

	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          plain,
		Format:        event.FormatHTML,
		FormattedBody: html,
		RelatesTo:     relatesTo,
	}

	senderID := id.UserID("")
	originalMsg := ""
	if evt, err := s.client.GetEvent(ctx, roomID, replyTo); err == nil {
		senderID = evt.Sender
		if msgContent, ok := evt.Content.Parsed.(*event.MessageEventContent); ok {
			originalMsg = msgContent.Body
		}
	} else {
		slog.Debug("Failed to get original event for reply fallback",
			"room", roomID.String(),
			"event_id", replyTo.String(),
			"error", err)
		senderID = id.UserID(replyTo.String())
	}

	content.Body = CreateReplyFallback(senderID, originalMsg, plain)

	resp, err := s.client.SendMessageEvent(
		ctx,
		roomID,
		event.EventMessage,
		content,
	)
	if err != nil {
		slog.Error("Failed to send formatted reply",
			"room", roomID.String(),
			"error", err)
		return "", err
	}

	return resp.EventID, nil
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
			senderID := id.UserID("")
			originalMsg := ""

			if evt, err := s.client.GetEvent(ctx, roomID, relatesTo.InReplyTo.EventID); err == nil {
				senderID = evt.Sender
				if msgContent, ok := evt.Content.Parsed.(*event.MessageEventContent); ok {
					originalMsg = msgContent.Body
				}
			} else {
				slog.Debug("Failed to get original event for reply fallback",
					"room", roomID.String(),
					"event_id", relatesTo.InReplyTo.EventID.String(),
					"error", err)
				senderID = id.UserID(relatesTo.InReplyTo.EventID.String())
			}

			content.Body = CreateReplyFallback(senderID, originalMsg, body)
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
func (s *CommandService) StopTyping(ctx context.Context, roomID id.RoomID) error {
	slog.Debug("Stopping typing indicator", "room", roomID.String())

	_, err := s.client.UserTyping(ctx, roomID, false, 0)
	if err != nil {
		slog.Error("Failed to stop typing indicator", "room", roomID.String(), "error", err)
		return fmt.Errorf("failed to stop typing: %w", err)
	}

	return nil
}

// htmlPolicy 是 HTML 净化策略。
var htmlPolicy = bluemonday.UGCPolicy()

// SanitizeHTML 净化 HTML 内容，移除危险标签和属性。
func SanitizeHTML(html string) string {
	return htmlPolicy.Sanitize(html)
}

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

// Handle 实现 CommandHandler。
func (c *HelpCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	commands := c.service.ListCommands()

	if len(commands) == 0 {
		return c.service.SendText(ctx, roomID, "暂无可用命令")
	}

	var result string
	for _, cmd := range commands {
		result += fmt.Sprintf("!%s - %s\n", cmd.Name, cmd.Description)
	}

	return c.service.SendText(ctx, roomID, "可用命令：\n"+result)
}

// VersionCommand 显示构建版本信息。
type VersionCommand struct {
	service *CommandService
}

// NewVersionCommand 创建一个新的版本命令处理器。
func NewVersionCommand(service *CommandService) *VersionCommand {
	return &VersionCommand{service: service}
}

// Handle 实现 CommandHandler。
func (c *VersionCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	info := c.service.GetBuildInfo()
	if info == nil {
		return c.service.SendText(ctx, roomID, "版本信息不可用")
	}

	msg := fmt.Sprintf("版本: %s\n提交: %s\n分支: %s\n构建时间: %s",
		info.Version, info.GitCommit, info.GitBranch, info.BuildTime)
	return c.service.SendText(ctx, roomID, msg)
}

// RegisterBuiltinCommands 注册默认命令。
func RegisterBuiltinCommands(service *CommandService) {
	service.RegisterCommandWithDesc("ping", "检查机器人是否在线", NewPingCommand(service))
	service.RegisterCommandWithDesc("help", "列出可用命令", NewHelpCommand(service))
	service.RegisterCommandWithDesc("version", "显示版本信息", NewVersionCommand(service))
}

// MCPListCommand 列出所有可用的 MCP 服务器和工具。
type MCPListCommand struct {
	service *CommandService
	mcp     *mcp.Manager
}

// NewMCPListCommand 创建一个新的 MCP 列表命令处理器。
func NewMCPListCommand(service *CommandService, mcpMgr *mcp.Manager) *MCPListCommand {
	return &MCPListCommand{service: service, mcp: mcpMgr}
}

// Handle 实现 CommandHandler。
func (c *MCPListCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if c.mcp == nil || !c.mcp.IsEnabled() {
		return c.service.SendText(ctx, roomID, "MCP 功能未启用")
	}

	servers := c.mcp.ListServers()
	var result string
	for _, srv := range servers {
		result += fmt.Sprintf("- %s (%s)\n", srv.Name, srv.Type)
	}

	return c.service.SendText(ctx, roomID, "MCP 服务器：\n"+result)
}

// RegisterMCPCommands 注册 MCP 相关命令。
func RegisterMCPCommands(service *CommandService, mcpMgr *mcp.Manager) {
	if mcpMgr != nil {
		service.RegisterCommandWithDesc("mcp-list", "列出所有 MCP 服务器和工具", NewMCPListCommand(service, mcpMgr))
	}
}
