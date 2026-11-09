package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
)

type geminiPart struct {
	Text             string          `json:"text,omitempty"`
	Thought          bool            `json:"thought,omitempty"`
	ThoughtSignature string          `json:"thoughtSignature,omitempty"`
	FunctionCall     *geminiFunction `json:"functionCall,omitempty"`
	InlineData       *geminiInline   `json:"inlineData,omitempty"`
}

type geminiFunction struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}
type geminiInline struct {
	MIME string `json:"mimeType"`
	Data string `json:"data"`
}
type geminiContent struct {
	Role  string `json:"role"`
	Parts []any  `json:"parts"`
}
type geminiResponse struct {
	ModelVersion string `json:"modelVersion"`
	Candidates   []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		Finish       string `json:"finishReason"`
		Continuation string `json:"continuationToken"`
	} `json:"candidates"`
	Usage struct {
		Input    int `json:"promptTokenCount"`
		Output   int `json:"candidatesTokenCount"`
		Thoughts int `json:"thoughtsTokenCount"`
		Total    int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
	PromptFeedback struct {
		Block string `json:"blockReason"`
	} `json:"promptFeedback"`
	Error struct {
		Code   int    `json:"code"`
		Status string `json:"status"`
	} `json:"error"`
	ContinuationToken string `json:"continuationToken"`
}

func geminiMajor(model string) int {
	_, version, ok := strings.Cut(model, "gemini-")
	if !ok {
		return 0
	}
	end := strings.IndexAny(version, ".-")
	if end >= 0 {
		version = version[:end]
	}
	major, _ := strconv.Atoi(version)
	return major
}

func geminiBase(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.Contains(base, "aiplatform.googleapis.com") && !strings.Contains(base, "publishers/google") {
		if !strings.Contains(base, "/v1") {
			base += "/v1beta1"
		}
		return base + "/publishers/google"
	}
	if !strings.Contains(base, "/v1") {
		base += "/v1beta"
	}
	return base
}

