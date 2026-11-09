package slackplatform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// fakeSlack 是 Web API 的最小替身。
type fakeSlack struct {
	mu      sync.Mutex
	socket  string
	sends   []map[string]string
	edits   []map[string]string
	editErr string
}

// newFakeSlack 启动假 Web API，socket 是 apps.connections.open 返回的地址。
func newFakeSlack(t *testing.T, socket string) (*httptest.Server, *fakeSlack) {
	t.Helper()
	state := &fakeSlack{socket: socket}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if err := r.ParseForm(); err != nil {
			t.Fatalf("解析表单失败: %v", err)
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		switch method {
		case "auth.test":
			writeJSON(t, w, map[string]any{"ok": true, "user_id": "U7", "bot_id": "B7", "user": "saber", "team": "test"})
		case "apps.connections.open":
			writeJSON(t, w, map[string]any{"ok": true, "url": state.socket})
		case "chat.postMessage":
			state.sends = append(state.sends, map[string]string{
				"channel":   r.FormValue("channel"),
				"text":      r.FormValue("text"),
				"thread_ts": r.FormValue("thread_ts"),
			})
			writeJSON(t, w, map[string]any{"ok": true, "ts": fmt.Sprintf("900.%d", len(state.sends))})
		case "chat.update":
			state.edits = append(state.edits, map[string]string{
				"channel": r.FormValue("channel"),
				"ts":      r.FormValue("ts"),
				"text":    r.FormValue("text"),
			})
			if state.editErr != "" {
				writeJSON(t, w, map[string]any{"ok": false, "error": state.editErr})
				return
			}
			writeJSON(t, w, map[string]any{"ok": true, "ts": r.FormValue("ts")})
		case "chat.delete":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, state
}

// writeJSON 写入一个 JSON 响应。
func writeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("写入假响应失败: %v", err)
	}
}

// fakeSocket 是 Socket Mode 的替身：完成 hello、推送一条事件并等待 ack。
type fakeSocket struct {
	acks chan string
}

