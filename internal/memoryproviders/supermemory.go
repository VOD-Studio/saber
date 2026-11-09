package memoryproviders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/task"
	"rua.plus/saber/internal/toolkit"
)

// 协议来源：https://github.com/supermemoryai/python-sdk/blob/main/fern/openapi.json
// 和 https://github.com/supermemoryai/hermes-supermemory；使用 namespace-first v5，非旧 v3。
type supermemoryBackend struct {
	cfg              config.MemoryBackendConfig
	life             nativeRecallLifetime
	searchMode       string
	dreaming         string
	entityContext    string
	maxResults       int
	profileFrequency int
	autoRecall       bool
	autoCapture      bool
}

// NewSupermemory 创建直连 Supermemory v5 托管或自托管 API 的原生适配器。
func NewSupermemory(cfg config.MemoryBackendConfig, _ toolkit.Host) (Backend, error) {
	cfg.Type = "supermemory"
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.supermemory.ai"
	}
	var err error
	cfg, err = nativeRecallConfig(cfg, "SUPERMEMORY_API_KEY", true)
	if err != nil {
		return nil, err
	}
	if err = nativeRecallOptionKeys(cfg.Options, map[string]bool{"search_mode": true, "dreaming": true, "entity_context": true, "max_recall_results": true, "profile_frequency": true, "auto_recall": true, "auto_capture": true}); err != nil {
		return nil, err
	}
	b := &supermemoryBackend{cfg: cfg, searchMode: "hybrid", dreaming: "dynamic", entityContext: "User-assistant conversation. Remember lasting facts, preferences, routines, tools and ongoing projects. Do not remember temporary tasks or assistant actions. When in doubt, store less.", maxResults: 10, profileFrequency: 50, autoRecall: true, autoCapture: true}
	if b.searchMode, err = nativeRecallEnum(cfg.Options, "search_mode", "hybrid", "hybrid", "memories", "chunks"); err != nil {
		return nil, err
	}
	if b.dreaming, err = nativeRecallEnum(cfg.Options, "dreaming", "dynamic", "dynamic", "instant"); err != nil {
		return nil, err
	}
	if b.maxResults, err = nativeRecallInt(cfg.Options, "max_recall_results", 10, 1, 20); err != nil {
		return nil, err
	}
	if b.profileFrequency, err = nativeRecallInt(cfg.Options, "profile_frequency", 50, 1, 500); err != nil {
		return nil, err
	}
	if b.autoRecall, err = nativeRecallBool(cfg.Options, "auto_recall", true); err != nil {
		return nil, err
	}
	if b.autoCapture, err = nativeRecallBool(cfg.Options, "auto_capture", true); err != nil {
		return nil, err
	}
	if value, ok := cfg.Options["entity_context"]; ok {
		var valid bool
		b.entityContext, valid = value.(string)
		if !valid || utf8.RuneCountInString(b.entityContext) > 1500 {
			return nil, errors.New("entity_context must be a string of at most 1500 characters")
		}
	}
	b.life.init()
	return b, nil
}

func (b *supermemoryBackend) Call(ctx context.Context, operation string, args map[string]any) (any, error) {
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
		return b.profile(ctx, scope)
	case "context", "prefetch":
		if !b.autoRecall {
			return map[string]any{"context": "", "disabled": true}, nil
		}
		query, err := nativeRecallRequired(payload, "query")
		if err != nil {
			return nil, err
		}
		state, err := b.life.session(session)
		if err != nil {
			return nil, err
		}
		state.mu.Lock()
		count := state.turnCount
		state.mu.Unlock()
		var profile map[string]any
		if count <= 1 || count%b.profileFrequency == 0 {
			profile, err = b.profile(ctx, scope)
			if err != nil {
				return nil, err
			}
		}
		results, err := b.search(ctx, scope, query, b.maxResults, b.searchMode)
		if err != nil {
			return nil, err
		}
		return map[string]any{"context": nativeRecallBound(supermemoryContext(profile, results), 32768)}, nil
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
		if _, ok := payload["messages"]; ok && b.autoCapture {
			return b.capture(ctx, scope, session, payload)
		}
		state, err := b.life.session(session)
		if err != nil {
			return nil, err
		}
		state.mu.Lock()
		doc := state.document
		state.mu.Unlock()
		b.life.release(session, state)
		return map[string]any{"pending_client_writes": false, "document_id": doc}, nil
	case "pre_compress":
		return b.checkpoint(ctx, scope, session, payload)
	case "memory_write":
		if action := nativeRecallText(payload, "action"); action != "add" {
			return nil, fmt.Errorf("supermemory memory_write does not support action %q", action)
		}
		content, err := nativeRecallRequired(payload, "content")
		if err != nil {
			return nil, err
		}
		doc := nativeRecallDocumentID("entry", scope, fmt.Sprint(nativeRecallOptionalInt(payload, "entry_id")), content)
		return b.writeDocument(ctx, scope, doc, content, "explicit_memory", nil)
	case "tools":
		return supermemoryTools(), nil
	case "tool":
		return b.tool(ctx, scope, payload)
	default:
		return nil, fmt.Errorf("supermemory does not support operation %q", operation)
	}
}

