package discordplatform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// fakeREST 是 Discord REST 的最小替身，记录发送与编辑请求。
type fakeREST struct {
	mu       sync.Mutex
	sends    []map[string]any
	edits    []map[string]any
	typings  int
	gateway  string
	botToken string
}

// newFakeREST 启动假 REST，gateway 是 /gateway/bot 返回的连接地址。
func newFakeREST(t *testing.T, gateway string) (*httptest.Server, *fakeREST) {
	t.Helper()
	state := &fakeREST{gateway: gateway}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.botToken = strings.TrimPrefix(r.Header.Get("Authorization"), "Bot ")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/gateway/bot"):
			writeJSON(t, w, map[string]any{"url": state.gateway})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/typing"):
			state.typings++
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/messages"):
			var payload map[string]any
			decodeJSON(t, r, &payload)
			state.sends = append(state.sends, payload)
			writeJSON(t, w, map[string]any{"id": "900"})
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/messages/"):
			var payload map[string]any
			decodeJSON(t, r, &payload)
			state.edits = append(state.edits, payload)
			writeJSON(t, w, map[string]any{"id": "900"})
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

// decodeJSON 解析请求体。
func decodeJSON(t *testing.T, r *http.Request, out any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		t.Fatalf("解析请求体失败: %v", err)
	}
}

// fakeGateway 是伪造的 Gateway：完成 Hello/Identify/READY/心跳并推送一条消息。
type fakeGateway struct {
	heartbeats atomic.Int64
	received   chan string
}

