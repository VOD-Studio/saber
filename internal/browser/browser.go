package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/task"
	"rua.plus/saber/internal/toolkit"
)

// Service 管理按可信用户、会话和名字隔离的浏览器，连接失败不会重放副作用。
type Service struct {
	cfg      config.BrowserConfig
	host     toolkit.Host
	mu       sync.Mutex
	sessions map[string]*browserSession
	active   map[string]string
	closed   bool
	stop     chan struct{}
	wg       sync.WaitGroup
	redactMu sync.RWMutex
	redactor func(string) string
	secrets  []string
	sourceFn func(context.Context, string, string) (string, func() error, error)
}

type browserSession struct {
	owner         *Service
	key           string
	label         string
	cdp           *cdpConn
	source        sourceSession
	contextID     string
	mu            sync.Mutex
	tabs          map[string]string
	ownedTabs     map[string]bool
	frameSessions map[string]string
	frameContexts map[string]int64
	scripts       map[string]string
	current       string
	dialogs       map[string]dialog
	blocked       []string
	last          time.Time
	op            chan struct{}
	events        chan cdpMessage
	stop          chan struct{}
	once          sync.Once
}

type dialog struct {
	ID      string `json:"dialog_id"`
	Target  string `json:"target_id"`
	Type    string `json:"type"`
	Message string `json:"message"`
	URL     string `json:"url"`
	opened  time.Time
	session string
	request string
}

var sessionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// New 验证静态配置；浏览器/凭据诊断在第一次真正获准调用时进行。
func New(cfg config.BrowserConfig, host toolkit.Host) (toolkit.Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Enabled && (host.Check == nil || host.Workspace == nil || host.StateDir == "") {
		return nil, errors.New("browser requires task-bound authorization, workspace and private state directory")
	}
	s := &Service{cfg: cfg, host: host, sessions: make(map[string]*browserSession), active: make(map[string]string), stop: make(chan struct{})}
	if cfg.Enabled {
		s.wg.Add(1)
		go s.janitor()
	}
	return s, nil
}

// SetRedactor 绑定保险库的秘密遮蔽器；所有模型可见文字都会使用同一个策略。
func (s *Service) SetRedactor(fn func(string) string) {
	s.redactMu.Lock()
	s.redactor = fn
	s.redactMu.Unlock()
}

// SetSource 绑定经宿主授权的插件浏览器生命周期，CDP凭据不会进入模型结果。
func (s *Service) SetSource(fn func(context.Context, string, string) (string, func() error, error)) {
	s.redactMu.Lock()
	s.sourceFn = fn
	s.redactMu.Unlock()
}
func (s *Service) redact(value string) string {
	s.redactMu.RLock()
	fn := s.redactor
	for _, secret := range s.secrets {
		value = strings.ReplaceAll(value, secret, "[redacted]")
	}
	s.redactMu.RUnlock()
	if fn != nil {
		value = fn(value)
	}
	for _, secret := range []string{s.cfg.APIKey, s.cfg.CDPURL} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}
func (s *Service) remember(secret string) {
	if secret == "" {
		return
	}
	s.redactMu.Lock()
	defer s.redactMu.Unlock()
	for _, value := range s.secrets {
		if value == secret {
			return
		}
	}
	if len(s.secrets) >= 256 {
		copy(s.secrets, s.secrets[1:])
		s.secrets = s.secrets[:255]
	}
	s.secrets = append(s.secrets, secret)
}
func (s *Service) redactData(data any) any {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	var result any
	if json.Unmarshal(raw, &result) != nil {
		return s.redact(fmt.Sprint(data))
	}
	return s.redactValue(result)
}
func (s *Service) redactValue(value any) any {
	switch value := value.(type) {
	case string:
		return s.redact(value)
	case map[string]any:
		type rename struct {
			old, new string
			item     any
		}
		var changes []rename
		for key, item := range value {
			item = s.redactValue(item)
			value[key] = item
			if redacted := s.redact(key); redacted != key {
				changes = append(changes, rename{key, redacted, item})
			}
		}
		for _, change := range changes {
			delete(value, change.old)
			value[change.new] = change.item
		}
		return value
	case []any:
		for index, item := range value {
			value[index] = s.redactValue(item)
		}
		return value
	default:
		return value
	}
}

