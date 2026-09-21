// Package ai 提供 AI 服务相关功能，包括对话管理、流式响应和工具调用。
package ai

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"
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

// ExecuteStreamingWithToolCalling 兼容旧入口，执行循环由 Runtime 统一管理。
func (te *ToolExecutor) ExecuteStreamingWithToolCalling(ctx context.Context, client *Client, req ChatCompletionRequest, roomID id.RoomID, messages []openai.ChatCompletionMessage, tools []openai.Tool, model string) (any, error) {
	req.Messages, req.Tools, req.Model, req.Stream = messages, tools, model, true
	return te.service.runAgentReply(ctx, req, roomID, client)
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
func (te *ToolExecutor) PrepareTools() ([]openai.Tool, bool) {
	if te.service.mcpManager == nil || !te.service.mcpManager.IsEnabled() {
		return nil, false
	}

	mcpTools := te.service.mcpManager.ListTools()
	if len(mcpTools) == 0 {
		return nil, false
	}

	tools := make([]openai.Tool, 0, len(mcpTools))
	for _, mcpTool := range mcpTools {
		tools = append(tools, openai.Tool{
			Type: "function",
			Function: &openai.FunctionDefinition{
				Name:        mcpTool.Name,
				Description: mcpTool.Description,
				Parameters:  mcpTool.InputSchema,
			},
		})
	}

	slog.Debug("启用工具调用", "tool_count", len(tools))
	return tools, true
}
