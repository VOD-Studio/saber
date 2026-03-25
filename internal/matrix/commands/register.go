// Package commands 提供 Matrix 机器人的命令注册和处理机制。
package commands

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"maunium.net/go/mautrix"
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

// Registry 管理命令注册和分发。
type Registry struct {
	mu       sync.RWMutex
	commands map[string]CommandInfo
	client   *mautrix.Client
	botID    id.UserID
}

// NewRegistry 创建一个新的命令注册表。
func NewRegistry(client *mautrix.Client, botID id.UserID) *Registry {
	return &Registry{
		commands: make(map[string]CommandInfo),
		client:   client,
		botID:    botID,
	}
}

// Register 注册一个不带描述的命令处理器。
// 命令名称不应包含前缀 (!)。
func (r *Registry) Register(cmd string, handler CommandHandler) {
	r.RegisterWithDesc(cmd, "", handler)
}

// RegisterWithDesc 注册带有描述的命令处理器。
// 命令名称不应包含前缀 (!)。
func (r *Registry) RegisterWithDesc(cmd, desc string, handler CommandHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.commands[strings.ToLower(cmd)] = CommandInfo{
		Name:        cmd,
		Description: desc,
		Handler:     handler,
	}

	slog.Debug("Registered command",
		"command", cmd,
		"description", desc)
}

// Unregister 从注册表中移除一个命令。
func (r *Registry) Unregister(cmd string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.commands, strings.ToLower(cmd))
}

// Get 按名称检索命令信息。
func (r *Registry) Get(cmd string) (CommandInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, ok := r.commands[strings.ToLower(cmd)]
	return info, ok
}

// List 返回所有已注册的命令。
func (r *Registry) List() []CommandInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]CommandInfo, 0, len(r.commands))
	for _, info := range r.commands {
		list = append(list, info)
	}
	return list
}

// Client 返回 Matrix 客户端。
func (r *Registry) Client() *mautrix.Client {
	return r.client
}

// BotID 返回机器人的用户 ID。
func (r *Registry) BotID() id.UserID {
	return r.botID
}

// ParsedCommand 表示从消息中解析的命令。
type ParsedCommand struct {
	Command string
	Args    []string
}

// Parse 从消息体中提取命令和参数。
// 支持基于前缀的命令 (!command args) 和提及 (@bot:command args)。
// 如果消息不是有效命令则返回 nil。
func (r *Registry) Parse(body string) *ParsedCommand {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}

	// 检查基于前缀的命令 (!command)
	if strings.HasPrefix(body, "!") {
		return r.parsePrefixed(body[1:])
	}

	// 检查基于提及的命令 (@bot:command)
	if strings.HasPrefix(body, "@") {
		return r.parseMention(body)
	}

	return nil
}

func (r *Registry) parsePrefixed(body string) *ParsedCommand {
	parts := strings.Fields(body)
	if len(parts) == 0 {
		return nil
	}

	return &ParsedCommand{
		Command: strings.ToLower(parts[0]),
		Args:    parts[1:],
	}
}

func (r *Registry) parseMention(body string) *ParsedCommand {
	// 格式: @bot:server.com command args
	// 或: @bot:server.com: command args
	parts := strings.Fields(body)
	if len(parts) == 0 {
		return nil
	}

	// 第一部分应该是提及
	mention := parts[0]

	// 验证是否是对本机器人的提及
	expectedMention := string(r.botID)
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