// Definitions 要求精确浏览器权限、执行权限和额外网络/宿主能力，列举和调用同样校验。
func (s *Service) Definitions() []toolkit.Definition {
	if !s.cfg.Enabled {
		return nil
	}
	defs := []struct {
		name, description string
		props             map[string]any
		required          []string
	}{
		{"browser_exec", "Drive a persistent real browser with JavaScript helpers: new_tab(url), goto_url(url), tabs(), use_tab(id), close_tab(id), wait_for_load(), page_info(), js(expression), fill_input(selector,text), click(selector), click_at_xy(x,y), key(key), scroll(x,y), capture_screenshot(), cdp(method,params), cookies() for isolated sessions only. Code is JavaScript, not Python. Each session isolates tabs and browser state; print(value) returns output.", map[string]any{"code": map[string]any{"type": "string"}, "actions": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}, "session": map[string]any{"type": "string"}, "timeout_s": map[string]any{"type": "integer", "minimum": 1, "maximum": 1800}, "local": map[string]any{"type": "boolean"}}, nil},
		{"browser_cdp", "Call the real CDP connection. target_id/frame_id must belong to this session. Security interception and ownership settings cannot be disabled.", map[string]any{"method": map[string]any{"type": "string"}, "params": map[string]any{"type": "object"}, "target_id": map[string]any{"type": "string"}, "frame_id": map[string]any{"type": "string"}, "session": map[string]any{"type": "string"}}, []string{"method"}},
		{"browser_dialog", "List, accept or dismiss a captured native JavaScript dialog. Can be invoked while browser_exec is waiting on that dialog.", map[string]any{"action": map[string]any{"type": "string", "enum": []string{"list", "accept", "dismiss"}}, "dialog_id": map[string]any{"type": "string"}, "prompt_text": map[string]any{"type": "string"}, "session": map[string]any{"type": "string"}}, []string{"action"}},
		{"browser_session", "List this caller's browser sessions, inspect status or close an owned session. Reconnect only on the next explicit call; dispatched actions are never replayed.", map[string]any{"action": map[string]any{"type": "string", "enum": []string{"list", "status", "close"}}, "session": map[string]any{"type": "string"}}, []string{"action"}},
	}
	out := make([]toolkit.Definition, 0, len(defs))
	for _, d := range defs {
		out = append(out, toolkit.Definition{Tool: openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: d.name, Description: d.description, Parameters: map[string]any{"type": "object", "properties": d.props, "required": d.required, "additionalProperties": false}}}, Permission: d.name, Requires: []string{"exec"}, External: true})
	}
	return out
}

func (s *Service) authorize(ctx context.Context, name string) error {
	if !s.cfg.Enabled {
		return errors.New("browser is disabled")
	}
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return errors.New("browser requires trusted identity")
	}
	if s.cfg.AttachExistingTabs && (s.host.IsAdmin == nil || !s.host.IsAdmin(ctx)) {
		return errors.New("adopting existing operator browser tabs requires a trusted administrator")
	}
	if err := s.host.Check(ctx, name, true); err != nil {
		return err
	}
	if err := s.host.Check(ctx, "exec", false); err != nil {
		return err
	}
	_, err := s.host.Workspace(ctx)
	return err
}
func browserLabel(ctx context.Context, label string) string {
	if label != "" {
		return label
	}
	if id := task.ID(ctx); id > 0 {
		return fmt.Sprintf("task-%d", id)
	}
	return "default"
}
func scopeKey(ctx context.Context, label string) (string, error) {
	id, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return "", errors.New("trusted identity required")
	}
	label = browserLabel(ctx, label)
	if !sessionName.MatchString(label) {
		return "", errors.New("session must be 1..64 alphanumeric/dot/underscore/dash characters")
	}
	return id.UserKey() + string(id.Session.Key()) + "/" + label, nil
}

