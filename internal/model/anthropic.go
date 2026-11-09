package model

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
)

type nativeHistory struct {
	Protocol string          `json:"protocol"`
	Origin   string          `json:"origin,omitempty"`
	Model    string          `json:"model,omitempty"`
	Content  json.RawMessage `json:"content"`
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

func effectiveAPI(provider, api string) string {
	if api != "" {
		return api
	}
	switch provider {
	case "anthropic", "claude", "claude-code", "minimax", "minimax-cn", "minimax-oauth", "kimi-coding", "tencent-tokenplan":
		return "anthropic-messages"
	case "gemini", "google", "google-gemini":
		return "google-gemini"
	case "bedrock", "aws-bedrock":
		return "bedrock-converse"
	case "openai-responses", "openai-codex":
		return "openai-responses"
	default:
		return "openai-completions"
	}
}

func (c *Client) usesNative() bool {
	api := effectiveAPI(c.config.Provider, c.config.API)
	return api == "anthropic-messages" || api == "google-gemini" || api == "bedrock-converse" || c.config.Provider == "moa" || c.config.Provider == "plugin"
}

func decodeArguments(raw string) (map[string]any, error) {
	value := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &value); err != nil || value == nil {
		return nil, errors.New("tool call arguments must be a JSON object")
	}
	return value, nil
}

func (c *Client) nativeStored(req agent.Request, msg openai.ChatCompletionMessage, protocol string) (json.RawMessage, bool) {
	if len(msg.ToolCalls) == 0 {
		return nil, false
	}
	for _, raw := range req.ResponsesHistory[msg.ToolCalls[0].ID] {
		var history nativeHistory
		if json.Unmarshal(raw, &history) == nil && history.Protocol == protocol && history.Origin == c.responseOrigin() && history.Model == c.getModelName(req.Model) && len(history.Content) > 0 {
			return history.Content, true
		}
	}
	return nil, false
}

func anthropicContent(msg openai.ChatCompletionMessage) ([]any, error) {
	if msg.Content != "" && len(msg.MultiContent) > 0 {
		return nil, openai.ErrContentFieldsMisused
	}
	content := make([]any, 0, len(msg.MultiContent)+len(msg.ToolCalls)+1)
	if msg.Content != "" {
		content = append(content, map[string]any{"type": "text", "text": msg.Content})
	}
	for _, part := range msg.MultiContent {
		switch part.Type {
		case openai.ChatMessagePartTypeText:
			content = append(content, map[string]any{"type": "text", "text": part.Text})
		case openai.ChatMessagePartTypeImageURL:
			if part.ImageURL == nil {
				return nil, errors.New("image URL missing")
			}
			raw := part.ImageURL.URL
			var source map[string]any
			if strings.HasPrefix(raw, "data:") {
				header, data, ok := strings.Cut(strings.TrimPrefix(raw, "data:"), ",")
				mime, enc, split := strings.Cut(header, ";")
				if !ok || !split || enc != "base64" || !strings.HasPrefix(mime, "image/") {
					return nil, errors.New("anthropic image requires base64 image data")
				}
				source = map[string]any{"type": "base64", "media_type": mime, "data": data}
			} else if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") {
				source = map[string]any{"type": "url", "url": raw}
			} else {
				return nil, errors.New("unsupported Anthropic image URL")
			}
			content = append(content, map[string]any{"type": "image", "source": source})
		default:
			return nil, fmt.Errorf("unsupported native message part %q", part.Type)
		}
	}
	for _, call := range msg.ToolCalls {
		if call.ID == "" || call.Function.Name == "" || call.Type != openai.ToolTypeFunction {
			return nil, errors.New("invalid native tool call")
		}
		args, err := decodeArguments(call.Function.Arguments)
		if err != nil {
			return nil, err
		}
		content = append(content, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": args})
	}
	return content, nil
}