func (c *Client) geminiRequest(req agent.Request) (map[string]any, error) {
	var system []any
	var contents []geminiContent
	calls := map[string]string{}
	ids := geminiMajor(c.getModelName(req.Model)) >= 3
	appendContent := func(role string, parts []any) {
		if len(parts) == 0 {
			return
		}
		if len(contents) > 0 && contents[len(contents)-1].Role == role {
			contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, parts...)
		} else {
			contents = append(contents, geminiContent{Role: role, Parts: parts})
		}
	}
	for _, msg := range req.Messages {
		if msg.Role == "system" || msg.Role == "developer" {
			if msg.Content != "" {
				system = append(system, map[string]any{"text": msg.Content})
			}
			continue
		}
		if msg.Role == "tool" {
			name := calls[msg.ToolCallID]
			if name == "" {
				return nil, errors.New("gemini tool result has no matching function call")
			}
			var value any
			if json.Unmarshal([]byte(msg.Content), &value) != nil {
				value = msg.Content
			}
			response := map[string]any{"name": name, "response": map[string]any{"result": value}}
			if ids {
				response["id"] = msg.ToolCallID
			}
			appendContent("user", []any{map[string]any{"functionResponse": response}})
			continue
		}
		if msg.Role != "assistant" && msg.Role != "user" {
			return nil, fmt.Errorf("unsupported Gemini role %q", msg.Role)
		}
		role := msg.Role
		if role == "assistant" {
			role = "model"
		}
		for _, call := range msg.ToolCalls {
			calls[call.ID] = call.Function.Name
		}
		if raw, ok := c.nativeStored(req, msg, "gemini"); ok {
			var parts []any
			if err := json.Unmarshal(raw, &parts); err != nil {
				return nil, err
			}
			appendContent(role, parts)
			continue
		}
		if msg.Content != "" && len(msg.MultiContent) > 0 {
			return nil, openai.ErrContentFieldsMisused
		}
		var parts []any
		if msg.Content != "" {
			parts = append(parts, map[string]any{"text": msg.Content})
		}
		for _, part := range msg.MultiContent {
			switch part.Type {
			case openai.ChatMessagePartTypeText:
				parts = append(parts, map[string]any{"text": part.Text})
			case openai.ChatMessagePartTypeImageURL:
				if part.ImageURL == nil {
					return nil, errors.New("gemini image requires URL")
				}
				raw := part.ImageURL.URL
				if strings.HasPrefix(raw, "data:") {
					header, data, ok := strings.Cut(raw[5:], ",")
					mime, enc, split := strings.Cut(header, ";")
					if !ok || !split || enc != "base64" {
						return nil, errors.New("gemini inline image requires base64")
					}
					parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": data}})
				} else {
					parsed, err := url.Parse(raw)
					if err != nil || parsed.Scheme != "https" {
						return nil, errors.New("gemini file URI requires HTTPS or inline data")
					}
					parts = append(parts, map[string]any{"fileData": map[string]any{"fileUri": raw}})
				}
			default:
				return nil, errors.New("unsupported Gemini content part")
			}
		}
		for _, call := range msg.ToolCalls {
			args, err := decodeArguments(call.Function.Arguments)
			if err != nil {
				return nil, err
			}
			function := map[string]any{"name": call.Function.Name, "args": args}
			if ids {
				function["id"] = call.ID
			}
			part := map[string]any{"functionCall": function}
			if ids {
				part["thoughtSignature"] = "skip_thought_signature_validator"
			}
			parts = append(parts, part)
		}
		appendContent(role, parts)
	}
	generation := map[string]any{}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 65535
	}
	generation["maxOutputTokens"] = maxTokens
	model := c.getModelName(req.Model)
	if !containsAny(model, "gemini-3.6", "gemini-3.7", "gemini-3.8", "gemini-3.9") && geminiMajor(model) < 4 {
		generation["temperature"] = req.Temperature
	}
	effort := c.reasoningEffort(req)
	if effort != "" {
		thinking := map[string]any{}
		if geminiMajor(model) >= 3 || strings.HasSuffix(model, "-latest") {
			level := "HIGH"
			switch effort {
			case "none", "minimal":
				level = "MINIMAL"
			case "low":
				level = "LOW"
			case "medium":
				level = "MEDIUM"
			}
			thinking["thinkingLevel"] = level
		} else {
			budget := map[string]int{"none": 0, "minimal": 1024, "low": 4000, "medium": 8000, "high": 16000, "xhigh": 24576, "max": 24576, "ultra": 24576}[effort]
			thinking["thinkingBudget"] = budget
		}
		generation["thinkingConfig"] = thinking
	}
	body := map[string]any{"contents": contents, "generationConfig": generation}
	if len(system) > 0 {
		body["systemInstruction"] = map[string]any{"parts": system}
	}
	if len(req.Tools) > 0 {
		var declarations []any
		for _, tool := range req.Tools {
			if tool.Type != openai.ToolTypeFunction || tool.Function == nil {
				return nil, errors.New("gemini supports function tools only")
			}
			declaration := map[string]any{"name": tool.Function.Name, "description": tool.Function.Description}
			if strings.HasSuffix(geminiBase(c.config.BaseURL), "/v1beta") {
				declaration["parametersJsonSchema"] = tool.Function.Parameters
			} else {
				raw, err := json.Marshal(tool.Function.Parameters)
				if err != nil {
					return nil, err
				}
				var schema map[string]any
				if err = json.Unmarshal(raw, &schema); err != nil {
					return nil, err
				}
				declaration["parameters"] = geminiSchema(schema)
			}
			declarations = append(declarations, declaration)
		}
		body["tools"] = []any{map[string]any{"functionDeclarations": declarations}}
		if req.ToolChoice != nil {
			choice, name, err := modelToolChoice(req.ToolChoice)
			if err != nil {
				return nil, err
			}
			mode := map[string]string{"auto": "AUTO", "required": "ANY", "none": "NONE", "named": "ANY"}[choice]
			functionConfig := map[string]any{"mode": mode}
			if choice == "named" {
				functionConfig["allowedFunctionNames"] = []string{name}
			}
			body["toolConfig"] = map[string]any{"functionCallingConfig": functionConfig}
		}
	}
	return body, nil
}

