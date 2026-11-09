package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

func chatFinish(t task.Task) string {
	switch t.Status {
	case "completed":
		return "stop"
	case "budget_exhausted":
		return "length"
	case "cancelled":
		return "cancelled"
	case "blocked", "review", "waiting_input":
		return "requires_action"
	default:
		return "error"
	}
}
func (h *handler) persistentChat(w http.ResponseWriter, r *http.Request, in openAIChatInput, model string) {
	if h.service.Tasks() == nil {
		openAIErrorResponse(w, 503, "持久任务服务未启用", "service_unavailable", "tasks_disabled")
		return
	}
	id := in.ID
	if id == "" {
		id = r.Header.Get("Idempotency-Key")
	}
	if id == "" {
		id = newChatCompletionID()
	}
	s := in.Session
	if s == "" {
		s = "openai_" + id
	}
	if !identifier.MatchString(id) || !identifier.MatchString(s) {
		openAIErrorResponse(w, 400, "无效会话或幂等键", "invalid_request_error", "invalid_request")
		return
	}
	r.SetPathValue("session", s)
	message := chat.Message{Session: session(r), SenderID: identity(r).SenderID, ID: id, Direct: true}
	for _, m := range in.Messages {
		if m.Role == "user" {
			message.Text = m.Content
		}
		for _, image := range m.Images {
			message.Attachments = append(message.Attachments, chat.Attachment{Kind: "image", URL: image})
		}
	}
	if message.Text == "" && len(message.Attachments) == 0 {
		openAIErrorResponse(w, 400, "需要 user 消息", "invalid_request_error", "invalid_request")
		return
	}
	request := ai.OpenAIChatRequest{Model: model, Messages: in.Messages, Stream: in.Stream, Temperature: in.Temperature, MaxTokens: in.MaxTokens, ToolNames: in.ToolNames, ToolChoice: in.ToolChoice, DisableTools: in.DisableTools}
	t, err := h.service.SubmitOpenAIChatTask(r.Context(), message, request)
	if err != nil {
		openAIErrorResponse(w, 409, err.Error(), "invalid_request_error", "admission_failed")
		return
	}
	w.Header().Set("X-Saber-Session", s)
	w.Header().Set("X-Saber-Task-ID", strconv.FormatInt(t.ID, 10))
	settled := false
	defer func() {
		if !settled {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			_, _ = h.service.Tasks().Cancel(ctx, identity(r), t.ID)
		}
	}()
	controller := http.NewResponseController(w)
	send := func(value any) bool {
		data, e := json.Marshal(value)
		return e == nil && writeSSE(w, controller, "data: "+string(data)+"\n\n")
	}
	chunk := func(delta openAIChunkDelta, finish *string) bool {
		return send(openAIChunk{ID: fmt.Sprintf("chatcmpl-%d", t.ID), Object: "chat.completion.chunk", Created: t.CreatedAt.Unix(), Model: model, Choices: []openAIChunkChoice{{Index: 0, Delta: delta, FinishReason: finish}}})
	}
	if in.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		if !chunk(openAIChunkDelta{Role: "assistant"}, nil) {
			return
		}
	}
	ticker := time.NewTicker(h.pollInterval())
	defer ticker.Stop()
	after := int64(0)
	wroteContent := false
	for {
		t, err = h.service.Tasks().Get(r.Context(), session(r), t.ID)
		if err != nil {
			return
		}
		if in.Stream {
			records, e := h.service.Tasks().ReadEvents(r.Context(), session(r), t.ID, after)
			if e != nil {
				return
			}
			for _, record := range records {
				if record.Kind == agent.TextDelta && record.Text != "" {
					if !chunk(openAIChunkDelta{Content: record.Text}, nil) {
						return
					}
					wroteContent = true
				} else if record.Kind == agent.ToolStarted || record.Kind == agent.ToolFinished {
					if !send(map[string]any{"id": fmt.Sprintf("chatcmpl-%d", t.ID), "object": "chat.completion.chunk", "created": t.CreatedAt.Unix(), "model": model, "choices": []any{}, "saber_tool": record}) {
						return
					}
				}
				after = record.ID
			}
			if len(records) == 256 {
				continue
			}
		}
		if t.Status != "queued" && t.Status != "running" {
			settled = true
			finish := chatFinish(t)
			usage := openAIUsage{PromptTokens: t.Result.Usage.PromptTokens, CompletionTokens: t.Result.Usage.CompletionTokens, TotalTokens: t.Result.Usage.TotalTokens}
			if in.Stream {
				if !wroteContent && t.Result.Content != "" {
					if !chunk(openAIChunkDelta{Content: t.Result.Content}, nil) {
						return
					}
				}
				if t.Error != "" {
					if !send(openAIError{Error: openAIErrorBody{Message: t.Error, Type: "server_error", Code: t.Status}}) {
						return
					}
				}
				if !chunk(openAIChunkDelta{}, &finish) {
					return
				}
				if in.IncludeUsage {
					if !send(map[string]any{"id": fmt.Sprintf("chatcmpl-%d", t.ID), "object": "chat.completion.chunk", "created": t.CreatedAt.Unix(), "model": model, "choices": []any{}, "usage": usage}) {
						return
					}
				}
				_ = writeSSE(w, controller, "data: [DONE]\n\n")
				return
			}
			body := openAIChatCompletion{ID: fmt.Sprintf("chatcmpl-%d", t.ID), Object: "chat.completion", Created: t.CreatedAt.Unix(), Model: model, Choices: []openAIChoice{{Index: 0, Message: openAIMessage{Role: "assistant", Content: t.Result.Content}, FinishReason: finish}}, Usage: usage}
			raw, e := json.Marshal(body)
			if e != nil {
				return
			}
			var out map[string]any
			if json.Unmarshal(raw, &out) != nil {
				return
			}
			out["session"] = s
			out["task_id"] = t.ID
			out["run_status"] = t.Status
			out["saber_tools"] = h.view(t).Tools
			if t.Error != "" {
				out["error"] = openAIErrorBody{Message: t.Error, Type: "server_error", Code: t.Status}
			}
			respond(w, out)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
