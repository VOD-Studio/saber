package ai

import (
	"context"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/id"
)

// ModelsCommand 处理列出所有可用模型的命令。
type ModelsCommand struct {
	service *Service
}

// NewModelsCommand 创建一个新的模型列表命令处理器。
//
// 参数:
//   - service: AI 服务实例
//
// 返回值:
//   - *ModelsCommand: 创建的命令处理器
func NewModelsCommand(service *Service) *ModelsCommand {
	return &ModelsCommand{service: service}
}

// Handle 处理列出所有可用模型命令。
//
// 参数:
//   - ctx: 上下文
//   - userID: 发送命令的用户 ID
//   - roomID: 命令所在的房间 ID
//   - args: 命令参数（未使用）
//
// 返回值:
//   - error: 处理过程中发生的错误
func (c *ModelsCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	registry := c.service.GetModelRegistry()
	models := registry.ListModels()
	currentDefault := registry.GetDefault()
	configDefault := registry.GetConfigDefault()

	if len(models) == 0 {
		return c.service.replyCommand(ctx, userID, roomID, "!ai models", "没有配置任何模型")
	}

	var rows strings.Builder

	for _, m := range models {
		status := ""
		if m.ID == currentDefault {
			status = "⭐ 当前默认"
		}
		if m.IsConfigDefault && m.ID != currentDefault {
			status = "📝 配置默认"
		}

		fmt.Fprintf(&rows, "| `%s` | `%s` | %s |\n", m.ID, m.Model, status)
	}

	return c.service.replyCommand(ctx, userID, roomID, "!ai models", fmt.Sprintf(
		"**📋 可用模型列表**（共 %d 个）\n\n| 模型 ID | 实际模型 | 状态 |\n| --- | --- | --- |\n%s\n配置默认：`%s`",
		len(models), rows.String(), configDefault,
	))
}

// SwitchModelCommand 处理切换默认模型的命令。
type SwitchModelCommand struct {
	service *Service
}

// NewSwitchModelCommand 创建一个新的模型切换命令处理器。
//
// 参数:
//   - service: AI 服务实例
//
// 返回值:
//   - *SwitchModelCommand: 创建的命令处理器
func NewSwitchModelCommand(service *Service) *SwitchModelCommand {
	return &SwitchModelCommand{service: service}
}

// Handle 处理切换默认模型命令。
//
// 参数:
//   - ctx: 上下文
//   - userID: 发送命令的用户 ID
//   - roomID: 命令所在的房间 ID
//   - args: 命令参数（第一个参数为模型 ID）
//
// 返回值:
//   - error: 处理过程中发生的错误
func (c *SwitchModelCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	registry := c.service.GetModelRegistry()

	if len(args) == 0 || args[0] == "" {
		return c.service.replyCommand(ctx, userID, roomID, "!ai switch", "**❌ 请指定模型 ID**\n用法：`!ai switch <model-id>`")
	}

	modelID := args[0]
	oldDefault := registry.GetDefault()

	if err := registry.SetDefault(modelID); err != nil {
		return c.service.replyCommand(ctx, userID, roomID, "!ai switch", "**❌ 切换模型失败：** "+err.Error())
	}

	newDefault := registry.GetDefault()
	return c.service.replyCommand(ctx, userID, roomID, "!ai switch", fmt.Sprintf(
		"**✅ 默认模型已切换**\n- 原模型：`%s`\n- 新模型：`%s`\n\n*注意：重启后将恢复配置文件中的默认模型*",
		oldDefault, newDefault,
	))
}

// CurrentModelCommand 处理显示当前默认模型的命令。
type CurrentModelCommand struct {
	service *Service
}

// NewCurrentModelCommand 创建一个新的当前模型查询命令处理器。
//
// 参数:
//   - service: AI 服务实例
//
// 返回值:
//   - *CurrentModelCommand: 创建的命令处理器
func NewCurrentModelCommand(service *Service) *CurrentModelCommand {
	return &CurrentModelCommand{service: service}
}

// Handle 处理显示当前默认模型命令。
//
// 参数:
//   - ctx: 上下文
//   - userID: 发送命令的用户 ID
//   - roomID: 命令所在的房间 ID
//   - args: 命令参数（未使用）
//
// 返回值:
//   - error: 处理过程中发生的错误
func (c *CurrentModelCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	registry := c.service.GetModelRegistry()
	currentDefault := registry.GetDefault()
	configDefault := registry.GetConfigDefault()

	isModified := currentDefault != configDefault

	var statusText string
	if isModified {
		statusText = "（已修改，重启后恢复）"
	}

	return c.service.replyCommand(ctx, userID, roomID, "!ai current", fmt.Sprintf(
		"**🤖 当前默认模型**\n- 当前模型：`%s`\n- 配置默认：`%s`",
		currentDefault+statusText, configDefault,
	))
}