func geminiSchema(schema map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range schema {
		switch key {
		case "type", "description", "enum", "required", "nullable", "format":
			result[key] = value
		case "properties":
			if properties, ok := value.(map[string]any); ok {
				converted := map[string]any{}
				for name, raw := range properties {
					if child, ok := raw.(map[string]any); ok {
						converted[name] = geminiSchema(child)
					}
				}
				result[key] = converted
			}
		case "items":
			if child, ok := value.(map[string]any); ok {
				result[key] = geminiSchema(child)
			}
		}
	}
	return result
}

func geminiResult(raw geminiResponse, model string, parts []geminiPart, finish string) (*agent.Response, error) {
	if raw.Error.Code != 0 {
		return nil, &ProtocolError{Status: raw.Error.Code, Code: raw.Error.Status}
	}
	result := &agent.Response{Model: model}
	if raw.ModelVersion != "" {
		result.Model = raw.ModelVersion
	}
	result.Usage = openai.Usage{PromptTokens: raw.Usage.Input, CompletionTokens: raw.Usage.Output + raw.Usage.Thoughts, TotalTokens: raw.Usage.Total}
	if result.Usage.TotalTokens == 0 {
		result.Usage.TotalTokens = result.Usage.PromptTokens + result.Usage.CompletionTokens
	}
	var text strings.Builder
	for index := range parts {
		part := &parts[index]
		if !part.Thought {
			text.WriteString(part.Text)
		}
		if part.FunctionCall != nil {
			call := part.FunctionCall
			if call.ID == "" {
				call.ID = "call_" + uuid.NewString()
			}
			if call.Args == nil {
				call.Args = json.RawMessage("{}")
			}
			result.ToolCalls = append(result.ToolCalls, openai.ToolCall{ID: call.ID, Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: call.Name, Arguments: string(call.Args)}})
		}
		if part.InlineData != nil {
			return result, errors.New("gemini returned binary output to a text model; use media generation tools")
		}
	}
	result.Content = text.String()
	switch finish {
	case "STOP":
		result.FinishReason = "stop"
	case "MAX_TOKENS", "CONTINUATION":
		result.FinishReason = "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT":
		result.FinishReason = "content_filter"
	default:
		result.FinishReason = finish
	}
	if raw.PromptFeedback.Block != "" {
		return result, errors.New("gemini prompt was blocked by provider safety policy")
	}
	if len(result.ToolCalls) > 0 && result.FinishReason == "stop" {
		result.FinishReason = "tool_calls"
	}
	if len(result.ToolCalls) > 0 {
		rawParts, err := json.Marshal(parts)
		if err != nil {
			return result, err
		}
		envelope, err := json.Marshal(nativeHistory{Protocol: "gemini", Content: rawParts})
		if err != nil {
			return result, err
		}
		result.ResponsesOutput = []json.RawMessage{envelope}
	}
	return result, agent.ValidateResponse(*result)
}

