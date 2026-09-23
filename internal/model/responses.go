package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
)

func (c *Client) usesResponses() bool {
	return c.config != nil && (c.config.API == "openai-responses" || c.config.API == "" && c.config.Provider == "openai-responses")
}

// responseRequest 使用完整历史和 store=false，不依赖中继持久化 response ID。
func (c *Client) responseRequest(req ChatCompletionRequest) (openai.CreateResponseRequest, error) {
	input := make([]any, 0, len(req.Messages))
	for _, msg := range req.Messages {
		if msg.Role == openai.ChatMessageRoleTool {
			if msg.ToolCallID == "" {
				return openai.CreateResponseRequest{}, errors.New("tool result has no call ID")
			}
			input = append(input, openai.ResponseFunctionCallOutput{Type: "function_call_output", CallID: msg.ToolCallID, Output: msg.Content})
			continue
		}
		switch msg.Role {
		case "system", "developer", "user", "assistant":
		default:
			return openai.CreateResponseRequest{}, fmt.Errorf("unsupported Responses message role %q", msg.Role)
		}
		if msg.FunctionCall != nil {
			return openai.CreateResponseRequest{}, errors.New("legacy function_call is not supported by Responses")
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			if output := req.ResponsesHistory[msg.ToolCalls[0].ID]; len(output) > 0 {
				for _, item := range output {
					input = append(input, item)
				}
				continue
			}
		}
		if msg.Content != "" && len(msg.MultiContent) > 0 {
			return openai.CreateResponseRequest{}, openai.ErrContentFieldsMisused
		}
		var content any = msg.Content
		if len(msg.MultiContent) > 0 {
			parts := make([]any, 0, len(msg.MultiContent))
			for _, part := range msg.MultiContent {
				switch part.Type {
				case openai.ChatMessagePartTypeText:
					kind := "input_text"
					if msg.Role == "assistant" {
						kind = "output_text"
					}
					parts = append(parts, openai.ResponseInputText{Type: kind, Text: part.Text})
				case openai.ChatMessagePartTypeImageURL:
					if msg.Role != "user" || part.ImageURL == nil || part.ImageURL.URL == "" {
						return openai.CreateResponseRequest{}, errors.New("responses image requires a user message and image URL")
					}
					parts = append(parts, openai.ResponseInputImage{Type: "input_image", ImageURL: part.ImageURL.URL, Detail: string(part.ImageURL.Detail)})
				default:
					return openai.CreateResponseRequest{}, fmt.Errorf("unsupported Responses content type %q", part.Type)
				}
			}
			content = parts
		}
		if msg.Content != "" || len(msg.MultiContent) > 0 || len(msg.ToolCalls) == 0 {
			input = append(input, openai.ResponseInputMessage{Type: "message", Role: msg.Role, Content: content})
		}
		for _, call := range msg.ToolCalls {
			if msg.Role != "assistant" || call.Type != openai.ToolTypeFunction || call.ID == "" || call.Function.Name == "" {
				return openai.CreateResponseRequest{}, errors.New("invalid Responses function call")
			}
			input = append(input, openai.ResponseOutputItem{Type: "function_call", CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
		}
	}
	store := false
	result := openai.CreateResponseRequest{
		Model: c.getModelName(req.Model), Input: input, MaxOutputTokens: req.MaxTokens,
		Store: &store, Include: []openai.ResponseInclude{openai.ResponseIncludeReasoningEncryptedContent},
	}
	if req.ToolChoice != nil {
		result.ToolChoice = *req.ToolChoice
	}
	for _, tool := range req.Tools {
		if tool.Type != openai.ToolTypeFunction || tool.Function == nil {
			return result, errors.New("responses supports only function tools in Saber")
		}
		converted := openai.NewResponseFunctionTool(*tool.Function)
		// Responses 默认启用 strict；显式沿用原工具定义，避免拒绝 MCP 的可选参数。
		converted.Parameters["strict"] = tool.Function.Strict
		result.Tools = append(result.Tools, converted)
	}
	if c.reasoningEffort(req) != "" {
		result.Reasoning = &openai.ResponseReasoning{Effort: c.reasoningEffort(req)}
	}
	// 推理模型常拒绝 temperature；仅显式关闭推理时发送采样参数。
	if c.reasoningEffort(req) == "none" {
		temperature := float32(req.Temperature)
		result.Temperature = &temperature
	}
	return result, nil
}

func responseResult(resp openai.CreateResponseResponse) (*ChatCompletionResponse, error) {
	result := &ChatCompletionResponse{Model: resp.Model}
	if resp.Usage != nil {
		result.Usage = openai.Usage{PromptTokens: resp.Usage.InputTokens, CompletionTokens: resp.Usage.OutputTokens, TotalTokens: resp.Usage.TotalTokens}
	}
	var content, thinking strings.Builder
	for _, raw := range resp.Output {
		data, err := json.Marshal(raw)
		if err != nil {
			return result, fmt.Errorf("encode Responses output: %w", err)
		}
		var item openai.ResponseOutputItem
		if err = json.Unmarshal(data, &item); err != nil {
			return result, fmt.Errorf("decode Responses output: %w", err)
		}
		result.ResponsesOutput = append(result.ResponsesOutput, json.RawMessage(data))
		if item.Status != "" && item.Status != "completed" {
			return result, fmt.Errorf("incomplete Responses output item: %s", item.Status)
		}
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					content.WriteString(part.Text)
				case "refusal":
					content.WriteString(part.Refusal)
				default:
					return result, fmt.Errorf("unsupported Responses output content %q", part.Type)
				}
			}
		case "function_call":
			result.ToolCalls = append(result.ToolCalls, openai.ToolCall{ID: item.CallID, Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: item.Name, Arguments: item.Arguments}})
		case "reasoning":
			// 公开摘要可展示；原始及加密推理仍只在 ResponsesOutput 中续轮。
			for _, part := range item.Summary {
				if part.Type == "summary_text" {
					thinking.WriteString(part.Text)
				}
			}
		default:
			return result, fmt.Errorf("unsupported Responses output item %q", item.Type)
		}
	}
	result.Content = content.String()
	result.Thinking = thinking.String()
	if result.Content == "" {
		result.Content = resp.OutputText
	}
	if resp.Error != nil {
		return result, fmt.Errorf("responses failed (%s): %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Status != openai.ResponseStatusCompleted {
		reason := string(resp.Status)
		if resp.IncompleteDetails != nil {
			reason += ": " + resp.IncompleteDetails.Reason
		}
		return result, fmt.Errorf("responses did not complete: %s", reason)
	}
	result.FinishReason = "stop"
	if len(result.ToolCalls) > 0 {
		result.FinishReason = "tool_calls"
	}
	return result, agent.ValidateResponse(*result)
}

