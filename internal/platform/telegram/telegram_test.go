package telegramplatform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// fakeAPI 是 Telegram Bot API 的最小替身，按方法名路由并记录请求体。
type fakeAPI struct {
	mu      sync.Mutex
	profile botUser
	sends   []map[string]any
	edits   []map[string]any
	actions []map[string]any
	// updates 是每次 getUpdates 依次返回的批次；耗尽后挂起直到客户端取消。
	updates [][]update
	// editError 非空时 editMessageText 返回该描述且 ok=false。
	editError string
	// done 在测试收尾时关闭，叫醒挂起的 getUpdates 处理器。
	done     chan struct{}
	stopOnce sync.Once
}

func newFakeAPI(t *testing.T, state *fakeAPI) *httptest.Server {
	t.Helper()
	if state.done == nil {
		state.done = make(chan struct{})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		switch method {
		case "getMe":
			writeEnvelope(t, w, true, state.profile, "")
		case "getUpdates":
			if len(state.updates) == 0 {
				state.mu.Unlock()
				select {
				case <-state.done:
				case <-r.Context().Done():
				}
				state.mu.Lock()
				return
			}
			batch := state.updates[0]
			state.updates = state.updates[1:]
			writeEnvelope(t, w, true, batch, "")
		case "sendMessage":
			var payload map[string]any
			decodeBody(t, r, &payload)
			state.sends = append(state.sends, payload)
			writeEnvelope(t, w, true, message{MessageID: int64(41 + len(state.sends))}, "")
		case "editMessageText":
			var payload map[string]any
			decodeBody(t, r, &payload)
			state.edits = append(state.edits, payload)
			if state.editError != "" {
				writeEnvelope(t, w, false, nil, state.editError)
				return
			}
			writeEnvelope(t, w, true, true, "")
		case "sendChatAction":
			var payload map[string]any
			decodeBody(t, r, &payload)
			state.actions = append(state.actions, payload)
			writeEnvelope(t, w, true, true, "")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { state.stopOnce.Do(func() { close(state.done) }) })
	return server
}

func writeEnvelope(t *testing.T, w http.ResponseWriter, ok bool, result any, description string) {
	t.Helper()
	envelope := map[string]any{"ok": ok}
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("序列化假响应失败: %v", err)
		}
		envelope["result"] = json.RawMessage(raw)
	}
	if description != "" {
		envelope["description"] = description
		envelope["error_code"] = 400
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(envelope); err != nil {
		t.Fatalf("写入假响应失败: %v", err)
	}
}

func decodeBody(t *testing.T, r *http.Request, out any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		t.Fatalf("解析请求体失败: %v", err)
	}
}

// testConfig 返回指向假 API 的启用配置。
func testConfig(apiBase string) config.TelegramConfig {
	return config.TelegramConfig{
		Enabled:               true,
		BotToken:              "123456:test-token",
		APIBase:               apiBase,
		DirectChatAutoReply:   true,
		GroupChatMentionReply: true,
		EditIntervalMs:        1,
		HTTPTimeoutSeconds:    5,
		PollTimeoutSeconds:    1,
	}
}

// newTestPlatform 用假 API 构造已取得身份的平台。
func newTestPlatform(t *testing.T) (*Platform, *fakeAPI) {
	t.Helper()
	state := &fakeAPI{profile: botUser{ID: 7, IsBot: true, Username: "saber_bot"}}
	server := newFakeAPI(t, state)
	cfg := &config.Config{Platforms: config.DefaultPlatformsConfig()}
	cfg.Platforms.Telegram = testConfig(server.URL)
	p := New(cfg)
	p.setIdentity(state.profile)
	return p, state
}

