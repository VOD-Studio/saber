package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// RegisterConnectors 把显式启用的原生平台登记到现有聊天链路；stateDir 必须是私有配置目录。
func RegisterConnectors(registry *Registry, cfg config.PlatformsConfig, stateDir string) error {
	if registry == nil {
		return errors.New("缺少平台注册表")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	names := make([]string, 0, len(cfg.Connectors))
	for name, c := range cfg.Connectors {
		if c.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if _, exists := registry.Lookup(name); exists {
			return fmt.Errorf("平台 %s 已注册", name)
		}
		p, err := newConnector(name, cfg.Connectors[name], stateDir)
		if err != nil {
			return fmt.Errorf("平台 %s: %w", name, err)
		}
		registry.Register(p)
	}
	return nil
}

type connector struct {
	name           string
	cfg            config.ConnectorConfig
	client         *http.Client
	db             *sql.DB
	handler        chat.Handler
	mu             sync.Mutex
	authMu         sync.Mutex
	redactedToken  atomic.Value
	sendMu         sync.Mutex
	cancel         context.CancelFunc
	done           chan struct{}
	server         *http.Server
	conn           io.Closer
	sendSocket     func(context.Context, map[string]any) (map[string]any, error)
	queue          chan chat.Message
	token          string
	tokenUntil     time.Time
	lastEdit       time.Time
	messageCancels map[string]context.CancelFunc
	closed         bool
	relayCaps      chat.Capabilities
}

func newConnector(name string, cfg config.ConnectorConfig, stateDir string) (*connector, error) {
	if stateDir == "" {
		return nil, errors.New("平台持久化需要私有 stateDir")
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "platforms"), 0700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(name + "\x00" + cfg.EffectiveAccount(name)))
	path := filepath.Join(stateDir, "platforms", hex.EncodeToString(sum[:16])+".db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS inbox(id TEXT PRIMARY KEY, state TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS state(key TEXT PRIMARY KEY,value TEXT NOT NULL);`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	_, err = db.Exec("UPDATE inbox SET state='interrupted',error='进程在派发后中断；不自动重放副作用' WHERE state='dispatched'")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	timeout := cfg.HTTPTimeoutSeconds
	if timeout == 0 {
		timeout = 45
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{Timeout: time.Duration(timeout) * time.Second, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return errors.New("平台请求不跟随重定向，避免凭据外泄")
	}}
	if cfg.MaxMediaBytes == 0 {
		cfg.MaxMediaBytes = 20 << 20
	}
	if cfg.PollSeconds == 0 {
		cfg.PollSeconds = 3
	}
	if cfg.WebhookPath == "" {
		cfg.WebhookPath = "/events"
	}
	if cfg.Mailbox == "" {
		cfg.Mailbox = "INBOX"
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint(name)
	}
	if cfg.SharedMediaDir != "" {
		if !filepath.IsAbs(cfg.SharedMediaDir) {
			_ = db.Close()
			return nil, errors.New("shared_media_dir 必须为运营者配置的绝对路径")
		}
		if _, err = db.Exec("INSERT INTO state(key,value) VALUES('media_root',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", cfg.SharedMediaDir); err != nil {
			_ = db.Close()
			return nil, err
		}
	} else {
		if _, err = db.Exec("DELETE FROM state WHERE key='media_root'"); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return &connector{name: name, cfg: cfg, client: client, db: db, queue: make(chan chat.Message, 64), messageCancels: make(map[string]context.CancelFunc)}, nil
}

func defaultEndpoint(name string) string {
	switch name {
	case "whatsapp_cloud":
		return "https://graph.facebook.com/v22.0"
	case "sms":
		return "https://api.twilio.com/2010-04-01"
	case "line":
		return "https://api.line.me"
	case "feishu":
		return "https://open.feishu.cn/open-apis"
	case "dingtalk":
		return "https://api.dingtalk.com"
	case "qqbot":
		return "https://api.sgroup.qq.com"
	case "wecom_callback":
		return "https://qyapi.weixin.qq.com/cgi-bin"
	case "weixin":
		return "https://ilinkai.weixin.qq.com"
	case "google_chat":
		return "https://chat.googleapis.com/v1"
	case "teams":
		return "https://smba.trafficmanager.net/teams"
	case "msgraph_webhook":
		return "https://graph.microsoft.com/v1.0"
	}
	return ""
}

// Name 返回固定的平台标识。
func (p *connector) Name() string { return p.name }

// Account 返回可信配置的账号，不受入站载荷覆盖。
func (p *connector) Account() string { return p.cfg.EffectiveAccount(p.name) }

// Enabled 判断扩展账号是否被运营者显式启用。
func (p *connector) Enabled() bool { return p.cfg.Enabled }

// DeliveryAdapter 返回用于持久任务结果投递的真实出站适配器。
func (p *connector) DeliveryAdapter() chat.Adapter { return p }

// Start 先完成本地监听或依赖检查，再启动可取消的入站消费者。
func (p *connector) Start(ctx context.Context, handler chat.Handler) error {
	if handler == nil {
		return errors.New("平台缺少共享 Handler")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("平台已关闭，需重新构造")
	}
	if p.cancel != nil {
		return nil
	}
	run, cancel := context.WithCancel(ctx)
	p.handler = handler
	var receive func(context.Context) error
	switch p.name {
	case "email":
		receive = p.receiveEmail
	case "irc":
		receive = p.receiveIRC
	case "mattermost", "simplex", "wecom", "qqbot", "dingtalk":
		receive = p.receiveSocket
	case "signal", "photon", "ntfy":
		receive = p.receiveStream
	case "relay":
		receive = p.receiveRelay
	case "whatsapp", "weixin", "buzz":
		receive = p.receivePoll
	default:
		if p.cfg.Listen == "" {
			cancel()
			return fmt.Errorf("%s 需要 listen（平台原生回调监听地址）", p.name)
		}
		listener, err := net.Listen("tcp", p.cfg.Listen)
		if err != nil {
			cancel()
			return err
		}
		p.server = &http.Server{Handler: http.HandlerFunc(p.webhook), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 0, MaxHeaderBytes: 64 << 10}
		receive = func(ctx context.Context) error {
			err := p.server.Serve(listener)
			if errors.Is(err, http.ErrServerClosed) {
				return ctx.Err()
			}
			return err
		}
	}
	if err := p.checkDependency(run); err != nil {
		cancel()
		if p.server != nil {
			_ = p.server.Close()
		}
		return err
	}
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		var workers sync.WaitGroup
		workers.Add(1)
		go func() { defer workers.Done(); p.work(run) }()
		defer workers.Wait()
		defer cancel()
		delay := time.Second
		for run.Err() == nil {
			err := receive(run)
			if run.Err() != nil {
				return
			}
			if err != nil {
				slog.Error("平台入站连接失败", "platform", p.name, "error", p.redact(err.Error()))
			}
			if p.server != nil {
				return
			}
			select {
			case <-run.Done():
				return
			case <-time.After(delay):
			}
			delay = min(delay*2, 60*time.Second)
		}
	}()
	return nil
}

// Stop 取消网络和在途 Handler；不重放已派发事件。
func (p *connector) Stop() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	cancel, done := p.cancel, p.done
	p.cancel = nil
	conn, server := p.conn, p.server
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
	if server != nil {
		_ = server.Close()
	}
	if done != nil {
		<-done
	}
	p.client.CloseIdleConnections()
	if err := p.db.Close(); err != nil {
		slog.Error("关闭平台持久库失败", "platform", p.name, "error", err)
	}
}

