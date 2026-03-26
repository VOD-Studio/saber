// Package ai 提供 AI 服务相关功能，包括对话管理、流式响应和工具调用。
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/matrix"
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

// ExecuteToolCallingLoop 执行工具调用循环，处理 AI 响应中的工具调用。
//
// 它最多执行配置的 MaxIterations 次迭代，每次迭代都会：
// 1. 向 AI 发送当前消息历史
// 2. 检查 AI 响应是否包含工具调用
// 3. 如果没有工具调用，返回最终内容
// 4. 如果有工具调用，执行每个工具并将其结果添加到消息历史中
// 5. 继续下一次迭代
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - messages: 聊天消息历史
//   - modelName: 使用的 AI 模型名称
//   - tools: 可用的工具列表
//
// 返回值:
//   - string: 最终的 AI 响应内容
//   - error: 执行过程中发生的错误
func (te *ToolExecutor) ExecuteToolCallingLoop(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	modelName string,
	tools []openai.Tool,
) (string, error) {
	currentMessages := make([]openai.ChatCompletionMessage, len(messages))
	copy(currentMessages, messages)

	maxIterations := te.service.core.GetConfig().ToolCalling.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 5
	}

	for i := 0; i < maxIterations; i++ {
		req := ChatCompletionRequest{
			Model:    modelName,
			Messages: currentMessages,
			Tools:    tools,
		}

		client, err := te.service.getClient(modelName)
		if err != nil {
			return "", fmt.Errorf("获取AI客户端失败: %w", err)
		}

		resp, err := client.CreateChatCompletion(ctx, req)
		if err != nil {
			return "", fmt.Errorf("AI请求失败: %w", err)
		}

		// No tool calls - return final content
		if len(resp.ToolCalls) == 0 {
			return resp.Content, nil
		}

		// 先将带工具调用的助手消息添加到历史记录
		// 这是 AI 查看其之前工具调用所必需的
		currentMessages = append(currentMessages, openai.ChatCompletionMessage{
			Role:      openai.ChatMessageRoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// Process tool calls
		for _, toolCall := range resp.ToolCalls {
			// Log tool call
			slog.Debug("执行工具调用", "tool_name", toolCall.Function.Name, "tool_id", toolCall.ID)

			// Parse args
			var args map[string]any
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				// Add error as tool result
				currentMessages = append(currentMessages, openai.ChatCompletionMessage{
					Role:       "tool",
					ToolCallID: toolCall.ID,
					Content:    fmt.Sprintf("Error parsing arguments: %v", err),
				})
				continue
			}

			// Execute tool
			if te.service.mcpManager == nil {
				currentMessages = append(currentMessages, openai.ChatCompletionMessage{
					Role:       "tool",
					ToolCallID: toolCall.ID,
					Content:    "Error: MCP manager not initialized",
				})
				continue
			}

			toolName := toolCall.Function.Name
			serverName := te.service.mcpManager.GetServerForTool(toolName)
			if serverName == "" {
				currentMessages = append(currentMessages, openai.ChatCompletionMessage{
					Role:       "tool",
					ToolCallID: toolCall.ID,
					Content:    fmt.Sprintf("Error: No server found for tool %s", toolName),
				})
				continue
			}

			result, err := te.service.mcpManager.CallTool(ctx, serverName, toolName, args)
			if err != nil {
				// Add error as tool result
				currentMessages = append(currentMessages, openai.ChatCompletionMessage{
					Role:       "tool",
					ToolCallID: toolCall.ID,
					Content:    fmt.Sprintf("Error: %v", err),
				})
				continue
			}

			// Add tool result to messages
			resultJSON, _ := json.Marshal(result)
			currentMessages = append(currentMessages, openai.ChatCompletionMessage{
				Role:       "tool",
				ToolCallID: toolCall.ID,
				Content:    string(resultJSON),
			})
		}
	}

	return "", fmt.Errorf("max tool iterations (%d) exceeded", maxIterations)
}