func (c *Client) anthropicRequest(req agent.Request) (map[string]any, error) {
	var system []any
	messages := make([]anthropicMessage, 0, len(req.Messages))
	appendMessage := func(role string, content []any) {
		if len(content) == 0 {
			return
		}
		if len(messages) > 0 && messages[len(messages)-1].Role == role {
			messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, content...)
		} else {
			messages = append(messages, anthropicMessage{Role: role, Content: content})
		}
	}
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system", "developer":
			if msg.Content != "" {
				system = append(system, map[string]any{"type": "text", "text": msg.Content})
			}
		case "tool":
			if msg.ToolCallID == "" {
				return nil, errors.New("anthropic tool result requires tool_call_id")
			}
			appendMessage("user", []any{map[string]any{"type": "tool_result", "tool_use_id": msg.ToolCallID, "content": msg.Content}})
		case "user", "assistant":
			if raw, ok := c.nativeStored(req, msg, "anthropic"); ok {
				var content []any
				if err := json.Unmarshal(raw, &content); err != nil {
					return nil, err
				}
				appendMessage(msg.Role, content)
				continue
			}
			content, err := anthropicContent(msg)
			if err != nil {
				return nil, err
			}
			appendMessage(msg.Role, content)
		default:
			return nil, fmt.Errorf("unsupported Anthropic role %q", msg.Role)
		}
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = anthropicOutputLimit(c.getModelName(req.Model))
	}
	body := map[string]any{"model": c.getModelName(req.Model), "messages": messages, "max_tokens": maxTokens, "stream": req.Stream}
	if len(system) > 0 {
		body["system"] = system
	}
	model := strings.ToLower(c.getModelName(req.Model))
	effort := c.reasoningEffort(req)
	legacy := !strings.Contains(model, "claude") || containsAny(model, "claude-3", "-4-0", "-4.0", "-4-1", "-4.1", "-4-2025", "-4-5", "-4.5")
	if legacy || containsAny(model, "-4-6", "-4.6") {
		body["temperature"] = req.Temperature
	}
	if effort != "" && effort != "none" {
		if legacy && !strings.Contains(model, "haiku") {
			budget := map[string]int{"minimal": 1024, "low": 4000, "medium": 8000, "high": 16000, "xhigh": 32000, "max": 32000, "ultra": 32000}[effort]
			if budget == 0 {
				return nil, errors.New("unsupported Anthropic reasoning effort")
			}
			budget = min(budget, maxTokens-1)
			if budget < 1024 {
				return nil, errors.New("anthropic thinking requires max_tokens greater than 1024")
			}
			body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
			body["temperature"] = 1
		} else if !legacy {
			if effort == "minimal" {
				effort = "low"
			}
			if effort == "ultra" {
				effort = "max"
			}
			if effort == "xhigh" && containsAny(model, "-4-6", "-4.6") {
				effort = "max"
			}
			body["thinking"] = map[string]any{"type": "adaptive"}
			body["output_config"] = map[string]any{"effort": effort}
		}
	} else if effort == "none" && !legacy {
		if containsAny(model, "sonnet-5-5", "sonnet-5.5") {
			body["thinking"] = map[string]any{"type": "between_tools"}
		} else if !containsAny(model, "fable", "mythos", "opus-5-5", "opus-5.5") {
			body["thinking"] = map[string]any{"type": "disabled"}
		}
	}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			if tool.Function == nil || tool.Type != openai.ToolTypeFunction {
				return nil, errors.New("anthropic requires function tools")
			}
			tools = append(tools, map[string]any{"name": tool.Function.Name, "description": tool.Function.Description, "input_schema": tool.Function.Parameters})
		}
		body["tools"] = tools
		if req.ToolChoice != nil {
			mode, name, err := modelToolChoice(req.ToolChoice)
			if err != nil {
				return nil, err
			}
			switch mode {
			case "auto":
				body["tool_choice"] = map[string]any{"type": "auto"}
			case "required":
				body["tool_choice"] = map[string]any{"type": "any"}
			case "none":
				body["tool_choice"] = map[string]any{"type": "none"}
			case "named":
				body["tool_choice"] = map[string]any{"type": "tool", "name": name}
			}
		}
	}
	if enabled, _ := c.provider.Extra["prompt_cache"].(bool); enabled {
		if len(system) > 0 {
			system[len(system)-1].(map[string]any)["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		if len(messages) > 0 {
			last := messages[len(messages)-1].Content
			if len(last) > 0 {
				if block, ok := last[len(last)-1].(map[string]any); ok {
					block["cache_control"] = map[string]any{"type": "ephemeral"}
				}
			}
		}
	}
	return body, nil
}

