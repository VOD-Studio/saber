package violetplatform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// 测试用的固定身份与会话 ID。
const (
	testBotUserID    = "bot-user-1"
	testBotUsername  = "saber"
	testDirectRoom   = "conv-direct"
	testGroupRoom    = "conv-room"
	testToken        = "violet_bot_test"
	testEditInterval = 20 * time.Millisecond
)

// sentMessage 是一次出站发送的观测记录。
type sentMessage struct {
	Conversation string
	Content      string
	ReplyTo      string
	Idempotency  string
	BotReply     *botReplyDTO
}

// editedMessage 是一次出站编辑的观测记录。
type editedMessage struct {
	Conversation string
	MessageID    string
	Content      string
	BotReply     *botReplyDTO
	At           time.Time
}

// fakeViolet 是 Violet Bot API 的内存假服务端。
//
// 它按真实契约做事：Idempotency-Key 缺失即 400、非本人消息不可编辑、
// 历史按 created_at 倒序 + cursor 翻页、SSE 帧带 event:/data: 与注释心跳。
// 只测 Saber 用到的子集，其余端点返回 404 让漏写立刻显形。
type fakeViolet struct {
	t *testing.T

	server *httptest.Server

	mu            sync.Mutex
	profile       botProfileDTO
	conversations map[string]conversationDTO
	history       map[string][]messageDTO
	byIdempotency map[string]string
	allowThinking bool

	sent    []sentMessage
	edits   []editedMessage
	typings []typingRecord
	events  chan string
	// conversationCalls 记录会话详情接口被调次数，用于验证形态缓存真的生效。
	conversationCalls int
	// kindFailure 让会话详情接口报错，用来验证形态未知时的从严判定。
	kindFailure bool
}

// typingRecord 是一次输入状态上报。
type typingRecord struct {
	Conversation string
	IsTyping     bool
}

// newFakeViolet 启动假服务端，测试结束时自动关闭。
func newFakeViolet(t *testing.T) *fakeViolet {
	t.Helper()
	fake := &fakeViolet{
		profile:       botProfileDTO{ID: "bot-1", UserID: testBotUserID, Username: testBotUsername, Name: "Saber"},
		conversations: map[string]conversationDTO{},
		history:       map[string][]messageDTO{},
		byIdempotency: map[string]string{},
		events:        make(chan string, 16),
	}
	fake.addConversation(testDirectRoom, kindDirect)
	fake.addConversation(testGroupRoom, kindRoom)
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

// endpoint 返回假服务端地址，可直接作为 platforms.violet.endpoint。
func (f *fakeViolet) endpoint() string { return f.server.URL }

func (f *fakeViolet) addConversation(id, kind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conversations[id] = conversationDTO{ID: id, Kind: kind, Title: id, Members: []memberDTO{{User: userDTO{ID: testBotUserID, Username: testBotUsername}}}}
}

// pushMessage 把消息加入历史（新在前）并返回快照，供 SSE 或补拉断言使用。
func (f *fakeViolet) pushMessage(conversation, sender, senderName, content string, createdAt time.Time) messageDTO {
	f.mu.Lock()
	defer f.mu.Unlock()
	message := messageDTO{
		ID:             fmt.Sprintf("msg-%s-%d", conversation, len(f.history[conversation])+1),
		ConversationID: conversation,
		Sender:         userDTO{ID: sender, Username: senderName},
		Type:           "text",
		Content:        content,
		CreatedAt:      createdAt.Format(time.RFC3339Nano),
	}
	f.history[conversation] = append([]messageDTO{message}, f.history[conversation]...)
	return message
}

// pushEvent 向 SSE 通道写一帧 message.created。
func (f *fakeViolet) pushEvent(message messageDTO) {
	payload, err := json.Marshal(eventFrame{
		Type:       eventMessageCreated,
		Version:    1,
		OccurredAt: message.CreatedAt,
		Data:       eventPayload{ConversationID: message.ConversationID, Message: message},
	})
	if err != nil {
		f.t.Fatalf("编码测试事件失败: %v", err)
	}
	f.events <- string(payload)
}

// closeEvents 结束当前事件流，用于模拟断线。
func (f *fakeViolet) closeEvents() {
	close(f.events)
}

// restartEvents 在断线后换一条新的 SSE 通道，使重连可以继续投递。
func (f *fakeViolet) restartEvents() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = make(chan string, 16)
}

