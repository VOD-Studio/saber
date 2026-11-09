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
	"strings"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/command"
	"rua.plus/saber/internal/task"
)

// Info 提供不含密钥的运行状态、能力与命令目录。
type Info struct {
	Enabled      bool                     `json:"enabled"`
	Models       []ai.ChatModel           `json:"models"`
	Capabilities map[string]ai.Capability `json:"capabilities"`
	Commands     []command.Descriptor     `json:"commands"`
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
	State     *agent.TaskState   `json:"state,omitempty"`
	Budget    *agent.BudgetStats `json:"budget,omitempty"`
}

// Message 包含客户端生成的幂等键和本轮选择，不接受客户端指定的执行身份。
type Message struct {
	ID          string            `json:"id"`
	Text        string            `json:"text"`
	Model       string            `json:"model"`
	Effort      string            `json:"effort"`
	Attachments []chat.Attachment `json:"attachments,omitempty"`
}

// CommandInput 是本机可信指令请求；ID 作为有副作用命令的幂等键。
type CommandInput struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// CommandResult 返回共享命令的文本回执；空文本表示命令未产生回执。
type CommandResult struct {
	Text string `json:"text"`
}

// CommandDispatcher 把本机终端已认证的指令交给共享命令注册表；
// 身份由服务端令牌与操作系统用户确定，不接受请求体中的发送者或房间。
type CommandDispatcher interface {
	Dispatch(ctx context.Context, session chat.Session, senderID, id, text string) (reply string, handled bool, err error)
	Describe() []command.Descriptor
}