// Execute 执行已获准的真实浏览器动作；取消后销毁该驱动，避免未知动作在后台继续。
func (s *Service) Execute(ctx context.Context, name string, args map[string]any) (result any, err error) {
	defer func() {
		if err != nil {
			err = redactedError{message: s.redact(err.Error()), cause: err}
		}
	}()
	switch name {
	case "browser_exec", "browser_cdp", "browser_dialog", "browser_session":
	default:
		return nil, fmt.Errorf("unknown browser tool %q", name)
	}
	if err = s.authorize(ctx, name); err != nil {
		return nil, err
	}
	label, _ := args["session"].(string)
	label = browserLabel(ctx, label)
	key, e := scopeKey(ctx, label)
	if e != nil {
		return nil, e
	}
	if name == "browser_session" {
		return s.sessionCommand(ctx, key, args)
	}
	baseKey := key
	local, _ := args["local"].(bool)
	if !local && s.cfg.AutoLocalForPrivateURLs && s.cfg.Provider != "local" && s.cfg.CDPURL == "" {
		if actions, ok := args["actions"].([]any); ok {
			for _, a := range actions {
				m, _ := a.(map[string]any)
				if raw, _ := m["url"].(string); raw != "" && raw != "about:blank" {
					private, e := urlPrivate(ctx, raw)
					if e != nil {
						return nil, e
					}
					local = private
					break
				}
			}
		}
	}
	if local && !s.cfg.AllowHostProcess {
		return nil, errors.New("explicit local browser routing requires browser.allow_host_process")
	}
	if local {
		explicit, _ := args["local"].(bool)
		if explicit && s.cfg.UseRealProfile {
			key += "::profile"
		} else {
			key += "::local"
		}
	}
	if name != "browser_exec" {
		s.mu.Lock()
		if selected := s.active[baseKey]; selected != "" {
			key = selected
			local = localRoute(key)
		}
		existing := s.sessions[key]
		s.mu.Unlock()
		if name == "browser_dialog" {
			if existing == nil {
				return nil, errors.New("no active browser session; open a page before handling dialogs")
			}
			out, e := existing.dialogCommand(ctx, args)
			if ctx.Err() != nil {
				e = errors.Join(e, ctx.Err(), s.remove(key, existing))
			}
			return out, e
		}
	}
	if name == "browser_exec" && !local {
		if _, explicit := args["local"]; !explicit {
			s.mu.Lock()
			if selected := s.active[baseKey]; selected != "" && s.sessions[selected] != nil {
				key = selected
				local = localRoute(key)
			}
			s.mu.Unlock()
		}
	}
	timeout := s.cfg.CommandTimeout
	if name == "browser_exec" {
		timeout = 300
	}
	if n, ok := args["timeout_s"].(float64); ok {
		timeout = int(n)
	}
	if n, ok := args["timeout_s"].(int); ok {
		timeout = n
	}
	if timeout < 1 || timeout > 1800 {
		return nil, errors.New("timeout_s must be 1..1800")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	b, e := s.get(ctx, key, label, local)
	if e != nil {
		return nil, e
	}
	s.mu.Lock()
	s.active[baseKey] = key
	s.mu.Unlock()
	if name == "browser_exec" {
		return b.executeCode(ctx, args)
	}
	select {
	case b.op <- struct{}{}:
		defer func() { <-b.op }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	b.mu.Lock()
	b.last = time.Now()
	b.mu.Unlock()
	defer func() {
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err(), s.remove(key, b))
		}
	}()
	method, _ := args["method"].(string)
	params, _ := args["params"].(map[string]any)
	target, _ := args["target_id"].(string)
	frame, _ := args["frame_id"].(string)
	data, e := b.raw(ctx, method, params, target, frame)
	if e != nil {
		return nil, e
	}
	return b.cdpResult(method, data)
}