func (p *connector) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-p.queue:
			var currentState string
			receipt := p.receiptID(msg)
			if err := p.db.QueryRowContext(ctx, "SELECT state FROM inbox WHERE id=?", receipt).Scan(&currentState); err != nil {
				slog.Error("读取入站回执失败", "platform", p.name, "error", err)
				continue
			}
			if currentState != "dispatched" {
				continue
			}
			identity := chat.Identity{Session: msg.Session, SenderID: msg.SenderID, Direct: msg.Direct}
			call, cancel := context.WithCancel(chat.WithIdentity(ctx, identity))
			p.mu.Lock()
			p.messageCancels[receipt] = cancel
			p.mu.Unlock()
			if err := p.db.QueryRowContext(ctx, "SELECT state FROM inbox WHERE id=?", receipt).Scan(&currentState); err != nil || currentState != "dispatched" {
				cancel()
				p.mu.Lock()
				delete(p.messageCancels, receipt)
				p.mu.Unlock()
				continue
			}
			result, err := p.handler(call, msg, p)
			cancel()
			p.mu.Lock()
			delete(p.messageCancels, receipt)
			p.mu.Unlock()
			state, detail := "completed", ""
			if err != nil {
				state, detail = "failed", p.redact(err.Error())
				slog.Error("平台消息处理失败", "platform", p.name, "error", detail)
			}
			if err == nil && result.Content != "" && (p.name == "a2a" || p.name == "webhook") {
				if storeErr := p.setState(context.WithoutCancel(ctx), "reply:"+msg.Session.Conversation+":"+msg.ID, result.Content); storeErr != nil {
					state, detail = "failed", storeErr.Error()
				}
			}
			if _, updateErr := p.db.ExecContext(context.WithoutCancel(ctx), "UPDATE inbox SET state=?,error=? WHERE id=? AND state='dispatched'", state, detail, receipt); updateErr != nil {
				slog.Error("平台回执持久化失败", "platform", p.name, "error", updateErr)
			}
		}
	}
}

