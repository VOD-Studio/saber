// Package ai 提供 AI 服务相关功能，包括对话管理、流式响应和工具调用。
package ai

import (
	"context"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/id"
)

// CommandHandler 定义命令处理器的接口。
// 与 matrix.CommandHandler 保持一致，便于类型复用。
type CommandHandler interface {
	Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error
}

// AICommandRouter 提供 AI 命令的路由分发功能。
// 它将 "!ai <subcommand>" 格式的命令分发到对应的子命令处理器。
//
// 支持的子命令:
//   - clear: 清除对话上下文
//   - context: 显示上下文信息
//   - models: 列出所有可用模型
//   - switch: 切换默认模型
//   - current: 显示当前默认模型
//
// 使用示例:
//
//	!ai clear          # 清除上下文
//	!ai models         # 列出模型
//	!ai switch gpt-4   # 切换模型
type AICommandRouter struct {
	service     *Service                  // AI 服务实例
	subcommands map[string]CommandHandler // 子命令映射表
}

// NewAICommandRouter 创建一个新的 AI 命令路由器。
//
// 参数:
//   - service: AI 服务实例
//
// 返回值:
//   - *AICommandRouter: 创建的路由器实例
func NewAICommandRouter(service *Service) *AICommandRouter {
	return &AICommandRouter{
		service:     service,
		subcommands: make(map[string]CommandHandler),
	}
}

// RegisterSubcommand 注册一个子命令处理器。
//
// 参数:
//   - name: 子命令名称（如 "clear", "models"）
//   - handler: 命令处理器实例
func (r *AICommandRouter) RegisterSubcommand(name string, handler CommandHandler) {
	r.subcommands[strings.ToLower(name)] = handler
}

// Handle 处理 AI 命令，根据子命令名分发到对应处理器。
//
// 参数:
//   - ctx: 上下文，用于取消和超时控制
//   - userID: 发送命令的用户 Matrix ID
//   - roomID: 发送命令的房间 Matrix ID
//   - args: 命令参数，第一个元素为子命令名
//
// 返回值:
//   - error: 处理过程中的错误，nil 表示成功
func (r *AICommandRouter) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	// 无参数时，将消息作为 AI 对话内容处理
	if len(args) == 0 {
		return r.service.handleAICommand(ctx, userID, roomID, r.service.modelRegistry.GetDefault(), args)
	}

	// 检查是否为已知子命令
	subcmd := strings.ToLower(args[0])
	handler, ok := r.subcommands[subcmd]
	if !ok {
		// 不是已知子命令，将所有参数作为 AI 对话内容
		return r.service.handleAICommand(ctx, userID, roomID, r.service.modelRegistry.GetDefault(), args)
	}

	// 分发到子命令处理器，传递剩余参数
	return handler.Handle(ctx, userID, roomID, args[1:])
}

// ListSubcommands 返回所有已注册的子命令名称。
//
// 返回值:
//   - []string: 子命令名称列表
func (r *AICommandRouter) ListSubcommands() []string {
	names := make([]string, 0, len(r.subcommands))
	for name := range r.subcommands {
		names = append(names, name)
	}
	return names
}

// UnknownSubcommandError 表示未知的子命令错误。
type UnknownSubcommandError struct {
	Subcommand string // 未知的子命令名称
}

// Error 实现 error 接口。
func (e *UnknownSubcommandError) Error() string {
	return fmt.Sprintf("未知子命令: %s", e.Subcommand)
}