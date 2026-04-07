package qq

import (
	"context"
	"fmt"
	"strings"

	"rua.plus/saber/internal/ai"
)

// AICommand 处理 !ai 系列命令。
//
// 支持的子命令:
//   - !ai <message>  - 与 AI 对话
//   - !ai clear      - 清除对话上下文
//   - !ai context    - 显示上下文信息
//   - !ai models     - 列出所有可用模型
//   - !ai switch <id> - 切换默认模型
//   - !ai current    - 显示当前默认模型
type AICommand struct {
	// aiService 是 AI 服务实例。
	aiService *ai.SimpleService
	// contextMgr 是上下文管理器。
	contextMgr *ContextManager
	// modelRegistry 是模型注册表（从 aiService 获取）。
	modelRegistry *ai.ModelRegistry
}

// NewAICommand 创建一个新的 AI 命令处理器。
//
// 参数:
//   - aiService: AI 服务实例
//   - contextMgr: 上下文管理器
//
// 返回值:
//   - *AICommand: 创建的命令处理器
func NewAICommand(aiService *ai.SimpleService, contextMgr *ContextManager) *AICommand {
	return &AICommand{
		aiService:     aiService,
		contextMgr:    contextMgr,
		modelRegistry: aiService.GetModelRegistry(),
	}
}

// Handle 实现 CommandHandler 接口。
func (c *AICommand) Handle(ctx context.Context, userID, groupID string, args []string, sender CommandSender) error {
	// 无参数时显示帮助
	if len(args) == 0 {
		return sender.Send(ctx, userID, groupID, c.getHelpText())
	}

	subCmd := args[0]
	subArgs := args[1:]

	switch subCmd {
	case "clear":
		return c.handleClear(ctx, userID, groupID, sender)
	case "context":
		return c.handleContext(ctx, userID, groupID, sender)
	case "models":
		return c.handleModels(ctx, userID, groupID, sender)
	case "switch":
		return c.handleSwitch(ctx, userID, groupID, subArgs, sender)
	case "current":
		return c.handleCurrent(ctx, userID, groupID, sender)
	default:
		// 不是子命令，当作消息处理
		return c.handleChat(ctx, userID, groupID, args, sender)
	}
}

// getHelpText 返回 AI 命令的帮助文本。
func (c *AICommand) getHelpText() string {
	return `AI 命令用法:
!ai <message>      - 与 AI 对话
!ai clear          - 清除对话上下文
!ai context        - 显示上下文信息
!ai models         - 列出所有可用模型
!ai switch <id>    - 切换默认模型
!ai current        - 显示当前默认模型`
}

// handleClear 处理 !ai clear 命令。
func (c *AICommand) handleClear(ctx context.Context, userID, groupID string, sender CommandSender) error {
	c.contextMgr.ClearContext(userID)
	return sender.Send(ctx, userID, groupID, "已清除对话上下文")
}

// handleContext 处理 !ai context 命令。
func (c *AICommand) handleContext(ctx context.Context, userID, groupID string, sender CommandSender) error {
	info := c.contextMgr.GetContextInfo(userID)
	return sender.Send(ctx, userID, groupID, info)
}

// handleModels 处理 !ai models 命令。
func (c *AICommand) handleModels(ctx context.Context, userID, groupID string, sender CommandSender) error {
	models := c.modelRegistry.ListModels()
	currentDefault := c.modelRegistry.GetDefault()

	var lines []string
	lines = append(lines, "可用模型:")

	for _, m := range models {
		marker := ""
		if m.ID == currentDefault {
			marker = " (当前)"
		}
		lines = append(lines, fmt.Sprintf("  %s%s", m.ID, marker))
	}

	return sender.Send(ctx, userID, groupID, strings.Join(lines, "\n"))
}

// handleSwitch 处理 !ai switch 命令。
func (c *AICommand) handleSwitch(ctx context.Context, userID, groupID string, args []string, sender CommandSender) error {
	if len(args) == 0 {
		return sender.Send(ctx, userID, groupID, "用法: !ai switch <模型ID>")
	}

	modelID := args[0]

	// 验证模型是否存在
	if _, exists := c.modelRegistry.GetModelInfo(modelID); !exists {
		return sender.Send(ctx, userID, groupID, fmt.Sprintf("模型不存在: %s", modelID))
	}

	if err := c.modelRegistry.SetDefault(modelID); err != nil {
		return sender.Send(ctx, userID, groupID, fmt.Sprintf("切换模型失败: %v", err))
	}
	return sender.Send(ctx, userID, groupID, fmt.Sprintf("已切换到: %s", modelID))
}

// handleCurrent 处理 !ai current 命令。
func (c *AICommand) handleCurrent(ctx context.Context, userID, groupID string, sender CommandSender) error {
	model := c.modelRegistry.GetDefault()
	return sender.Send(ctx, userID, groupID, fmt.Sprintf("当前模型: %s", model))
}

// handleChat 处理 !ai <message> 命令。
func (c *AICommand) handleChat(ctx context.Context, userID, groupID string, args []string, sender CommandSender) error {
	message := strings.Join(args, " ")
	if message == "" {
		return sender.Send(ctx, userID, groupID, "请输入消息内容")
	}

	// 添加用户消息到上下文
	c.contextMgr.AddMessage(userID, "user", message)

	// 获取上下文构建对话
	// TODO: 支持带上下文的对话
	_ = c.contextMgr.GetContext(userID)

	// 调用 AI 服务
	response, err := c.aiService.Chat(ctx, userID, message)
	if err != nil {
		return sender.Send(ctx, userID, groupID, fmt.Sprintf("AI 请求失败: %v", err))
	}

	// 添加助手回复到上下文
	c.contextMgr.AddMessage(userID, "assistant", response)

	return sender.Send(ctx, userID, groupID, response)
}