// TestTelegramConfig_Validate 覆盖启用时的必填项与默认值。
func TestTelegramConfig_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*config.TelegramConfig)
		wantErr bool
	}{
		{name: "disabled_passes", mutate: func(c *config.TelegramConfig) { c.Enabled = false }},
		{name: "missing_token", mutate: func(c *config.TelegramConfig) { c.BotToken = "" }, wantErr: true},
		{name: "bad_api_base", mutate: func(c *config.TelegramConfig) { c.APIBase = "api.telegram.org" }, wantErr: true},
		{name: "zero_edit_interval", mutate: func(c *config.TelegramConfig) { c.EditIntervalMs = 0 }, wantErr: true},
		{name: "zero_poll_timeout", mutate: func(c *config.TelegramConfig) { c.PollTimeoutSeconds = 0 }, wantErr: true},
		{name: "valid", mutate: func(c *config.TelegramConfig) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.DefaultTelegramConfig()
			cfg.Enabled = true
			cfg.BotToken = "123:abc"
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestTelegramAdapter_SendAllSegments 验证超长回复完整分段并保留首段引用。
func TestTelegramAdapter_SendAllSegments(t *testing.T) {
	t.Parallel()
	p, state := newTestPlatform(t)
	long := strings.Repeat("汉", maxMessageRunes+500)
	id, err := p.DeliveryAdapter().Send(context.Background(), chatReply("-1001", "9", "", long))
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if id != "42" {
		t.Fatalf("Send 返回消息 ID = %q, want 42", id)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sends) != 2 {
		t.Fatalf("sendMessage 调用次数 = %d, want 2", len(state.sends))
	}
	var full strings.Builder
	for _, sent := range state.sends {
		text, _ := sent["text"].(string)
		if len([]rune(text)) > maxMessageRunes {
			t.Fatal("消息分段超过平台上限")
		}
		full.WriteString(text)
	}
	if full.String() != long {
		t.Fatal("长回复未完整送达")
	}
	if state.sends[0]["chat_id"] != float64(-1001) {
		t.Fatalf("chat_id = %v, want -1001", state.sends[0]["chat_id"])
	}
	replyParameters, ok := state.sends[0]["reply_parameters"].(map[string]any)
	if !ok || replyParameters["message_id"] != float64(9) {
		t.Fatalf("reply_parameters = %v, want message_id 9", state.sends[0]["reply_parameters"])
	}
}

// TestTelegramAdapter_Edit 验证编辑请求与「未修改」降级。
func TestTelegramAdapter_Edit(t *testing.T) {
	t.Parallel()
	t.Run("success", func(t *testing.T) {
		t.Parallel()
		p, state := newTestPlatform(t)
		if err := p.DeliveryAdapter().Edit(context.Background(), "42", chatReply("-1001", "", "", "答案")); err != nil {
			t.Fatalf("Edit 失败: %v", err)
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if len(state.edits) != 1 || state.edits[0]["text"] != "答案" {
			t.Fatalf("editMessageText 请求 = %v", state.edits)
		}
	})

	t.Run("not_modified_is_success", func(t *testing.T) {
		t.Parallel()
		p, state := newTestPlatform(t)
		state.mu.Lock()
		state.editError = "Bad Request: message is not modified"
		state.mu.Unlock()
		if err := p.DeliveryAdapter().Edit(context.Background(), "42", chatReply("-1001", "", "", "答案")); err != nil {
			t.Fatalf("未修改的编辑应视为成功，得到 %v", err)
		}
	})

	t.Run("foreign_account_rejected", func(t *testing.T) {
		t.Parallel()
		p, _ := newTestPlatform(t)
		reply := chatReply("-1001", "", "", "答案")
		reply.Session.Account = "someone-else"
		if err := p.DeliveryAdapter().Edit(context.Background(), "42", reply); err == nil {
			t.Fatal("跨账号编辑应被拒绝")
		}
	})
}

// TestTelegramAdapter_Typing 验证输入状态只在 active 时调用 API。
func TestTelegramAdapter_Typing(t *testing.T) {
	t.Parallel()
	p, state := newTestPlatform(t)
	session := chatReply("-1001", "", "", "").Session
	if err := p.DeliveryAdapter().SetTyping(context.Background(), session, false); err != nil {
		t.Fatalf("SetTyping(false) 失败: %v", err)
	}
	state.mu.Lock()
	if len(state.actions) != 0 {
		state.mu.Unlock()
		t.Fatalf("SetTyping(false) 不应调用 API，实际 %v", state.actions)
	}
	state.mu.Unlock()
	if err := p.DeliveryAdapter().SetTyping(context.Background(), session, true); err != nil {
		t.Fatalf("SetTyping(true) 失败: %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.actions) != 1 || state.actions[0]["action"] != "typing" {
		t.Fatalf("sendChatAction 请求 = %v", state.actions)
	}
}

// chatReply 构造一条指向 Telegram 会话的回复。
func chatReply(conversation, replyTo, thread, text string) chat.Reply {
	return chat.Reply{
		Session: chat.Session{Platform: platformName, Account: "telegram", Conversation: conversation, Thread: thread},
		ReplyTo: replyTo,
		Text:    text,
	}
}

// TestTelegramNormalize 覆盖自回声、非文本、私聊/群聊触发规则与提及剥离。
func TestTelegramNormalize(t *testing.T) {
	t.Parallel()
	group := chatRef{ID: -1001, Type: "supergroup", Title: "群"}
	private := chatRef{ID: 11, Type: "private"}
	sender := &user{ID: 99, Username: "alice"}
	makeMessage := func(chat chatRef, from *user, text string) message {
		return message{MessageID: 5, From: from, Chat: chat, Text: text}
	}
	tests := []struct {
		name       string
		mutate     func(*config.TelegramConfig)
		message    message
		wantOK     bool
		wantText   string
		wantDirect bool
	}{
		{
			name:    "self_message_skipped",
			message: makeMessage(group, &user{ID: 7, IsBot: true, Username: "saber_bot"}, "@saber_bot hi"),
			wantOK:  false,
		},
		{
			name:    "non_text_skipped",
			message: makeMessage(private, sender, ""),
			wantOK:  false,
		},
		{
			name:       "direct_replies",
			message:    makeMessage(private, sender, "你好"),
			wantOK:     true,
			wantText:   "你好",
			wantDirect: true,
		},
		{
			name:    "direct_disabled",
			mutate:  func(c *config.TelegramConfig) { c.DirectChatAutoReply = false },
			message: makeMessage(private, sender, "你好"),
			wantOK:  false,
		},
		{
			name:    "group_without_mention_skipped",
			message: makeMessage(group, sender, "大家好"),
			wantOK:  false,
		},
		{
			name:     "group_mention_stripped",
			message:  makeMessage(group, sender, "@saber_bot 你好"),
			wantOK:   true,
			wantText: "你好",
		},
		{
			name: "group_reply_to_bot",
			message: message{
				MessageID: 5, From: sender, Chat: group, Text: "接着说",
				ReplyToMessage: &message{MessageID: 3, From: &user{ID: 7, IsBot: true}},
			},
			wantOK:   true,
			wantText: "接着说",
		},
		{
			name:    "group_mention_disabled",
			mutate:  func(c *config.TelegramConfig) { c.GroupChatMentionReply = false },
			message: makeMessage(group, sender, "@saber_bot 你好"),
			wantOK:  false,
		},
		{
			name:    "mention_only_skipped",
			message: makeMessage(group, sender, "@saber_bot"),
			wantOK:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, _ := newTestPlatform(t)
			if tt.mutate != nil {
				tt.mutate(&p.cfg)
			}
			got, ok := p.normalize(tt.message)
			if ok != tt.wantOK {
				t.Fatalf("normalize() ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.Text != tt.wantText {
				t.Fatalf("Text = %q, want %q", got.Text, tt.wantText)
			}
			if got.Direct != tt.wantDirect {
				t.Fatalf("Direct = %v, want %v", got.Direct, tt.wantDirect)
			}
			if got.Session.Platform != platformName || got.Session.Conversation == "" {
				t.Fatalf("Session = %+v", got.Session)
			}
		})
	}
}

// TestTelegramStartStop_Idempotent 验证后台轮询、消息投递与重复启停。
func TestTelegramStartStop_Idempotent(t *testing.T) {
	t.Parallel()
	received := make(chan string, 4)
	state := &fakeAPI{
		profile: botUser{ID: 7, IsBot: true, Username: "saber_bot"},
		updates: [][]update{{{
			UpdateID: 10,
			Message: &message{
				MessageID: 1, From: &user{ID: 99, Username: "alice"},
				Chat: chatRef{ID: 11, Type: "private"}, Text: "hello",
			},
		}}},
	}
	server := newFakeAPI(t, state)
	p := New(&config.Config{Platforms: config.DefaultPlatformsConfig()})
	p.cfg = testConfig(server.URL)
	p.api = newClient(p.cfg)
	p.adapter = newAdapter(p.api, p.cfg.EffectiveAccount(), p.throttle)

	handler := func(_ context.Context, m chat.Message, _ chat.Adapter) (agent.Result, error) {
		received <- m.Text
		return agent.Result{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx, handler); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if err := p.Start(ctx, handler); err != nil {
		t.Fatalf("重复 Start 应幂等，得到 %v", err)
	}
	select {
	case text := <-received:
		if text != "hello" {
			t.Fatalf("handler 收到 %q, want hello", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("5 秒内未收到入站消息")
	}
	p.Stop()
	p.Stop()

	// 停掉后重新启动：仍应回到可用状态，且不因旧队列残留而 panic。
	if err := p.Start(ctx, handler); err != nil {
		t.Fatalf("重启 Start 失败: %v", err)
	}
	p.Stop()
}

// TestTelegramNew_NilConfig 验证无配置时构造不 panic、Start 报错。
func TestTelegramNew_NilConfig(t *testing.T) {
	t.Parallel()
	p := New(nil)
	if p.Name() != platformName {
		t.Fatalf("Name = %q", p.Name())
	}
	if p.DeliveryAdapter() != nil {
		t.Fatal("无配置时不应有投递 adapter")
	}
	if err := p.Start(context.Background(), func(context.Context, chat.Message, chat.Adapter) (agent.Result, error) {
		return agent.Result{}, nil
	}); err == nil {
		t.Fatal("无配置时 Start 应报错")
	}
	p.Stop()
}