func (s *Service) get(ctx context.Context, key, label string, local bool) (*browserSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("browser service closed")
	}
	if b := s.sessions[key]; b != nil {
		select {
		case <-b.cdp.done:
			delete(s.sessions, key)
			go func() { _ = b.close() }()
		default:
			return b, nil
		}
	}
	if len(s.sessions) >= s.cfg.MaxSessions {
		return nil, errors.New("browser session limit reached; close an idle session")
	}
	if s.cfg.AttachExistingTabs && len(s.sessions) > 0 {
		return nil, errors.New("connected operator tabs are exclusive to one named session; reuse it or close it before opening another")
	}
	b := &browserSession{owner: s, key: key, label: label, tabs: make(map[string]string), ownedTabs: make(map[string]bool), frameSessions: make(map[string]string), frameContexts: make(map[string]int64), scripts: make(map[string]string), dialogs: make(map[string]dialog), last: time.Now(), op: make(chan struct{}, 1), events: make(chan cdpMessage, 512), stop: make(chan struct{})}
	source, e := s.source(ctx, key, local)
	if e != nil {
		return nil, e
	}
	b.source = source
	s.remember(source.endpoint)
	b.cdp, e = dialCDP(ctx, source.endpoint, func(msg cdpMessage) {
		select {
		case b.events <- msg:
		case <-b.stop:
		default:
			go func() {
				if b.cdp != nil {
					b.cdp.fail(errors.New("CDP event queue overflow; security interception closed the session"))
				}
			}()
		}
	})
	if e != nil {
		return nil, errors.Join(e, source.release())
	}
	go b.eventLoop()
	if s.cfg.CDPURL != "" && !s.cfg.AttachExistingTabs && !local {
		var out struct {
			BrowserContextID string `json:"browserContextId"`
		}
		if e = b.cdp.call(ctx, "", "Target.createBrowserContext", map[string]any{"disposeOnDetach": true}, &out); e != nil {
			_ = b.close()
			return nil, e
		}
		b.contextID = out.BrowserContextID
	}
	if s.cfg.AttachExistingTabs && !local {
		e = b.attachExisting(ctx)
	} else {
		_, e = b.newTab(ctx, "about:blank")
	}
	if e != nil {
		_ = b.close()
		return nil, e
	}
	s.sessions[key] = b
	return b, nil
}

func (s *Service) remove(key string, b *browserSession) error {
	s.mu.Lock()
	if s.sessions[key] == b {
		delete(s.sessions, key)
	}
	for base, selected := range s.active {
		if selected == key {
			delete(s.active, base)
		}
	}
	s.mu.Unlock()
	return b.close()
}
func (b *browserSession) close() error {
	var err error
	b.once.Do(func() {
		close(b.stop)
		b.mu.Lock()
		type target struct {
			id, session string
			owned       bool
		}
		targets := make([]target, 0, len(b.tabs))
		for id, session := range b.tabs {
			targets = append(targets, target{id, session, b.ownedTabs[id]})
		}
		scripts := make(map[string]string, len(b.scripts))
		for session, id := range b.scripts {
			scripts[session] = id
		}
		b.mu.Unlock()
		for session, id := range scripts {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err = errors.Join(err, b.cdp.call(ctx, session, "Page.removeScriptToEvaluateOnNewDocument", map[string]any{"identifier": id}, nil))
			cancel()
		}
		if b.contextID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err = errors.Join(err, b.cdp.call(ctx, "", "Target.disposeBrowserContext", map[string]any{"browserContextId": b.contextID}, nil))
			cancel()
		} else {
			for _, t := range targets {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				if t.owned {
					err = errors.Join(err, b.cdp.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": t.id}, nil))
				} else {
					if b.owner.cfg.Provider == "browserbase" {
						_, e := b.evalSession(ctx, t.session, `(()=>{const o=window.__saberDialogOriginals;if(o){window.alert=o.alert;window.confirm=o.confirm;window.prompt=o.prompt;delete window.__saberDialogOriginals}return true})()`)
						err = errors.Join(err, e)
					}
					err = errors.Join(err, b.cdp.call(ctx, t.session, "Network.setBypassServiceWorker", map[string]any{"bypass": false}, nil))
					err = errors.Join(err, b.cdp.call(ctx, "", "Target.detachFromTarget", map[string]any{"sessionId": t.session}, nil))
				}
				cancel()
			}
		}
		err = errors.Join(err, b.cdp.close(), b.source.release())
	})
	return err
}
func (s *Service) janitor() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.mu.Lock()
			var stale []*browserSession
			for _, b := range s.sessions {
				b.mu.Lock()
				idle := time.Since(b.last) > time.Duration(s.cfg.InactivityTimeout)*time.Second
				b.mu.Unlock()
				if idle {
					select {
					case b.op <- struct{}{}:
						stale = append(stale, b)
						delete(s.sessions, b.key)
						for base, selected := range s.active {
							if selected == b.key {
								delete(s.active, base)
							}
						}
					default:
					}
				}
			}
			s.mu.Unlock()
			for _, b := range stale {
				_ = b.close()
				<-b.op
			}
		}
	}
}