func containsAny(value string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(value, part) {
			return true
		}
	}
	return false
}

func anthropicOutputLimit(model string) int {
	limits := map[string]int{"claude-fable": 128000, "claude-sonnet-5": 128000, "claude-opus-4-7": 128000, "claude-opus-4-6": 128000, "claude-sonnet-4-6": 64000, "claude-opus-4-5": 64000, "claude-sonnet-4-5": 64000, "claude-haiku-4-5": 64000, "claude-opus-4": 32000, "claude-sonnet-4": 64000, "claude-3-7-sonnet": 128000, "claude-3-5-sonnet": 8192, "claude-3-5-haiku": 8192, "claude-3-opus": 4096, "claude-3-sonnet": 4096, "claude-3-haiku": 4096, "minimax": 131072, "qwen3": 65536}
	model = strings.ReplaceAll(model, ".", "-")
	longest, limit := 0, 128000
	for key, value := range limits {
		if len(key) > longest && strings.Contains(model, key) {
			longest, limit = len(key), value
		}
	}
	return limit
}

func anthropicFinish(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "refusal":
		return "content_filter"
	default:
		return reason
	}
}

func anthropicResult(model, stop string, blocks []anthropicBlock, usage openai.Usage) (*agent.Response, error) {
	result := &agent.Response{Model: model, Usage: usage, FinishReason: anthropicFinish(stop)}
	var text strings.Builder
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			if !json.Valid(block.Input) {
				return result, errors.New("invalid Anthropic tool arguments")
			}
			result.ToolCalls = append(result.ToolCalls, openai.ToolCall{ID: block.ID, Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: block.Name, Arguments: string(block.Input)}})
		case "thinking", "redacted_thinking":
		default:
			return result, fmt.Errorf("unsupported Anthropic content block %q", block.Type)
		}
	}
	result.Content = text.String()
	if len(result.ToolCalls) > 0 {
		raw, err := json.Marshal(blocks)
		if err != nil {
			return result, err
		}
		envelope, err := json.Marshal(nativeHistory{Protocol: "anthropic", Content: raw})
		if err != nil {
			return result, err
		}
		result.ResponsesOutput = []json.RawMessage{envelope}
	}
	return result, agent.ValidateResponse(*result)
}

