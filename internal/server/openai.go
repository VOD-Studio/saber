package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/ai"
)

const (
	// maxOpenAIBodyBytes 限制 OpenAI 兼容请求体，避免无界内存占用。
	maxOpenAIBodyBytes = 1 << 20
	// maxOpenAIMessages 限制单次请求的消息条数。
	maxOpenAIMessages = 100
	// maxOpenAIMessageBytes 限制单条消息的文本长度。
	maxOpenAIMessageBytes = 128 << 10
)

// openAIModel 是 /v1/models 列表中的单个模型。
type openAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// openAIModelList 是 /v1/models 的响应。
type openAIModelList struct {
	Object string        `json:"object"`
	Data   []openAIModel `json:"data"`
}

// openAIErrorBody 是 OpenAI 风格错误对象，不包含密钥或内部堆栈。
type openAIErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// openAIError 是 OpenAI 风格错误响应外层。
type openAIError struct {
	Error openAIErrorBody `json:"error"`
}

// openAIMessage 是响应中的助手消息。
type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openAIChoice 是完整响应中的一个候选。
type openAIChoice struct {
	Index        int           `json:"index"`
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// openAIUsage 是响应中的用量统计。
type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// openAIChatCompletion 是 /v1/chat/completions 的非流式响应。
type openAIChatCompletion struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   openAIUsage    `json:"usage"`
}

// openAIChunkDelta 是流式增量；首块只带 role，后续块只带 content。
type openAIChunkDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// openAIChunkChoice 是流式响应中的一个候选。
type openAIChunkChoice struct {
	Index        int              `json:"index"`
	Delta        openAIChunkDelta `json:"delta"`
	FinishReason *string          `json:"finish_reason"`
}

// openAIChunk 是流式响应的单个数据块。
type openAIChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []openAIChunkChoice `json:"choices"`
}

// openAIChatInput 是校验后的 HTTP 请求，消息已限制为纯文本。
type openAIChatInput struct {
	Model       string
	Messages    []ai.OpenAIMessage
	Stream      bool
	Temperature *float64
	MaxTokens   int
}

// openAIEnabled 判断服务是否具备模型能力；新接口不要求任务数据库。
func (h *handler) openAIEnabled() bool {
	return h.service != nil && h.service.IsEnabled()
}

// models 返回 OpenAI 风格的模型列表，默认模型排在最前。
func (h *handler) models(w http.ResponseWriter, r *http.Request) {
	if !h.openAIEnabled() {
		openAIErrorResponse(w, http.StatusServiceUnavailable, "AI 服务未启用", "service_unavailable", "ai_disabled")
		return
	}
	created := time.Now().Unix()
	models := h.service.ChatModels()
	data := make([]openAIModel, 0, len(models))
	for _, model := range models {
		if model.Default {
			data = append(data, openAIModel{ID: model.ID, Object: "model", Created: created, OwnedBy: "saber"})
		}
	}
	for _, model := range models {
		if !model.Default {
			data = append(data, openAIModel{ID: model.ID, Object: "model", Created: created, OwnedBy: "saber"})
		}
	}
	respond(w, openAIModelList{Object: "list", Data: data})
}

// chatCompletions 提供非任务、不持久化的 OpenAI 兼容聊天入口。
func (h *handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if !h.openAIEnabled() {
		openAIErrorResponse(w, http.StatusServiceUnavailable, "AI 服务未启用", "service_unavailable", "ai_disabled")
		return
	}
	input, apiError, status := decodeOpenAIChatRequest(w, r)
	if apiError != nil {
		openAIErrorResponse(w, status, apiError.Message, apiError.Type, apiError.Code)
		return
	}
	model, err := h.service.ResolveOpenAIModel(input.Model)
	if err != nil {
		if errors.Is(err, ai.ErrOpenAIModelNotFound) {
			message := fmt.Sprintf("模型 %q 不存在", input.Model)
			if input.Model == "" {
				message = "没有可用的默认模型"
			}
			openAIErrorResponse(w, http.StatusNotFound, message, "invalid_request_error", "model_not_found")
			return
		}
		openAIErrorResponse(w, http.StatusInternalServerError, "模型解析失败", "server_error", "model_resolution_failed")
		return
	}
	request := ai.OpenAIChatRequest{
		Model:       model,
		Messages:    input.Messages,
		Stream:      input.Stream,
		Temperature: input.Temperature,
		MaxTokens:   input.MaxTokens,
	}
	if input.Stream {
		h.streamChat(w, r, request)
		return
	}
	h.completeChat(w, r, request)
}