func (b *supermemoryBackend) Close() error { return b.life.close() }

func (b *supermemoryBackend) request(ctx context.Context, scope, method, suffix string, body any) (map[string]any, error) {
	data, err := HTTPJSON(ctx, b.cfg, method, nativeRecallPath(b.cfg, "ns/"+url.PathEscape(scope)+suffix), body, map[string]string{"x-sm-source": "saber"})
	if err != nil {
		return nil, fmt.Errorf("supermemory %s: %w", method, err)
	}
	return nativeRecallObject(data)
}

func (b *supermemoryBackend) profile(ctx context.Context, scope string) (map[string]any, error) {
	result, err := b.request(ctx, scope, http.MethodPost, "/profile", map[string]any{})
	if err != nil {
		return nil, err
	}
	profile, ok := result["profile"].(map[string]any)
	if !ok {
		return nil, errors.New("supermemory v5 profile response has no profile")
	}
	for _, key := range []string{"static", "dynamic"} {
		if _, ok := profile[key].([]any); !ok {
			return nil, errors.New("supermemory v5 profile has no " + key + " facts")
		}
	}
	if _, ok := profile["buckets"].(map[string]any); !ok {
		return nil, errors.New("supermemory v5 profile has no buckets")
	}
	return result, nil
}

func (b *supermemoryBackend) search(ctx context.Context, scope, query string, limit int, mode string) (map[string]any, error) {
	result, err := b.request(ctx, scope, http.MethodPost, "/search", map[string]any{"query": query, "limit": limit, "searchMode": mode, "include": map[string]any{"documents": true}})
	if err != nil {
		return nil, err
	}
	if _, ok := result["results"].([]any); !ok {
		return nil, errors.New("supermemory v5 search response has no results")
	}
	return result, nil
}

// PATCH 完整替换内容；仅明确 404 时创建，不在超时或未知错误后重放写操作。
func (b *supermemoryBackend) writeDocument(ctx context.Context, scope, doc, content, kind string, extra map[string]any) (map[string]any, error) {
	if err := nativeRecallContent(content); err != nil {
		return nil, err
	}
	metadata := map[string]any{"type": kind}
	for key, value := range extra {
		metadata[key] = value
	}
	for key, value := range nativeRecallMetadata(ctx, scope, doc) {
		metadata[key] = value
	}
	body := map[string]any{"content": content, "metadata": metadata, "supportingContext": b.entityContext, "dreaming": b.dreaming}
	result, err := b.request(ctx, scope, http.MethodPatch, "/document/"+url.PathEscape(doc), body)
	if nativeRecallStatus(err, http.StatusNotFound) {
		body["id"] = doc
		result, err = b.request(ctx, scope, http.MethodPost, "/document", body)
	}
	if err != nil {
		return nil, err
	}
	status := nativeRecallText(result, "status")
	if nativeRecallText(result, "id") == "" || status == "" {
		return nil, errors.New("supermemory document response has no id or status")
	}
	if status == "failed" || status == "unknown" {
		return nil, fmt.Errorf("supermemory document processing state is %s", status)
	}
	result["document_id"] = doc
	return result, nil
}