// sentMessages 返回出站发送记录快照。
func (f *fakeViolet) sentMessages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

// editRecords 返回出站编辑记录快照。
func (f *fakeViolet) editRecords() []editedMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]editedMessage(nil), f.edits...)
}

// typingRecords 返回输入状态上报快照。
func (f *fakeViolet) typingRecords() []typingRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]typingRecord(nil), f.typings...)
}

// lastMessageID 返回某会话当前最新一条消息的 ID（历史按新→旧存放）。
func (f *fakeViolet) lastMessageID(conversation string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.history[conversation]) == 0 {
		return ""
	}
	return f.history[conversation][0].ID
}

// endpointHost 返回假服务端的主机名，即账号标识的默认取值。
func (f *fakeViolet) endpointHost() string {
	return strings.TrimPrefix(f.server.URL, "http://")
}

// resetConversationCalls 清空会话详情调用计数，便于断言缓存生效。
func (f *fakeViolet) resetConversationCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conversationCalls = 0
}

// conversationCalls 返回会话详情接口的调用次数。
func (f *fakeViolet) getConversationCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conversationCalls
}

// setKindFailure 控制会话详情接口是否报错。
func (f *fakeViolet) setKindFailure(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kindFailure = fail
}

// setAuthFailure 让所有请求在未携带正确 token 时返回 401。
func (f *fakeViolet) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, apiPrefix)
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "缺少或无效的 Bot Token")
		return
	}
	switch {
	case path == "/profile" && r.Method == http.MethodGet:
		f.mu.Lock()
		profile := f.profile
		f.mu.Unlock()
		writeAPIData(w, http.StatusOK, profile)
	case path == "/conversations" && r.Method == http.MethodGet:
		f.mu.Lock()
		list := make([]conversationDTO, 0, len(f.conversations))
		for _, conversation := range f.conversations {
			list = append(list, conversation)
		}
		f.mu.Unlock()
		writeAPIData(w, http.StatusOK, list)
	case strings.HasPrefix(path, "/conversations/") && r.Method == http.MethodGet && strings.HasSuffix(path, "/messages"):
		f.serveMessages(w, r, path)
	case strings.HasPrefix(path, "/conversations/") && r.Method == http.MethodGet:
		f.serveConversation(w, path)
	case strings.HasPrefix(path, "/conversations/") && r.Method == http.MethodPost && strings.HasSuffix(path, "/messages"):
		f.serveSendMessage(w, r, path)
	case strings.Contains(path, "/messages/") && r.Method == http.MethodPatch:
		f.serveEdit(w, r, path)
	case strings.HasSuffix(path, "/typing") && r.Method == http.MethodPost:
		f.serveTyping(w, r, path)
	case path == "/events" && r.Method == http.MethodGet:
		f.serveEvents(w, r)
	default:
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+path+" 未实现")
	}
}

// conversationIDFrom 从 /conversations/{id}... 里取会话 ID。
func conversationIDFrom(path string) string {
	rest := strings.TrimPrefix(path, "/conversations/")
	id, _, _ := strings.Cut(rest, "/")
	return id
}

func (f *fakeViolet) serveConversation(w http.ResponseWriter, path string) {
	id := conversationIDFrom(path)
	f.mu.Lock()
	f.conversationCalls++
	failure := f.kindFailure
	conversation, ok := f.conversations[id]
	f.mu.Unlock()
	if failure {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL", "会话服务不可用")
		return
	}
	if !ok {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "会话不存在或本 bot 不是成员")
		return
	}
	writeAPIData(w, http.StatusOK, conversation)
}