// completeChat 运行一次同步 Agent 并返回完整响应。
func (h *handler) completeChat(w http.ResponseWriter, r *http.Request, request ai.OpenAIChatRequest) {
	result, err := h.service.RunOpenAIChat(r.Context(), request, nil)
	if err != nil {
		writeOpenAIRunError(w, err)
		return
	}
	if result.Status != agent.Completed {
		openAIErrorResponse(w, http.StatusBadGateway, "模型运行未完成", "server_error", "incomplete_response")
		return
	}
	respond(w, openAIChatCompletion{
		ID:      newChatCompletionID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   result.Model,
		Choices: []openAIChoice{{Index: 0, Message: openAIMessage{Role: "assistant", Content: result.Content}, FinishReason: "stop"}},
		Usage:   openAIUsage{PromptTokens: result.Usage.PromptTokens, CompletionTokens: result.Usage.CompletionTokens, TotalTokens: result.Usage.TotalTokens},
	})
}

// streamChat 以 SSE 转发 Agent 的文本增量；断开连接会取消运行。
func (h *handler) streamChat(w http.ResponseWriter, r *http.Request, request ai.OpenAIChatRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// emit 必须在运行 goroutine 内快速返回，缓冲不足或调用方停读时由 ctx 解除阻塞。
	events := make(chan agent.Event, 64)
	type outcome struct {
		result ai.OpenAIChatResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		defer close(events)
		result, err := h.service.RunOpenAIChat(ctx, request, func(event agent.Event) {
			if event.Kind != agent.TextDelta {
				return
			}
			select {
			case events <- event:
			case <-ctx.Done():
			}
		})
		done <- outcome{result: result, err: err}
	}()

	id := newChatCompletionID()
	created := time.Now().Unix()
	send := func(value any) bool {
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		return writeSSE(w, controller, "data: "+string(data)+"\n\n")
	}
	sendRaw := func(payload string) bool {
		return writeSSE(w, controller, "data: "+payload+"\n\n")
	}
	chunk := func(delta openAIChunkDelta, finish *string) bool {
		return send(openAIChunk{
			ID: id, Object: "chat.completion.chunk", Created: created, Model: request.Model,
			Choices: []openAIChunkChoice{{Index: 0, Delta: delta, FinishReason: finish}},
		})
	}

	if !chunk(openAIChunkDelta{Role: "assistant"}, nil) {
		cancel()
		return
	}
	wroteContent := false
	for event := range events {
		if event.Text == "" {
			continue
		}
		if !chunk(openAIChunkDelta{Content: event.Text}, nil) {
			cancel()
			return
		}
		wroteContent = true
	}
	result := <-done
	if result.err != nil || result.result.Status != agent.Completed {
		message, code := "模型运行失败", "upstream_error"
		if errors.Is(result.err, context.DeadlineExceeded) {
			message, code = "模型运行超时", "timeout"
		}
		if !send(openAIError{Error: openAIErrorBody{Message: message, Type: "server_error", Code: code}}) {
			return
		}
		_ = sendRaw("[DONE]")
		return
	}
	if !wroteContent && result.result.Content != "" {
		if !chunk(openAIChunkDelta{Content: result.result.Content}, nil) {
			return
		}
	}
	stop := "stop"
	if !chunk(openAIChunkDelta{}, &stop) {
		return
	}
	_ = sendRaw("[DONE]")
}

