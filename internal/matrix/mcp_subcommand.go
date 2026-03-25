// Package matrix 提供 Matrix 事件处理和命令处理功能。
package matrix

import (
	"context"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/mcp"
)

// MCPCommandRouter 提供 MCP 命令的路由分发功能。
// 它将 "!mcp <subcommand>" 格式的命令分发到对应的子命令处理器。
//
// 支持的子命令:
//   - list: 列出所有 MCP 服务器和工具
//
// 使用示例:
//
//	!mcp list    # 列出 MCP 服务器
type MCPCommandRouter struct {
	service     *CommandService            // 命令服务实例
	mcpMgr      *mcp.Manager               // MCP 管理器实例
	subcommands map[string]CommandHandler  // 子命令映射表
}

// NewMCPCommandRouter 创建一个新的 MCP 命令路由器。
//
// 参数:
//   - service: 命令服务实例
//   - mcpMgr: MCP 管理器实例
//
// 返回值:
//   - *MCPCommandRouter: 创建的路由器实例
func NewMCPCommandRouter(service *CommandService, mcpMgr *mcp.Manager) *MCPCommandRouter {
	return &MCPCommandRouter{
		service:     service,
		mcpMgr:      mcpMgr,
		subcommands: make(map[string]CommandHandler),
	}
}

// RegisterSubcommand 注册一个子命令处理器。
//
// 参数:
//   - name: 子命令名称（如 "list"）
//   - handler: 命令处理器实例
func (r *MCPCommandRouter) RegisterSubcommand(name string, handler CommandHandler) {
	r.subcommands[strings.ToLower(name)] = handler
}

// Handle 处理 MCP 命令，根据子命令名分发到对应处理器。
//
// 参数:
//   - ctx: 上下文，用于取消和超时控制
//   - userID: 发送命令的用户 Matrix ID
//   - roomID: 发送命令的房间 Matrix ID
//   - args: 命令参数，第一个元素为子命令名
//
// 返回值:
//   - error: 处理过程中的错误，nil 表示成功
func (r *MCPCommandRouter) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	// 无参数时显示帮助
	if len(args) == 0 {
		return r.showHelp(ctx, roomID)
	}

	// 检查是否为已知子命令
	subcmd := strings.ToLower(args[0])
	handler, ok := r.subcommands[subcmd]
	if !ok {
		return r.showUnknownSubcommand(ctx, roomID, subcmd)
	}

	// 分发到子命令处理器，传递剩余参数
	return handler.Handle(ctx, userID, roomID, args[1:])
}

// ListSubcommands 返回所有已注册的子命令名称。
//
// 返回值:
//   - []string: 子命令名称列表
func (r *MCPCommandRouter) ListSubcommands() []string {
	names := make([]string, 0, len(r.subcommands))
	for name := range r.subcommands {
		names = append(names, name)
	}
	return names
}

// showHelp 显示 MCP 命令的帮助信息。
//
// 参数:
//   - ctx: 上下文
//   - roomID: 发送命令的房间 Matrix ID
//
// 返回值:
//   - error: 发送消息过程中的错误
func (r *MCPCommandRouter) showHelp(ctx context.Context, roomID id.RoomID) error {
	help := `📦 MCP 命令帮助

用法: !mcp <子命令>

子命令:
  list    列出所有 MCP 服务器和工具

示例:
  !mcp list    # 列出 MCP 服务器`

	return r.service.SendText(ctx, roomID, help)
}

// showUnknownSubcommand 显示未知子命令的错误信息。
//
// 参数:
//   - ctx: 上下文
//   - roomID: 发送命令的房间 Matrix ID
//   - subcmd: 未知的子命令名称
//
// 返回值:
//   - error: 发送消息过程中的错误
func (r *MCPCommandRouter) showUnknownSubcommand(ctx context.Context, roomID id.RoomID, subcmd string) error {
	msg := fmt.Sprintf("❌ 未知子命令: %s\n\n使用 !mcp 查看可用子命令", subcmd)
	return r.service.SendText(ctx, roomID, msg)
}