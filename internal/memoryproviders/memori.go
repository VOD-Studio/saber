package memoryproviders

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

// 协议来源：MemoriLabs/Memori 的 memori/agent.py、_network.py 及 integrations/hermes。
// hermes-memori 是安装器，不是请求代理；这里直接实现其公开云传输，不启动 Hermes/Python。
type memoriBackend struct {
	nativeLocalState
	collector      config.MemoryBackendConfig
	integrationKey string
}

type nativeLocalState struct {
	cfg    config.MemoryBackendConfig
	host   toolkit.Host
	mu     sync.Mutex
	closed bool
	life   context.Context
	cancel context.CancelFunc
}

func newNativeLocalState(cfg config.MemoryBackendConfig, host toolkit.Host) nativeLocalState {
	life, cancel := context.WithCancel(context.Background())
	return nativeLocalState{cfg: cfg, host: host, life: life, cancel: cancel}
}

func (s *nativeLocalState) begin(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if _, err := ScopedIdentity(ctx); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, nil, errors.New("memory backend is closed")
	}
	seconds := s.cfg.TimeoutSeconds
	if seconds == 0 {
		seconds = 30
	}
	call, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	stop := context.AfterFunc(s.life, cancel)
	return call, func() { stop(); cancel() }, nil
}

func (s *nativeLocalState) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	return nil
}

func nativeLocalPayload(args map[string]any) map[string]any {
	if payload, ok := args["payload"].(map[string]any); ok {
		return payload
	}
	return args
}

func nativeLocalString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func nativeLocalInt(args map[string]any, key string, fallback, max int) (int, error) {
	value, ok := args[key]
	if !ok {
		return fallback, nil
	}
	var n int
	switch v := value.(type) {
	case int:
		n = v
	case int64:
		n = int(v)
	case float64:
		n = int(v)
		if float64(n) != v {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		n = int(i)
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if n < 1 || n > max {
		return 0, fmt.Errorf("%s must be 1..%d", key, max)
	}
	return n, nil
}

func nativeLocalSession(ctx context.Context) (string, error) {
	scope, err := ScopedIdentity(ctx)
	if err != nil {
		return "", err
	}
	identity, _ := chat.IdentityFromContext(ctx)
	data, err := json.Marshal([]string{scope, string(identity.Session.Key())})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "saber-" + hex.EncodeToString(sum[:]), nil
}

func nativeLocalTool(name, description string, properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "function", "function": map[string]any{
		"name": name, "description": description,
		"parameters": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false},
	}}
}

func nativeLocalField(kind, description string) map[string]any {
	return map[string]any{"type": kind, "description": description}
}

func nativeLocalToolCall(operation string, payload map[string]any) (string, map[string]any, error) {
	if operation != "tool" {
		return operation, payload, nil
	}
	name := nativeLocalString(payload, "tool_name")
	arguments, ok := payload["arguments"].(map[string]any)
	if name == "" || !ok {
		return "", nil, errors.New("memory tool requires tool_name and arguments")
	}
	for _, key := range []string{"identity", "scope", "user_id", "entity_id", "entityId", "project_id", "projectId", "session_id", "sessionId", "process_id", "bank_id", "containerTag", "endpoint", "api_key"} {
		if _, exists := arguments[key]; exists {
			return "", nil, fmt.Errorf("%s cannot replace trusted memory scope", key)
		}
	}
	return name, arguments, nil
}

func nativeLocalCredential(cfg config.MemoryBackendConfig) (string, error) {
	if cfg.APIKeyEnv != "" {
		key := os.Getenv(cfg.APIKeyEnv)
		if key == "" {
			return "", fmt.Errorf("memory credential environment %s is not set", cfg.APIKeyEnv)
		}
		return key, nil
	}
	if cfg.APIKey == "" {
		return "", errors.New("memory API key is required")
	}
	return cfg.APIKey, nil
}

// NewMemori 直接连接官方 agent HTTP 协议；实体、项目与会话均来自可信上下文。
func NewMemori(cfg config.MemoryBackendConfig, host toolkit.Host) (Backend, error) {
	cfg.Type = "memori"
	custom := cfg.Endpoint != ""
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.memorilabs.ai"
	}
	if cfg.APIKey == "" && cfg.APIKeyEnv == "" {
		cfg.APIKeyEnv = "MEMORI_API_KEY"
	}
	collector := cfg
	if !custom {
		collector.Endpoint = "https://collector.memorilabs.ai"
	}
	if endpoint := nativeLocalString(cfg.Options, "collector_endpoint"); endpoint != "" {
		collector.Endpoint = endpoint
	}
	if err := (config.MemoryBackendsConfig{Backends: map[string]config.MemoryBackendConfig{"memori": cfg, "collector": collector}}).Validate(); err != nil {
		return nil, err
	}
	// 这是上游 SDK 的公开客户端标识，不是用户的 Bearer 凭据。
	integrationKey := "96a7ea3e-11c2-428c-b9ae-5a168363dc80"
	if custom {
		integrationKey = "c18b1022-7fe2-42af-ab01-b1f9139184f0"
	}
	if value := nativeLocalString(cfg.Options, "integration_key"); value != "" {
		integrationKey = value
	}
	return &memoriBackend{nativeLocalState: newNativeLocalState(cfg, host), collector: collector, integrationKey: integrationKey}, nil
}