// startFakeGateway 启动假 Gateway，返回服务地址与状态。
func startFakeGateway(t *testing.T, inbound gatewayMessage) (*httptest.Server, *fakeGateway) {
	t.Helper()
	state := &fakeGateway{received: make(chan string, 1)}
	server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		if err := websocket.JSON.Send(ws, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 20}}); err != nil {
			return
		}
		var identify map[string]any
		if err := websocket.JSON.Receive(ws, &identify); err != nil {
			return
		}
		if identify["op"] != float64(2) {
			return
		}
		if err := websocket.JSON.Send(ws, map[string]any{
			"op": 0, "s": 1, "t": "READY",
			"d": map[string]any{"session_id": "session-1", "user": map[string]any{"id": "7", "username": "saber", "bot": true}},
		}); err != nil {
			return
		}
		state.received <- "ready"
		encoded, err := json.Marshal(inbound)
		if err != nil {
			return
		}
		if err := websocket.JSON.Send(ws, map[string]any{"op": 0, "s": 2, "t": "MESSAGE_CREATE", "d": json.RawMessage(encoded)}); err != nil {
			return
		}
		for {
			var frame map[string]any
			if err := websocket.JSON.Receive(ws, &frame); err != nil {
				return
			}
			if frame["op"] == float64(1) {
				state.heartbeats.Add(1)
				if err := websocket.JSON.Send(ws, map[string]any{"op": 11}); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	return server, state
}

// testConfig 返回启用配置；REST 与 Gateway 都由假服务覆盖。
func testConfig(apiBase string) config.DiscordConfig {
	return config.DiscordConfig{
		Enabled:               true,
		BotToken:              "test-bot-token",
		APIBase:               apiBase,
		DirectChatAutoReply:   true,
		GroupChatMentionReply: true,
		EditIntervalMs:        1,
		HTTPTimeoutSeconds:    5,
	}
}

// newTestPlatform 构造已取得 bot 身份的平台。
func newTestPlatform(t *testing.T) (*Platform, *fakeREST) {
	t.Helper()
	server, state := newFakeREST(t, "")
	cfg := &config.Config{Platforms: config.DefaultPlatformsConfig()}
	cfg.Platforms.Discord = testConfig(server.URL)
	p := New(cfg)
	p.setBotUser("7", "saber")
	return p, state
}

// TestDiscordConfig_Validate 覆盖启用时的必填项。
func TestDiscordConfig_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*config.DiscordConfig)
		wantErr bool
	}{
		{name: "disabled_passes", mutate: func(c *config.DiscordConfig) { c.Enabled = false }},
		{name: "missing_token", mutate: func(c *config.DiscordConfig) { c.BotToken = "" }, wantErr: true},
		{name: "bad_api_base", mutate: func(c *config.DiscordConfig) { c.APIBase = "discord.com" }, wantErr: true},
		{name: "zero_http_timeout", mutate: func(c *config.DiscordConfig) { c.HTTPTimeoutSeconds = 0 }, wantErr: true},
		{name: "valid", mutate: func(c *config.DiscordConfig) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.DefaultDiscordConfig()
			cfg.Enabled = true
			cfg.BotToken = "token"
			tt.mutate(&cfg)
			if err := cfg.Validate(); tt.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestDiscordAdapter_SendEdit 验证发送、引用、幂等 nonce、截断与编辑。
func TestDiscordAdapter_SendEdit(t *testing.T) {
	t.Parallel()
	p, state := newTestPlatform(t)
	long := strings.Repeat("汉", maxContentRunes+200)
	id, err := p.DeliveryAdapter().Send(context.Background(), chat.Reply{
		Session:       chat.Session{Platform: platformName, Account: "discord", Conversation: "555"},
		ReplyTo:       "123",
		TransactionID: "saber-task-abc",
		Text:          long,
	})
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if id != "900" {
		t.Fatalf("Send 返回 ID = %q, want 900", id)
	}
	state.mu.Lock()
	if len(state.sends) != 1 {
		state.mu.Unlock()
		t.Fatalf("发送次数 = %d, want 1", len(state.sends))
	}
	sent := state.sends[0]
	state.mu.Unlock()
	content, _ := sent["content"].(string)
	if len([]rune(content)) > maxContentRunes {
		t.Fatalf("截断后仍有 %d 个字符", len([]rune(content)))
	}
	reference, ok := sent["message_reference"].(map[string]any)
	if !ok || reference["message_id"] != "123" {
		t.Fatalf("message_reference = %v, want 123", sent["message_reference"])
	}
	if nonceValue, _ := sent["nonce"].(string); len(nonceValue) != nonceBytes {
		t.Fatalf("nonce = %q, 长度应为 %d", nonceValue, nonceBytes)
	}
	if sent["enforce_nonce"] != true {
		t.Fatalf("enforce_nonce = %v, want true", sent["enforce_nonce"])
	}
	allowed, ok := sent["allowed_mentions"].(map[string]any)
	if !ok || len(allowed["parse"].([]any)) != 0 {
		t.Fatalf("allowed_mentions = %v, want 空 parse", sent["allowed_mentions"])
	}

	if err := p.DeliveryAdapter().Edit(context.Background(), "900", chat.Reply{
		Session: chat.Session{Platform: platformName, Account: "discord", Conversation: "555"},
		Text:    "答案",
	}); err != nil {
		t.Fatalf("Edit 失败: %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.edits) != 1 || state.edits[0]["content"] != "答案" {
		t.Fatalf("编辑请求 = %v", state.edits)
	}
}

// TestDiscordAdapter_Typing 验证输入状态只在 active 时发请求。
func TestDiscordAdapter_Typing(t *testing.T) {
	t.Parallel()
	p, state := newTestPlatform(t)
	session := chat.Session{Platform: platformName, Account: "discord", Conversation: "555"}
	if err := p.DeliveryAdapter().SetTyping(context.Background(), session, false); err != nil {
		t.Fatalf("SetTyping(false) 失败: %v", err)
	}
	if err := p.DeliveryAdapter().SetTyping(context.Background(), session, true); err != nil {
		t.Fatalf("SetTyping(true) 失败: %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.typings != 1 {
		t.Fatalf("typing 调用次数 = %d, want 1", state.typings)
	}
}

// TestDiscordNormalize 覆盖自回声、系统事件、提及与回复规则。
func TestDiscordNormalize(t *testing.T) {
	t.Parallel()
	dm := gatewayMessage{ID: "10", ChannelID: "555", Content: "你好"}
	dm.Author.ID = "99"
	dm.Author.Username = "alice"

	group := dm
	group.GuildID = "888"

	mentioned := group
	mentioned.Content = "<@7> 帮我看看"

	replyToBot := group
	replyToBot.Content = "接着说"
	referenced := gatewayMessage{}
	referenced.Author.ID = "7"
	replyToBot.ReferencedMessage = &referenced

	selfMessage := dm
	selfMessage.Author.ID = "7"

	botMessage := group
	botMessage.Author.Bot = true

	systemMessage := group
	systemMessage.Type = 7

	attachmentOnly := group
	attachmentOnly.Content = ""

	tests := []struct {
		name       string
		mutate     func(*config.DiscordConfig)
		message    gatewayMessage
		wantOK     bool
		wantText   string
		wantDirect bool
	}{
		{name: "self_skipped", message: selfMessage, wantOK: false},
		{name: "other_bot_skipped", message: botMessage, wantOK: false},
		{name: "system_skipped", message: systemMessage, wantOK: false},
		{name: "attachment_only_skipped", message: attachmentOnly, wantOK: false},
		{name: "dm_replies", message: dm, wantOK: true, wantText: "你好", wantDirect: true},
		{name: "dm_disabled", mutate: func(c *config.DiscordConfig) { c.DirectChatAutoReply = false }, message: dm, wantOK: false},
		{name: "group_without_mention_skipped", message: group, wantOK: false},
		{name: "group_mention_stripped", message: mentioned, wantOK: true, wantText: "帮我看看"},
		{name: "group_reply_to_bot", message: replyToBot, wantOK: true, wantText: "接着说"},
		{name: "group_mention_disabled", mutate: func(c *config.DiscordConfig) { c.GroupChatMentionReply = false }, message: mentioned, wantOK: false},
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
			if got.Text != tt.wantText || got.Direct != tt.wantDirect {
				t.Fatalf("normalize() = %+v, want text %q direct %v", got, tt.wantText, tt.wantDirect)
			}
		})
	}
}

// TestDiscordStartStop_Idempotent 覆盖握手、心跳、消息投递与重复启停。
func TestDiscordStartStop_Idempotent(t *testing.T) {
	t.Parallel()
	inbound := gatewayMessage{ID: "10", ChannelID: "555", Content: "hello"}
	inbound.Author.ID = "99"
	gatewayServer, gatewayState := startFakeGateway(t, inbound)
	wsURL := "ws" + strings.TrimPrefix(gatewayServer.URL, "http")
	restServer, _ := newFakeREST(t, wsURL)

	cfg := &config.Config{Platforms: config.DefaultPlatformsConfig()}
	cfg.Platforms.Discord = testConfig(restServer.URL)
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
	deadline := time.Now().Add(2 * time.Second)
	for gatewayState.heartbeats.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if gatewayState.heartbeats.Load() == 0 {
		t.Fatal("未观察到 gateway 心跳")
	}
	p.Stop()
	p.Stop()
	if err := p.Start(ctx, handler); err != nil {
		t.Fatalf("重启 Start 失败: %v", err)
	}
	p.Stop()
}

// TestDiscordNew_NilConfig 验证无配置时构造不 panic、Start 报错。
func TestDiscordNew_NilConfig(t *testing.T) {
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
