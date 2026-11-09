package memoryproviders

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/task"
	"rua.plus/saber/internal/toolkit"
)

// 协议来源：https://hindsight.vectorize.io/openapi.json (1.2.0)，以及
// https://github.com/vectorize-io/hindsight/tree/main/hindsight-integrations/hermes。
// retain 使用同步完成语义；不将 async 队列受理误报为已经存储或可检索。
type hindsightBackend struct {
	cfg           config.MemoryBackendConfig
	life          nativeRecallLifetime
	budget        string
	method        string
	maxTokens     int
	recallTypes   []string
	recallTags    []string
	reflectTags   []string
	retainTags    []string
	recallMatch   string
	reflectMatch  string
	retainContext string
	autoRecall    bool
	autoCapture   bool
}

// NewHindsight 创建直连 Hindsight Cloud 或管理员运行的自托管 API 的原生适配器。
func NewHindsight(cfg config.MemoryBackendConfig, _ toolkit.Host) (Backend, error) {
	cfg.Type = "hindsight"
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.hindsight.vectorize.io"
	}
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = 120
	}
	var err error
	cfg, err = nativeRecallConfig(cfg, "HINDSIGHT_API_KEY", strings.HasPrefix(cfg.Endpoint, "https://api.hindsight.vectorize.io"))
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{"recall_budget": true, "recall_prefetch_method": true, "recall_max_tokens": true, "recall_types": true, "recall_tags": true, "reflect_tags": true, "retain_tags": true, "recall_tags_match": true, "reflect_tags_match": true, "retain_context": true, "auto_recall": true, "auto_retain": true}
	if err = nativeRecallOptionKeys(cfg.Options, allowed); err != nil {
		return nil, err
	}
	b := &hindsightBackend{cfg: cfg, budget: "mid", method: "recall", maxTokens: 4096, recallTypes: []string{"observation"}, recallMatch: "any", reflectMatch: "any", retainContext: "conversation between Saber and the User", autoRecall: true, autoCapture: true}
	if b.budget, err = nativeRecallEnum(cfg.Options, "recall_budget", "mid", "low", "mid", "high"); err != nil {
		return nil, err
	}
	if b.method, err = nativeRecallEnum(cfg.Options, "recall_prefetch_method", "recall", "recall", "reflect"); err != nil {
		return nil, err
	}
	if b.maxTokens, err = nativeRecallInt(cfg.Options, "recall_max_tokens", 4096, 1, 32768); err != nil {
		return nil, err
	}
	if b.recallMatch, err = nativeRecallEnum(cfg.Options, "recall_tags_match", "any", "any", "all", "any_strict", "all_strict", "exact"); err != nil {
		return nil, err
	}
	if b.reflectMatch, err = nativeRecallEnum(cfg.Options, "reflect_tags_match", "any", "any", "all", "any_strict", "all_strict", "exact"); err != nil {
		return nil, err
	}
	for key, dest := range map[string]*[]string{"recall_types": &b.recallTypes, "recall_tags": &b.recallTags, "reflect_tags": &b.reflectTags, "retain_tags": &b.retainTags} {
		if _, ok := cfg.Options[key]; ok {
			if *dest, err = nativeRecallStrings(cfg.Options[key]); err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	for _, kind := range b.recallTypes {
		if kind != "world" && kind != "experience" && kind != "observation" {
			return nil, errors.New("recall_types must contain world, experience or observation")
		}
	}
	if len(b.recallTypes) == 0 {
		return nil, errors.New("recall_types must not be empty")
	}
	if value, ok := cfg.Options["retain_context"]; ok {
		var valid bool
		b.retainContext, valid = value.(string)
		if !valid || len(b.retainContext) > 4096 {
			return nil, errors.New("retain_context must be a string of at most 4096 bytes")
		}
	}
	if b.autoRecall, err = nativeRecallBool(cfg.Options, "auto_recall", true); err != nil {
		return nil, err
	}
	if b.autoCapture, err = nativeRecallBool(cfg.Options, "auto_retain", true); err != nil {
		return nil, err
	}
	b.life.init()
	return b, nil
}

func (b *hindsightBackend) Call(ctx context.Context, operation string, args map[string]any) (any, error) {
	ctx, done, err := b.life.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	scope, session, err := nativeRecallIdentity(ctx)
	if err != nil {
		return nil, err
	}
	payload := nativeRecallPayload(args)
	switch operation {
	case "initialize":
		result, err := b.request(ctx, scope, http.MethodGet, "/config", nil)
		if nativeRecallStatus(err, http.StatusNotFound) {
			result, err = b.request(ctx, scope, http.MethodPut, "", map[string]any{})
		}
		if err != nil {
			return nil, err
		}
		if nativeRecallText(result, "bank_id") != scope {
			return nil, errors.New("hindsight initialization returned a different bank")
		}
		return result, nil
	case "context", "prefetch":
		if !b.autoRecall {
			return map[string]any{"context": "", "disabled": true}, nil
		}
		query, err := nativeRecallRequired(payload, "query")
		if err != nil {
			return nil, err
		}
		result, err := b.recall(ctx, scope, query, b.method)
		if err != nil {
			return nil, err
		}
		return map[string]any{"context": nativeRecallBound(b.contextText(result, b.method), 32768)}, nil
	case "turn_start":
		state, err := b.life.session(session)
		if err != nil {
			return nil, err
		}
		state.mu.Lock()
		state.turnCount++
		count := state.turnCount
		state.mu.Unlock()
		return map[string]any{"turn": count}, nil
	case "sync_turn":
		if !b.autoCapture {
			return map[string]any{"captured": false, "disabled": true}, nil
		}
		return b.capture(ctx, scope, session, payload)
	case "session_end", "session_switch", "shutdown":
		return b.flush(ctx, scope, session, payload, operation)
	case "pre_compress":
		return b.checkpoint(ctx, scope, session, payload)
	case "memory_write":
		action := nativeRecallText(payload, "action")
		if action != "add" && action != "replace" {
			return nil, fmt.Errorf("hindsight memory_write does not support action %q", action)
		}
		content, err := nativeRecallRequired(payload, "content")
		if err != nil {
			return nil, err
		}
		doc := nativeRecallDocumentID("entry", scope, strconv.Itoa(nativeRecallOptionalInt(payload, "entry_id")), content)
		return b.retain(ctx, scope, doc, content, nil)
	case "tools":
		return hindsightTools(), nil
	case "tool":
		return b.tool(ctx, scope, payload)
	default:
		return nil, fmt.Errorf("hindsight does not support operation %q", operation)
	}
}

func (b *hindsightBackend) Close() error { return b.life.close() }

func (b *hindsightBackend) request(ctx context.Context, scope, method, suffix string, body any) (map[string]any, error) {
	data, err := HTTPJSON(ctx, b.cfg, method, nativeRecallPath(b.cfg, "v1/default/banks/"+url.PathEscape(scope)+suffix), body, map[string]string{"User-Agent": "saber-memory-hindsight"})
	if err != nil {
		return nil, fmt.Errorf("hindsight %s: %w", method, err)
	}
	return nativeRecallObject(data)
}

func (b *hindsightBackend) recall(ctx context.Context, scope, query, method string) (map[string]any, error) {
	body := map[string]any{"query": query, "budget": b.budget, "max_tokens": b.maxTokens}
	suffix := "/memories/recall"
	if method == "reflect" {
		suffix = "/reflect"
		if len(b.reflectTags) > 0 || b.reflectMatch == "exact" {
			body["tags"] = b.reflectTags
			body["tags_match"] = b.reflectMatch
		}
	} else {
		body["types"] = b.recallTypes
		if len(b.recallTags) > 0 || b.recallMatch == "exact" {
			body["tags"] = b.recallTags
			body["tags_match"] = b.recallMatch
		}
	}
	result, err := b.request(ctx, scope, http.MethodPost, suffix, body)
	if err != nil {
		return nil, err
	}
	if method == "reflect" {
		if _, ok := result["text"].(string); !ok {
			return nil, errors.New("hindsight reflect response has no text")
		}
	} else {
		if _, ok := result["results"].([]any); !ok {
			return nil, errors.New("hindsight recall response has no results")
		}
	}
	return result, nil
}

func (b *hindsightBackend) contextText(result map[string]any, method string) string {
	if method == "reflect" {
		return nativeRecallText(result, "text")
	}
	var lines []string
	items, _ := result["results"].([]any)
	for _, item := range items {
		if value, ok := item.(map[string]any); ok {
			if text := nativeRecallText(value, "text"); text != "" {
				lines = append(lines, "- "+text)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func (b *hindsightBackend) retain(ctx context.Context, scope, doc, content string, extra map[string]any) (map[string]any, error) {
	if err := nativeRecallContent(content); err != nil {
		return nil, err
	}
	metadata := nativeRecallMetadata(ctx, scope, doc)
	item := map[string]any{"content": content, "document_id": doc, "context": b.retainContext, "metadata": metadata}
	if len(b.retainTags) > 0 {
		item["tags"] = b.retainTags
	}
	for key, value := range extra {
		item[key] = value
	}
	result, err := b.request(ctx, scope, http.MethodPost, "/memories", map[string]any{"items": []any{item}, "async": false})
	if err != nil {
		return nil, err
	}
	success, _ := result["success"].(bool)
	async, _ := result["async"].(bool)
	if !success || async || nativeRecallText(result, "bank_id") != scope || nativeRecallOptionalInt(result, "items_count") != 1 {
		return nil, errors.New("hindsight retain did not confirm synchronous storage in the requested bank")
	}
	result["document_id"] = doc
	return result, nil
}

func (b *hindsightBackend) capture(ctx context.Context, scope, session string, payload map[string]any) (any, error) {
	state, err := b.life.session(session)
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	turns, compact, err := nativeRecallTurns(payload, true)
	if err != nil {
		return nil, err
	}
	if len(turns) == 0 {
		return nil, errors.New("hindsight sync_turn has no completed user-assistant turns")
	}
	_, full := payload["messages"]
	deltaKey := ""
	if !full {
		deltaKey = nativeRecallHash(fmt.Sprint(task.ID(ctx)) + "\x00" + nativeRecallRender(turns))
		if state.lastDelta == deltaKey {
			return map[string]any{"captured": false, "unchanged": true, "document_id": state.document}, nil
		}
	}
	if !full {
		turns = append(append([]nativeRecallTurn(nil), state.turns...), turns...)
	} else if compact || state.compacted {
		turns = nativeRecallJoin(state.turns, turns)
	}
	content := nativeRecallRender(turns)
	if err := nativeRecallContent(content); err != nil {
		return nil, err
	}
	doc := nativeRecallDocumentID("conversation", scope, session)
	if !state.loaded && (!full || compact || state.compacted) {
		stored, err := b.request(ctx, scope, http.MethodGet, "/documents/"+url.PathEscape(doc), nil)
		if err != nil && !nativeRecallStatus(err, 404) {
			return nil, err
		}
		if err == nil {
			original, ok := stored["original_text"].(string)
			if !ok {
				return nil, errors.New("hindsight cannot preserve compacted history: document text storage is unavailable")
			}
			turns = nativeRecallJoin(nativeRecallParse(original), turns)
			content = nativeRecallRender(turns)
		}
	}
	digest := nativeRecallHash(content)
	if state.digest == digest {
		return map[string]any{"captured": false, "unchanged": true, "document_id": doc}, nil
	}
	result, err := b.retain(ctx, scope, doc, content, nil)
	if err != nil {
		return nil, err
	}
	state.turns = turns
	state.digest = digest
	state.loaded = true
	state.compacted = false
	state.document = doc
	state.lastDelta = deltaKey
	result["captured"] = true
	return result, nil
}

func (b *hindsightBackend) flush(ctx context.Context, scope, session string, payload map[string]any, operation string) (any, error) {
	if _, ok := payload["messages"]; ok && b.autoCapture {
		return b.capture(ctx, scope, session, payload)
	}
	state, err := b.life.session(session)
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	result := map[string]any{"pending_client_writes": false, "document_id": state.document}
	if operation == "session_end" || operation == "session_switch" || operation == "shutdown" {
		b.life.release(session, state)
	}
	return result, nil
}

func (b *hindsightBackend) checkpoint(ctx context.Context, scope, session string, payload map[string]any) (any, error) {
	messages, ok := payload["messages"]
	if !ok {
		return nil, errors.New("hindsight checkpoint requires messages")
	}
	turns, compact, err := nativeRecallTurns(payload, true)
	if err != nil {
		return nil, err
	}
	state, err := b.life.session(session)
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	data, err := json.Marshal(messages)
	if err != nil {
		return nil, err
	}
	content := string(data)
	doc := nativeRecallDocumentID("checkpoint", scope, session, content)
	if _, err = b.retain(ctx, scope, doc, content, nil); err != nil {
		return nil, err
	}
	stored, err := b.request(ctx, scope, http.MethodGet, "/documents/"+url.PathEscape(doc), nil)
	if err != nil {
		return nil, err
	}
	if nativeRecallText(stored, "original_text") != content {
		return nil, errors.New("hindsight checkpoint original text is not durably readable; enable document text storage")
	}
	if compact {
		state.turns = nativeRecallJoin(state.turns, turns)
	} else if len(turns) > 0 {
		state.turns = turns
	}
	state.compacted = true
	return map[string]any{"checkpointed": true, "checkpoint_id": doc}, nil
}

func (b *hindsightBackend) tool(ctx context.Context, scope string, payload map[string]any) (any, error) {
	name := nativeRecallText(payload, "tool_name")
	args, _ := payload["arguments"].(map[string]any)
	if err := nativeRecallToolScope(args); err != nil {
		return nil, err
	}
	switch name {
	case "hindsight_recall", "hindsight_reflect":
		query, err := nativeRecallRequired(args, "query")
		if err != nil {
			return nil, err
		}
		method := "recall"
		if name == "hindsight_reflect" {
			method = "reflect"
		}
		return b.recall(ctx, scope, query, method)
	case "hindsight_retain":
		content, err := nativeRecallRequired(args, "content")
		if err != nil {
			return nil, err
		}
		extra := map[string]any{}
		if value, ok := args["context"]; ok {
			if _, valid := value.(string); !valid {
				return nil, errors.New("context must be a string")
			}
			extra["context"] = value
		}
		if value, ok := args["occurred_at"]; ok {
			if _, valid := value.(string); !valid {
				return nil, errors.New("occurred_at must be an ISO timestamp string")
			}
			extra["timestamp"] = value
		}
		if value, ok := args["tags"]; ok {
			tags, err := nativeRecallStrings(value)
			if err != nil {
				return nil, err
			}
			extra["tags"] = append(append([]string(nil), b.retainTags...), tags...)
		}
		return b.retain(ctx, scope, nativeRecallDocumentID("explicit", scope, content), content, extra)
	default:
		return nil, fmt.Errorf("unknown hindsight tool %q", name)
	}
}

func hindsightTools() []any {
	return []any{
		nativeRecallTool("hindsight_retain", "保存长期记忆；同步确认 Hindsight 已完成提取。", map[string]any{"content": map[string]any{"type": "string"}, "context": map[string]any{"type": "string"}, "tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "occurred_at": map[string]any{"type": "string"}}, "content"),
		nativeRecallTool("hindsight_recall", "在当前可信作用域检索 Hindsight 事实。", map[string]any{"query": map[string]any{"type": "string"}}, "query"),
		nativeRecallTool("hindsight_reflect", "在当前可信作用域通过 Hindsight 综合长期记忆。", map[string]any{"query": map[string]any{"type": "string"}}, "query"),
	}
}

// 以下辅助只封装两种 recall 后端的本地状态及已授权传输，不假定统一厂商协议。
type nativeRecallLifetime struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	wg       sync.WaitGroup
	sessions map[string]*nativeRecallSession
}

type nativeRecallSession struct {
	mu        sync.Mutex
	turns     []nativeRecallTurn
	digest    string
	document  string
	turnCount int
	loaded    bool
	compacted bool
	lastDelta string
}

type nativeRecallTurn struct{ User, Assistant string }

func (l *nativeRecallLifetime) init() {
	l.ctx, l.cancel = context.WithCancel(context.Background())
	l.sessions = make(map[string]*nativeRecallSession)
}
func (l *nativeRecallLifetime) begin(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, nil, errors.New("native memory backend is closed")
	}
	l.wg.Add(1)
	life := l.ctx
	l.mu.Unlock()
	call, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(life, cancel)
	return call, func() { stop(); cancel(); l.wg.Done() }, nil
}
func (l *nativeRecallLifetime) close() error {
	l.mu.Lock()
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	l.wg.Wait()
	return nil
}
func (l *nativeRecallLifetime) session(key string) (*nativeRecallSession, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if state := l.sessions[key]; state != nil {
		return state, nil
	}
	if len(l.sessions) >= 128 {
		return nil, errors.New("native memory has 128 active sessions; end inactive sessions first")
	}
	state := &nativeRecallSession{}
	l.sessions[key] = state
	return state, nil
}
func (l *nativeRecallLifetime) release(key string, state *nativeRecallSession) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sessions[key] == state {
		delete(l.sessions, key)
	}
}

func nativeRecallConfig(cfg config.MemoryBackendConfig, env string, required bool) (config.MemoryBackendConfig, error) {
	if cfg.APIKey == "" && cfg.APIKeyEnv == "" && os.Getenv(env) != "" {
		cfg.APIKeyEnv = env
	}
	if cfg.APIKeyEnv != "" {
		cfg.APIKey = os.Getenv(cfg.APIKeyEnv)
		if cfg.APIKey == "" {
			return cfg, fmt.Errorf("credential environment %s is not set", cfg.APIKeyEnv)
		}
		cfg.APIKeyEnv = ""
	}
	if required && strings.TrimSpace(cfg.APIKey) == "" {
		return cfg, fmt.Errorf("%s or api_key is required", env)
	}
	if err := (config.MemoryBackendsConfig{Backends: map[string]config.MemoryBackendConfig{"adapter": cfg}}).Validate(); err != nil {
		return cfg, err
	}
	data, err := json.Marshal(cfg.Options)
	if err != nil {
		return cfg, errors.New("memory options are not JSON-serializable")
	}
	if err = json.Unmarshal(data, &cfg.Options); err != nil {
		return cfg, err
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/") + "/"
	return cfg, nil
}
func nativeRecallIdentity(ctx context.Context) (string, string, error) {
	scope, err := ScopedIdentity(ctx)
	if err != nil {
		return "", "", err
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return "", "", errors.New("native memory requires trusted session")
	}
	return scope, nativeRecallHash(scope + "\x00" + string(identity.Session.Key())), nil
}
func nativeRecallPayload(args map[string]any) map[string]any {
	if value, ok := args["payload"].(map[string]any); ok {
		return value
	}
	return args
}
func nativeRecallHash(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}
func nativeRecallDocumentID(kind string, parts ...string) string {
	data, _ := json.Marshal(parts)
	return "saber_" + kind + "_" + nativeRecallHash(string(data))
}
func nativeRecallPath(cfg config.MemoryBackendConfig, path string) string {
	base, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return path
	}
	return strings.TrimRight(base.EscapedPath(), "/") + "/" + path
}
func nativeRecallObject(data json.RawMessage) (map[string]any, error) {
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil || result == nil {
		return nil, errors.New("memory API response must be a JSON object")
	}
	return result, nil
}
func nativeRecallText(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}
func nativeRecallRequired(args map[string]any, key string) (string, error) {
	value := nativeRecallText(args, key)
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	if len(value) > 512*1024 {
		return "", fmt.Errorf("%s exceeds 512 KiB", key)
	}
	return value, nil
}
func nativeRecallContent(content string) error {
	if strings.TrimSpace(content) == "" {
		return errors.New("memory content must not be empty")
	}
	if len(content) > 512*1024 {
		return errors.New("memory content exceeds 512 KiB")
	}
	return nil
}
func nativeRecallBound(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}
func nativeRecallOptionalInt(args map[string]any, key string) int {
	switch n := args[key].(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		if n == float64(int(n)) {
			return int(n)
		}
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}
func nativeRecallInt(args map[string]any, key string, def, min, max int) (int, error) {
	value, ok := args[key]
	if !ok {
		return def, nil
	}
	valid := false
	switch n := value.(type) {
	case int:
		valid = true
	case int64:
		valid = n >= int64(min) && n <= int64(max)
	case float64:
		valid = n >= float64(min) && n <= float64(max) && n == float64(int(n))
	case json.Number:
		i, err := n.Int64()
		valid = err == nil && i >= int64(min) && i <= int64(max)
	}
	n := nativeRecallOptionalInt(args, key)
	if !valid || n < min || n > max {
		return 0, fmt.Errorf("%s must be an integer in %d..%d", key, min, max)
	}
	return n, nil
}
func nativeRecallEnum(args map[string]any, key, def string, choices ...string) (string, error) {
	if _, ok := args[key]; !ok {
		return def, nil
	}
	value, ok := args[key].(string)
	if ok {
		for _, choice := range choices {
			if value == choice {
				return value, nil
			}
		}
	}
	return "", fmt.Errorf("unsupported %s", key)
}
func nativeRecallBool(args map[string]any, key string, def bool) (bool, error) {
	value, ok := args[key]
	if !ok {
		return def, nil
	}
	b, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be boolean", key)
	}
	return b, nil
}
func nativeRecallOptionKeys(options map[string]any, allowed map[string]bool) error {
	for key := range options {
		if !allowed[key] {
			return fmt.Errorf("unsupported memory option %q", key)
		}
	}
	return nil
}
func nativeRecallStrings(value any) ([]string, error) {
	var out []string
	switch v := value.(type) {
	case []string:
		out = append(out, v...)
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, errors.New("expected string list")
			}
			out = append(out, s)
		}
	case string:
		if strings.TrimSpace(v) != "" {
			out = strings.Split(v, ",")
		}
	default:
		return nil, errors.New("expected string list")
	}
	if len(out) > 100 {
		return nil, errors.New("string list exceeds 100 items")
	}
	for i, s := range out {
		out[i] = strings.TrimSpace(s)
		if out[i] == "" || len(out[i]) > 256 {
			return nil, errors.New("list items must be nonempty and at most 256 bytes")
		}
	}
	return out, nil
}
func nativeRecallStatus(err error, status int) bool {
	var value interface{ HTTPStatusCode() int }
	return errors.As(err, &value) && value.HTTPStatusCode() == status
}
func nativeRecallMetadata(ctx context.Context, scope, doc string) map[string]string {
	out := map[string]string{"sm_source": "saber", "source": "saber", "scope": scope, "docId": doc}
	if id := task.ID(ctx); id > 0 {
		out["task_id"] = strconv.FormatInt(id, 10)
	}
	return out
}
func nativeRecallToolScope(args map[string]any) error {
	for _, key := range []string{"identity", "scope", "user_id", "user", "peer", "peer_id", "bank_id", "container_tag", "containerTag", "namespace", "session_id", "base_url", "endpoint", "api_key", "token"} {
		if _, ok := args[key]; ok {
			return fmt.Errorf("%s cannot override the trusted memory scope or configuration", key)
		}
	}
	return nil
}
func nativeRecallTool(name, description string, properties map[string]any, required ...string) map[string]any {
	parameters := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		parameters["required"] = required
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": description, "parameters": parameters}}
}

var nativeRecallInjected = regexp.MustCompile(`(?s)<supermemory-(?:context|containers)>.*?</supermemory-(?:context|containers)>\s*`)
var nativeRecallDataURI = regexp.MustCompile(`data:[^;,\s]+;base64,[A-Za-z0-9+/=]+`)
var nativeRecallStoredTurn = regexp.MustCompile(`(?s)\[role: user\]\n(.*?)\n\[user:end\](?:\n\[role: assistant\]\n(.*?)\n\[assistant:end\])?`)

func nativeRecallMessageText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		var lines []string
		for _, part := range v {
			if p, ok := part.(map[string]any); ok {
				kind := nativeRecallText(p, "type")
				if kind == "text" || kind == "input_text" || kind == "output_text" {
					lines = append(lines, nativeRecallText(p, "text"))
				}
			}
		}
		return strings.Join(lines, "\n")
	}
	return ""
}
func nativeRecallTurns(payload map[string]any, completed bool) ([]nativeRecallTurn, bool, error) {
	var turns []nativeRecallTurn
	compact := false
	if value, ok := payload["messages"]; ok {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, false, err
		}
		var messages []map[string]any
		if err = json.Unmarshal(data, &messages); err != nil {
			return nil, false, errors.New("messages must be a conversation array")
		}
		for _, message := range messages {
			role := nativeRecallText(message, "role")
			if role != "user" && role != "assistant" {
				continue
			}
			text := nativeRecallMessageText(message["content"])
			if flag, _ := message["_compressed_summary"].(bool); flag {
				compact = true
				continue
			}
			const summaryEnd = "--- END OF CONTEXT SUMMARY — respond to the message below, not the summary above ---"
			if index := strings.Index(text, summaryEnd); index >= 0 {
				compact = true
				text = text[index+len(summaryEnd):]
			}
			text = strings.TrimSpace(nativeRecallDataURI.ReplaceAllString(nativeRecallInjected.ReplaceAllString(text, ""), "[image]"))
			if text == "" {
				continue
			}
			if role == "user" {
				turns = append(turns, nativeRecallTurn{User: text})
			} else if len(turns) > 0 {
				turns[len(turns)-1].Assistant = text
			}
		}
	} else {
		user := strings.TrimSpace(nativeRecallDataURI.ReplaceAllString(nativeRecallInjected.ReplaceAllString(nativeRecallMessageText(payload["user_content"]), ""), "[image]"))
		assistant := strings.TrimSpace(nativeRecallDataURI.ReplaceAllString(nativeRecallInjected.ReplaceAllString(nativeRecallMessageText(payload["assistant_content"]), ""), "[image]"))
		if user != "" {
			turns = append(turns, nativeRecallTurn{User: user, Assistant: assistant})
		}
	}
	if len(turns) > 0 && turns[len(turns)-1].Assistant == "" {
		turns[len(turns)-1].Assistant = strings.TrimSpace(nativeRecallDataURI.ReplaceAllString(nativeRecallInjected.ReplaceAllString(nativeRecallMessageText(payload["assistant_content"]), ""), "[image]"))
	}
	if completed && len(turns) > 0 && turns[len(turns)-1].Assistant == "" {
		turns = turns[:len(turns)-1]
	}
	return turns, compact, nil
}
func nativeRecallRender(turns []nativeRecallTurn) string {
	var out strings.Builder
	for i, turn := range turns {
		if i > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString("[role: user]\n")
		out.WriteString(turn.User)
		out.WriteString("\n[user:end]")
		if turn.Assistant != "" {
			out.WriteString("\n[role: assistant]\n")
			out.WriteString(turn.Assistant)
			out.WriteString("\n[assistant:end]")
		}
	}
	return out.String()
}
func nativeRecallParse(text string) []nativeRecallTurn {
	var turns []nativeRecallTurn
	for _, match := range nativeRecallStoredTurn.FindAllStringSubmatch(text, -1) {
		turns = append(turns, nativeRecallTurn{User: match[1], Assistant: match[2]})
	}
	return turns
}
func nativeRecallJoin(archive, turns []nativeRecallTurn) []nativeRecallTurn {
	head := 0
	for head < len(archive) && head < len(turns) && archive[head] == turns[head] {
		head++
	}
	rest := turns[head:]
	tail := 0
	for n := min(len(archive)-head, len(rest)); n > 0; n-- {
		equal := true
		for i := range n {
			if archive[len(archive)-n+i] != rest[i] {
				equal = false
				break
			}
		}
		if equal {
			tail = n
			break
		}
	}
	out := make([]nativeRecallTurn, 0, len(archive)+len(rest)-tail)
	out = append(out, archive...)
	return append(out, rest[tail:]...)
}