// writeOpenAIRunError 把运行错误映射为 OpenAI 错误响应，不暴露内部细节。
func writeOpenAIRunError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ai.ErrOpenAIModelNotFound):
		openAIErrorResponse(w, http.StatusNotFound, "模型不存在", "invalid_request_error", "model_not_found")
	case errors.Is(err, context.Canceled):
		// 客户端已断开，没有可写的响应。
	case errors.Is(err, context.DeadlineExceeded):
		openAIErrorResponse(w, http.StatusGatewayTimeout, "模型运行超时", "server_error", "timeout")
	default:
		slog.Warn("OpenAI 兼容接口运行失败", "error", err)
		openAIErrorResponse(w, http.StatusBadGateway, "模型运行失败", "server_error", "upstream_error")
	}
}

// openAIErrorResponse 写出 OpenAI 风格错误，响应体不含密钥或内部堆栈。
func openAIErrorResponse(w http.ResponseWriter, status int, message, kind, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(openAIError{Error: openAIErrorBody{Message: message, Type: kind, Code: code}}); err != nil {
		slog.Debug("写出 OpenAI 错误响应失败", "error", err)
	}
}

// writeSSE 写入一个完整的 SSE 事件并刷新，失败表示连接已不可用。
func writeSSE(w http.ResponseWriter, controller *http.ResponseController, payload string) bool {
	if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return false
	}
	if _, err := io.WriteString(w, payload); err != nil {
		return false
	}
	return controller.Flush() == nil
}

// newChatCompletionID 生成不可预测的响应编号。
func newChatCompletionID() string {
	var buffer [12]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	return "chatcmpl-" + hex.EncodeToString(buffer[:])
}

// decodeOpenAIChatRequest 读取并校验请求；返回的 apiError 非空时 status 可用。
func decodeOpenAIChatRequest(w http.ResponseWriter, r *http.Request) (openAIChatInput, *openAIErrorBody, int) {
	var input openAIChatInput
	body := http.MaxBytesReader(w, r.Body, maxOpenAIBodyBytes)
	data, err := io.ReadAll(body)
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			return input, &openAIErrorBody{Message: "请求体超过 1 MiB", Type: "invalid_request_error", Code: "request_too_large"}, http.StatusRequestEntityTooLarge
		}
		return input, &openAIErrorBody{Message: "无法读取请求体", Type: "invalid_request_error", Code: "invalid_request"}, http.StatusBadRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var raw map[string]json.RawMessage
	if err = decoder.Decode(&raw); err != nil {
		return input, &openAIErrorBody{Message: "无效 JSON 请求体", Type: "invalid_request_error", Code: "invalid_json"}, http.StatusBadRequest
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return input, &openAIErrorBody{Message: "请求体必须是单个 JSON 对象", Type: "invalid_request_error", Code: "invalid_request"}, http.StatusBadRequest
	}

	// 只接受本版本实现的参数；其余字段显式拒绝，避免静默忽略客户端意图。
	for key, value := range raw {
		switch key {
		case "model", "messages", "stream", "temperature", "max_tokens":
		case "n":
			var count int
			if err = json.Unmarshal(value, &count); err != nil || count != 1 {
				return input, &openAIErrorBody{Message: "仅支持 n=1", Type: "invalid_request_error", Code: "unsupported_parameter"}, http.StatusBadRequest
			}
		default:
			return input, &openAIErrorBody{Message: fmt.Sprintf("不支持的参数 %q", key), Type: "invalid_request_error", Code: "unsupported_parameter"}, http.StatusBadRequest
		}
	}

	if value, ok := raw["model"]; ok {
		if err = json.Unmarshal(value, &input.Model); err != nil {
			return input, &openAIErrorBody{Message: "model 必须是字符串", Type: "invalid_request_error", Code: "invalid_request"}, http.StatusBadRequest
		}
	}
	if value, ok := raw["stream"]; ok {
		if err = json.Unmarshal(value, &input.Stream); err != nil {
			return input, &openAIErrorBody{Message: "stream 必须是布尔值", Type: "invalid_request_error", Code: "invalid_request"}, http.StatusBadRequest
		}
	}
	if value, ok := raw["temperature"]; ok {
		var temperature float64
		if err = json.Unmarshal(value, &temperature); err != nil {
			return input, &openAIErrorBody{Message: "temperature 必须是数字", Type: "invalid_request_error", Code: "invalid_request"}, http.StatusBadRequest
		}
		if temperature < 0 || temperature > 2 {
			return input, &openAIErrorBody{Message: "temperature 必须在 0 到 2 之间", Type: "invalid_request_error", Code: "invalid_request"}, http.StatusBadRequest
		}
		input.Temperature = &temperature
	}
	if value, ok := raw["max_tokens"]; ok {
		if err = json.Unmarshal(value, &input.MaxTokens); err != nil || input.MaxTokens <= 0 {
			return input, &openAIErrorBody{Message: "max_tokens 必须是正整数", Type: "invalid_request_error", Code: "invalid_request"}, http.StatusBadRequest
		}
	}

	messages, apiError := decodeOpenAIMessages(raw["messages"])
	if apiError != nil {
		return input, apiError, http.StatusBadRequest
	}
	input.Messages = messages
	return input, nil, 0
}