// Close 关闭全部自有标签页、进程、连接和云会话，不终止用户连接的浏览器。
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	sessions := s.sessions
	s.sessions = make(map[string]*browserSession)
	s.mu.Unlock()
	s.wg.Wait()
	var err error
	for _, b := range sessions {
		err = errors.Join(err, b.close())
	}
	if err != nil {
		return redactedError{message: s.redact(err.Error()), cause: err}
	}
	return nil
}

func (s *Service) sessionCommand(ctx context.Context, key string, args map[string]any) (any, error) {
	action, _ := args["action"].(string)
	switch action {
	case "list":
		id, _ := chat.IdentityFromContext(ctx)
		prefix := id.UserKey() + string(id.Session.Key()) + "/"
		s.mu.Lock()
		defer s.mu.Unlock()
		var list []any
		for k, b := range s.sessions {
			if strings.HasPrefix(k, prefix) {
				b.mu.Lock()
				list = append(list, map[string]any{"session": b.label, "tabs": len(b.tabs), "local": b.source.local, "text_only": b.source.textOnly, "last_used": b.last})
				b.mu.Unlock()
			}
		}
		return list, nil
	case "status", "close":
		s.mu.Lock()
		b := s.sessions[key]
		side := s.sessions[key+"::local"]
		profile := s.sessions[key+"::profile"]
		selected := s.sessions[s.active[key]]
		s.mu.Unlock()
		if action == "close" {
			var err error
			for _, item := range []*browserSession{b, side, profile} {
				if item != nil {
					err = errors.Join(err, s.remove(item.key, item))
				}
			}
			return map[string]any{"closed": true}, err
		}
		if selected != nil {
			b = selected
		} else if b == nil {
			b = side
			if b == nil {
				b = profile
			}
		}
		if b == nil {
			return map[string]any{"active": false}, nil
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		return s.redactData(map[string]any{"active": true, "session": b.label, "target_id": b.current, "tabs": b.tabs, "dialogs": b.dialogs, "blocked_requests": b.blocked, "local": b.source.local, "text_only": b.source.textOnly, "browser_note": b.source.note}), nil
	default:
		return nil, errors.New("browser_session action must be list, status or close")
	}
}

func urlPrivate(ctx context.Context, raw string) (bool, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ws" && u.Scheme != "wss") {
		return false, errors.New("browser navigation permits only absolute HTTP(S)/WS(S) URLs without userinfo")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "metadata.google.internal" || host == "metadata" || host == "169.254.169.254" || host == "100.100.100.200" || host == "fd00:ec2::254" {
		return false, errors.New("cloud metadata endpoint is always blocked")
	}
	ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
	if e != nil {
		return false, fmt.Errorf("browser URL DNS lookup failed: %w", e)
	}
	if len(ips) == 0 {
		return false, errors.New("browser URL DNS returned no addresses")
	}
	for _, item := range ips {
		ip := item.IP
		if ip.String() == "169.254.169.254" || ip.String() == "100.100.100.200" {
			return false, errors.New("cloud metadata address is always blocked")
		}
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return true, nil
		}
	}
	return false, nil
}
func (b *browserSession) checkURL(ctx context.Context, raw string) error {
	if raw == "about:blank" {
		return nil
	}
	private, e := urlPrivate(ctx, raw)
	if e != nil {
		return e
	}
	if private && !b.owner.cfg.AllowPrivateURLs {
		return errors.New("private/internal browser URL blocked; configure allow_private_urls explicitly (local routing does not waive this policy)")
	}
	return nil
}