// ExecuteStreamingWithToolCalling 执行支持工具调用的流式响应。
//
// 此方法结合了流式输出和工具调用能力：
// 1. 使用流式请求获取 AI 响应
// 2. 如果响应包含工具调用，累积工具调用参数
// 3. 执行工具并将结果添加到消息历史
// 4. 继续流式请求直到获得最终响应
//
// 参数:
//   - ctx: 上下文
//   - client: AI 客户端
//   - req: 聊天完成请求
//   - roomID: Matrix 房间 ID
//   - messages: 消息历史
//   - tools: 可用工具列表
//   - model: 使用的模型名称
//
// 返回值:
//   - any: 响应结果
//   - error: 错误信息
func (te *ToolExecutor) ExecuteStreamingWithToolCalling(
	ctx context.Context,
	client *Client,
	req ChatCompletionRequest,
	roomID id.RoomID,
	messages []openai.ChatCompletionMessage,
	_ []openai.Tool,
	model string,
) (any, error) {
	currentMessages := make([]openai.ChatCompletionMessage, len(messages))
	copy(currentMessages, messages)

	eventID := matrix.GetEventID(ctx)

	maxIterations := te.service.core.GetConfig().ToolCalling.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 5
	}

	for i := 0; i < maxIterations; i++ {
		// 创建流编辑器和处理器
		cfg := te.service.core.GetConfig()
		editor := NewStreamEditor(te.service.matrixService, roomID, "", cfg.StreamEdit, eventID)
		handler := NewStreamToolHandler(editor, cfg.StreamEdit)

		// 发送流式请求
		req.Messages = currentMessages
		err := client.CreateStreamingChatCompletionWithTools(ctx, req, handler)
		if err != nil {
			slog.Error("流式AI请求失败", "model", model, "iteration", i+1, "error", err)
			return nil, err
		}

		// 检查是否有工具调用
		if !handler.HasToolCalls() || handler.GetFinishReason() != "tool_calls" {
			// 没有工具调用，流式响应已完成
			slog.Debug("流式AI请求完成（无工具调用）", "model", model, "iteration", i+1)
			return nil, nil
		}

		// 有工具调用，需要执行
		toolCalls := handler.GetAccumulatedToolCalls()
		accumulatedContent := handler.GetAccumulatedContent()
		slog.Debug("检测到工具调用，开始执行",
			"iteration", i+1,
			"tool_count", len(toolCalls),
			"accumulated_content_length", len(accumulatedContent))

		// 如果有累积的文本内容，添加到消息历史
		if accumulatedContent != "" {
			currentMessages = append(currentMessages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: accumulatedContent,
			})
		}

		// 添加带工具调用的助手消息
		currentMessages = append(currentMessages, openai.ChatCompletionMessage{
			Role:      openai.ChatMessageRoleAssistant,
			Content:   accumulatedContent,
			ToolCalls: toolCalls,
		})

		// 执行每个工具调用
		for _, toolCall := range toolCalls {
			slog.Debug("执行工具调用", "tool_name", toolCall.Function.Name, "tool_id", toolCall.ID)

			// 解析参数
			var args map[string]any
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				currentMessages = append(currentMessages, openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					ToolCallID: toolCall.ID,
					Content:    fmt.Sprintf("Error parsing arguments: %v", err),
				})
				continue
			}

			// 执行工具
			result, execErr := te.ExecuteToolCall(ctx, toolCall.Function.Name, args)
			if execErr != nil {
				currentMessages = append(currentMessages, openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					ToolCallID: toolCall.ID,
					Content:    fmt.Sprintf("Error: %v", execErr),
				})
				continue
			}

			// 添加工具结果
			resultJSON, _ := json.Marshal(result)
			currentMessages = append(currentMessages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: toolCall.ID,
				Content:    string(resultJSON),
			})
		}

		slog.Debug("工具调用执行完成，继续流式请求", "iteration", i+1)
	}

	slog.Error("超过最大工具调用迭代次数", "max_iterations", maxIterations)
	return nil, fmt.Errorf("max tool iterations (%d) exceeded", maxIterations)
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
	if te.service.mcpManager == nil {
		return nil, fmt.Errorf("MCP manager not initialized")
	}

	serverName := te.service.mcpManager.GetServerForTool(toolName)
	if serverName == "" {
		return nil, fmt.Errorf("no server found for tool %s", toolName)
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
