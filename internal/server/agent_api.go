package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

type runInput struct {
	Session     string            `json:"session"`
	ID          string            `json:"id"`
	Text        string            `json:"text"`
	Model       string            `json:"model"`
	Effort      string            `json:"effort"`
	CWD         string            `json:"cwd"`
	MCPServers  []string          `json:"mcp_servers"`
	Attachments []chat.Attachment `json:"attachments,omitempty"`
}

func decodeAPI(w http.ResponseWriter, r *http.Request, value any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("需要单个 JSON 对象")
	}
	return nil
}
func (h *handler) createRun(w http.ResponseWriter, r *http.Request) {
	var in runInput
	if err := decodeAPI(w, r, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if in.ID == "" {
		in.ID = r.Header.Get("Idempotency-Key")
	}
	if in.ID == "" {
		in.ID = newChatCompletionID()
	}
	if in.Session == "" {
		in.Session = "api_" + in.ID
	}
	if !identifier.MatchString(in.Session) || !identifier.MatchString(in.ID) {
		http.Error(w, "无效会话或幂等键", http.StatusBadRequest)
		return
	}
	r.SetPathValue("session", in.Session)
	message := chat.Message{Session: session(r), SenderID: identity(r).SenderID, ID: in.ID, Text: in.Text, Attachments: in.Attachments}
	t, err := h.service.SubmitAPIChatTask(r.Context(), message, in.Model, in.Effort, in.CWD, in.MCPServers)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	respond(w, h.view(t))
}
func (h *handler) getRun(w http.ResponseWriter, r *http.Request) {
	if err := h.bindRunPaths(r); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	id, err := taskID(r)
	if err != nil {
		http.Error(w, "无效任务编号", http.StatusBadRequest)
		return
	}
	t, err := h.service.Tasks().Get(r.Context(), session(r), id)
	if err != nil {
		http.Error(w, "任务不存在", http.StatusNotFound)
		return
	}
	respond(w, h.view(t))
}
func (h *handler) runEvents(w http.ResponseWriter, r *http.Request) {
	if err := h.bindRunPaths(r); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	h.events(w, r)
}
func (h *handler) cancelRun(w http.ResponseWriter, r *http.Request) {
	if err := h.bindRunPaths(r); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	h.cancel(w, r)
}

type controlInput struct {
	Session   string         `json:"session"`
	Arguments map[string]any `json:"arguments"`
}

func (h *handler) control(w http.ResponseWriter, r *http.Request) {
	var in controlInput
	if err := decodeAPI(w, r, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !identifier.MatchString(in.Session) {
		http.Error(w, "无效会话编号", http.StatusBadRequest)
		return
	}
	r.SetPathValue("session", in.Session)
	ctx := chat.WithIdentity(r.Context(), identity(r))
	var out any
	var err error
	if strings.HasSuffix(r.URL.Path, "questions") {
		out, err = h.service.Tasks().QuestionTool(ctx, in.Arguments)
	} else {
		out, err = h.service.Tasks().GraphTool(ctx, in.Arguments)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	respond(w, out)
}
func (h *handler) tool(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Session   string `json:"session"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		TaskID    int64  `json:"task_id"`
	}
	// 接受结构化参数，不允许请求携带身份。
	var raw struct {
		Session   string          `json:"session"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		TaskID    int64           `json:"task_id"`
	}
	if err := decodeAPI(w, r, &raw); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	in.Session = raw.Session
	in.Name = raw.Name
	in.Arguments = string(raw.Arguments)
	in.TaskID = raw.TaskID
	if !identifier.MatchString(in.Session) || in.TaskID <= 0 {
		http.Error(w, "需要会话和持久任务编号", http.StatusBadRequest)
		return
	}
	r.SetPathValue("session", in.Session)
	out, err := h.service.CallTaskTool(r.Context(), identity(r), in.TaskID, in.Name, in.Arguments)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	respond(w, out)
}

func responseEnvelope(t task.Task) map[string]any {
	status := "in_progress"
	switch t.Status {
	case "queued":
		status = "queued"
	case "completed", "succeeded", "done":
		status = "completed"
	case "cancelled", "canceled":
		status = "cancelled"
	case "failed", "interrupted", "timed_out":
		status = "failed"
	case "budget_exhausted", "blocked", "review", "waiting_input":
		status = "incomplete"
	}
	if t.Status != "queued" && t.Status != "running" && status == "in_progress" {
		if t.Error != "" {
			status = "failed"
		} else {
			status = "incomplete"
		}
	}
	output := []any{map[string]any{"id": fmt.Sprintf("msg_%d", t.ID), "type": "message", "role": "assistant", "status": status, "content": []any{map[string]any{"type": "output_text", "text": t.Result.Content, "annotations": []any{}}}}}
	for _, round := range t.Result.Rounds {
		for _, tool := range round.Tools {
			output = append(output, map[string]any{"type": "function_call", "call_id": tool.Call.ID, "name": tool.Call.Function.Name, "arguments": tool.Call.Function.Arguments}, map[string]any{"type": "function_call_output", "call_id": tool.Call.ID, "output": tool.Content, "error": tool.ErrorCode})
		}
	}
	return map[string]any{"id": fmt.Sprintf("resp_%d", t.ID), "object": "response", "created_at": t.CreatedAt.Unix(), "status": status, "run_status": t.Status, "model": t.Request.Model, "output": output, "output_text": t.Result.Content, "error": t.Error, "usage": map[string]any{"total_tokens": t.Result.Usage.TotalTokens}, "conversation": t.Message.Session.Conversation}
}
func (h *handler) responseGet(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("run", strings.TrimPrefix(r.PathValue("response"), "resp_"))
	if err := h.bindRunPaths(r); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	id, err := taskID(r)
	if err != nil {
		http.Error(w, "无效响应编号", http.StatusBadRequest)
		return
	}
	t, err := h.service.Tasks().Get(r.Context(), session(r), id)
	if err != nil {
		http.Error(w, "响应不存在", http.StatusNotFound)
		return
	}
	respond(w, responseEnvelope(t))
}
func (h *handler) responses(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Input        json.RawMessage `json:"input"`
		Model        string          `json:"model"`
		Stream       bool            `json:"stream"`
		Background   bool            `json:"background"`
		Conversation string          `json:"conversation"`
		Previous     string          `json:"previous_response_id"`
		Instructions string          `json:"instructions"`
	}
	if err := decodeAPI(w, r, &in); err != nil {
		openAIErrorResponse(w, 400, err.Error(), "invalid_request_error", "invalid_request")
		return
	}
	text, attachments, err := responseInput(in.Input)
	if err != nil {
		openAIErrorResponse(w, 400, err.Error(), "invalid_request_error", "invalid_input")
		return
	}
	id := r.Header.Get("Idempotency-Key")
	if id == "" {
		id = newChatCompletionID()
	}
	s := in.Conversation
	if s == "" {
		s = "api_" + id
	}
	if !identifier.MatchString(s) || !identifier.MatchString(id) {
		http.Error(w, "无效会话或幂等键", http.StatusBadRequest)
		return
	}
	r.SetPathValue("session", s)
	if in.Previous != "" {
		n, e := strconv.ParseInt(strings.TrimPrefix(in.Previous, "resp_"), 10, 64)
		if e != nil {
			http.Error(w, "无效 previous_response_id", http.StatusBadRequest)
			return
		}
		if _, e = h.service.Tasks().Get(r.Context(), session(r), n); e != nil {
			http.Error(w, "前序响应不存在于本会话", http.StatusNotFound)
			return
		}
		history, e := h.service.Tasks().History(r.Context(), session(r))
		if e != nil {
			http.Error(w, "读取会话失败", http.StatusInternalServerError)
			return
		}
		if len(history) > 0 && history[len(history)-1].ID != n && history[len(history)-1].Message.ID != id {
			http.Error(w, "前序响应不是当前会话末端；请使用最新响应或新会话", http.StatusConflict)
			return
		}
	}
	if in.Instructions != "" {
		text = in.Instructions + "\n\n" + text
	}
	t, err := h.service.SubmitAPIChatTask(r.Context(), chat.Message{Session: session(r), SenderID: identity(r).SenderID, ID: id, Text: text, Attachments: attachments}, in.Model, "", "", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if in.Background && !in.Stream {
		respond(w, responseEnvelope(t))
		return
	}
	controller := http.NewResponseController(w)
	if in.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		if sendEvent(w, controller, "response.created", 0, map[string]any{"type": "response.created", "response": responseEnvelope(t)}) != nil {
			return
		}
	}
	ticker := time.NewTicker(h.pollInterval())
	defer ticker.Stop()
	after := int64(0)
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
				if responseEvent(w, controller, t, record) != nil {
					return
				}
				after = record.ID
			}
			if len(records) == 256 {
				continue
			}
		}
		if t.Status != "queued" && t.Status != "running" {
			if in.Stream {
				env := responseEnvelope(t)
				kind := "response." + env["status"].(string)
				_ = sendEvent(w, controller, kind, 0, map[string]any{"type": kind, "response": env})
			} else {
				respond(w, responseEnvelope(t))
			}
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
func responseInput(raw json.RawMessage) (string, []chat.Attachment, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if strings.TrimSpace(text) == "" {
			return "", nil, fmt.Errorf("input 不能为空")
		}
		return text, nil, nil
	}
	var items []struct {
		Role    string          `json:"role"`
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", nil, fmt.Errorf("input 必须为文本或消息数组")
	}
	var parts []string
	var attachments []chat.Attachment
	for _, item := range items {
		if item.Role != "user" && item.Role != "assistant" && item.Role != "system" && item.Role != "developer" {
			return "", nil, fmt.Errorf("不支持 input role %q", item.Role)
		}
		var s string
		if json.Unmarshal(item.Content, &s) == nil {
			parts = append(parts, s)
			continue
		}
		var blocks []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL string `json:"image_url"`
		}
		if json.Unmarshal(item.Content, &blocks) != nil {
			return "", nil, fmt.Errorf("无效消息内容")
		}
		for _, b := range blocks {
			switch b.Type {
			case "input_text", "output_text", "text":
				parts = append(parts, b.Text)
			case "input_image":
				attachments = append(attachments, chat.Attachment{Kind: "image", URL: b.ImageURL})
			default:
				return "", nil, fmt.Errorf("不支持内容类型 %q", b.Type)
			}
		}
	}
	if len(parts) == 0 && len(attachments) == 0 {
		return "", nil, fmt.Errorf("input 不能为空")
	}
	return strings.Join(parts, "\n"), attachments, nil
}