func (c *Client) createAnthropic(ctx context.Context, req agent.Request, handler StreamingChatCompletionHandler) (result *agent.Response, err error) {
	defer func() {
		if err != nil && handler != nil {
			handler.OnError(ctx, err)
		}
	}()
	body, err := c.anthropicRequest(req)
	if err != nil {
		return nil, err
	}
	headers := http.Header{"anthropic-version": {"2023-06-01"}}
	if strings.HasPrefix(c.config.APIKey, "sk-ant-oat") {
		headers.Set("Authorization", "Bearer "+c.config.APIKey)
		headers.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	} else if c.config.APIKey != "" {
		headers.Set("x-api-key", c.config.APIKey)
	}
	base := strings.TrimSuffix(strings.TrimRight(c.config.BaseURL, "/"), "/v1")
	resp, err := c.postProtocol(ctx, base+"/v1/messages", body, headers)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, readErr := readBoundedBody(resp.Body)
		if readErr != nil {
			return nil, readErr
		}
		return nil, protocolError(resp, data)
	}
	if !req.Stream {
		var raw struct {
			Model   string           `json:"model"`
			Stop    string           `json:"stop_reason"`
			Content []anthropicBlock `json:"content"`
			Usage   struct {
				Input         int `json:"input_tokens"`
				Output        int `json:"output_tokens"`
				CacheCreation int `json:"cache_creation_input_tokens"`
				CacheRead     int `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		data, err := readBoundedBody(resp.Body)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &raw); err != nil {
			return nil, errors.New("invalid Anthropic response")
		}
		input := raw.Usage.Input + raw.Usage.CacheCreation + raw.Usage.CacheRead
		return anthropicResult(raw.Model, raw.Stop, raw.Content, openai.Usage{PromptTokens: input, CompletionTokens: raw.Usage.Output, TotalTokens: input + raw.Usage.Output})
	}
	var blocks []anthropicBlock
	var argumentBuffers []*strings.Builder
	var usage openai.Usage
	model, stop := c.getModelName(req.Model), ""
	complete := false
	err = readSSE(resp.Body, func(data []byte) error {
		var event struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Model string `json:"model"`
				Usage struct {
					Input    int `json:"input_tokens"`
					Output   int `json:"output_tokens"`
					Creation int `json:"cache_creation_input_tokens"`
					Read     int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Block anthropicBlock `json:"content_block"`
			Delta struct {
				Type      string `json:"type"`
				Text      string `json:"text"`
				Partial   string `json:"partial_json"`
				Thinking  string `json:"thinking"`
				Signature string `json:"signature"`
				Stop      string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				Output int `json:"output_tokens"`
			} `json:"usage"`
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return errors.New("invalid Anthropic stream frame")
		}
		switch event.Type {
		case "message_start":
			model = event.Message.Model
			usage.PromptTokens = event.Message.Usage.Input + event.Message.Usage.Creation + event.Message.Usage.Read
			usage.CompletionTokens = event.Message.Usage.Output
		case "content_block_start":
			if event.Index != len(blocks) {
				return errors.New("out-of-order Anthropic content block")
			}
			blocks = append(blocks, event.Block)
			argumentBuffers = append(argumentBuffers, &strings.Builder{})
		case "content_block_delta":
			if event.Index < 0 || event.Index >= len(blocks) {
				return errors.New("anthropic stream references missing block")
			}
			block := &blocks[event.Index]
			switch event.Delta.Type {
			case "text_delta":
				block.Text += event.Delta.Text
				if handler != nil {
					handler.OnChunk(ctx, event.Delta.Text)
				}
			case "input_json_delta":
				argumentBuffers[event.Index].WriteString(event.Delta.Partial)
			case "thinking_delta":
				block.Thinking += event.Delta.Thinking
			case "signature_delta":
				block.Signature += event.Delta.Signature
			default:
				return errors.New("unsupported Anthropic stream delta")
			}
		case "content_block_stop":
			if event.Index < 0 || event.Index >= len(blocks) {
				return errors.New("anthropic stream stop references missing block")
			}
			if blocks[event.Index].Type == "tool_use" && argumentBuffers[event.Index].Len() > 0 {
				blocks[event.Index].Input = json.RawMessage(argumentBuffers[event.Index].String())
			}
		case "message_delta":
			stop = event.Delta.Stop
			usage.CompletionTokens = event.Usage.Output
		case "message_stop":
			complete = true
		case "ping":
		case "error":
			return &ProtocolError{Status: 502, Code: event.Error.Type}
		default:
			return fmt.Errorf("unsupported Anthropic stream event %q", event.Type)
		}
		return ctx.Err()
	})
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	result, validationErr := anthropicResult(model, stop, blocks, usage)
	if err != nil {
		if result != nil {
			result.FinishReason = ""
		}
		return result, err
	}
	if !complete {
		result.FinishReason = ""
		return result, errors.New("anthropic stream ended before message_stop")
	}
	if validationErr != nil {
		return result, validationErr
	}
	notifyNative(ctx, result, handler)
	return result, nil
}

func readSSE(reader io.Reader, consume func([]byte) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxProtocolBody)
	var data strings.Builder
	total := 0
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		value := strings.TrimSuffix(data.String(), "\n")
		data.Reset()
		if value == "[DONE]" {
			return nil
		}
		return consume([]byte(value))
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line)
		if total > maxProtocolBody {
			return errors.New("model stream exceeds 32 MiB")
		}
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data.WriteString(strings.TrimPrefix(value, " "))
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func notifyNative(ctx context.Context, result *agent.Response, handler StreamingChatCompletionHandler) {
	if handler == nil {
		return
	}
	if tools, ok := handler.(StreamingToolCallHandler); ok {
		for i, call := range result.ToolCalls {
			tools.OnToolCallChunk(ctx, i, call.ID, call.Function.Name, call.Function.Arguments)
		}
		tools.OnFinishReason(ctx, result.FinishReason)
	}
	handler.OnComplete(ctx, result.Content, result.Usage, result.Model)
}