// startFakeSocket 启动假 Socket Mode 服务。
func startFakeSocket(t *testing.T, event slackEvent) (*httptest.Server, *fakeSocket) {
	t.Helper()
	state := &fakeSocket{acks: make(chan string, 4)}
	server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		if err := websocket.JSON.Send(ws, map[string]any{"type": "hello"}); err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, map[string]any{
			"type":        "events_api",
			"envelope_id": "env-1",
			"payload":     map[string]any{"event": event},
		}); err != nil {
			return
		}
		for {
			var frame map[string]any
			if err := websocket.JSON.Receive(ws, &frame); err != nil {
				return
			}
			if id, ok := frame["envelope_id"].(string); ok {
				select {
				case state.acks <- id:
				default:
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	return server, state
}

// testConfig 返回指向假 Web API 的启用配置。
func testConfig(apiBase string) config.SlackConfig {
	return config.SlackConfig{
		Enabled:               true,
		BotToken:              "xoxb-test",
		AppToken:              "xapp-test",
		APIBase:               apiBase,
		DirectChatAutoReply:   true,
		GroupChatMentionReply: true,
		EditIntervalMs:        1,
		HTTPTimeoutSeconds:    5,
	}
}

// newTestPlatform 构造已取得 bot 身份的平台。
func newTestPlatform(t *testing.T) (*Platform, *fakeSlack) {
	t.Helper()
	server, state := newFakeSlack(t, "")
	cfg := &config.Config{Platforms: config.DefaultPlatformsConfig()}
	cfg.Platforms.Slack = testConfig(server.URL)
	p := New(cfg)
	p.setIdentity("U7")
	return p, state
}

// TestSlackConfig_Validate 覆盖 token 前缀与必填项。
func TestSlackConfig_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*config.SlackConfig)
		wantErr bool
	}{
		{name: "disabled_passes", mutate: func(c *config.SlackConfig) { c.Enabled = false }},
		{name: "missing_bot_token", mutate: func(c *config.SlackConfig) { c.BotToken = "" }, wantErr: true},
		{name: "bot_token_prefix", mutate: func(c *config.SlackConfig) { c.BotToken = "xoxp-test" }, wantErr: true},
		{name: "missing_app_token", mutate: func(c *config.SlackConfig) { c.AppToken = "" }, wantErr: true},
		{name: "app_token_prefix", mutate: func(c *config.SlackConfig) { c.AppToken = "xoxb-test" }, wantErr: true},
		{name: "bad_api_base", mutate: func(c *config.SlackConfig) { c.APIBase = "slack.com" }, wantErr: true},
		{name: "valid", mutate: func(c *config.SlackConfig) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.DefaultSlackConfig()
			cfg.Enabled = true
			cfg.BotToken = "xoxb-test"
			cfg.AppToken = "xapp-test"
			tt.mutate(&cfg)
			if err := cfg.Validate(); tt.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestSlackAdapter_SendEdit 验证完整分段、线程、转义与编辑收尾。
func TestSlackAdapter_SendEdit(t *testing.T) {
	t.Parallel()
	p, state := newTestPlatform(t)
	long := strings.Repeat("汉", maxContentRunes+500)
	id, err := p.DeliveryAdapter().Send(context.Background(), chat.Reply{
		Session: chat.Session{Platform: platformName, Account: "slack", Conversation: "C1"},
		ReplyTo: "111.1",
		Text:    "<@U9> & " + long,
	})
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if id != "900.1" {
		t.Fatalf("Send 返回 ts = %q, want 900.1", id)
	}
	state.mu.Lock()
	if len(state.sends) != 2 {
		state.mu.Unlock()
		t.Fatalf("发送次数=%d, want 2", len(state.sends))
	}
	if state.sends[0]["text"]+state.sends[1]["text"] != escapeText("<@U9> & "+long) {
		state.mu.Unlock()
		t.Fatal("长回复未完整送达")
	}
	sent := state.sends[0]
	state.mu.Unlock()
	if sent["thread_ts"] != "111.1" {
		t.Fatalf("thread_ts = %q, want 111.1", sent["thread_ts"])
	}
	if !strings.Contains(sent["text"], "&lt;@U9&gt; &amp;") {
		t.Fatalf("正文未转义 Slack 控制字符: %q", sent["text"])
	}
	if len([]rune(sent["text"])) > maxContentRunes {
		t.Fatalf("分段后仍有 %d 个字符", len([]rune(sent["text"])))
	}

	if err := p.DeliveryAdapter().Edit(context.Background(), "900.1", chat.Reply{
		Session: chat.Session{Platform: platformName, Account: "slack", Conversation: "C1"},
		Text:    "答案",
	}); err != nil {
		t.Fatalf("Edit 失败: %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.edits) != 1 || state.edits[0]["text"] != "答案" || state.edits[0]["ts"] != "900.1" {
		t.Fatalf("编辑请求 = %v", state.edits)
	}
}

// TestSlackAdapter_EditNotModified 验证 Slack 的「未修改」错误按成功处理。
func TestSlackAdapter_EditNotModified(t *testing.T) {
	t.Parallel()
	p, state := newTestPlatform(t)
	state.mu.Lock()
	state.editErr = "message_not_modified"
	state.mu.Unlock()
	if err := p.DeliveryAdapter().Edit(context.Background(), "900.1", chat.Reply{
		Session: chat.Session{Platform: platformName, Account: "slack", Conversation: "C1"},
		Text:    "答案",
	}); err != nil {
		t.Fatalf("message_not_modified 应视为成功，得到 %v", err)
	}
}

// TestSlackAdapter_Typing 验证 Slack 的输入状态是空操作。
func TestSlackAdapter_Typing(t *testing.T) {
	t.Parallel()
	p, _ := newTestPlatform(t)
	session := chat.Session{Platform: platformName, Account: "slack", Conversation: "C1"}
	if err := p.DeliveryAdapter().SetTyping(context.Background(), session, true); err != nil {
		t.Fatalf("SetTyping 返回错误: %v", err)
	}
	if p.DeliveryAdapter().Capabilities().Typing {
		t.Fatal("Slack 不应声明输入状态能力")
	}
}

// TestSlackNormalize 覆盖自回声、子类型、编辑事件、提及与线程回复规则。
func TestSlackNormalize(t *testing.T) {
	t.Parallel()
	dm := slackEvent{Type: "message", User: "U9", Channel: "D1", ChannelType: "im", Text: "你好", TS: "100.1"}
	channel := slackEvent{Type: "message", User: "U9", Channel: "C1", ChannelType: "channel", Text: "大家好", TS: "101.1"}
	mentioned := channel
	mentioned.Text = "<@U7> 看一下"
	selfMessage := channel
	selfMessage.User = "U7"
	botMessage := channel
	botMessage.BotID = "B9"
	subtyped := channel
	subtyped.Subtype = "message_changed"
	edited := channel
	edited.Edited = &struct {
		User string `json:"user"`
		TS   string `json:"ts"`
	}{User: "U9", TS: "102.1"}
	empty := channel
	empty.Text = ""

	threadReply := channel
	threadReply.Text = "接着说"
	threadReply.ThreadTS = "55.1"
	threadReply.TS = "103.1"

	tests := []struct {
		name       string
		mutate     func(*config.SlackConfig)
		markSelf   bool
		event      slackEvent
		wantOK     bool
		wantText   string
		wantDirect bool
		wantThread string
	}{
		{name: "self_skipped", event: selfMessage, wantOK: false},
		{name: "other_bot_skipped", event: botMessage, wantOK: false},
		{name: "subtype_skipped", event: subtyped, wantOK: false},
		{name: "edited_skipped", event: edited, wantOK: false},
		{name: "empty_skipped", event: empty, wantOK: false},
		{name: "dm_replies", event: dm, wantOK: true, wantText: "你好", wantDirect: true},
		{name: "dm_disabled", mutate: func(c *config.SlackConfig) { c.DirectChatAutoReply = false }, event: dm, wantOK: false},
		{name: "channel_without_mention_skipped", event: channel, wantOK: false},
		{name: "channel_mention_stripped", event: mentioned, wantOK: true, wantText: "看一下"},
		{name: "channel_reply_to_bot_thread", markSelf: true, event: threadReply, wantOK: true, wantText: "接着说", wantThread: "55.1"},
		{name: "channel_thread_not_mine_skipped", event: threadReply, wantOK: false},
		{name: "channel_mention_disabled", mutate: func(c *config.SlackConfig) { c.GroupChatMentionReply = false }, event: mentioned, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p, _ := newTestPlatform(t)
			if tt.mutate != nil {
				tt.mutate(&p.cfg)
			}
			if tt.markSelf {
				p.self.add(tt.event.ThreadTS)
			}
			got, ok := p.normalize(tt.event)
			if ok != tt.wantOK {
				t.Fatalf("normalize() ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.Text != tt.wantText || got.Direct != tt.wantDirect || got.Session.Thread != tt.wantThread {
				t.Fatalf("normalize() = %+v, want text %q direct %v thread %q", got, tt.wantText, tt.wantDirect, tt.wantThread)
			}
		})
	}
}

// TestSlackStartStop_Idempotent 覆盖 Socket Mode 握手、ack、消息投递与重复启停。
func TestSlackStartStop_Idempotent(t *testing.T) {
	t.Parallel()
	inbound := slackEvent{Type: "message", User: "U9", Channel: "D1", ChannelType: "im", Text: "hello", TS: "100.1"}
	socketServer, socketState := startFakeSocket(t, inbound)
	wsURL := "ws" + strings.TrimPrefix(socketServer.URL, "http")
	apiServer, _ := newFakeSlack(t, wsURL)

	cfg := &config.Config{Platforms: config.DefaultPlatformsConfig()}
	cfg.Platforms.Slack = testConfig(apiServer.URL)
	p := New(cfg)

	received := make(chan string, 2)
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
	select {
	case ack := <-socketState.acks:
		if ack != "env-1" {
			t.Fatalf("ack envelope = %q, want env-1", ack)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 envelope ack")
	}
	p.Stop()
	p.Stop()
	if err := p.Start(ctx, handler); err != nil {
		t.Fatalf("重启 Start 失败: %v", err)
	}
	p.Stop()
}

// TestSlackNew_NilConfig 验证无配置时构造不 panic、Start 报错。
func TestSlackNew_NilConfig(t *testing.T) {
	t.Parallel()
	p := New(nil)
	if p.Name() != platformName || p.DeliveryAdapter() != nil {
		t.Fatalf("nil 配置下的平台状态异常: %+v", p)
	}
	if err := p.Start(context.Background(), func(context.Context, chat.Message, chat.Adapter) (agent.Result, error) {
		return agent.Result{}, nil
	}); err == nil {
		t.Fatal("无配置时 Start 应报错")
	}
	p.Stop()
}
