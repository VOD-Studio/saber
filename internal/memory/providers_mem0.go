package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
)

func mem0NativeTools() []openai.Tool {
	return []openai.Tool{
		memoryNativeTool("mem0_search", "以当前可信空间 user_id 过滤的 Mem0 语义检索", map[string]any{"query": nativeTextField(), "top_k": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}, "rerank": map[string]any{"type": "boolean"}}, "query"),
		memoryNativeTool("mem0_add", "向当前空间提交原文事实；异步结果会报告event_id，不能声称已完成持久化", map[string]any{"content": nativeTextField()}, "content"),
		memoryNativeTool("mem0_update", "读取并确认所属空间后修改远端 memory ID", map[string]any{"memory_id": nativeTextField(), "text": nativeTextField()}, "memory_id", "text"),
		memoryNativeTool("mem0_delete", "读取并确认所属空间后删除远端 memory ID", map[string]any{"memory_id": nativeTextField()}, "memory_id"),
	}
}

func (n *NativeBackend) mem0MemoryPath(id string) string {
	if n.option("mode", "platform") == "platform" {
		return "/v1/memories/" + url.PathEscape(id) + "/"
	}
	return "/memories/" + url.PathEscape(id)
}
func (n *NativeBackend) mem0Search(ctx context.Context, space, query string, limit int, rerank bool) (any, error) {
	endpoint := "/v3/memories/search/"
	body := map[string]any{"query": query, "top_k": limit, "filters": map[string]any{"user_id": space}}
	if n.option("mode", "platform") == "platform" {
		body["rerank"] = rerank
	} else {
		endpoint = "/search"
	}
	return n.request(ctx, space, http.MethodPost, endpoint, nil, body)
}

func (n *NativeBackend) mem0Add(ctx context.Context, space, session string, messages []map[string]any, infer, wait bool, metadata map[string]any) (any, error) {
	if len(messages) == 0 {
		return nil, errors.New("Mem0没有可提交的消息")
	}
	body := map[string]any{"messages": messages, "user_id": space, "agent_id": "saber-" + space, "infer": infer, "metadata": metadata}
	endpoint := "/memories"
	platform := n.option("mode", "platform") == "platform"
	if platform {
		endpoint = "/v3/memories/add/"
		body["run_id"] = session
	}
	value, err := n.request(ctx, space, http.MethodPost, endpoint, nil, body)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return value, nil
	}
	eventID, _ := object["event_id"].(string)
	if eventID != "" {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if _, err = n.owner.store.db.ExecContext(ctx, `INSERT OR IGNORE INTO memory_native_events(provider,scope_key,session_key,event_id,status,result,created_at) VALUES(?,?,?,?,'pending',?,?)`, n.id, space, session, eventID, data, nowMillis()); err != nil {
			return nil, err
		}
		if wait {
			return n.mem0WaitEvent(ctx, space, eventID)
		}
	}
	if status, _ := object["status"].(string); status == "FAILED" {
		return nil, fmt.Errorf("Mem0写入失败: %v", object["error"])
	}
	if wait && object["status"] == "PENDING" {
		return nil, errors.New("Mem0未返回event_id，无法证明checkpoint已完成")
	}
	return value, nil
}

