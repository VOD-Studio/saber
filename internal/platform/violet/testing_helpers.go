package violetplatform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

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
	// uploadedMedia 记录 bot 上传的图片，key 为内部生成的 file_id。
	uploadedMedia map[string]uploadedMedia
	// conversationCalls 记录会话详情接口被调次数，用于验证形态缓存真的生效。
	conversationCalls int
	// kindFailure 让会话详情接口报错，用来验证形态未知时的从严判定。
	kindFailure bool

	// tweets 是已登记的推文，key 为推文 ID；tweetComments 按推文分组保存评论。
	tweets        map[string]tweetDTO
	tweetComments map[string][]commentDTO
	// tweetPosts 与 tweetCommentPosts 记录 bot 的写请求，供断言正文/父评论/图片。
	tweetPosts        []tweetPost
	tweetCommentPosts []tweetCommentPost
	// tweetCalls 记录推文端点的每次调用（METHOD path），用于断言路径。
	tweetCalls []string
	// tweetPublicAuth 记录公开读端点收到的 Authorization 头，验证它确实免鉴权。
	tweetPublicAuth []string
	// tweetLikes 记录点赞状态，tweetDeleted 记录被删除的推文 ID。
	tweetLikes   map[string]bool
	tweetDeleted []string
	// tweetIdempotency 复现服务端语义：同键重试返回同一条推文或评论。
	tweetIdempotency map[string]string
	// tweetUploads 记录推文图片上传。
	tweetUploads []uploadedMedia
	// tweetCommentSeq 生成评论 ID，跨推文单调递增。
	tweetCommentSeq int

	// emojiCatalog 是表情目录端点返回的内容；emojiCalls 记录路径与鉴权头，
	// emojiFavoriteRecords 记录收藏/取消收藏的 ID 与请求头。
	emojiCatalog         emojiCatalogDTO
	emojiCalls           []emojiCall
	emojiFavoriteRecords []emojiFavoriteRecord
	// emojiFavoriteStatus 让收藏端点返回指定错误码，用于验证错误映射。
	emojiFavoriteStatus int
}

// emojiCall 是一次表情端点的调用记录。
type emojiCall struct {
	Method string
	Path   string
	Auth   string
}

// emojiFavoriteRecord 是一次收藏或取消收藏的观测记录。
type emojiFavoriteRecord struct {
	ID        string
	Favorited bool
	Auth      string
}

// uploadedMedia 是一次图片上传的观测记录。
type uploadedMedia struct {
	ID       string
	URL      string
	MIMEType string
	Filename string
	Bytes    []byte
	Width    int
	Height   int
	Size     int64
}

// typingRecord 是一次输入状态上报。
type typingRecord struct {
	Conversation string
	IsTyping     bool
}

// tweetPost 是一次 bot 发推的观测记录。
type tweetPost struct {
	Path        string
	Content     string
	Images      []string
	QuoteOf     string
	Idempotency string
}

// tweetCommentPost 是一次 bot 发评论的观测记录。
type tweetCommentPost struct {
	Path        string
	TweetID     string
	ParentID    string
	Body        string
	Pictures    []tweetPictureDTO
	Idempotency string
}

