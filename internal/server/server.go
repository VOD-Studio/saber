// Package server 提供常驻 Saber 的本机会话接口和可续读事件流。
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

// Info 提供不含密钥的运行状态和模型目录。
type Info struct {
	Enabled bool           `json:"enabled"`
	Models  []ai.ChatModel `json:"models"`
}

// Turn 是供界面展示的任务快照，不包含系统提示和原始模型协议。
type Turn struct {
	ID        int64              `json:"id"`
	Session   string             `json:"session"`
	RequestID string             `json:"request_id"`
	Input     string             `json:"input"`
	Content   string             `json:"content"`
	Status    string             `json:"status"`
	Error     string             `json:"error,omitempty"`
	Model     string             `json:"model"`
	Effort    string             `json:"effort"`
	Tools     []agent.ToolRecord `json:"tools,omitempty"`
	Tokens    int                `json:"tokens"`
	Duration  time.Duration      `json:"duration"`
	CreatedAt time.Time          `json:"created_at"`
}

// Message 包含客户端生成的幂等键和本轮选择，不接受客户端指定的执行身份。
type Message struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

type handler struct {
	service *ai.Service
	token   string
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// New 创建带本机令牌认证的 HTTP 入口；聊天服务可为空，此时仍可查询健康状态。
func New(service *ai.Service, token string) http.Handler {
	h := &handler{service: service, token: token}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", h.info)
	mux.HandleFunc("GET /v1/sessions", h.sessions)
	mux.HandleFunc("GET /v1/sessions/{session}/tasks", h.history)
	mux.HandleFunc("POST /v1/sessions/{session}/messages", h.submit)
	mux.HandleFunc("GET /v1/sessions/{session}/tasks/{task}/events", h.events)
	mux.HandleFunc("POST /v1/sessions/{session}/tasks/{task}/cancel", h.cancel)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.token)) != 1 {
			http.Error(w, "需要 Saber 服务端令牌", http.StatusUnauthorized)
			return
		}
		// 本机入口不接受网页跨源调用；令牌不放入 URL 或日志。
		if r.Header.Get("Origin") != "" {
			http.Error(w, "不接受浏览器跨源请求", http.StatusForbidden)
			return
		}
		if r.URL.Path != "/v1/info" && (service == nil || service.Tasks() == nil) {
			http.Error(w, "请配置 ai.enabled 和模型后重启服务", http.StatusServiceUnavailable)
			return
		}

		mux.ServeHTTP(w, r)
	})
}

func session(r *http.Request) chat.Session {
	return chat.Session{Platform: "terminal", Account: "saber", Conversation: r.PathValue("session")}
}
func identity(r *http.Request) chat.Identity {
	return chat.Identity{Session: session(r), SenderID: strconv.Itoa(os.Getuid())}
}
func (h *handler) view(t task.Task) Turn {
	effort := t.Request.ReasoningEffort
	if effort == "" {
		for _, model := range h.service.ChatModels() {
			if model.ID == t.Request.Model {
				effort = model.ReasoningEffort
				break
			}
		}
	}
	result := Turn{ID: t.ID, Session: t.Message.Session.Conversation, RequestID: t.Message.ID, Input: t.Message.Text, Content: t.Result.Content, Status: t.Status, Error: t.Error, Model: t.Request.Model, Effort: effort, Tokens: t.Result.Usage.TotalTokens, Duration: t.Result.Duration, CreatedAt: t.CreatedAt}
	for _, round := range t.Result.Rounds {
		result.Tools = append(result.Tools, round.Tools...)
	}
	return result
}
func respond(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}
func (h *handler) info(w http.ResponseWriter, _ *http.Request) {
	info := Info{Models: []ai.ChatModel{}}
	if h.service != nil {
		info.Enabled = h.service.Tasks() != nil
		info.Models = h.service.ChatModels()
	}
	respond(w, info)
}
func (h *handler) sessions(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.service.Tasks().Conversations(r.Context(), "terminal", "saber")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.turns(w, tasks)
}
func (h *handler) turns(w http.ResponseWriter, tasks []task.Task) {
	turns := make([]Turn, 0, len(tasks))
	for _, t := range tasks {
		turns = append(turns, h.view(t))
	}
	respond(w, turns)
}
func (h *handler) history(w http.ResponseWriter, r *http.Request) {
	if !identifier.MatchString(r.PathValue("session")) {
		http.Error(w, "无效会话编号", http.StatusBadRequest)
		return
	}
	tasks, err := h.service.Tasks().History(r.Context(), session(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.turns(w, tasks)
}
func (h *handler) submit(w http.ResponseWriter, r *http.Request) {
	if !identifier.MatchString(r.PathValue("session")) {
		http.Error(w, "无效会话编号", http.StatusBadRequest)
		return
	}
	var input Message
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "无效消息: "+err.Error(), http.StatusBadRequest)
		return
	}
	if decoder.Decode(new(any)) != io.EOF || !identifier.MatchString(input.ID) {
		http.Error(w, "需要有效的消息编号和单个 JSON 对象", http.StatusBadRequest)
		return
	}
	t, err := h.service.SubmitChatTask(r.Context(), chat.Message{Session: session(r), SenderID: identity(r).SenderID, ID: input.ID, Text: input.Text}, input.Model, input.Effort)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, task.ErrBusy) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	respond(w, h.view(t))
}
func taskID(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("task"), 10, 64) }
func (h *handler) cancel(w http.ResponseWriter, r *http.Request) {
	id, err := taskID(r)
	if err != nil {
		http.Error(w, "无效任务编号", http.StatusBadRequest)
		return
	}
	t, err := h.service.Tasks().Cancel(r.Context(), identity(r), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	respond(w, h.view(t))
}
func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	id, err := taskID(r)
	if err != nil {
		http.Error(w, "无效任务编号", http.StatusBadRequest)
		return
	}
	after := int64(0)
	cursor := r.Header.Get("Last-Event-ID")
	if cursor != "" {
		after, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || after < 0 {
			http.Error(w, "无效事件游标", http.StatusBadRequest)
			return
		}
	}
	manager := h.service.Tasks()
	if _, err = manager.Get(r.Context(), session(r), id); err != nil {
		http.Error(w, "任务不存在", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	controller := http.NewResponseController(w)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	previousStatus := ""
	for {
		// 先读状态再读事件，确保终态之前的增量全部发出。
		t, err := manager.Get(r.Context(), session(r), id)
		if err != nil {
			return
		}
		records, err := manager.ReadEvents(r.Context(), session(r), id, after)
		if err != nil {
			return
		}
		for _, record := range records {
			if err = sendEvent(w, controller, "event", record.ID, record); err != nil {
				return
			}
			after = record.ID
		}
		terminal := t.Status != "queued" && t.Status != "running"
		if terminal && len(records) == 256 {
			continue
		}
		if t.Status != previousStatus {
			if err = sendEvent(w, controller, "task", 0, h.view(t)); err != nil {
				return
			}
			previousStatus = t.Status
		}
		if terminal {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
func sendEvent(w http.ResponseWriter, controller *http.ResponseController, kind string, id int64, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if id > 0 {
		if _, err = fmt.Fprintf(w, "id: %d\n", id); err != nil {
			return err
		}
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data); err != nil {
		return err
	}
	return controller.Flush()
}

// Serve 在取消时关闭 HTTP 连接；任务取消和收尾由应用生命周期统一处理。
func Serve(ctx context.Context, srv *http.Server, listener net.Listener) error {
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		err := srv.Close()
		serveErr := <-done
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(err, serveErr)
	}
}