func (c *Client) createGemini(ctx context.Context, req agent.Request, handler StreamingChatCompletionHandler) (result *agent.Response, err error) {
	defer func() {
		if err != nil && handler != nil {
			handler.OnError(ctx, err)
		}
	}()
	body, err := c.geminiRequest(req)
	if err != nil {
		return nil, err
	}
	generation := body["generationConfig"].(map[string]any)
	budget := generation["maxOutputTokens"].(int)
	if req.MaxTokens <= 0 && geminiMajor(c.getModelName(req.Model)) >= 4 {
		switch c.reasoningEffort(req) {
		case "xhigh":
			budget = 262144
		case "max", "ultra":
			budget = 1048576
		}
		generation["maxOutputTokens"] = budget
	}
	headers := http.Header{}
	if c.config.APIKey != "" {
		headers.Set("x-goog-api-key", c.config.APIKey)
	}
	method := ":generateContent"
	if req.Stream {
		method = ":streamGenerateContent?alt=sse"
	}
	endpoint := geminiBase(c.config.BaseURL) + "/models/" + url.PathEscape(c.getModelName(req.Model)) + method
	var merged geminiResponse
	var parts []geminiPart
	seenTokens := map[string]bool{}
	limit := 1
	if geminiMajor(c.getModelName(req.Model)) >= 4 {
		limit = min((budget+32767)/32768+1, 33)
	}
	finish := ""
	for hop := range limit {
		if err = ctx.Err(); err != nil {
			break
		}
		remaining := budget - merged.Usage.Output - merged.Usage.Thoughts
		if remaining <= 0 {
			err = errors.New("gemini exhausted the configured long-decode output budget")
			break
		}
		request := req
		request.MaxTokens = remaining
		if geminiMajor(c.getModelName(req.Model)) >= 4 {
			cap := 32768
			if req.Stream {
				cap = 65536
			}
			request.MaxTokens = min(request.MaxTokens, cap)
		}
		gate, gated := agent.AttemptGateFromContext(ctx)
		if gated {
			wanted := request.MaxTokens
			request, err = gate.Admit(request)
			if err != nil {
				break
			}
			if request.MaxTokens < wanted {
				if hop > 0 {
					gate.Record(request, agent.Response{})
					err = agent.ErrBudgetExhausted
					break
				}
				budget = request.MaxTokens
				generation["maxOutputTokens"] = budget
			}
		}
		var frameState geminiResponse
		continuation := ""
		finish = ""
		consume := func(data []byte) error {
			var frame geminiResponse
			if err := json.Unmarshal(data, &frame); err != nil {
				return errors.New("invalid Gemini response frame")
			}
			if frame.Error.Code != 0 {
				return &ProtocolError{Status: frame.Error.Code, Code: frame.Error.Status}
			}
			if frame.ModelVersion != "" {
				merged.ModelVersion = frame.ModelVersion
			}
			if frame.Usage.Total > 0 {
				frameState.Usage = frame.Usage
			}
			merged.PromptFeedback = frame.PromptFeedback
			if len(frame.Candidates) > 0 {
				candidate := frame.Candidates[0]
				parts = append(parts, candidate.Content.Parts...)
				if candidate.Finish != "" {
					finish = candidate.Finish
				}
				if candidate.Continuation != "" {
					continuation = candidate.Continuation
				}
				if handler != nil {
					for _, part := range candidate.Content.Parts {
						if part.Text != "" && !part.Thought {
							handler.OnChunk(ctx, part.Text)
						}
					}
				}
			}
			return ctx.Err()
		}
		resp, requestErr := c.postProtocol(ctx, endpoint, body, headers)
		if requestErr == nil {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				data, readErr := readBoundedBody(resp.Body)
				if readErr != nil {
					requestErr = readErr
				} else {
					requestErr = protocolError(resp, data)
				}
			} else if req.Stream {
				requestErr = readSSE(resp.Body, consume)
			} else {
				data, readErr := readBoundedBody(resp.Body)
				if readErr != nil {
					requestErr = readErr
				} else {
					requestErr = consume(data)
				}
			}
			closeErr := resp.Body.Close()
			if requestErr == nil {
				requestErr = closeErr
			}
		}
		usage := openai.Usage{PromptTokens: frameState.Usage.Input, CompletionTokens: frameState.Usage.Output + frameState.Usage.Thoughts, TotalTokens: frameState.Usage.Total}
		if gated {
			gate.Record(request, agent.Response{Model: c.getModelName(req.Model), Usage: usage})
		}
		merged.Usage.Input += frameState.Usage.Input
		merged.Usage.Output += frameState.Usage.Output
		merged.Usage.Thoughts += frameState.Usage.Thoughts
		merged.Usage.Total += frameState.Usage.Total
		if requestErr != nil {
			err = requestErr
			break
		}
		if finish != "CONTINUATION" {
			break
		}
		if continuation == "" || seenTokens[continuation] {
			err = errors.New("missing or repeated Gemini continuation token")
			break
		}
		seenTokens[continuation] = true
		body["continuationToken"] = continuation
	}
	result, validationErr := geminiResult(merged, c.getModelName(req.Model), parts, finish)
	if err != nil {
		if result != nil {
			result.FinishReason = ""
		}
		return result, err
	}
	if finish == "" || finish == "CONTINUATION" {
		if result != nil {
			result.FinishReason = ""
		}
		return result, errors.New("gemini response ended without a final finishReason")
	}
	if validationErr != nil {
		return result, validationErr
	}
	if req.Stream {
		notifyNative(ctx, result, handler)
	}
	return result, nil
}