// createResponse 统一三种调用入口；仅终态确认成功后通知工具和完成回调。
func (c *Client) createResponse(ctx context.Context, req ChatCompletionRequest, handler StreamingChatCompletionHandler) (result *ChatCompletionResponse, err error) {
	defer func() {
		// 失败响应不能在任务恢复时被误判为可回放的完整工具轮次。
		if err != nil && result != nil {
			result.FinishReason = ""
		}
		if err != nil && handler != nil {
			handler.OnError(ctx, err)
		}
	}()
	request, err := c.responseRequest(req)
	if err != nil {
		return nil, err
	}
	if !req.Stream {
		resp, err := c.openaiClient.CreateResponse(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("create response: %w", err)
		}
		return responseResult(resp)
	}
	stream, err := c.openaiClient.CreateResponseStream(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("create response stream: %w", err)
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			slog.Debug("Failed to close Responses stream", "error", closeErr)
		}
	}()
	// 百炼 Qwen3.8 的 reasoning_text.delta 是可展示摘要；其他模型可能返回原始推理。
	qwenSummary := strings.HasPrefix(c.getModelName(req.Model), "qwen3.8-")
	var text strings.Builder
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("responses stream ended before response.completed")
		}
		if err != nil {
			return nil, fmt.Errorf("read Responses stream: %w", err)
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		switch event.Type {
		case openai.ResponseStreamEventReasoningSummaryTextDelta, openai.ResponseStreamEventReasoningTextDelta:
			if event.Type == openai.ResponseStreamEventReasoningTextDelta && !qwenSummary {
				break
			}
			if handler != nil && event.Delta != "" {
				if thinking, ok := handler.(interface{ OnThinkingChunk(context.Context, string) }); ok {
					thinking.OnThinkingChunk(ctx, event.Delta)
				}
			}
		case openai.ResponseStreamEventOutputTextDelta, openai.ResponseStreamEventRefusalDelta:
			text.WriteString(event.Delta)
			if handler != nil {
				handler.OnChunk(ctx, event.Delta)
			}
		case openai.ResponseStreamEventCompleted:
			if event.Response == nil {
				return nil, errors.New("response.completed has no response")
			}
			result, err = responseResult(*event.Response)
			if err != nil {
				return result, err
			}
			// 终态携带完整输出，也兼容只发送终态而不发送 delta 的中继。
			if !strings.HasPrefix(result.Content, text.String()) {
				return result, errors.New("responses final text does not match streamed text")
			}
			if handler != nil {
				if rest := strings.TrimPrefix(result.Content, text.String()); rest != "" {
					handler.OnChunk(ctx, rest)
				}
				if err = ctx.Err(); err != nil {
					return result, err
				}
				if toolHandler, ok := handler.(StreamingToolCallHandler); ok {
					for i, call := range result.ToolCalls {
						toolHandler.OnToolCallChunk(ctx, i, call.ID, call.Function.Name, call.Function.Arguments)
					}
					toolHandler.OnFinishReason(ctx, result.FinishReason)
				}
				handler.OnComplete(ctx, result.Content, result.Usage, result.Model)
			}
			return result, nil
		case openai.ResponseStreamEventFailed, openai.ResponseStreamEventIncomplete:
			if event.Response != nil {
				result, err = responseResult(*event.Response)
				if err != nil {
					return result, err
				}
			}
			return nil, fmt.Errorf("responses stream failed: %s", event.Type)
		case openai.ResponseStreamEventError:
			if event.Error != nil {
				return nil, fmt.Errorf("responses stream error (%s): %s", event.Error.Code, event.Error.Message)
			}
			return nil, fmt.Errorf("responses stream error (%s): %s", event.Code, event.Message)
		}
	}
}