// newFakeViolet 启动假服务端，测试结束时自动关闭。
func newFakeViolet(t *testing.T) *fakeViolet {
	t.Helper()
	fake := &fakeViolet{
		profile:       botProfileDTO{ID: "bot-1", UserID: testBotUserID, Username: testBotUsername, Name: "Saber"},
		conversations: map[string]conversationDTO{},
		history:       map[string][]messageDTO{},
		byIdempotency: map[string]string{},
		uploadedMedia: map[string]uploadedMedia{},
		events:        make(chan string, 16),

		tweets:           map[string]tweetDTO{},
		tweetComments:    map[string][]commentDTO{},
		tweetLikes:       map[string]bool{},
		tweetIdempotency: map[string]string{},
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

// pushImageMessage 把一条带 media 的图片消息加入历史并返回快照。
//
// 它只构造消息快照，不登记可下载字节：media.url 指向 /uploads/{id}，是否可下载
// 取决于调用方是否已通过 addUploadedImage 登记。这样测试能精确构造「某张图缺失」的场景。
func (f *fakeViolet) pushImageMessage(conversation, sender, senderName, caption string, media []messageMediaDTO, createdAt time.Time) messageDTO {
	f.mu.Lock()
	defer f.mu.Unlock()
	message := messageDTO{
		ID:             fmt.Sprintf("msg-%s-%d", conversation, len(f.history[conversation])+1),
		ConversationID: conversation,
		Sender:         userDTO{ID: sender, Username: senderName},
		Type:           "image",
		Content:        caption,
		Media:          media,
		CreatedAt:      createdAt.Format(time.RFC3339Nano),
	}
	f.history[conversation] = append([]messageDTO{message}, f.history[conversation]...)
	return message
}

// addUploadedImage 登记一张可经 /uploads/{id} 下载的图片字节。
func (f *fakeViolet) addUploadedImage(id, mimeType string, bytes []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploadedMedia[id] = uploadedMedia{
		ID:       id,
		URL:      "/uploads/" + id,
		MIMEType: mimeType,
		Bytes:    bytes,
		Size:     int64(len(bytes)),
	}
}

// pushEvent 向 SSE 通道写一帧 message.created。
func (f *fakeViolet) pushEvent(message messageDTO) {
	payload, err := json.Marshal(eventPayload{ConversationID: message.ConversationID, Message: message})
	if err != nil {
		f.t.Fatalf("编码测试事件负载失败: %v", err)
	}
	frame, err := json.Marshal(eventFrame{Type: eventMessageCreated, Version: 1, OccurredAt: message.CreatedAt, Data: payload})
	if err != nil {
		f.t.Fatalf("编码测试事件失败: %v", err)
	}
	f.events <- string(frame)
}

// pushTweetEvent 向 SSE 通道写一帧推文事件；payload 按事件契约由调用方构造。
func (f *fakeViolet) pushTweetEvent(eventType string, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatalf("编码测试推文事件负载失败: %v", err)
	}
	frame, err := json.Marshal(eventFrame{Type: eventType, Version: 1, Data: encoded})
	if err != nil {
		f.t.Fatalf("编码测试推文事件失败: %v", err)
	}
	f.events <- string(frame)
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

// uploadedMediaRecords 返回图片上传记录快照（按上传顺序）。
func (f *fakeViolet) uploadedMediaRecords() []uploadedMedia {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]uploadedMedia, 0, len(f.uploadedMedia))
	for _, m := range f.uploadedMedia {
		out = append(out, m)
	}
	return out
}