func (p *connector) accept(ctx context.Context, msg chat.Message) error {
	msg.Session.Platform = p.name
	msg.Session.Account = p.Account()
	if err := msg.Validate(); err != nil {
		return err
	}
	if err := p.incomingAllowed(msg); err != nil {
		return err
	}
	if !msg.Direct {
		if !contains(p.cfg.AllowedGroups, msg.Session.Conversation) && !contains(p.cfg.AllowedGroups, "*") {
			return errors.New("群聊未获授权")
		}
		if p.cfg.GroupMentionOnly && (p.cfg.BotID == "" || !strings.Contains(msg.Text, p.cfg.BotID)) {
			return errors.New("群聊仅接受显式提及")
		}
	}
	if msg.ID == "" {
		return errors.New("平台事件缺少稳定消息 ID，不能安全去重")
	}
	receipt := p.receiptID(msg)
	result, err := p.db.ExecContext(ctx, "INSERT OR IGNORE INTO inbox(id,state,created) VALUES(?,'dispatched',?)", receipt, time.Now().Unix())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	if err = p.setState(ctx, "message-session:"+receipt, receipt+"\x00"+msg.Session.Conversation); err != nil {
		return err
	}
	if p.name == "webhook" {
		metadata := chat.Message{Session: msg.Session, ID: msg.ID, SenderID: msg.SenderID, Direct: msg.Direct}
		if err = p.setState(ctx, "receipt-message:"+receipt, jsonString(metadata)); err != nil {
			return err
		}
	}
	select {
	case p.queue <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		_, err = p.db.ExecContext(ctx, "DELETE FROM inbox WHERE id=? AND state='dispatched'", receipt)
		if err != nil {
			return err
		}
		return errors.New("平台入站队列已满，请重试")
	}
}

func contains(values []string, v string) bool {
	for _, item := range values {
		if item == v {
			return true
		}
	}
	return false
}
func (p *connector) redact(s string) string {
	for _, secret := range []string{p.cfg.Token, p.cfg.Secret, p.cfg.Password, p.cfg.EncodingAESKey} {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	if secret, ok := p.redactedToken.Load().(string); ok && secret != "" {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	return s
}
func (p *connector) validateSession(ctx context.Context, s chat.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Platform != p.name || s.Account != p.Account() {
		return errors.New("目标平台账号不匹配")
	}
	return nil
}
func (p *connector) state(key string) string {
	var v string
	_ = p.db.QueryRow("SELECT value FROM state WHERE key=?", key).Scan(&v)
	return v
}
func (p *connector) setState(ctx context.Context, key, value string) error {
	_, err := p.db.ExecContext(ctx, "INSERT INTO state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}

func (p *connector) request(ctx context.Context, method, path string, payload any) (map[string]any, error) {
	var body io.Reader
	contentType := "application/json"
	if payload != nil {
		switch v := payload.(type) {
		case url.Values:
			body = strings.NewReader(v.Encode())
			contentType = "application/x-www-form-urlencoded"
		case []byte:
			body = bytes.NewReader(v)
			contentType = "application/octet-stream"
		default:
			data, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			body = bytes.NewReader(data)
		}
	}
	base := p.cfg.Endpoint
	if scoped, ok := ctx.Value(platformEndpointKey{}).(string); ok {
		base = scoped
	}
	endpoint := strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if err = p.authorizeRequest(ctx, req); err != nil {
		return nil, err
	}
	response, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %s", p.name, method, p.redact(err.Error()))
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s HTTP %d: %s", p.name, response.StatusCode, p.redact(string(data)))
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err = json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s API 无效 JSON: %w", p.name, err)
	}
	if apiErr(out) {
		return nil, fmt.Errorf("%s API 拒绝: %s", p.name, p.redact(string(data)))
	}
	return out, nil
}
func apiErr(m map[string]any) bool {
	if m["error"] != nil {
		return true
	}
	if ok, exists := m["ok"].(bool); exists && !ok {
		return true
	}
	if ok, exists := m["success"].(bool); exists && !ok {
		return true
	}
	for _, key := range []string{"code", "errcode", "ret"} {
		if n, ok := m[key].(float64); ok && n != 0 {
			return true
		}
	}
	return false
}
func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func arr(v any) []any { a, _ := v.([]any); return a }
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	case json.Number:
		return x.String()
	}
	return ""
}
func first(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := str(m[key]); v != "" {
			return v
		}
	}
	return ""
}
func stableID(m map[string]any) string {
	if id := first(m, "id", "messageId", "message_id", "msg_id", "guid", "event_id", "timestamp"); id != "" {
		return id
	}
	return ""
}