func (f *fakeViolet) serveMessages(w http.ResponseWriter, r *http.Request, path string) {
	id := conversationIDFrom(path)
	f.mu.Lock()
	messages := append([]messageDTO(nil), f.history[id]...)
	f.mu.Unlock()
	if messages == nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "会话不存在")
		return
	}
	offset, err := strconv.Atoi(r.URL.Query().Get("cursor"))
	if err != nil {
		offset = 0
	}
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		limit = 20
	}
	page := messages[offset:]
	hasMore := false
	if len(page) > limit {
		hasMore = true
		page = page[:limit]
	}
	next := ""
	if hasMore {
		next = strconv.Itoa(offset + limit)
	}
	writeEnvelope(w, http.StatusOK, page, &pagination{Limit: limit, HasMore: hasMore, NextCursor: next})
}

func (f *fakeViolet) serveSendMessage(w http.ResponseWriter, r *http.Request, path string) {
	id := conversationIDFrom(path)
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Idempotency-Key 必填")
		return
	}
	var body struct {
		Content   string `json:"content"`
		ReplyToID string `json:"reply_to_id"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	if strings.TrimSpace(body.Content) == "" && body.Status != "pending" {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST", "文本消息无效")
		return
	}
	if body.Status != "" && body.Status != "pending" {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST", "创建时只能设置 pending 状态")
		return
	}
	if len([]rune(body.Content)) > maxContentRunes {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "内容超过 10000 字符")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	idempotencyKey := id + "|" + testBotUserID + "|" + key
	if existing, ok := f.byIdempotency[idempotencyKey]; ok {
		for _, message := range f.history[id] {
			if message.ID == existing {
				writeAPIData(w, http.StatusCreated, message)
				return
			}
		}
	}
	created := messageDTO{
		ID:             fmt.Sprintf("sent-%d", len(f.sent)+1),
		ConversationID: id,
		Sender:         userDTO{ID: testBotUserID, Username: testBotUsername},
		Type:           "text",
		Content:        body.Content,
		SenderKind:     "bot",
		CreatedAt:      time.Now().Format(time.RFC3339Nano),
	}
	if body.Status == "pending" {
		state := botReplyDTO{Status: body.Status}
		state.UpdatedAt = time.Now().Format(time.RFC3339Nano)
		created.BotReply = &state
	}
	if body.ReplyToID != "" {
		created.ReplyTo = &messageRefDTO{ID: body.ReplyToID}
	}
	f.history[id] = append([]messageDTO{created}, f.history[id]...)
	f.byIdempotency[idempotencyKey] = created.ID
	f.sent = append(f.sent, sentMessage{Conversation: id, Content: body.Content, ReplyTo: body.ReplyToID, Idempotency: key, BotReply: created.BotReply})
	writeAPIData(w, http.StatusCreated, created)
}

func (f *fakeViolet) serveEdit(w http.ResponseWriter, r *http.Request, path string) {
	id := conversationIDFrom(path)
	messageID := path[strings.LastIndex(path, "/")+1:]
	var body struct {
		Content  string `json:"content"`
		Thinking string `json:"thinking"`
		Status   string `json:"status"`
		Revision int64  `json:"revision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for index, message := range f.history[id] {
		if message.ID != messageID {
			continue
		}
		if message.Sender.ID != testBotUserID {
			writeAPIError(w, http.StatusForbidden, "FORBIDDEN", "不是本 bot 发的消息")
			return
		}
		if message.BotReply != nil && (message.BotReply.Status == "completed" || message.BotReply.Status == "failed") {
			writeAPIError(w, http.StatusConflict, "CONFLICT", "回复已结束")
			return
		}
		if body.Status != "" && (message.BotReply == nil || body.Revision <= message.BotReply.Revision) {
			writeAPIError(w, http.StatusConflict, "CONFLICT", "Bot 回复版本已过期")
			return
		}
		message.Content = body.Content
		edited := time.Now()
		if body.Status != "" {
			state := botReplyDTO{Status: body.Status, Thinking: body.Thinking, Revision: body.Revision}
			state.UpdatedAt = edited.Format(time.RFC3339Nano)
			if !f.allowThinking {
				state.Thinking = ""
			}
			message.BotReply = &state
		} else {
			message.EditedAt = edited.Format(time.RFC3339Nano)
		}
		f.history[id][index] = message
		f.edits = append(f.edits, editedMessage{Conversation: id, MessageID: messageID, Content: body.Content, BotReply: message.BotReply, At: edited})
		writeAPIData(w, http.StatusOK, message)
		return
	}
	writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "消息不存在")
}

