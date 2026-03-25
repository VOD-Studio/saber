// Package commands 提供 Matrix 机器人的命令处理实现。
package commands

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/id"
)

// AIService 定义 AI 服务接口。
type AIService interface {
	// Chat 与 AI 进行对话。
	Chat(ctx context.Context, roomID id.RoomID, userID id.UserID, message string) (string, error)
	// ClearContext 清除对话上下文。
	ClearContext(roomID id.RoomID, userID id.UserID)
	// GetContextInfo 获取对话上下文信息。
	GetContextInfo(roomID id.RoomID, userID id.UserID) string
	// ListModels 列出所有可用模型。
	ListModels() []string
	// GetCurrentModel 获取当前默认模型。
	GetCurrentModel() string
	// SetModel 设置默认模型。
	SetModel(model string) error
}

// AICommand 处理 !ai 命令。
type AICommand struct {
	sender Sender
	text   TextOnlySender
	svc    AIService
}

// TextOnlySender 定义仅发送文本消息的接口。
type TextOnlySender interface {
	// SendText 向房间发送纯文本消息。
	SendText(ctx context.Context, roomID id.RoomID, body string) error
}

// NewAICommand 创建一个新的 AI 命令处理器。
func NewAICommand(sender Sender, text TextOnlySender, svc AIService) *AICommand {
	return &AICommand{
		sender: sender,
		text:   text,
		svc:    svc,
	}
}

// Handle 实现 CommandHandler。
func (c *AICommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if c.svc == nil {
		return c.text.SendText(ctx, roomID, "AI 服务未启用")
	}

	if len(args) == 0 {
		return c.text.SendText(ctx, roomID, "请提供消息内容，例如：!ai 你好")
	}

	// 这里应该调用 AI 服务
	// 实际实现由外部包提供
	return c.text.SendText(ctx, roomID, "AI 服务接口占位符")
}

// AIClearContextCommand 处理 !ai-clear 命令。
type AIClearContextCommand struct {
	text TextOnlySender
	svc  AIService
}

// NewAIClearContextCommand 创建一个新的清除上下文命令处理器。
func NewAIClearContextCommand(text TextOnlySender, svc AIService) *AIClearContextCommand {
	return &AIClearContextCommand{
		text: text,
		svc:  svc,
	}
}

// Handle 实现 CommandHandler。
func (c *AIClearContextCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if c.svc == nil {
		return c.text.SendText(ctx, roomID, "AI 服务未启用")
	}

	c.svc.ClearContext(roomID, userID)
	return c.text.SendText(ctx, roomID, "对话上下文已清除")
}

// AIContextInfoCommand 处理 !ai-context 命令。
type AIContextInfoCommand struct {
	text TextOnlySender
	svc  AIService
}

// NewAIContextInfoCommand 创建一个新的上下文信息命令处理器。
func NewAIContextInfoCommand(text TextOnlySender, svc AIService) *AIContextInfoCommand {
	return &AIContextInfoCommand{
		text: text,
		svc:  svc,
	}
}

// Handle 实现 CommandHandler。
func (c *AIContextInfoCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if c.svc == nil {
		return c.text.SendText(ctx, roomID, "AI 服务未启用")
	}

	info := c.svc.GetContextInfo(roomID, userID)
	return c.text.SendText(ctx, roomID, info)
}

// AIModelsCommand 处理 !ai-models 命令。
type AIModelsCommand struct {
	text TextOnlySender
	svc  AIService
}

// NewAIModelsCommand 创建一个新的模型列表命令处理器。
func NewAIModelsCommand(text TextOnlySender, svc AIService) *AIModelsCommand {
	return &AIModelsCommand{
		text: text,
		svc:  svc,
	}
}

// Handle 实现 CommandHandler。
func (c *AIModelsCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if c.svc == nil {
		return c.text.SendText(ctx, roomID, "AI 服务未启用")
	}

	models := c.svc.ListModels()
	if len(models) == 0 {
		return c.text.SendText(ctx, roomID, "暂无可用模型")
	}

	var result string
	for _, model := range models {
		result += fmt.Sprintf("- %s\n", model)
	}
	return c.text.SendText(ctx, roomID, "可用模型：\n"+result)
}

// AICurrentModelCommand 处理 !ai-current 命令。
type AICurrentModelCommand struct {
	text TextOnlySender
	svc  AIService
}

// NewAICurrentModelCommand 创建一个新的当前模型命令处理器。
func NewAICurrentModelCommand(text TextOnlySender, svc AIService) *AICurrentModelCommand {
	return &AICurrentModelCommand{
		text: text,
		svc:  svc,
	}
}

// Handle 实现 CommandHandler。
func (c *AICurrentModelCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if c.svc == nil {
		return c.text.SendText(ctx, roomID, "AI 服务未启用")
	}

	model := c.svc.GetCurrentModel()
	return c.text.SendText(ctx, roomID, fmt.Sprintf("当前模型：%s", model))
}

// AISwitchModelCommand 处理 !ai-switch 命令。
type AISwitchModelCommand struct {
	text TextOnlySender
	svc  AIService
}

// NewAISwitchModelCommand 创建一个新的切换模型命令处理器。
func NewAISwitchModelCommand(text TextOnlySender, svc AIService) *AISwitchModelCommand {
	return &AISwitchModelCommand{
		text: text,
		svc:  svc,
	}
}

// Handle 实现 CommandHandler。
func (c *AISwitchModelCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if c.svc == nil {
		return c.text.SendText(ctx, roomID, "AI 服务未启用")
	}

	if len(args) == 0 {
		return c.text.SendText(ctx, roomID, "请指定模型名称，例如：!ai-switch gpt-4")
	}

	model := args[0]
	if err := c.svc.SetModel(model); err != nil {
		return c.text.SendText(ctx, roomID, fmt.Sprintf("切换模型失败：%v", err))
	}

	return c.text.SendText(ctx, roomID, fmt.Sprintf("已切换到模型：%s", model))
}
