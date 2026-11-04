// Package ai 提供 AI 服务相关功能，包括对话管理、流式响应和工具调用。
package ai

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/memory"
)

// ToolExecutor 负责执行 AI 工具调用。
//
// 它封装了工具调用的核心逻辑，包括：
//   - 非流式工具调用循环
//   - 流式工具调用处理
//   - 单工具调用执行
//   - MCP 工具准备
type ToolExecutor struct {
	service *Service
}

// NewToolExecutor 创建一个新的工具执行器。
//
// 参数:
//   - service: AI 服务实例
//
// 返回值:
//   - *ToolExecutor: 新创建的工具执行器实例
func NewToolExecutor(service *Service) *ToolExecutor {
	return &ToolExecutor{service: service}
}

// ExecuteToolCallingLoop 通过独立 Agent Runtime 执行非流式工具对话。
func (te *ToolExecutor) ExecuteToolCallingLoop(ctx context.Context, messages []openai.ChatCompletionMessage, modelName string, tools []openai.Tool) (string, error) {
	cfg := te.service.core.GetConfig()
	result, err := te.service.RunAgent(ctx, ChatCompletionRequest{
		Model: modelName, Messages: messages, Tools: tools, MaxTokens: cfg.MaxTokens, Temperature: cfg.Temperature,
	}, nil)
	return result.Content, err
}