// setAuthFailure 让所有请求在未携带正确 token 时返回 401。
func (f *fakeViolet) serve(w http.ResponseWriter, r *http.Request) {
	// 推文端点与聊天端点不同前缀：公开读免鉴权，写端点单独验 Bearer token。
	if strings.HasPrefix(r.URL.Path, publicTweetAPIPrefix) {
		f.serveTweet(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, apiPrefix)
	// /uploads/* 是公开读取路径，不经 BotAuth，须在鉴权前放行。
	if strings.HasPrefix(path, "/uploads/") && r.Method == http.MethodGet {
		f.serveUpload(w, r, path)
		return
	}
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
	case path == "/media" && r.Method == http.MethodPost:
		f.serveMediaUpload(w, r)
	case path == "/emojis" && r.Method == http.MethodGet:
		f.serveEmojiCatalog(w, r)
	case strings.HasPrefix(path, "/emojis/") && strings.HasSuffix(path, "/favorite") && (r.Method == http.MethodPost || r.Method == http.MethodDelete):
		f.serveEmojiFavorite(w, r, path)
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
		Content   string   `json:"content"`
		ReplyToID string   `json:"reply_to_id"`
		Status    string   `json:"status"`
		Type      string   `json:"type"`
		MediaIDs  []string `json:"media_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	messageType := body.Type
	if messageType == "" {
		messageType = "text"
	}
	switch messageType {
	case "text":
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
	case "image":
		if len(body.MediaIDs) == 0 {
			writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "图片消息必须提供 media_ids")
			return
		}
		f.mu.Lock()
		for _, mid := range body.MediaIDs {
			if _, ok := f.uploadedMedia[mid]; !ok {
				f.mu.Unlock()
				writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "media_id 不存在: "+mid)
				return
			}
		}
		f.mu.Unlock()
	default:
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "不支持的消息类型: "+messageType)
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
		Type:           messageType,
		Content:        body.Content,
		SenderKind:     "bot",
		CreatedAt:      time.Now().Format(time.RFC3339Nano),
	}
	if messageType == "image" {
		created.Media = make([]messageMediaDTO, 0, len(body.MediaIDs))
		for _, mid := range body.MediaIDs {
			media := f.uploadedMedia[mid]
			created.Media = append(created.Media, messageMediaDTO{
				ID:       media.ID,
				URL:      media.URL,
				MIMEType: media.MIMEType,
			})
		}
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

// serveMediaUpload 处理 POST /media：解析 multipart，登记 file_id，返回定位信息。
//
// 它只做契约级校验（必须 multipart、必须有 file 部分），不做大小/MIME 限制——
// 那些是 Violet 服务端的职责，fake 侧放宽以便测试聚焦在 saber 的上传调用链。
func (f *fakeViolet) serveMediaUpload(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Content-Type 必须是 multipart/form-data")
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "解析 multipart 失败: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "缺少 file 字段")
		return
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL", "读取上传字节失败")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fmt.Sprintf("media-%d", len(f.uploadedMedia)+1)
	mimeType := header.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	record := uploadedMedia{
		ID:       id,
		URL:      "/uploads/" + id,
		MIMEType: mimeType,
		Filename: header.Filename,
		Bytes:    data,
		Size:     int64(len(data)),
	}
	// 尝试解析宽高，与 violet 服务端 image.DecodeConfig 行为对齐；解析失败留 0。
	if config, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		record.Width = config.Width
		record.Height = config.Height
	}
	f.uploadedMedia[id] = record
	width, height := record.Width, record.Height
	writeAPIData(w, http.StatusCreated, mediaUploadDTO{
		ID:       record.ID,
		URL:      record.URL,
		MIMEType: record.MIMEType,
		Width:    width,
		Height:   height,
		Size:     record.Size,
	})
}

// serveEmojiCatalog 返回已登记的表情目录，并记录路径与鉴权头。
func (f *fakeViolet) serveEmojiCatalog(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.emojiCalls = append(f.emojiCalls, emojiCall{Method: r.Method, Path: r.URL.RequestURI(), Auth: r.Header.Get("Authorization")})
	catalog := f.emojiCatalog
	f.mu.Unlock()
	writeAPIData(w, http.StatusOK, catalog)
}

// serveEmojiFavorite 复现收藏端点：ID 必须是 UUID，成功返回 204。
// emojiFavoriteStatus 非零时按指定状态码报错，用于验证 403/404 的映射。
func (f *fakeViolet) serveEmojiFavorite(w http.ResponseWriter, r *http.Request, path string) {
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/emojis/"), "/favorite")
	f.mu.Lock()
	f.emojiCalls = append(f.emojiCalls, emojiCall{Method: r.Method, Path: r.URL.RequestURI(), Auth: r.Header.Get("Authorization")})
	status := f.emojiFavoriteStatus
	f.mu.Unlock()
	if _, err := uuid.Parse(id); err != nil {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "custom emoji id 必须是 UUID")
		return
	}
	if status != 0 {
		writeAPIError(w, status, "EMOJI_ERROR", "表情接口拒绝该操作")
		return
	}
	f.mu.Lock()
	f.emojiFavoriteRecords = append(f.emojiFavoriteRecords, emojiFavoriteRecord{
		ID:        id,
		Favorited: r.Method == http.MethodPost,
		Auth:      r.Header.Get("Authorization"),
	})
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// setEmojiCatalog 设置表情目录端点返回的内容。
func (f *fakeViolet) setEmojiCatalog(catalog emojiCatalogDTO) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emojiCatalog = catalog
}

// setEmojiFavoriteStatus 让收藏端点返回指定状态码；0 表示恢复正常。
func (f *fakeViolet) setEmojiFavoriteStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emojiFavoriteStatus = status
}

// emojiCallRecords 返回表情端点的调用记录。
func (f *fakeViolet) emojiCallRecords() []emojiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]emojiCall(nil), f.emojiCalls...)
}

// emojiFavoriteRecordList 返回收藏与取消收藏的观测记录。
func (f *fakeViolet) emojiFavoriteRecordList() []emojiFavoriteRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]emojiFavoriteRecord(nil), f.emojiFavoriteRecords...)
}

// serveUpload 按 /uploads/{id} 回放已登记媒体的字节，公开读取无需鉴权。
func (f *fakeViolet) serveUpload(w http.ResponseWriter, r *http.Request, path string) {
	id := strings.TrimPrefix(path, "/uploads/")
	f.mu.Lock()
	media, ok := f.uploadedMedia[id]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", media.MIMEType)
	w.Header().Set("Content-Length", strconv.Itoa(len(media.Bytes)))
	if _, err := w.Write(media.Bytes); err != nil {
		// 连接中断在测试里不视为失败，仅记录。
		return
	}
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
			// 帧内 type 与 event: 行同源；推文事件必须原样转发，否则接入端只看到 message.created。
			eventName := eventMessageCreated
			var frame struct {
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(data), &frame) == nil && frame.Type != "" {
				eventName = frame.Type
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, data)
			flusher.Flush()
		}
	}
}

// serveTweet 路由推文端点：bot 命名空间要求 Bearer token，公开读端点免鉴权。
func (f *fakeViolet) serveTweet(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	bot := strings.HasPrefix(path, tweetAPIPrefix)
	if bot && r.Header.Get("Authorization") != "Bearer "+testToken {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "缺少或无效的 Bot Token")
		return
	}
	f.mu.Lock()
	f.tweetCalls = append(f.tweetCalls, r.Method+" "+r.URL.RequestURI())
	if !bot {
		f.tweetPublicAuth = append(f.tweetPublicAuth, r.Header.Get("Authorization"))
	}
	f.mu.Unlock()
	if bot {
		f.serveBotTweet(w, r, strings.TrimPrefix(path, tweetAPIPrefix))
		return
	}
	f.servePublicTweet(w, r, strings.TrimPrefix(path, publicTweetAPIPrefix))
}

// splitTweetPath 把 /a/b/c 拆成 [a b c]；空路径返回空切片。
func splitTweetPath(rest string) []string {
	trimmed := strings.Trim(rest, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// requireUUIDKeyHeader 复现 Violet 对 Idempotency-Key 的 UUID 校验。
func requireUUIDKeyHeader(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if _, err := uuid.Parse(key); err != nil {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Idempotency-Key 必须是 UUID")
		return "", false
	}
	return key, true
}

// serveBotTweet 处理 bot 推文写端点，路径已去掉 /api/v1/tweets/bot。
func (f *fakeViolet) serveBotTweet(w http.ResponseWriter, r *http.Request, rest string) {
	segments := splitTweetPath(rest)
	switch {
	case len(segments) == 0 && r.Method == http.MethodPost:
		f.createTweet(w, r)
	case len(segments) == 1 && segments[0] == "media" && r.Method == http.MethodPost:
		f.uploadTweetMedia(w, r)
	case len(segments) == 2 && segments[1] == "comments" && r.Method == http.MethodPost:
		f.createTweetComment(w, r, segments[0])
	case len(segments) == 3 && segments[1] == "comments" && r.Method == http.MethodDelete:
		f.deleteTweetComment(w, segments[0], segments[2])
	case len(segments) == 2 && segments[1] == "like" && (r.Method == http.MethodPost || r.Method == http.MethodDelete):
		f.likeTweet(w, r, segments[0])
	case len(segments) == 1 && r.Method == http.MethodDelete:
		f.deleteTweet(w, segments[0])
	default:
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" 未实现")
	}
}

// createTweet 发布推文：校验 UUID 幂等键与内容，并按同键返回同一条推文。
func (f *fakeViolet) createTweet(w http.ResponseWriter, r *http.Request) {
	key, ok := requireUUIDKeyHeader(w, r)
	if !ok {
		return
	}
	var body newTweetRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	if strings.TrimSpace(body.Content) == "" && len(body.Images) == 0 && body.QuoteOf == "" {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "推文缺少正文、图片与引用对象")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, found := f.tweetIdempotency["tweet:"+key]; found {
		writeAPIData(w, http.StatusCreated, f.tweets[existing])
		return
	}
	id := fmt.Sprintf("tweet-%d", len(f.tweets)+1)
	created := tweetDTO{
		ID:        id,
		Author:    tweetAuthorDTO{ID: testBotUserID, Username: testBotUsername, IsBot: true},
		Content:   body.Content,
		QuoteOf:   body.QuoteOf,
		CreatedAt: time.Now().Format(time.RFC3339Nano),
	}
	for _, imageURL := range body.Images {
		created.Images = append(created.Images, tweetImageDTO{URL: imageURL})
	}
	if body.QuoteOf != "" {
		if quoted, found := f.tweets[body.QuoteOf]; found {
			snapshot := quoted
			created.QuotedTweet = &snapshot
		}
	}
	f.tweets[id] = created
	f.tweetIdempotency["tweet:"+key] = id
	f.tweetPosts = append(f.tweetPosts, tweetPost{Path: r.URL.Path, Content: body.Content, Images: body.Images, QuoteOf: body.QuoteOf, Idempotency: key})
	writeAPIData(w, http.StatusCreated, created)
}

// createTweetComment 发布评论：校验父评论存在，并按同键返回同一条评论。
func (f *fakeViolet) createTweetComment(w http.ResponseWriter, r *http.Request, tweetID string) {
	key, ok := requireUUIDKeyHeader(w, r)
	if !ok {
		return
	}
	var body newCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	if strings.TrimSpace(body.Body) == "" && len(body.Pictures) == 0 {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "评论缺少正文与图片")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	tweet, found := f.tweets[tweetID]
	if !found {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "推文不存在")
		return
	}
	if existing, seen := f.tweetIdempotency["comment:"+key]; seen {
		for _, comment := range f.tweetComments[tweetID] {
			if comment.ID == existing {
				writeAPIData(w, http.StatusCreated, comment)
				return
			}
		}
	}
	depth := 0
	if body.ParentID != "" {
		parentFound := false
		for index := range f.tweetComments[tweetID] {
			if f.tweetComments[tweetID][index].ID != body.ParentID {
				continue
			}
			depth = f.tweetComments[tweetID][index].Depth + 1
			f.tweetComments[tweetID][index].RepliesCount++
			parentFound = true
			break
		}
		if !parentFound {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "父评论不存在")
			return
		}
	}
	f.tweetCommentSeq++
	created := commentDTO{
		ID:        fmt.Sprintf("comment-%d", f.tweetCommentSeq),
		TweetID:   tweetID,
		Author:    tweetAuthorDTO{ID: testBotUserID, Username: testBotUsername, IsBot: true},
		Body:      body.Body,
		Pictures:  body.Pictures,
		ParentID:  body.ParentID,
		Depth:     depth,
		CreatedAt: time.Now().Format(time.RFC3339Nano),
	}
	f.tweetComments[tweetID] = append(f.tweetComments[tweetID], created)
	tweet.CommentCount++
	f.tweets[tweetID] = tweet
	f.tweetIdempotency["comment:"+key] = created.ID
	f.tweetCommentPosts = append(f.tweetCommentPosts, tweetCommentPost{Path: r.URL.Path, TweetID: tweetID, ParentID: body.ParentID, Body: body.Body, Pictures: body.Pictures, Idempotency: key})
	writeAPIData(w, http.StatusCreated, created)
}

// likeTweet 设置或取消点赞。
func (f *fakeViolet) likeTweet(w http.ResponseWriter, r *http.Request, tweetID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tweet, found := f.tweets[tweetID]
	if !found {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "推文不存在")
		return
	}
	if r.Method == http.MethodPost {
		if !f.tweetLikes[tweetID] {
			tweet.LikeCount++
		}
		f.tweetLikes[tweetID] = true
	} else {
		if f.tweetLikes[tweetID] {
			tweet.LikeCount--
		}
		delete(f.tweetLikes, tweetID)
	}
	f.tweets[tweetID] = tweet
	w.WriteHeader(http.StatusNoContent)
}

// deleteTweet 删除推文。
func (f *fakeViolet) deleteTweet(w http.ResponseWriter, tweetID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, found := f.tweets[tweetID]; !found {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "推文不存在")
		return
	}
	delete(f.tweets, tweetID)
	f.tweetDeleted = append(f.tweetDeleted, tweetID)
	w.WriteHeader(http.StatusNoContent)
}

// deleteTweetComment 删除评论。
func (f *fakeViolet) deleteTweetComment(w http.ResponseWriter, tweetID, commentID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	comments := f.tweetComments[tweetID]
	for index, comment := range comments {
		if comment.ID != commentID {
			continue
		}
		f.tweetComments[tweetID] = append(comments[:index], comments[index+1:]...)
		if tweet, found := f.tweets[tweetID]; found && tweet.CommentCount > 0 {
			tweet.CommentCount--
			f.tweets[tweetID] = tweet
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "评论不存在")
}

// uploadTweetMedia 处理推文图片上传，与聊天媒体上传共用一套校验口径。
func (f *fakeViolet) uploadTweetMedia(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Content-Type 必须是 multipart/form-data")
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "解析 multipart 失败: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "缺少 file 字段")
		return
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL", "读取上传字节失败")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fmt.Sprintf("tweet-media-%d", len(f.tweetUploads)+1)
	mimeType := header.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	record := uploadedMedia{ID: id, URL: "/uploads/" + id, MIMEType: mimeType, Filename: header.Filename, Bytes: data, Size: int64(len(data))}
	if config, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		record.Width, record.Height = config.Width, config.Height
	}
	f.tweetUploads = append(f.tweetUploads, record)
	writeAPIData(w, http.StatusCreated, mediaUploadDTO{
		ID: record.ID, URL: record.URL, MIMEType: record.MIMEType,
		Width: record.Width, Height: record.Height, Size: record.Size,
	})
}

// servePublicTweet 处理公开读端点，路径已去掉 /api/v1/tweets。
func (f *fakeViolet) servePublicTweet(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" 未实现")
		return
	}
	segments := splitTweetPath(rest)
	if len(segments) == 0 {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "推文路径为空")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	tweet, found := f.tweets[segments[0]]
	if !found {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "推文不存在")
		return
	}
	switch {
	case len(segments) == 1:
		writeAPIData(w, http.StatusOK, tweet)
	case len(segments) == 2 && segments[1] == "comments":
		writeAPIData(w, http.StatusOK, f.filteredComments(segments[0], "", commentLimit(r)))
	case len(segments) == 4 && segments[1] == "comments" && segments[3] == "replies":
		writeAPIData(w, http.StatusOK, f.filteredComments(segments[0], segments[2], commentLimit(r)))
	default:
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path+" 未实现")
	}
}

// filteredComments 按父评论过滤；parentID 为空取顶层评论。调用方必须持有锁。
func (f *fakeViolet) filteredComments(tweetID, parentID string, limit int) []commentDTO {
	out := make([]commentDTO, 0, len(f.tweetComments[tweetID]))
	for _, comment := range f.tweetComments[tweetID] {
		if comment.ParentID != parentID {
			continue
		}
		out = append(out, comment)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// commentLimit 解析公开读的 limit 参数，缺省 20。
func commentLimit(r *http.Request) int {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		return 20
	}
	return limit
}

// addTweet 登记一条已存在的推文，供公开读与引用关系使用。
func (f *fakeViolet) addTweet(tweet tweetDTO) tweetDTO {
	f.mu.Lock()
	defer f.mu.Unlock()
	if tweet.ID == "" {
		tweet.ID = fmt.Sprintf("tweet-%d", len(f.tweets)+1)
	}
	if tweet.CreatedAt == "" {
		tweet.CreatedAt = time.Now().Format(time.RFC3339Nano)
	}
	f.tweets[tweet.ID] = tweet
	return tweet
}

// addTweetComment 登记一条已存在的评论。
func (f *fakeViolet) addTweetComment(comment commentDTO) commentDTO {
	f.mu.Lock()
	defer f.mu.Unlock()
	if comment.ID == "" {
		f.tweetCommentSeq++
		comment.ID = fmt.Sprintf("comment-%d", f.tweetCommentSeq)
	}
	if comment.CreatedAt == "" {
		comment.CreatedAt = time.Now().Format(time.RFC3339Nano)
	}
	f.tweetComments[comment.TweetID] = append(f.tweetComments[comment.TweetID], comment)
	return comment
}

// tweetPostRecords 返回 bot 发推记录快照。
func (f *fakeViolet) tweetPostRecords() []tweetPost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tweetPost(nil), f.tweetPosts...)
}

// tweetCommentRecords 返回 bot 发评论记录快照。
func (f *fakeViolet) tweetCommentRecords() []tweetCommentPost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tweetCommentPost(nil), f.tweetCommentPosts...)
}

// tweetCallRecords 返回推文端点调用记录快照。
func (f *fakeViolet) tweetCallRecords() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tweetCalls...)
}

// tweetPublicAuthRecords 返回公开读端点收到的 Authorization 头快照。
func (f *fakeViolet) tweetPublicAuthRecords() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tweetPublicAuth...)
}

// tweetLikeState 返回当前点赞状态。
func (f *fakeViolet) tweetLikeState(tweetID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tweetLikes[tweetID]
}

// tweetDeletedRecords 返回被删除的推文 ID 快照。
func (f *fakeViolet) tweetDeletedRecords() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tweetDeleted...)
}

// tweetUploadedRecords 返回推文图片上传记录快照。
func (f *fakeViolet) tweetUploadedRecords() []uploadedMedia {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uploadedMedia(nil), f.tweetUploads...)
}

// commentsFor 返回某推文下的评论快照（含回复）。
func (f *fakeViolet) commentsFor(tweetID string) []commentDTO {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]commentDTO(nil), f.tweetComments[tweetID]...)
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
	violet := config.DefaultVioletConfig()
	violet.Enabled = true
	violet.Endpoint = endpoint
	violet.BotToken = testToken
	violet.EditIntervalMs = int(testEditInterval / time.Millisecond)
	violet.HTTPTimeoutSeconds = 5
	cfg.Platforms.Violet = violet
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