func (b *memoriBackend) request(ctx context.Context, cfg config.MemoryBackendConfig, method, path string, body any) (any, error) {
	if _, err := nativeLocalCredential(cfg); err != nil {
		return nil, err
	}
	raw, err := HTTPJSON(ctx, cfg, method, path, body, map[string]string{"X-Memori-API-Key": b.integrationKey})
	if err != nil {
		return nil, err
	}
	var result any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (b *memoriBackend) Call(ctx context.Context, operation string, args map[string]any) (any, error) {
	ctx, done, err := b.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	scope, _ := ScopedIdentity(ctx)
	session, err := nativeLocalSession(ctx)
	if err != nil {
		return nil, err
	}
	payload := nativeLocalPayload(args)
	operation, payload, err = nativeLocalToolCall(operation, payload)
	if err != nil {
		return nil, err
	}
	switch operation {
	case "tools":
		return memoriTools(), nil
	case "initialize", "memori_quota":
		return b.request(ctx, b.cfg, "GET", "/v1/sdk/quota", nil)
	case "context", "prefetch", "turn_start", "memori_recall", "memori_recall_summary", "memori_compaction":
		path := "/v1/agent/recall"
		query := url.Values{"entity_id": {scope}, "project_id": {scope}}
		if operation == "memori_recall_summary" || (operation == "context" && nativeLocalString(payload, "query") == "") {
			path = "/v1/agent/recall/summary"
		}
		if operation == "memori_compaction" {
			path = "/v1/agent/compaction"
			query.Set("session_id", session)
		}
		text := nativeLocalString(payload, "query")
		if text == "" {
			text = nativeLocalString(payload, "message")
		}
		if text != "" && path == "/v1/agent/recall" {
			query.Set("query", text)
		}
		for _, key := range []string{"date_start", "date_end"} {
			value := nativeLocalString(payload, key)
			if value == "" {
				if key == "date_start" {
					value = nativeLocalString(payload, "dateStart")
				} else {
					value = nativeLocalString(payload, "dateEnd")
				}
			}
			if value != "" {
				if _, err := time.Parse("2006-01-02", value); err != nil {
					return nil, fmt.Errorf("%s must be YYYY-MM-DD", key)
				}
				query.Set(key, value)
			}
		}
		source, signal := nativeLocalString(payload, "source"), nativeLocalString(payload, "signal")
		if source != "" || signal != "" {
			pairs := map[string]string{"constraint": "discovery", "decision": "commit", "fact": "verification", "execution": "failure", "instruction": "discovery", "insight": "inference", "status": "update", "strategy": "pattern", "task": "result"}
			if pairs[source] != signal || signal == "" {
				return nil, errors.New("invalid Memori source/signal classification pair")
			}
			query.Set("source", source)
			query.Set("signal", signal)
		}
		if _, ok := payload["num_messages"]; ok {
			n, err := nativeLocalInt(payload, "num_messages", 20, 1000)
			if err != nil {
				return nil, err
			}
			query.Set("num_messages", fmt.Sprint(n))
		}
		return b.request(ctx, b.cfg, "GET", path+"?"+query.Encode(), nil)
	case "sync_turn":
		user, assistant, messages, err := nativeLocalTurn(payload)
		if err != nil {
			return nil, err
		}
		trace := memoriTrace(messages)
		attribution := map[string]any{"entity": map[string]any{"id": scope}, "process": map[string]any{"id": "saber"}}
		turnMessages := []any{map[string]any{"role": "user", "content": user, "type": "text", "trace": nil}, map[string]any{"role": "assistant", "content": assistant, "type": "text", "trace": trace}}
		turn := map[string]any{"attribution": attribution, "messages": turnMessages, "project": map[string]any{"id": scope}, "session": map[string]any{"id": session}}
		result, err := b.request(ctx, b.cfg, "POST", "/v1/agent/conversation/turn", turn)
		if err != nil {
			return nil, err
		}
		augmentation := map[string]any{"attribution": attribution, "conversation": map[string]any{"messages": turnMessages}, "project": map[string]any{"id": scope}, "session": map[string]any{"id": session, "summary": nil}, "trace": trace,
			"meta": map[string]any{"sdk": map[string]any{"lang": "go", "version": "saber-native"}, "framework": map[string]any{"provider": nil}, "llm": map[string]any{"model": map[string]any{"provider": nil, "sdk": map[string]any{"version": nil}, "version": nil}}, "platform": map[string]any{"provider": "saber"}, "storage": map[string]any{"cockroachdb": false, "dialect": nil}}}
		_, err = b.request(ctx, b.collector, "POST", "/v1/agent/augmentation", augmentation)
		if err != nil {
			return map[string]any{"captured": true, "result": result, "augmentation_failed": true}, fmt.Errorf("memori turn was captured; augmentation failed (do not replay): %w", err)
		}
		return map[string]any{"captured": true, "result": result}, nil
	case "memori_signup":
		email := nativeLocalString(payload, "email")
		parsed, err := mail.ParseAddress(email)
		if err != nil || parsed.Address != email {
			return nil, errors.New("signup requires a valid email address")
		}
		return b.request(ctx, b.cfg, "POST", "/v1/sdk/account", map[string]any{"email": email})
	case "memori_feedback":
		content := nativeLocalString(payload, "content")
		if content == "" || len(content) > 10000 {
			return nil, errors.New("feedback content must be 1..10000 bytes")
		}
		return b.request(ctx, b.cfg, "POST", "/v1/agent/feedback", map[string]any{"content": content})
	case "session_end", "session_switch":
		// 已完成轮次同步提交，没有背景写入需要重放；读取真实当前会话的压缩摘要。
		return b.request(ctx, b.cfg, "GET", "/v1/agent/compaction?"+url.Values{"project_id": {scope}, "session_id": {session}}.Encode(), nil)
	case "shutdown":
		return nil, b.Close()
	default:
		return nil, fmt.Errorf("memori does not support operation %q", operation)
	}
}

func nativeLocalTurn(payload map[string]any) (string, string, []map[string]any, error) {
	if status := nativeLocalString(payload, "status"); status != "" && status != "completed" {
		return "", "", nil, errors.New("memory captures only completed turns")
	}
	data, err := json.Marshal(payload["messages"])
	if err != nil {
		return "", "", nil, err
	}
	var messages []map[string]any
	if payload["messages"] != nil {
		if err := json.Unmarshal(data, &messages); err != nil {
			return "", "", nil, errors.New("messages must be a conversation array")
		}
	}
	user := nativeLocalString(payload, "user_content")
	assistant := nativeLocalString(payload, "assistant_content")
	start := -1
	for i, message := range messages {
		if nativeLocalString(message, "role") == "user" {
			start = i
		}
	}
	if start >= 0 {
		messages = messages[start:]
		if user == "" {
			user = nativeLocalString(messages[0], "content")
		}
		if assistant == "" {
			for i := len(messages) - 1; i >= 0; i-- {
				if nativeLocalString(messages[i], "role") == "assistant" {
					assistant = nativeLocalString(messages[i], "content")
					if assistant != "" {
						break
					}
				}
			}
		}
	}
	if user == "" || assistant == "" {
		return "", "", nil, errors.New("memory sync requires a completed user/assistant turn")
	}
	if len(user)+len(assistant)+len(data) > 2*1024*1024 {
		return "", "", nil, errors.New("memory turn exceeds 2 MiB")
	}
	return user, assistant, messages, nil
}

func memoriTrace(messages []map[string]any) any {
	tools := make([]map[string]any, 0)
	byID := make(map[string]int)
	for _, message := range messages {
		calls, _ := message["tool_calls"].([]any)
		for _, raw := range calls {
			call, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			function, ok := call["function"].(map[string]any)
			if !ok {
				continue
			}
			id := nativeLocalString(call, "id")
			arguments := function["arguments"]
			if text, ok := arguments.(string); ok {
				var parsed any
				if json.Unmarshal([]byte(text), &parsed) == nil {
					arguments = parsed
				}
			}
			byID[id] = len(tools)
			tools = append(tools, map[string]any{"name": function["name"], "args": arguments, "result": nil})
		}
		if nativeLocalString(message, "role") == "tool" {
			if index, ok := byID[nativeLocalString(message, "tool_call_id")]; ok {
				tools[index]["result"] = message["content"]
			}
		}
	}
	if len(tools) == 0 {
		return nil
	}
	return map[string]any{"tools": tools}
}

func memoriTools() []map[string]any {
	filters := map[string]any{"query": nativeLocalField("string", "Relevant facts, decisions or execution outcomes"), "date_start": nativeLocalField("string", "UTC date YYYY-MM-DD"), "date_end": nativeLocalField("string", "UTC date YYYY-MM-DD"), "source": nativeLocalField("string", "Classification source; requires matching signal"), "signal": nativeLocalField("string", "Classification signal; requires matching source")}
	return []map[string]any{
		nativeLocalTool("memori_recall", "Recall structured memory within the current trusted scope", filters),
		nativeLocalTool("memori_recall_summary", "Read current scoped project summaries", map[string]any{"date_start": filters["date_start"], "date_end": filters["date_end"]}),
		nativeLocalTool("memori_compaction", "Read the current session continuation brief", map[string]any{"num_messages": nativeLocalField("integer", "Maximum recent messages, 1..1000")}),
		nativeLocalTool("memori_quota", "Read actual Memori account quota", map[string]any{}),
		nativeLocalTool("memori_signup", "Request signup email only when explicitly requested", map[string]any{"email": nativeLocalField("string", "Email provided by the user")}, "email"),
		nativeLocalTool("memori_feedback", "Send integration quality feedback", map[string]any{"content": nativeLocalField("string", "Feedback supplied by the user")}, "content"),
	}
}