// ExecuteToolCall 执行单个工具调用。
//
// 参数:
//   - ctx: 上下文
//   - toolName: 工具名称
//   - args: 工具参数
//
// 返回值:
//   - any: 工具执行结果
//   - error: 错误信息
func (te *ToolExecutor) ExecuteToolCall(ctx context.Context, toolName string, args map[string]any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 公开互动回合的回复会发布给所有人，任何工具都可能把私有上下文带进公开内容，
	// 因此这里整类拒绝；PrepareTools 已经不提供工具，这里是第二道防线。
	if isPublicThread(ctx) {
		return nil, fmt.Errorf("公开互动回合不允许调用工具")
	}
	if execution.LocalTool(toolName) {
		if te.service.executor == nil {
			return nil, fmt.Errorf("execution is not configured")
		}
		// 平台没有文件发送能力时提前反馈给模型，避免任务结束才发现无法发送。
		if toolName == "read_file" && args["deliver"] == true && !te.service.canDeliverFiles(ctx) {
			return execution.Result{ExitCode: -1, Error: "该平台不支持文件交付，请直接在回复中给出内容，不要使用 deliver=true"}, nil
		}
		result, err := te.service.executor.Run(ctx, toolName, args)
		// 保留失败时的完整日志位置；运行时用 IsError 将失败反馈给模型。
		if err != nil {
			result.Error = err.Error()
		}
		return result, nil
	}
	if toolName == "saber_schedule" {
		return te.service.executeScheduleTool(ctx, args)
	}
	if toolName == "saber_memory" {
		return te.service.executeMemoryTool(ctx, args)
	}
	if toolName == "saber_history" {
		return te.service.executeHistoryTool(ctx, args)
	}
	if toolName == "saber_skill" {
		return te.service.executeSkillTool(ctx, args)
	}
	if toolName == "saber_trace" {
		return te.service.executeTraceTool(ctx, args)
	}
	if toolName == "saber_batch" {
		return te.service.executeBatchTool(ctx, args)
	}
	if toolName == tweetToolName {
		return te.service.executeTweetTool(ctx, args)
	}
	if toolName == emojiToolName {
		return te.service.executeEmojiTool(ctx, args)
	}
	if toolName == "saber_task" && te.service.tasks != nil {
		identity, ok := chat.IdentityFromContext(ctx)
		if !ok {
			return nil, fmt.Errorf("task tool requires trusted identity")
		}
		action, _ := args["action"].(string)
		if action != "list" && action != "status" && action != "cancel" && action != "logs" {
			return nil, fmt.Errorf("invalid task action")
		}
		var taskID int64
		if action != "list" {
			var err error
			taskID, err = strconv.ParseInt(fmt.Sprint(args["id"]), 10, 64)
			if err != nil || taskID <= 0 {
				return nil, fmt.Errorf("task id must be a positive integer")
			}
		}
		return te.service.taskOperation(ctx, identity, action, taskID)
	}
	if te.service.mcpManager == nil {
		return nil, fmt.Errorf("MCP manager not initialized")
	}

	serverName := te.service.mcpManager.GetServerForTool(toolName)
	if serverName == "" {
		return nil, fmt.Errorf("no server found for tool %s", toolName)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := te.service.mcpManager.CallTool(ctx, serverName, toolName, args)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// PrepareTools 准备 MCP 工具列表。
//
// 返回值:
//   - []openai.Tool: 可用的 OpenAI 工具列表
//   - bool: 是否成功准备了工具
func (te *ToolExecutor) PrepareTools(contexts ...context.Context) ([]openai.Tool, bool) {
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	// 公开互动回合不应拥有任何工具：回复会公开发布，工具结果与私有上下文都可能外泄。
	if isPublicThread(ctx) {
		return nil, false
	}
	var tools []openai.Tool
	if te.service.executor != nil {
		tools = append(tools, te.service.executor.Tools(ctx)...)
	}
	if te.service.tasks != nil {
		tools = append(tools, taskTool())
		// 计划、轨迹与批次工具都不依赖执行后端，只要求可信身份；轨迹导出还要在运行时
		// 有持久任务上下文并按 AuthorizeOutput 复核来源，批次还要按批次归属复核访问权，
		// 故此处只做可见性判断。
		if _, ok := chat.IdentityFromContext(ctx); ok {
			tools = append(tools, scheduleTool())
			tools = append(tools, traceTool())
			tools = append(tools, batchTool())
		}
	}
	// 推文工具不依赖执行后端或任务服务：它的可见性由平台、配置与账号身份决定，
	// 因此无论是否启用持久任务都要在 MCP 与 delegate 之前加入。
	if te.service.tweetToolAvailable(ctx) {
		tools = append(tools, tweetTool())
	}
	// 表情工具同样不依赖执行后端：可见性由平台、开关与账号身份决定。
	if te.service.emojiToolAvailable(ctx) {
		tools = append(tools, emojiTool())
	}
	if te.service.memory != nil {
		if identity, ok := chat.IdentityFromContext(ctx); ok {
			if _, ok := memory.Space(identity); ok {
				tools = append(tools, memoryTool())
				tools = append(tools, historyTool())
				tools = append(tools, skillTool())
			}
		}
	}
	if te.service.mcpManager == nil || !te.service.mcpManager.IsEnabled() {
		tools = te.exposeDelegate(ctx, tools)
		return tools, len(tools) > 0
	}

	mcpTools := te.service.mcpManager.ListTools()
	if len(mcpTools) == 0 {
		tools = te.exposeDelegate(ctx, tools)
		return tools, len(tools) > 0
	}

	for _, mcpTool := range mcpTools {
		if execution.LocalTool(mcpTool.Name) || builtinToolName(mcpTool.Name) {
			continue
		}
		server := te.service.mcpManager.GetServerForTool(mcpTool.Name)
		if te.service.executor == nil || te.service.executor.CheckMCP(ctx, server, mcpTool.Name) != nil {
			continue
		}
		tools = append(tools, openai.Tool{
			Type: "function",
			Function: &openai.FunctionDefinition{
				Name:        mcpTool.Name,
				Description: mcpTool.Description,
				Parameters:  mcpTool.InputSchema,
			},
		})
	}

	tools = te.exposeDelegate(ctx, tools)
	slog.Debug("启用工具调用", "tool_count", len(tools))
	return tools, len(tools) > 0
}

// builtinToolName 判断名称是否已被内置聊天工具占用。
//
// MCP 同名工具必须在列表阶段让位，否则模型看到的定义会与实际执行路径不一致：
// ExecuteToolCall 优先走内置分支，MCP 的实现永远等不到调用。
func builtinToolName(name string) bool {
	switch name {
	case "saber_task", "saber_schedule", "saber_memory", "saber_history", "saber_skill", "saber_task_state", "saber_trace", "saber_batch", tweetToolName, emojiToolName, agent.DelegateTool:
		return true
	}
	return false
}

// canDeliverFiles 判断当前会话所在平台能否接收任务产物文件；缺少可信身份时按不能处理。
func (s *Service) canDeliverFiles(ctx context.Context) bool {
	identity, ok := chat.IdentityFromContext(ctx)
	return ok && s.taskFileAdapter(identity.Session.Platform, nil) != nil
}
