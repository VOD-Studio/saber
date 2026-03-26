// Package ai 提供 AI 服务相关功能，包括对话管理、流式响应和工具调用。
package ai

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/id"
)

// AICommand 处理默认的 AI 聊天命令。
type AICommand struct {
	service *Service
}

// NewAICommand 创建一个新的 AI 命令处理器。
//
// 参数:
//   - service: AI 服务实例
//
// 返回值:
//   - *AICommand: 创建的命令处理器
func NewAICommand(service *Service) *AICommand {
	return &AICommand{service: service}
}

// Handle 处理 AI 聊天命令。
//
// 参数:
//   - ctx: 上下文
//   - userID: 发送命令的用户 ID
//   - roomID: 命令所在的房间 ID
//   - args: 命令参数
//
// 返回值:
//   - error: 处理过程中发生的错误
func (c *AICommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	return c.service.handleAICommand(ctx, userID, roomID, c.service.GetModelRegistry().GetDefault(), args)
}

// MultiModelAICommand 处理指定模型的 AI 聊天命令。
type MultiModelAICommand struct {
	service   *Service
	modelName string
}

// NewMultiModelAICommand 创建一个新的多模型 AI 命令处理器。
//
// 参数:
//   - service: AI 服务实例
//   - modelName: 要使用的 AI 模型名称
//
// 返回值:
//   - *MultiModelAICommand: 创建的命令处理器
func NewMultiModelAICommand(service *Service, modelName string) *MultiModelAICommand {
	return &MultiModelAICommand{
		service:   service,
		modelName: modelName,
	}
}

// Handle 处理指定模型的 AI 聊天命令。
//
// 参数:
//   - ctx: 上下文
//   - userID: 发送命令的用户 ID
//   - roomID: 命令所在的房间 ID
//   - args: 命令参数
//
// 返回值:
//   - error: 处理过程中发生的错误
func (c *MultiModelAICommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	return c.service.handleAICommand(ctx, userID, roomID, c.modelName, args)
}

// ClearContextCommand 处理清除对话上下文的命令。
type ClearContextCommand struct {
	service *Service
}

// NewClearContextCommand 创建一个新的清除上下文命令处理器。
//
// 参数:
//   - service: AI 服务实例
//
// 返回值:
//   - *ClearContextCommand: 创建的命令处理器
func NewClearContextCommand(service *Service) *ClearContextCommand {
	return &ClearContextCommand{service: service}
}

// Handle 处理清除对话上下文命令。
//
// 参数:
//   - ctx: 上下文
//   - userID: 发送命令的用户 ID
//   - roomID: 命令所在的房间 ID
//   - args: 命令参数（未使用）
//
// 返回值:
//   - error: 处理过程中发生的错误
func (c *ClearContextCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if c.service.contextManager == nil {
		return c.service.matrixService.SendText(ctx, roomID, "上下文管理未启用")
	}

	c.service.contextManager.ClearContext(roomID)

	html := "<strong>✅ 对话上下文已清除</strong>"
	plain := "✅ 对话上下文已清除"
	return c.service.matrixService.SendFormattedText(ctx, roomID, html, plain)
}

// ContextInfoCommand 处理查询对话上下文信息的命令。
type ContextInfoCommand struct {
	service *Service
}

// NewContextInfoCommand 创建一个新的上下文信息查询命令处理器。
//
// 参数:
//   - service: AI 服务实例
//
// 返回值:
//   - *ContextInfoCommand: 创建的命令处理器
func NewContextInfoCommand(service *Service) *ContextInfoCommand {
	return &ContextInfoCommand{service: service}
}

// Handle 处理查询对话上下文信息命令。
//
// 参数:
//   - ctx: 上下文
//   - userID: 发送命令的用户 ID
//   - roomID: 命令所在的房间 ID
//   - args: 命令参数（未使用）
//
// 返回值:
//   - error: 处理过程中发生的错误
func (c *ContextInfoCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if c.service.contextManager == nil {
		return c.service.matrixService.SendText(ctx, roomID, "上下文管理未启用")
	}

	msgCount, tokenCount := c.service.contextManager.GetContextSize(roomID)

	html := `<table>
<thead><tr><th colspan="2">📊 对话上下文信息</th></tr></thead>
<tbody>
<tr><td>消息数量</td><td><strong>%d</strong></td></tr>
<tr><td>估算令牌数</td><td><strong>%d</strong></td></tr>
</tbody></table>`
	html = fmt.Sprintf(html, msgCount, tokenCount)

	plain := fmt.Sprintf("📊 对话上下文信息\n- 消息数量：%d\n- 估算令牌数：%d", msgCount, tokenCount)

	return c.service.matrixService.SendFormattedText(ctx, roomID, html, plain)
}