func (n *NativeBackend) mem0WaitEvent(ctx context.Context, space, eventID string) (any, error) {
	for {
		value, err := n.request(ctx, space, http.MethodGet, "/v1/event/"+url.PathEscape(eventID)+"/", nil, nil)
		if err != nil {
			return nil, err
		}
		event, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("Mem0 event响应无效")
		}
		status, _ := event["status"].(string)
		if status == "SUCCEEDED" || status == "FAILED" {
			data, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			if _, err = n.owner.store.db.ExecContext(ctx, `UPDATE memory_native_events SET status=?,result=? WHERE provider=? AND scope_key=? AND event_id=?`, strings.ToLower(status), data, n.id, space, eventID); err != nil {
				return nil, err
			}
			if status == "FAILED" {
				return nil, fmt.Errorf("Mem0 event %s失败: %v", eventID, event["error"])
			}
			return value, nil
		}
		if status != "PENDING" && status != "RUNNING" {
			return nil, fmt.Errorf("未知 Mem0 event状态 %q", status)
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (n *NativeBackend) mem0Drain(ctx context.Context, space, session string) (any, error) {
	rows, err := n.owner.store.db.QueryContext(ctx, `SELECT event_id FROM memory_native_events WHERE provider=? AND scope_key=? AND session_key=? AND status IN ('pending','running') ORDER BY created_at`, n.id, space, session)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	var results []any
	for _, id := range ids {
		value, err := n.mem0WaitEvent(ctx, space, id)
		if err != nil {
			return nil, err
		}
		results = append(results, value)
	}
	return map[string]any{"drained_events": len(ids), "results": results}, nil
}

func (n *NativeBackend) mem0Owned(ctx context.Context, space, id string) (map[string]any, error) {
	value, err := n.request(ctx, space, http.MethodGet, n.mem0MemoryPath(id), nil, nil)
	if err != nil {
		return nil, err
	}
	item, ok := value.(map[string]any)
	if !ok || item["user_id"] != space {
		return nil, errors.New("Mem0 memory不属于当前可信空间或未返回可证明的user_id")
	}
	return item, nil
}

func (n *NativeBackend) mem0(ctx context.Context, space, session, operation string, payload map[string]any) (any, error) {
	switch operation {
	case "initialize":
		if n.option("mode", "platform") == "platform" {
			return n.request(ctx, space, http.MethodGet, "/v1/ping/", nil, nil)
		}
		return n.mem0Search(ctx, space, "Saber memory initialization", 1, false)
	case "context", "prefetch":
		query, _ := payload["query"].(string)
		if strings.TrimSpace(query) == "" {
			query = "用户偏好、长期约定和已有项目事实"
		}
		rerank, _ := n.cfg.Options["rerank"].(bool)
		return n.mem0Search(ctx, space, query, 10, rerank)
	case "sync_turn":
		return n.mem0Add(ctx, space, session, nativeMessages(payload, false), true, false, map[string]any{"source": "saber", "saber_space": space, "saber_session": session})
	case "session_end", "shutdown", "session_switch":
		return n.mem0Drain(ctx, space, session)
	case "delegation":
		messages := []map[string]any{{"role": "assistant", "content": "[已消费的子任务结果]\n" + fmt.Sprint(payload["task"]) + "\n" + fmt.Sprint(payload["result"])}}
		return n.mem0Add(ctx, space, session, messages, true, false, map[string]any{"source": "saber_delegation", "saber_space": space})
	case "pre_compress":
		value, err := n.mem0Add(ctx, space, session, nativeMessages(payload, true), false, true, map[string]any{"source": "saber_checkpoint", "saber_space": space})
		if err != nil {
			return nil, err
		}
		var checkpoint string
		if object, ok := value.(map[string]any); ok {
			checkpoint, _ = object["id"].(string)
			if checkpoint == "" {
				checkpoint, _ = object["event_id"].(string)
			}
		}
		if checkpoint == "" {
			items := nativeResults(value)
			if len(items) > 0 {
				checkpoint, _ = items[len(items)-1]["id"].(string)
			}
		}
		if checkpoint == "" {
			return nil, errors.New("Mem0未返回耐久checkpoint ID")
		}
		return map[string]any{"checkpointed": true, "checkpoint_id": checkpoint, "content": "压缩前原文已由 Mem0 确认存储"}, nil
	case "memory_write":
		return n.mem0Mirror(ctx, space, session, payload)
	case "tool":
		name, _ := payload["tool_name"].(string)
		args, _ := payload["arguments"].(map[string]any)
		switch name {
		case "mem0_search":
			query, err := nativeString(args, "query")
			if err != nil {
				return nil, err
			}
			rerank, _ := args["rerank"].(bool)
			return n.mem0Search(ctx, space, query, nativeInt(args, "top_k", 10, 50), rerank)
		case "mem0_add":
			content, err := nativeString(args, "content")
			if err != nil {
				return nil, err
			}
			return n.mem0Add(ctx, space, session, []map[string]any{{"role": "user", "content": content}}, false, false, map[string]any{"source": "saber_explicit", "saber_space": space})
		case "mem0_update", "mem0_delete":
			id, err := nativeString(args, "memory_id")
			if err != nil {
				return nil, err
			}
			if _, err = n.mem0Owned(ctx, space, id); err != nil {
				return nil, err
			}
			if name == "mem0_delete" {
				return n.request(ctx, space, http.MethodDelete, n.mem0MemoryPath(id), nil, nil)
			}
			text, err := nativeString(args, "text")
			if err != nil {
				return nil, err
			}
			return n.request(ctx, space, http.MethodPut, n.mem0MemoryPath(id), nil, map[string]any{"text": text})
		default:
			return nil, fmt.Errorf("未知Mem0 tool %q", name)
		}
	default:
		return nil, fmt.Errorf("Mem0未实现operation %q", operation)
	}
}

func (n *NativeBackend) mem0Mirror(ctx context.Context, space, session string, payload map[string]any) (any, error) {
	id := int64(nativeInt(payload, "entry_id", 0, int(^uint(0)>>1)))
	if id <= 0 {
		return nil, errors.New("Mem0 mirror需要entry_id")
	}
	action, _ := payload["action"].(string)
	content, _ := payload["content"].(string)
	if action == ActionReplace || action == ActionRemove {
		ids, err := n.links(ctx, space, id)
		if err != nil {
			return nil, err
		}
		for _, remote := range ids {
			if _, err = n.mem0Owned(ctx, space, remote); err != nil {
				return nil, err
			}
			method := http.MethodDelete
			var body any
			if action == ActionReplace {
				method = http.MethodPut
				body = map[string]any{"text": content}
			}
			if _, err = n.request(ctx, space, method, n.mem0MemoryPath(remote), nil, body); err != nil {
				return nil, err
			}
		}
		return map[string]any{"status": "completed", "action": action, "ids": ids}, nil
	}
	if action != ActionAdd {
		return nil, errors.New("未知Mem0 mirror action")
	}
	value, err := n.mem0Add(ctx, space, session, []map[string]any{{"role": "user", "content": content}}, false, true, map[string]any{"source": "saber_native_memory", "saber_space": space, "entry_id": id})
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, item := range nativeResults(value) {
		if remote, ok := item["id"].(string); ok && remote != "" {
			ids = append(ids, remote)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("Mem0写入没有返回memory ID，无法确认来源映射")
	}
	return value, n.saveLinks(ctx, space, id, ids)
}