func (b *supermemoryBackend) capture(ctx context.Context, scope, session string, payload map[string]any) (any, error) {
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
		return nil, errors.New("supermemory sync_turn has no completed user-assistant turns")
	}
	_, full := payload["messages"]
	deltaKey := ""
	if !full {
		deltaKey = nativeRecallHash(fmt.Sprint(task.ID(ctx)) + "\x00" + nativeRecallRender(turns))
		if state.lastDelta == deltaKey {
			return map[string]any{"captured": false, "unchanged": true, "document_id": state.document}, nil
		}
	}
	doc := nativeRecallDocumentID("conversation", scope, session)
	if !state.loaded && (!full || compact || state.compacted) {
		stored, err := b.request(ctx, scope, http.MethodGet, "/document/"+url.PathEscape(doc), nil)
		if err != nil && !nativeRecallStatus(err, 404) {
			return nil, err
		}
		if err == nil {
			content, ok := stored["content"].(string)
			if !ok {
				return nil, errors.New("supermemory cannot preserve compacted history: canonical content is unavailable")
			}
			state.turns = nativeRecallParse(content)
		}
	}
	if !full {
		turns = append(append([]nativeRecallTurn(nil), state.turns...), turns...)
	} else if compact || state.compacted {
		turns = nativeRecallJoin(state.turns, turns)
	}
	content := nativeRecallRender(turns)
	digest := nativeRecallHash(content)
	if state.digest == digest {
		return map[string]any{"captured": false, "unchanged": true, "document_id": doc}, nil
	}
	result, err := b.writeDocument(ctx, scope, doc, content, "conversation", map[string]any{"session_id": session})
	if err != nil {
		return nil, err
	}
	state.turns = turns
	state.digest = digest
	state.document = doc
	state.loaded = true
	state.compacted = false
	state.lastDelta = deltaKey
	result["captured"] = true
	return result, nil
}