// decodeOpenAIMessages 校验消息列表只包含受支持的文本角色。
func decodeOpenAIMessages(value json.RawMessage) ([]ai.OpenAIMessage, *openAIErrorBody) {
	if len(value) == 0 {
		return nil, &openAIErrorBody{Message: "messages 不能为空", Type: "invalid_request_error", Code: "invalid_request"}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(value, &items); err != nil {
		return nil, &openAIErrorBody{Message: "messages 必须是数组", Type: "invalid_request_error", Code: "invalid_request"}
	}
	if len(items) == 0 {
		return nil, &openAIErrorBody{Message: "messages 不能为空", Type: "invalid_request_error", Code: "invalid_request"}
	}
	if len(items) > maxOpenAIMessages {
		return nil, &openAIErrorBody{Message: fmt.Sprintf("messages 最多 %d 条", maxOpenAIMessages), Type: "invalid_request_error", Code: "too_many_messages"}
	}
	messages := make([]ai.OpenAIMessage, 0, len(items))
	for index, item := range items {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item, &fields); err != nil {
			return nil, &openAIErrorBody{Message: fmt.Sprintf("messages[%d] 必须是对象", index), Type: "invalid_request_error", Code: "invalid_request"}
		}
		for key := range fields {
			if key != "role" && key != "content" {
				return nil, &openAIErrorBody{Message: fmt.Sprintf("不支持的消息字段 messages[%d].%s", index, key), Type: "invalid_request_error", Code: "unsupported_parameter"}
			}
		}
		var role string
		if err := json.Unmarshal(fields["role"], &role); err != nil {
			return nil, &openAIErrorBody{Message: fmt.Sprintf("messages[%d].role 必须是字符串", index), Type: "invalid_request_error", Code: "invalid_request"}
		}
		switch role {
		case "system", "user", "assistant":
		default:
			return nil, &openAIErrorBody{Message: fmt.Sprintf("不支持的消息角色 %q", role), Type: "invalid_request_error", Code: "unsupported_role"}
		}
		rawContent := fields["content"]
		if len(rawContent) == 0 || rawContent[0] != '"' {
			return nil, &openAIErrorBody{Message: fmt.Sprintf("messages[%d].content 只支持字符串", index), Type: "invalid_request_error", Code: "unsupported_content"}
		}
		var content string
		if err := json.Unmarshal(rawContent, &content); err != nil {
			return nil, &openAIErrorBody{Message: fmt.Sprintf("messages[%d].content 只支持字符串", index), Type: "invalid_request_error", Code: "unsupported_content"}
		}
		if len(content) > maxOpenAIMessageBytes {
			return nil, &openAIErrorBody{Message: fmt.Sprintf("messages[%d].content 超过 128 KiB", index), Type: "invalid_request_error", Code: "message_too_large"}
		}
		messages = append(messages, ai.OpenAIMessage{Role: role, Content: content})
	}
	return messages, nil
}
