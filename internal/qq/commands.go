// Package qq 提供 QQ 机器人的适配器实现。
package qq

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// BuildInfo 存储构建信息。
//
// 包含版本号、Git 提交信息、构建时间等元数据，用于 !version 命令。
type BuildInfo struct {
	// Version 是语义化版本号，如 "1.0.0"。
	Version string
	// GitCommit 是 Git 提交哈希（短格式）。
	GitCommit string
	// GitBranch 是 Git 分支名。
	GitBranch string
	// BuildTime 是构建时间（RFC3339 格式）。
	BuildTime string
	// GoVersion 是 Go 版本号。
	GoVersion string
	// BuildPlatform 是构建平台，如 "darwin/arm64"。
	BuildPlatform string
}

// CommandSender 定义命令消息发送接口。
//
// 该接口抽象了消息发送操作，使命令处理器不依赖具体的 QQ API。
// 支持私聊和群聊两种场景。
type CommandSender interface {
	// Send 发送消息给指定用户或群。
	//
	// 参数:
	//   - ctx: 上下文，用于取消操作
	//   - userID: 用户 ID（私聊时使用）
	//   - groupID: 群 ID（群聊时使用，私聊为空）
	//   - message: 消息内容
	//
	// 返回值:
	//   - error: 发送失败时的错误
	Send(ctx context.Context, userID, groupID, message string) error
}

// CommandHandler 定义命令处理器接口。
//
// 每个命令实现该接口，处理特定的命令逻辑。
type CommandHandler interface {
	// Handle 处理命令。
	//
	// 参数:
	//   - ctx: 上下文，用于取消操作
	//   - userID: 发送命令的用户 ID
	//   - groupID: 群 ID（私聊为空）
	//   - args: 命令参数（不含命令名本身）
	//   - sender: 消息发送器，用于回复
	//
	// 返回值:
	//   - error: 处理失败时的错误
	Handle(ctx context.Context, userID, groupID string, args []string, sender CommandSender) error
}

// commandEntry 存储命令注册信息。
type commandEntry struct {
	handler  CommandHandler
	helpText string
}

// CommandRegistry 是命令注册表。
//
// 管理命令的注册、解析和分发。线程安全。
type CommandRegistry struct {
	mu       sync.RWMutex
	commands map[string]commandEntry
}

// NewCommandRegistry 创建一个新的命令注册表。
//
// 返回值:
//   - *CommandRegistry: 新创建的命令注册表实例
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{
		commands: make(map[string]commandEntry),
	}
}

// Register 注册一个命令。
//
// 参数:
//   - name: 命令名（不含 ! 前缀）
//   - handler: 命令处理器
//   - helpText: 帮助文本，显示在 !help 命令中
func (r *CommandRegistry) Register(name string, handler CommandHandler, helpText string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands[name] = commandEntry{
		handler:  handler,
		helpText: helpText,
	}
}

// ParsedCommand 表示解析后的命令。
type ParsedCommand struct {
	// Name 是命令名（不含 ! 前缀）。
	Name string
	// Args 是命令参数。
	Args []string
}

// Parse 解析消息内容，提取命令。
//
// 支持格式: !command arg1 arg2 ...
//
// 参数:
//   - body: 消息内容
//
// 返回值:
//   - *ParsedCommand: 解析后的命令，如果不是命令则返回 nil
func (r *CommandRegistry) Parse(body string) *ParsedCommand {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "!") {
		return nil
	}

	// 去除 ! 前缀
	body = body[1:]
	if body == "" {
		return nil
	}

	// 分割命令和参数
	parts := strings.Fields(body)
	if len(parts) == 0 {
		return nil
	}

	return &ParsedCommand{
		Name: parts[0],
		Args: parts[1:],
	}
}

// Dispatch 分发命令到对应的处理器。
//
// 参数:
//   - ctx: 上下文
//   - userID: 用户 ID
//   - groupID: 群 ID
//   - parsed: 解析后的命令
//   - sender: 消息发送器
//
// 返回值:
//   - bool: 是否找到并执行了命令
//   - error: 执行过程中的错误
func (r *CommandRegistry) Dispatch(ctx context.Context, userID, groupID string, parsed *ParsedCommand, sender CommandSender) (bool, error) {
	r.mu.RLock()
	entry, ok := r.commands[parsed.Name]
	r.mu.RUnlock()

	if !ok {
		return false, nil
	}

	err := entry.handler.Handle(ctx, userID, groupID, parsed.Args, sender)
	return true, err
}

// GetHelpText 获取所有命令的帮助文本。
//
// 返回值:
//   - string: 格式化的帮助文本
func (r *CommandRegistry) GetHelpText() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var lines []string
	var names []string
	for name := range r.commands {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		entry := r.commands[name]
		lines = append(lines, fmt.Sprintf("!%s - %s", name, entry.helpText))
	}

	return "可用命令:\n" + strings.Join(lines, "\n")
}

// --- 内置命令实现 ---

// PingCommand 处理 !ping 命令。
//
// 用于测试机器人是否在线，回复 "pong"。
type PingCommand struct{}

// Handle 实现 CommandHandler 接口。
//
// 发送 "pong" 作为回复。
func (c *PingCommand) Handle(ctx context.Context, userID, groupID string, args []string, sender CommandSender) error {
	return sender.Send(ctx, userID, groupID, "pong")
}

// HelpCommand 处理 !help 命令。
//
// 显示所有可用命令的帮助信息。
type HelpCommand struct {
	registry *CommandRegistry
}

// Handle 实现 CommandHandler 接口。
//
// 从注册表获取所有命令的帮助文本并发送。
func (c *HelpCommand) Handle(ctx context.Context, userID, groupID string, args []string, sender CommandSender) error {
	return sender.Send(ctx, userID, groupID, c.registry.GetHelpText())
}

// VersionCommand 处理 !version 命令。
//
// 显示机器人的版本信息和构建详情。
type VersionCommand struct {
	buildInfo *BuildInfo
}

// Handle 实现 CommandHandler 接口。
//
// 格式化输出版本信息，包括 Git 提交、分支、构建时间等。
func (c *VersionCommand) Handle(ctx context.Context, userID, groupID string, args []string, sender CommandSender) error {
	info := c.buildInfo
	msg := fmt.Sprintf("Saber %s\nCommit: %s\nBranch: %s\nBuilt: %s\nGo: %s\nPlatform: %s",
		info.Version, info.GitCommit, info.GitBranch, info.BuildTime, info.GoVersion, info.BuildPlatform)
	return sender.Send(ctx, userID, groupID, msg)
}