type handler struct {
	service  *ai.Service
	token    string
	commands CommandDispatcher
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// New 创建带本机令牌认证的 HTTP 入口；聊天服务可为空，此时仍可查询健康状态。
// commands 非空时启用本机共享命令入口。
func New(service *ai.Service, token string, commands ...CommandDispatcher) http.Handler {
	h := &handler{service: service, token: token}
	if len(commands) > 0 {
		h.commands = commands[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", h.info)
	// OpenAI 兼容接口不依赖任务数据库，由处理器自行确认模型能力。
	mux.HandleFunc("GET /v1/models", h.models)
	mux.HandleFunc("POST /v1/chat/completions", h.chatCompletions)
	mux.HandleFunc("POST /v1/runs", h.createRun)
	mux.HandleFunc("GET /v1/runs/{run}", h.getRun)
	mux.HandleFunc("GET /v1/runs/{run}/events", h.runEvents)
	mux.HandleFunc("POST /v1/runs/{run}/cancel", h.cancelRun)
	mux.HandleFunc("POST /v1/runs/{run}/stop", h.cancelRun)
	mux.HandleFunc("POST /v1/runs/{run}/resume", h.runControl)
	mux.HandleFunc("POST /v1/runs/{run}/approval", h.runControl)
	mux.HandleFunc("POST /v1/runs/{run}/clarify", h.runControl)
	mux.HandleFunc("POST /v1/runs/{run}/steer", h.steerRun)
	mux.HandleFunc("POST /v1/responses", h.responses)
	mux.HandleFunc("GET /v1/responses/{response}", h.responseGet)
	mux.HandleFunc("POST /v1/control/graph", h.control)
	mux.HandleFunc("POST /v1/control/questions", h.control)
	mux.HandleFunc("POST /v1/tool", h.tool)
	mux.HandleFunc("POST /v1/audio/transcriptions", h.audio)
	mux.HandleFunc("POST /v1/audio/speech", h.audio)
	mux.HandleFunc("POST /v1/audio/live", h.audio)
	mux.HandleFunc("GET /v1/audio/transcriptions/stream", h.audioStream)
	mux.HandleFunc("GET /v1/audio/speech/stream", h.audioStream)
	mux.HandleFunc("POST /v1/audio/wake", h.wake)
	mux.HandleFunc("POST /v1/audio/sessions", h.startAudioSession)
	mux.HandleFunc("DELETE /v1/audio/sessions/{task}", h.finishAudioSession)
	mux.HandleFunc("GET /v1/vault/items", h.vaultAPI)
	mux.HandleFunc("POST /v1/vault/items", h.vaultAPI)
	mux.HandleFunc("DELETE /v1/vault/items/{handle}", h.vaultAPI)
	mux.HandleFunc("POST /v1/vault/sources/{source}/unlock", h.vaultAPI)
	mux.HandleFunc("GET /v1/vault/sources", h.vaultSources)
	mux.HandleFunc("POST /v1/vault/code", h.vaultCode)
	mux.HandleFunc("GET /v1/sessions", h.sessions)
	mux.HandleFunc("GET /v1/sessions/{session}/tasks", h.history)
	mux.HandleFunc("POST /v1/sessions/{session}/messages", h.submit)
	mux.HandleFunc("POST /v1/sessions/{session}/commands", h.command)
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
		if service != nil && service.Config() != nil {
			cfg := service.Config().API
			if !cfg.Enabled && r.URL.Path != "/v1/info" {
				http.Error(w, "Agent API 未启用", http.StatusServiceUnavailable)
				return
			}
			if cfg.MaxRequestBytes > 0 {
				r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxRequestBytes)
			}
		}
		// 本机命令入口由自己的分发器判定是否可用，ping/help 等命令不依赖 AI 与任务。
		// OpenAI 兼容接口只要求模型能力，不要求任务数据库。
		openAI := r.URL.Path == "/v1/models" || r.URL.Path == "/v1/chat/completions"
		privateVault := service != nil && strings.HasPrefix(r.URL.Path, "/v1/vault/")
		if !openAI && !privateVault && r.URL.Path != "/v1/info" && !strings.HasSuffix(r.URL.Path, "/commands") && (service == nil || service.Tasks() == nil) {
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
	result := Turn{ID: t.ID, Session: t.Message.Session.Conversation, RequestID: t.Message.ID, Input: t.Message.Text, Content: t.Result.Content, Status: t.Status, Error: t.Error, Model: t.Request.Model, Effort: effort, Tokens: t.Result.Usage.TotalTokens, Duration: t.Result.Duration, CreatedAt: t.CreatedAt, State: t.Result.State, Budget: t.Result.Budget}
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
func (h *handler) info(w http.ResponseWriter, r *http.Request) {
	info := Info{Models: []ai.ChatModel{}, Capabilities: map[string]ai.Capability{}, Commands: []command.Descriptor{}}
	if h.service != nil {
		info.Enabled = h.service.Tasks() != nil
		info.Models = h.service.ChatModels()
		// 本机诊断会话只用于能力查询，不伪装成已有用户会话。
		diagnostic := chat.Identity{Session: chat.Session{Platform: "terminal", Account: "saber", Conversation: "status"}, SenderID: strconv.Itoa(os.Getuid())}
		info.Capabilities = h.service.Capabilities(r.Context(), diagnostic)
	} else {
		info.Capabilities = ai.DisabledCapabilities("AI 服务未启用")
	}
	if h.commands != nil {
		info.Commands = h.commands.Describe()
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
	t, err := h.service.SubmitChatTask(r.Context(), chat.Message{Session: session(r), SenderID: identity(r).SenderID, ID: input.ID, Text: input.Text, Attachments: input.Attachments}, input.Model, input.Effort)
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
func (h *handler) command(w http.ResponseWriter, r *http.Request) {
	if !identifier.MatchString(r.PathValue("session")) {
		http.Error(w, "无效会话编号", http.StatusBadRequest)
		return
	}
	if h.commands == nil {
		http.Error(w, "本机命令入口未启用", http.StatusServiceUnavailable)
		return
	}
	var input CommandInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "无效指令: "+err.Error(), http.StatusBadRequest)
		return
	}
	if decoder.Decode(new(any)) != io.EOF || !identifier.MatchString(input.ID) || strings.TrimSpace(input.Text) == "" {
		http.Error(w, "需要有效的指令编号和单个 JSON 对象", http.StatusBadRequest)
		return
	}
	reply, handled, err := h.commands.Dispatch(r.Context(), session(r), identity(r).SenderID, input.ID, input.Text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !handled {
		http.Error(w, "不是已知命令，请以 / 或 ! 开头", http.StatusBadRequest)
		return
	}
	respond(w, CommandResult{Text: reply})
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
	ticker := time.NewTicker(h.pollInterval())
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