func (f *fakeViolet) serveTyping(w http.ResponseWriter, r *http.Request, path string) {
	id := conversationIDFrom(path)
	var body struct {
		IsTyping *bool `json:"is_typing"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.IsTyping == nil {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "is_typing 必填")
		return
	}
	f.mu.Lock()
	f.typings = append(f.typings, typingRecord{Conversation: id, IsTyping: *body.IsTyping})
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeViolet) serveEvents(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	stream := f.events
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case data, open := <-stream:
			if !open {
				return
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventMessageCreated, data)
			flusher.Flush()
		}
	}
}

// writeAPIData 按 Violet 的信封格式写响应。
func writeAPIData(w http.ResponseWriter, status int, data any) {
	writeEnvelope(w, status, data, nil)
}

func writeEnvelope(w http.ResponseWriter, status int, data any, page *pagination) {
	body := envelope2{Data: data}
	if page != nil {
		body.Meta = &envelopeMeta{Pagination: page}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic(err)
	}
}

// envelope2 是测试侧的宽松信封：data 为任意 Go 值，不必先编码成 RawMessage。
type envelope2 struct {
	Data any           `json:"data"`
	Meta *envelopeMeta `json:"meta,omitempty"`
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errorBody{Error: code, Message: message}); err != nil {
		panic(err)
	}
}

// newTestConfig 构造指向假服务端的完整配置。
func newTestConfig(endpoint string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Platforms.Violet = config.VioletConfig{
		Enabled:               true,
		Endpoint:              endpoint,
		BotToken:              testToken,
		DirectChatAutoReply:   true,
		GroupChatMentionReply: true,
		EditIntervalMs:        int(testEditInterval / time.Millisecond),
		HTTPTimeoutSeconds:    5,
	}
	return cfg
}

// recordingHandler 记录交给共享聊天链路的消息，用于断言入站规范化结果。
type recordingHandler struct {
	mu      sync.Mutex
	seen    []chat.Message
	admited chan struct{}
}

// newRecordingHandler 构造带缓冲信号通道的记录器。
func newRecordingHandler() *recordingHandler {
	return &recordingHandler{admited: make(chan struct{}, 32)}
}

// handle 实现 chat.Handler。
func (h *recordingHandler) handle(_ context.Context, message chat.Message, _ chat.Adapter) (agent.Result, error) {
	h.mu.Lock()
	h.seen = append(h.seen, message)
	h.mu.Unlock()
	select {
	case h.admited <- struct{}{}:
	default:
	}
	return agent.Result{Status: agent.Completed, Content: "已回答"}, nil
}

// messages 返回已记录消息快照。
func (h *recordingHandler) messages() []chat.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]chat.Message(nil), h.seen...)
}

// waitForText 等待某条正文出现的消息抵达，返回它；并发处理下只按内容认领，
// 不假装跨消息顺序是承诺。
func (h *recordingHandler) waitForText(t *testing.T, want string) chat.Message {
	t.Helper()
	for attempt := 0; attempt < 400; attempt++ {
		for _, message := range h.messages() {
			if message.Text == want || strings.Contains(message.Text, want) {
				return message
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待正文 %q 超时，已收到 %+v", want, h.messages())
	return chat.Message{}
}

// waitForMessage 等待第 index 条消息抵达（0 基），超时即失败。
func (h *recordingHandler) waitForMessage(t *testing.T, index int) chat.Message {
	t.Helper()
	for attempt := 0; attempt < 200; attempt++ {
		if messages := h.messages(); len(messages) > index {
			return messages[index]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待第 %d 条消息超时，当前共 %d 条", index, len(h.messages()))
	return chat.Message{}
}

// waitForIdle 给「应当什么都不发生」的断言留出观察窗口。
func waitForIdle(t *testing.T, window time.Duration) {
	t.Helper()
	time.Sleep(window)
}