func (b *supermemoryBackend) checkpoint(ctx context.Context, scope, session string, payload map[string]any) (any, error) {
	messages, ok := payload["messages"]
	if !ok {
		return nil, errors.New("supermemory checkpoint requires messages")
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
	if _, err = b.writeDocument(ctx, scope, doc, content, "checkpoint", nil); err != nil {
		return nil, err
	}
	stored, err := b.request(ctx, scope, http.MethodGet, "/document/"+url.PathEscape(doc), nil)
	if err != nil {
		return nil, err
	}
	if nativeRecallText(stored, "content") != content {
		return nil, errors.New("supermemory checkpoint canonical content is not durably readable")
	}
	if compact {
		state.turns = nativeRecallJoin(state.turns, turns)
	} else if len(turns) > 0 {
		state.turns = turns
	}
	state.compacted = true
	return map[string]any{"checkpointed": true, "checkpoint_id": doc, "extraction_complete": false}, nil
}

func (b *supermemoryBackend) tool(ctx context.Context, scope string, payload map[string]any) (any, error) {
	name := nativeRecallText(payload, "tool_name")
	args, _ := payload["arguments"].(map[string]any)
	if err := nativeRecallToolScope(args); err != nil {
		return nil, err
	}
	switch name {
	case "supermemory-save":
		content, err := nativeRecallRequired(args, "content")
		if err != nil {
			return nil, err
		}
		metadata := map[string]any{}
		if raw, ok := args["metadata"]; ok {
			var valid bool
			metadata, valid = raw.(map[string]any)
			if !valid {
				return nil, errors.New("metadata must be an object")
			}
		}
		if err := supermemoryMetadata(metadata); err != nil {
			return nil, err
		}
		return b.writeDocument(ctx, scope, nativeRecallDocumentID("explicit", scope, content), content, "explicit_memory", metadata)
	case "supermemory-search":
		query, err := nativeRecallRequired(args, "query")
		if err != nil {
			return nil, err
		}
		limit, err := nativeRecallInt(args, "limit", 5, 1, 20)
		if err != nil {
			return nil, err
		}
		return b.search(ctx, scope, query, limit, b.searchMode)
	case "supermemory-profile":
		profile, err := b.profile(ctx, scope)
		if err != nil {
			return nil, err
		}
		if query := nativeRecallText(args, "query"); query != "" {
			results, err := b.search(ctx, scope, query, b.maxResults, b.searchMode)
			if err != nil {
				return nil, err
			}
			profile["results"] = results["results"]
		}
		return profile, nil
	case "supermemory-read-document":
		doc, err := nativeRecallRequired(args, "document_id")
		if err != nil {
			return nil, err
		}
		if len(doc) > 255 {
			return nil, errors.New("document_id exceeds 255 bytes")
		}
		offset, err := nativeRecallInt(args, "offset", 0, 0, 1000000000)
		if err != nil {
			return nil, err
		}
		stored, err := b.request(ctx, scope, http.MethodGet, "/document/"+url.PathEscape(doc), nil)
		if err != nil {
			return nil, err
		}
		content, ok := stored["content"].(string)
		if !ok {
			return nil, errors.New("supermemory document canonical content is unavailable")
		}
		window, next := supermemoryWindow(content, offset, 8000)
		return map[string]any{"id": doc, "content": window, "next_offset": next, "system": stored["system"]}, nil
	case "supermemory-forget":
		id := nativeRecallText(args, "id")
		if id == "" {
			query, err := nativeRecallRequired(args, "query")
			if err != nil {
				return nil, errors.New("forget requires id or query")
			}
			found, err := b.search(ctx, scope, query, 5, "memories")
			if err != nil {
				return nil, err
			}
			items, _ := found["results"].([]any)
			if len(items) == 0 {
				return nil, errors.New("supermemory has no matching memory to forget")
			}
			first, _ := items[0].(map[string]any)
			id = nativeRecallText(first, "id")
			if id == "" {
				return nil, errors.New("supermemory search match has no memory id")
			}
		}
		result, err := b.request(ctx, scope, http.MethodDelete, "/memories", map[string]any{"ids": []string{id}})
		if err != nil {
			return nil, err
		}
		failures, ok := result["errors"].([]any)
		if !ok {
			return nil, errors.New("supermemory forget response has no per-id errors")
		}
		for _, failure := range failures {
			value, _ := failure.(map[string]any)
			if nativeRecallText(value, "id") == id {
				return nil, errors.New("supermemory did not forget requested memory (missing or ineligible id)")
			}
		}
		matches, ok := result["matches"].([]any)
		if !ok {
			return nil, errors.New("supermemory forget response has no matches")
		}
		for _, match := range matches {
			value, _ := match.(map[string]any)
			if nativeRecallText(value, "id") == id {
				return result, nil
			}
		}
		return nil, errors.New("supermemory did not confirm the requested memory was forgotten")
	default:
		return nil, fmt.Errorf("unknown supermemory tool %q", name)
	}
}

func supermemoryMetadata(metadata map[string]any) error {
	for _, value := range metadata {
		switch v := value.(type) {
		case string, bool, float64, float32, int, int64, int32, uint, uint64, json.Number:
		case []string:
		case []any:
			for _, item := range v {
				if _, ok := item.(string); !ok {
					return errors.New("supermemory metadata arrays must contain strings")
				}
			}
		default:
			return errors.New("supermemory metadata values must be string, number, boolean or string arrays")
		}
	}
	return nil
}

func supermemoryContext(profile, search map[string]any) string {
	var lines []string
	seen := map[string]bool{}
	appendFact := func(raw any) {
		value, _ := raw.(map[string]any)
		text := nativeRecallText(value, "memory")
		if text == "" {
			text = nativeRecallText(value, "chunk")
		}
		if text != "" && !seen[text] {
			seen[text] = true
			lines = append(lines, "- "+text)
		}
	}
	if facts, ok := profile["profile"].(map[string]any); ok {
		for _, key := range []string{"static", "dynamic"} {
			items, _ := facts[key].([]any)
			for _, item := range items {
				appendFact(item)
			}
		}
		buckets, _ := facts["buckets"].(map[string]any)
		keys := make([]string, 0, len(buckets))
		for key := range buckets {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			items, _ := buckets[key].([]any)
			for _, item := range items {
				appendFact(item)
			}
		}
	}
	items, _ := search["results"].([]any)
	for _, item := range items {
		appendFact(item)
	}
	return strings.Join(lines, "\n")
}

func supermemoryWindow(text string, offset, limit int) (string, any) {
	start, end, count := -1, len(text), 0
	for index := range text {
		if count == offset {
			start = index
		}
		if count == offset+limit {
			end = index
			break
		}
		count++
	}
	if start < 0 {
		return "", nil
	}
	if end < len(text) {
		return text[start:end], offset + limit
	}
	return text[start:end], nil
}

func supermemoryTools() []any {
	return []any{
		nativeRecallTool("supermemory-save", "保存明确长期记忆；返回真实文档处理状态，不将 queued 当作完成。", map[string]any{"content": map[string]any{"type": "string"}, "metadata": map[string]any{"type": "object"}}, "content"),
		nativeRecallTool("supermemory-search", "在当前可信 namespace 搜索记忆和文档片段。", map[string]any{"query": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 20}}, "query"),
		nativeRecallTool("supermemory-forget", "按精确记忆 id 或 query 的最佳匹配遗忘；检查逐 id 错误。", map[string]any{"id": map[string]any{"type": "string"}, "query": map[string]any{"type": "string"}}),
		nativeRecallTool("supermemory-profile", "读取当前可信 namespace 的稳定事实、动态事实和自定义分组。", map[string]any{"query": map[string]any{"type": "string"}}),
		nativeRecallTool("supermemory-read-document", "读取搜索结果中的文档，按 Unicode 字符 offset 分页。", map[string]any{"document_id": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}}, "document_id"),
	}
}
