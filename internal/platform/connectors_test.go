package platform

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

func testConnector(t *testing.T, name string, cfg config.ConnectorConfig) *connector {
	t.Helper()
	if cfg.Account == "" {
		cfg.Account = "account"
	}
	if cfg.Tenant == "" {
		cfg.Tenant = "tenant"
	}
	if len(cfg.AllowedUsers) == 0 {
		cfg.AllowedUsers = []string{"user"}
	}
	p, err := newConnector(name, cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return p
}
func lineRequest(body []byte, secret string) *http.Request {
	request := httptest.NewRequest("POST", "http://saber/events", bytes.NewReader(body))
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	request.Header.Set("X-Line-Signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return request
}

func TestLineWebhookAuthorizationPrecedesPrivateMediaDownload(t *testing.T) {
	var downloads atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	p := testConnector(t, "line", config.ConnectorConfig{Endpoint: upstream.URL, Token: "token", Secret: "secret", Enabled: true})
	body := []byte(`{"events":[{"type":"message","source":{"type":"user","userId":"attacker"},"message":{"id":"m1","type":"audio"}}]}`)
	response := httptest.NewRecorder()
	p.webhook(response, lineRequest(body, "secret"))
	if response.Code != 403 {
		t.Fatalf("status=%d, want 403", response.Code)
	}
	if downloads.Load() != 0 {
		t.Fatal("越权回调触发了私有媒体下载")
	}
	select {
	case <-p.queue:
		t.Fatal("越权消息进入消费者")
	default:
	}
}
func TestLineWebhookRejectsTamperedSignatureBeforeDispatch(t *testing.T) {
	p := testConnector(t, "line", config.ConnectorConfig{Token: "token", Secret: "secret"})
	body := []byte(`{"events":[{"type":"message","source":{"type":"user","userId":"user"},"message":{"id":"m1","type":"text","text":"hello"}}]}`)
	response := httptest.NewRecorder()
	p.webhook(response, lineRequest(body, "different-secret"))
	if response.Code != 401 {
		t.Fatalf("status=%d", response.Code)
	}
	select {
	case <-p.queue:
		t.Fatal("签名失败的消息被派发")
	default:
	}
}
func TestLineWebhookDownloadsActualMediaAndBindsConfiguredAccount(t *testing.T) {
	wave := []byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x80\xbb\x00\x00\x00\x77\x01\x00\x02\x00\x10\x00data\x00\x00\x00\x00")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/bot/message/m1/content" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("错误的平台媒体请求: %s", r.URL.Path)
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(wave)
	}))
	defer upstream.Close()
	p := testConnector(t, "line", config.ConnectorConfig{Endpoint: upstream.URL, Token: "token", Secret: "secret", Account: "trusted-account"})
	body := []byte(`{"account":"forged","events":[{"type":"message","source":{"type":"user","userId":"user"},"message":{"id":"m1","type":"audio"}}]}`)
	response := httptest.NewRecorder()
	p.webhook(response, lineRequest(body, "secret"))
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case message := <-p.queue:
		if message.Session.Account != "trusted-account" || message.SenderID != "user" || !message.Direct {
			t.Fatalf("未绑定可信账号: %+v", message)
		}
		if len(message.Attachments) != 1 || !bytes.Equal(message.Attachments[0].Data, wave) || message.Attachments[0].Kind != "audio" {
			t.Fatal("入站语音没有真实媒体字节")
		}
	default:
		t.Fatal("合法媒体没有到达消费者")
	}
}
func TestNativeInboxScopesIdenticalMessageIDsBySessionAndSender(t *testing.T) {
	p := testConnector(t, "webhook", config.ConnectorConfig{AllowedUsers: []string{"u1", "u2"}})
	for _, user := range []string{"u1", "u2"} {
		if err := p.accept(context.Background(), chat.Message{ID: "same", SenderID: user, Direct: true, Text: "hello", Session: chat.Session{Conversation: user}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.queue) != 2 {
		t.Fatalf("不同用户消息 ID 碰撞，queue=%d", len(p.queue))
	}
}
func TestNativeInboxRecoveryNeverReplaysDispatchedSideEffects(t *testing.T) {
	stateDir := t.TempDir()
	cfg := config.ConnectorConfig{Account: "bot", AllowedUsers: []string{"user"}}
	p, err := newConnector("webhook", cfg, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	message := chat.Message{ID: "m1", SenderID: "user", Direct: true, Text: "run", Session: chat.Session{Conversation: "room"}}
	if err = p.accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	receipt := p.receiptID(message)
	p.Stop()
	reopened, err := newConnector("webhook", cfg, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Stop()
	if err = reopened.accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if len(reopened.queue) != 0 {
		t.Fatal("重启后重放了已派发输入")
	}
	var state string
	if err = reopened.db.QueryRow("SELECT state FROM inbox WHERE id=?", receipt).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "interrupted" {
		t.Fatalf("state=%s", state)
	}
}
func TestWebhookCancellationReachesConsumerAndCannotBeOverwritten(t *testing.T) {
	p := testConnector(t, "webhook", config.ConnectorConfig{Token: "secret"})
	started, returned := make(chan struct{}), make(chan struct{})
	p.handler = func(ctx context.Context, message chat.Message, adapter chat.Adapter) (agent.Result, error) {
		identity, ok := chat.IdentityFromContext(ctx)
		if !ok || identity.SenderID != "user" || identity.Session.Account != "account" {
			return agent.Result{}, errors.New("missing trusted identity")
		}
		close(started)
		<-ctx.Done()
		close(returned)
		return agent.Result{}, ctx.Err()
	}
	run, cancel := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); p.work(run) }()
	defer func() { cancel(); <-workerDone }()
	message := chat.Message{ID: "m1", SenderID: "user", Direct: true, Text: "run", Session: chat.Session{Conversation: "room"}}
	if err := p.accept(run, message); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("消费者未启动")
	}
	request := httptest.NewRequest("DELETE", "http://saber/responses/"+p.receiptID(message), nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	p.webhook(response, request)
	if response.Code != 200 {
		t.Fatalf("取消状态=%d %s", response.Code, response.Body.String())
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("取消未传递给真实消费者")
	}
	var state string
	if err := p.db.QueryRow("SELECT state FROM inbox WHERE id=?", p.receiptID(message)).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "canceled" {
		t.Fatalf("取消被处理回执覆盖: %s", state)
	}
}
func TestWecomReplyBindsOriginalMessageRequestNotLatestConversation(t *testing.T) {
	p := testConnector(t, "wecom", config.ConnectorConfig{BotID: "bot"})
	for _, id := range []string{"first", "second"} {
		_, err := p.decodeSocketEvents(context.Background(), map[string]any{"cmd": "aibot_msg_callback", "headers": map[string]any{"req_id": "req-" + id}, "body": map[string]any{"msgid": id, "chattype": "single", "from": map[string]any{"userid": "user"}, "text": map[string]any{"content": "question"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	var requestID string
	p.sendSocket = func(ctx context.Context, frame map[string]any) (map[string]any, error) {
		requestID = str(obj(frame["headers"])["req_id"])
		stream := obj(obj(frame["body"])["stream"])
		if stream["finish"] != true || str(stream["content"]) != "answer" {
			return nil, errors.New("invalid native stream")
		}
		return map[string]any{"errcode": float64(0)}, nil
	}
	id, err := p.Send(context.Background(), chat.Reply{Session: chat.Session{Platform: "wecom", Account: "account", Conversation: "user"}, ReplyTo: "first", Text: "answer", Status: chat.ReplyCompleted})
	if err != nil {
		t.Fatal(err)
	}
	if requestID != "req-first" || id == "" {
		t.Fatalf("错误的原生响应绑定: %s %s", requestID, id)
	}
}
func TestSharedMediaRootRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	p := testConnector(t, "simplex", config.ConnectorConfig{SharedMediaDir: root})
	if _, err := p.sharedMedia(filepath.Join(root, "escape"), "", "", "file"); err == nil {
		t.Fatal("读取了授权媒体目录之外的秘密")
	}
}
func TestUploadedMediaCannotCrossAccountOrConversation(t *testing.T) {
	p := testConnector(t, "line", config.ConnectorConfig{})
	session := chat.Session{Platform: "line", Account: "account", Conversation: "room"}
	payload, err := p.UploadFile(context.Background(), chat.FileUpload{Session: session, Name: "file.txt", Data: []byte("private")})
	if err != nil {
		t.Fatal(err)
	}
	other := session
	other.Conversation = "other"
	if _, err = p.SendUploadedFile(context.Background(), chat.Reply{Session: other, Text: "file"}, payload); err == nil {
		t.Fatal("媒体载荷跨会话复用")
	}
}
func TestHandoffResolverRequiresExactTrustedMapping(t *testing.T) {
	target := testConnector(t, "line", config.ConnectorConfig{Enabled: true, Account: "line-account"})
	registry := NewRegistry()
	registry.Register(target)
	source := config.HandoffEndpoint{Platform: "telegram", Account: "telegram-account", Conversation: "source", User: "tg-user", Tenant: "tenant", Person: "human", Direct: true}
	destination := config.HandoffEndpoint{Platform: "line", Account: "line-account", Conversation: "target", User: "line-user", Tenant: "tenant", Person: "human", Direct: true}
	cfg := config.HandoffConfig{Enabled: true, Routes: map[string]config.HandoffRoute{"route": {Source: source, Target: destination}}}
	resolver := HandoffResolver(cfg, registry)
	identity := handoffIdentity(source)
	result, err := resolver(context.Background(), identity, "route")
	if err != nil || result != handoffIdentity(destination) {
		t.Fatalf("合法交接=%+v,%v", result, err)
	}
	identity.SenderID = "attacker"
	if _, err = resolver(context.Background(), identity, "route"); err == nil {
		t.Fatal("非映射用户交接被放行")
	}
	route := cfg.Routes["route"]
	route.Target.Tenant = "other-tenant"
	cfg.Routes["route"] = route
	if _, err = resolver(context.Background(), handoffIdentity(source), "route"); err == nil {
		t.Fatal("跨租户交接被放行")
	}
}
func TestHandoffResolverRejectsDisabledDestination(t *testing.T) {
	target := testConnector(t, "line", config.ConnectorConfig{Enabled: false})
	registry := NewRegistry()
	registry.Register(target)
	source := config.HandoffEndpoint{Platform: "telegram", Account: "tg", Conversation: "source", User: "user", Tenant: "tenant", Person: "human", Direct: true}
	destination := config.HandoffEndpoint{Platform: "line", Account: "account", Conversation: "dest", User: "user", Tenant: "tenant", Person: "human", Direct: true}
	resolver := HandoffResolver(config.HandoffConfig{Enabled: true, Routes: map[string]config.HandoffRoute{"r": {Source: source, Target: destination}}}, registry)
	if _, err := resolver(context.Background(), handoffIdentity(source), "r"); err == nil {
		t.Fatal("关闭平台仍被用作交接目的地")
	}
}
func TestPublicMediaDoesNotFetchPrivateNetwork(t *testing.T) {
	p := testConnector(t, "line", config.ConnectorConfig{Endpoint: "https://api.line.me"})
	if _, err := p.fetchExternalMedia(context.Background(), "http://127.0.0.1:9/secret", "", "", false); err == nil {
		t.Fatal("媒体 URL 访问了未配置的私有网络")
	}
}
func TestWebhookResponseRetainsActualCompletedContent(t *testing.T) {
	p := testConnector(t, "webhook", config.ConnectorConfig{Token: "secret"})
	message := chat.Message{ID: "m1", SenderID: "user", Direct: true, Text: "question", Session: chat.Session{Conversation: "room"}}
	if err := p.accept(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if err := p.setState(context.Background(), "reply:room:m1", "actual result"); err != nil {
		t.Fatal(err)
	}
	_, err := p.db.Exec("UPDATE inbox SET state='completed' WHERE id=?", p.receiptID(message))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://saber/responses/"+p.receiptID(message), nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	p.webhook(response, request)
	var result map[string]any
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || str(result["text"]) != "actual result" {
		t.Fatalf("结果消费者未读取持久输出: %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret") {
		t.Fatal("响应泄露凭据")
	}
}
