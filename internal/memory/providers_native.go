package memory

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
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/task"
	"rua.plus/saber/internal/toolkit"
)

// NativeBackend 为 Honcho、OpenViking、Mem0 提供原生 HTTP 协议；不运行 Hermes/Python agent 内核。
type NativeBackend struct {
	owner       *Service
	id          string
	cfg         config.MemoryBackendConfig
	host        toolkit.Host
	client      *http.Client
	mu          sync.Mutex
	closed      bool
	vikingUsers map[string]string
	fileReader  func(context.Context, string, int) ([]byte, error)
}

// NewNativeBackend 构造真实厂商 transport；root 配置和凭据不进入模型参数。
func (s *Service) NewNativeBackend(id string, cfg config.MemoryBackendConfig, host toolkit.Host) (*NativeBackend, error) {
	if validateSkillName(id) != nil {
		return nil, errors.New("native memory backend id 无效")
	}
	if host.Check == nil {
		return nil, errors.New("native memory backend 需要宿主权限检查")
	}
	switch cfg.Type {
	case "honcho", "mem0", "openviking":
	default:
		return nil, fmt.Errorf("该 transport 不支持 memory backend %q", cfg.Type)
	}
	if cfg.APIKeyEnv != "" {
		cfg.APIKey = os.Getenv(cfg.APIKeyEnv)
	}
	if cfg.Type == "mem0" {
		mode, _ := cfg.Options["mode"].(string)
		switch mode {
		case "", "platform":
		case "selfhosted", "oss":
			if cfg.Endpoint == "" {
				return nil, errors.New("Mem0 selfhosted/OSS需要显式配置官方REST服务器endpoint")
			}
		default:
			return nil, errors.New("Mem0 mode必须platform/selfhosted/oss")
		}
	}
	if cfg.Endpoint == "" {
		switch cfg.Type {
		case "honcho":
			cfg.Endpoint = "https://api.honcho.dev"
		case "mem0":
			cfg.Endpoint = "https://api.mem0.ai"
		case "openviking":
			cfg.Endpoint = "http://127.0.0.1:1933"
		}
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("memory endpoint 必须配置的无凭据 HTTP(S) URL")
	}
	if u.Scheme == "http" && !cfg.AllowPrivate {
		return nil, errors.New("HTTP/selfhosted memory endpoint 需要显式 allow_private")
	}
	if cfg.TimeoutSeconds < 0 || cfg.TimeoutSeconds > 300 {
		return nil, errors.New("memory backend timeout_seconds 必须0..300")
	}
	seconds := cfg.TimeoutSeconds
	if seconds == 0 {
		seconds = 30
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var last error
		for _, ip := range ips {
			if !cfg.AllowPrivate && (ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() || ip.IP.IsUnspecified() || ip.IP.IsMulticast()) {
				last = errors.New("memory endpoint 解析到私有地址，需要显式 allow_private")
				continue
			}
			conn, err := (&net.Dialer{Timeout: time.Duration(seconds) * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		if last == nil {
			last = errors.New("memory endpoint 没有可用地址")
		}
		return nil, last
	}
	return &NativeBackend{owner: s, id: id, cfg: cfg, host: host, client: &http.Client{Transport: transport, Timeout: time.Duration(seconds) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("memory transport 禁止重定向，避免泄露凭据")
	}}, vikingUsers: make(map[string]string)}, nil
}

// SetFileReader 绑定执行器的原始字节读取；本地资源无法越过 task/workspace 文件权限。
func (n *NativeBackend) SetFileReader(reader func(context.Context, string, int) ([]byte, error)) {
	n.fileReader = reader
}

// Close 关闭后拒绝新调用并释放空闲连接；后台召回由 memory.Service 的生命周期取消并等待。
func (n *NativeBackend) Close() error {
	n.mu.Lock()
	n.closed = true
	n.mu.Unlock()
	n.client.CloseIdleConnections()
	return nil
}

// ProviderCall 兼容公共 dispatcher，名称必须匹配配置且只提供 memory 能力。
func (n *NativeBackend) ProviderCall(ctx context.Context, name, kind, operation string, args map[string]any) (any, error) {
	if name != "native:"+n.id || kind != "memory" {
		return nil, errors.New("native memory provider 路由不匹配")
	}
	return n.Call(ctx, operation, args)
}

// Call 每次重验身份及精确 memory_<id> grant；模型不能改变提供者 URL、凭据或存储作用域。
func (n *NativeBackend) Call(ctx context.Context, operation string, args map[string]any) (any, error) {
	n.mu.Lock()
	closed := n.closed
	n.mu.Unlock()
	if closed {
		return nil, errors.New("native memory backend 已关闭")
	}
	if err := n.host.Check(ctx, "memory_"+n.id, true); err != nil {
		return nil, err
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return nil, ErrScope
	}
	spaceKey, sessionKey, err := providerScopeKeys(identity)
	if err != nil {
		return nil, err
	}
	if supplied, ok := args["space"].(string); ok && supplied != spaceKey {
		return nil, errors.New("memory space 与可信身份不符")
	}
	if supplied, ok := args["conversation_key"].(string); ok && supplied != sessionKey {
		return nil, errors.New("memory session 与可信身份不符")
	}
	payload, _ := args["payload"].(map[string]any)
	if payload == nil {
		payload = make(map[string]any)
	}
	if providerOperationWrites(operation) && !n.owner.ExternalMemoryWritesAllowed(ctx) {
		return nil, errors.New("无权写入群共享外部记忆")
	}
	if operation == "tools" {
		return n.tools(), nil
	}
	if operation == "tool" {
		tool, _ := payload["tool_name"].(string)
		arguments, _ := payload["arguments"].(map[string]any)
		if arguments == nil {
			arguments = make(map[string]any)
		}
		scope, err := space(identity)
		if err != nil {
			return nil, err
		}
		if !providerToolReadOnly(tool, arguments) && !n.owner.writeAllowed(identity, scope) {
			return nil, errors.New("无权写入群共享外部记忆")
		}
	}
	var call func() (any, error)
	switch n.cfg.Type {
	case "honcho":
		call = func() (any, error) { return n.honcho(ctx, spaceKey, sessionKey, operation, payload) }
	case "mem0":
		call = func() (any, error) { return n.mem0(ctx, spaceKey, sessionKey, operation, payload) }
	case "openviking":
		call = func() (any, error) { return n.openviking(ctx, spaceKey, sessionKey, operation, payload) }
	}
	if operation == "sync_turn" || operation == "pre_compress" || operation == "delegation" {
		return n.once(ctx, spaceKey, sessionKey, operation, payload, call)
	}
	return call()
}

func (n *NativeBackend) key() string { return strings.TrimSpace(n.cfg.APIKey) }
func (n *NativeBackend) option(name, def string) string {
	if value, ok := n.cfg.Options[name].(string); ok && value != "" {
		return value
	}
	return def
}
func nativeInt(args map[string]any, key string, def, maximum int) int {
	var v int
	switch value := args[key].(type) {
	case float64:
		v = int(value)
	case int:
		v = value
	case int64:
		v = int(value)
	case json.Number:
		i, _ := value.Int64()
		v = int(i)
	}
	if v <= 0 {
		return def
	}
	return min(v, maximum)
}
func nativeString(args map[string]any, key string) (string, error) {
	v, _ := args[key].(string)
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("缺少参数 %s", key)
	}
	return v, nil
}
func (n *NativeBackend) safeText(text string) string {
	text = scrubProviderText(text)
	if key := n.key(); key != "" {
		text = strings.ReplaceAll(text, key, "[REDACTED]")
	}
	return text
}

func (n *NativeBackend) request(ctx context.Context, spaceKey, method, endpoint string, query url.Values, payload any) (any, error) {
	var reader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	target := strings.TrimRight(n.cfg.Endpoint, "/") + endpoint
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	key := n.key()
	switch n.cfg.Type {
	case "honcho":
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		} else if !n.cfg.AllowPrivate {
			return nil, errors.New("honcho cloud 需要 APIKey/APIKeyEnv")
		}
	case "mem0":
		if n.option("mode", "platform") == "platform" {
			if key == "" {
				return nil, errors.New("Mem0 Platform 需要 APIKey/APIKeyEnv")
			}
			req.Header.Set("Authorization", "Token "+key)
		} else if key != "" {
			req.Header.Set("X-API-Key", key)
		}
	case "openviking":
		if key != "" {
			req.Header.Set("X-API-Key", key)
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("X-OpenViking-Account", n.option("account", "default"))
		req.Header.Set("X-OpenViking-User", n.option("user", "default"))
		req.Header.Set("X-OpenViking-Actor-Peer", "saber-"+spaceKey)
		req.Header.Set("User-Agent", "saber-openviking-memory/1")
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, errors.New("memory provider 响应超过8MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text := scrubProviderText(string(data))
		if key != "" {
			text = strings.ReplaceAll(text, key, "[REDACTED]")
		}
		return nil, fmt.Errorf("%s HTTP %d: %s", n.cfg.Type, resp.StatusCode, truncateUTF8Bytes(text, 2048))
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{"status": "completed"}, nil
	}
	var value any
	if err = json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("%s 返回无效JSON: %w", n.cfg.Type, err)
	}
	if object, ok := value.(map[string]any); ok {
		if object["status"] == "error" || object["success"] == false {
			data, _ := json.Marshal(object)
			return nil, errors.New(n.safeText(string(data)))
		}
	}
	return value, nil
}

func (n *NativeBackend) once(ctx context.Context, scope, session, operation string, payload map[string]any, call func() (any, error)) (any, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	key := session + ":" + operation + ":" + hex.EncodeToString(sum[:])
	if operation == "sync_turn" {
		if id := task.ID(ctx); id > 0 {
			key = session + ":turn:" + strconv.FormatInt(id, 10)
		}
	}
	res, err := n.owner.store.db.ExecContext(ctx, `INSERT OR IGNORE INTO memory_native_operations(provider,scope_key,event_key,status,created_at) VALUES(?,?,?,'dispatched',?)`, n.id, scope, key, nowMillis())
	if err != nil {
		return nil, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		var status string
		var stored []byte
		err = n.owner.store.db.QueryRowContext(ctx, `SELECT status,result FROM memory_native_operations WHERE provider=? AND scope_key=? AND event_key=?`, n.id, scope, key).Scan(&status, &stored)
		if err != nil {
			return nil, err
		}
		if status != "completed" {
			return nil, errors.New("先前memory副作用已派发但未确认；拒绝自动重放，请检查provider状态后显式恢复")
		}
		var result any
		if err = json.Unmarshal(stored, &result); err != nil {
			return nil, err
		}
		return result, nil
	}
	value, callErr := call()
	status := "completed"
	if callErr != nil {
		status = "uncertain"
	}
	result, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		return nil, errors.Join(callErr, marshalErr)
	}
	_, saveErr := n.owner.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE memory_native_operations SET status=?,result=? WHERE provider=? AND scope_key=? AND event_key=?`, status, result, n.id, scope, key)
	return value, errors.Join(callErr, saveErr)
}

func nativeResults(value any) []map[string]any {
	var raw []any
	switch v := value.(type) {
	case []any:
		raw = v
	case map[string]any:
		for _, key := range []string{"results", "items", "messages", "conclusions"} {
			if list, ok := v[key].([]any); ok {
				raw = list
				break
			}
		}
	}
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if item, ok := v.(map[string]any); ok {
			out = append(out, item)
		}
	}
	return out
}
func nativeResult(value any) any {
	if object, ok := value.(map[string]any); ok {
		if result, ok := object["result"]; ok {
			return result
		}
	}
	return value
}
func nativeMessages(payload map[string]any, full bool) []map[string]any {
	raw, _ := payload["messages"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		m, ok := value.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		text, _ := m["content"].(string)
		if role == "system" || text == "" {
			continue
		}
		if role != "user" && role != "assistant" {
			role = "assistant"
			text = "[工具输出资料]\n" + text
		}
		out = append(out, map[string]any{"role": role, "content": scrubProviderText(text)})
	}
	if full {
		return out
	}
	start := 0
	for i, m := range out {
		if m["role"] == "user" {
			start = i
		}
	}
	if len(out) > 0 {
		out = out[start:]
	}
	if text, _ := payload["assistant_content"].(string); text != "" && (len(out) == 0 || out[len(out)-1]["content"] != text) {
		out = append(out, map[string]any{"role": "assistant", "content": scrubProviderText(text)})
	}
	return out
}

func (n *NativeBackend) saveLinks(ctx context.Context, scope string, id int64, links []string) error {
	data, err := json.Marshal(links)
	if err != nil {
		return err
	}
	_, err = n.owner.store.db.ExecContext(ctx, `INSERT INTO memory_native_links(provider,scope_key,entry_id,ids) VALUES(?,?,?,?) ON CONFLICT(provider,scope_key,entry_id) DO UPDATE SET ids=excluded.ids`, n.id, scope, id, data)
	return err
}
func (n *NativeBackend) links(ctx context.Context, scope string, id int64) ([]string, error) {
	var data []byte
	err := n.owner.store.db.QueryRowContext(ctx, `SELECT ids FROM memory_native_links WHERE provider=? AND scope_key=? AND entry_id=?`, n.id, scope, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("外部条目缺少权威来源映射，拒绝猜测相似记录进行更新或删除")
	}
	if err != nil {
		return nil, err
	}
	var links []string
	err = json.Unmarshal(data, &links)
	return links, err
}

func memoryNativeTool(name, description string, properties map[string]any, required ...string) openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: name, Description: description, Parameters: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}}
}
func nativeTextField() map[string]any { return map[string]any{"type": "string"} }
func (n *NativeBackend) tools() []openai.Tool {
	switch n.cfg.Type {
	case "honcho":
		return honchoNativeTools()
	case "mem0":
		return mem0NativeTools()
	case "openviking":
		return vikingNativeTools()
	}
	return nil
}
