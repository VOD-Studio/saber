package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/sashabaranov/go-openai"
)

func honchoNativeTools() []openai.Tool {
	peer := map[string]any{"type": "string", "enum": []string{"user", "ai"}}
	return []openai.Tool{
		memoryNativeTool("honcho_profile", "读取或更新当前可信空间的 Honcho peer card，不允许任意 peer ID", map[string]any{"peer": peer, "card": map[string]any{"type": "array", "items": nativeTextField()}}),
		memoryNativeTool("honcho_search", "语义检索当前空间在 Honcho 存储的原始消息", map[string]any{"query": nativeTextField(), "peer": peer, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "query"),
		memoryNativeTool("honcho_context", "读取真实会话摘要、表示、peer card 与最近消息", map[string]any{"peer": peer}),
		memoryNativeTool("honcho_reasoning", "通过 Honcho dialectic 推理回答当前空间问题", map[string]any{"query": nativeTextField(), "peer": peer, "reasoning_level": map[string]any{"type": "string", "enum": []string{"minimal", "low", "medium", "high", "max"}}}, "query"),
		memoryNativeTool("honcho_conclude", "创建、按来源检查后删除，或分页/语义检索当前空间的持久结论；三种操作互斥", map[string]any{"conclusion": nativeTextField(), "delete_id": nativeTextField(), "list": map[string]any{"type": "boolean"}, "query": nativeTextField(), "peer": peer, "cursor": nativeTextField()}),
	}
}

func (n *NativeBackend) honchoBase() string {
	return "/v3/workspaces/" + url.PathEscape(n.option("workspace", "saber"))
}
func honchoPeer(space, alias string) (string, error) {
	switch alias {
	case "", "user":
		return "u-" + space, nil
	case "ai":
		return "a-" + space, nil
	default:
		return "", errors.New("honcho peer 只能为当前空间 user/ai 别名")
	}
}

func (n *NativeBackend) honchoInitialize(ctx context.Context, space, session string) (any, error) {
	workspace := n.option("workspace", "saber")
	if _, err := n.request(ctx, space, http.MethodPost, "/v3/workspaces", nil, map[string]any{"id": workspace}); err != nil {
		return nil, err
	}
	for _, peer := range []string{"u-" + space, "a-" + space} {
		if _, err := n.request(ctx, space, http.MethodPost, n.honchoBase()+"/peers", nil, map[string]any{"id": peer}); err != nil {
			return nil, err
		}
	}
	return n.honchoCreateSession(ctx, space, session)
}
func (n *NativeBackend) honchoCreateSession(ctx context.Context, space, session string) (any, error) {
	return n.request(ctx, space, http.MethodPost, n.honchoBase()+"/sessions", nil, map[string]any{"id": "s-" + session, "peers": map[string]any{"u-" + space: map[string]any{"observe_me": true, "observe_others": true}, "a-" + space: map[string]any{"observe_me": true, "observe_others": true}}, "metadata": map[string]any{"source": "saber"}})
}

func (n *NativeBackend) honchoMessages(ctx context.Context, space, session string, messages []map[string]any) (any, error) {
	if len(messages) == 0 {
		return nil, errors.New("没有可存储的原始消息")
	}
	batch := make([]map[string]any, 0, 100)
	var last any
	flush := func() error {
		value, err := n.request(ctx, space, http.MethodPost, n.honchoBase()+"/sessions/s-"+url.PathEscape(session)+"/messages", nil, map[string]any{"messages": batch})
		if err != nil {
			return err
		}
		last = value
		batch = batch[:0]
		return nil
	}
	for _, message := range messages {
		text, _ := message["content"].(string)
		peer := "a-" + space
		if message["role"] == "user" {
			peer = "u-" + space
		}
		for len(text) > 0 {
			chunk := text
			rest := ""
			if len(chunk) > 25000 {
				chunk = truncateUTF8Bytes(chunk, 25000)
				rest = text[len(chunk):]
			}
			batch = append(batch, map[string]any{"peer_id": peer, "content": chunk})
			if len(batch) == 100 {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			text = rest
		}
	}
	if len(batch) > 0 {
		if err := flush(); err != nil {
			return nil, err
		}
	}
	return last, nil
}

func (n *NativeBackend) honcho(ctx context.Context, space, session, operation string, payload map[string]any) (any, error) {
	base := n.honchoBase()
	switch operation {
	case "initialize", "session_switch":
		return n.honchoInitialize(ctx, space, session)
	case "context", "session_end", "shutdown":
		return n.request(ctx, space, http.MethodGet, base+"/sessions/s-"+url.PathEscape(session)+"/context", url.Values{"tokens": {"2000"}, "peer_target": {"u-" + space}, "peer_perspective": {"a-" + space}}, nil)
	case "prefetch":
		query, err := nativeString(payload, "query")
		if err != nil {
			return nil, err
		}
		return n.request(ctx, space, http.MethodGet, base+"/peers/a-"+space+"/context", url.Values{"target": {"u-" + space}, "search_query": {query}, "search_top_k": {"10"}}, nil)
	case "sync_turn":
		return n.honchoMessages(ctx, space, session, nativeMessages(payload, false))
	case "delegation":
		text := fmt.Sprint(payload["task"]) + "\n" + fmt.Sprint(payload["result"])
		return n.honchoMessages(ctx, space, session, []map[string]any{{"role": "assistant", "content": "[已消费的子任务结果]\n" + text}})
	case "pre_compress":
		data, err := json.Marshal(payload["messages"])
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(data)
		checkpointSession := session + "-checkpoint-" + hex.EncodeToString(hash[:8])
		if _, err = n.honchoCreateSession(ctx, space, checkpointSession); err != nil {
			return nil, err
		}
		value, err := n.honchoMessages(ctx, space, checkpointSession, nativeMessages(payload, true))
		if err != nil {
			return nil, err
		}
		items := nativeResults(value)
		if len(items) == 0 {
			return nil, errors.New("honcho 未返回持久消息确认")
		}
		id, _ := items[len(items)-1]["id"].(string)
		if id == "" {
			return nil, errors.New("honcho 未返回 checkpoint 消息 ID")
		}
		return map[string]any{"checkpointed": true, "checkpoint_id": id, "content": "历史原文已保存在 Honcho checkpoint 会话 s-" + checkpointSession}, nil
	case "memory_write":
		return n.honchoMirror(ctx, space, session, payload)
	case "tool":
		name, _ := payload["tool_name"].(string)
		args, _ := payload["arguments"].(map[string]any)
		alias, _ := args["peer"].(string)
		peer, err := honchoPeer(space, alias)
		if err != nil {
			return nil, err
		}
		switch name {
		case "honcho_profile":
			if card, ok := args["card"]; ok {
				return n.request(ctx, space, http.MethodPut, base+"/peers/a-"+space+"/card", url.Values{"target": {peer}}, map[string]any{"peer_card": card})
			}
			return n.request(ctx, space, http.MethodGet, base+"/peers/a-"+space+"/context", url.Values{"target": {peer}}, nil)
		case "honcho_context":
			return n.request(ctx, space, http.MethodGet, base+"/sessions/s-"+session+"/context", url.Values{"tokens": {"2000"}, "peer_target": {peer}, "peer_perspective": {"a-" + space}}, nil)
		case "honcho_search":
			query, err := nativeString(args, "query")
			if err != nil {
				return nil, err
			}
			return n.request(ctx, space, http.MethodPost, base+"/peers/"+peer+"/search", nil, map[string]any{"query": query, "limit": nativeInt(args, "limit", 10, 100)})
		case "honcho_reasoning":
			query, err := nativeString(args, "query")
			if err != nil {
				return nil, err
			}
			level, _ := args["reasoning_level"].(string)
			if level == "" {
				level = n.option("dialectic_level", "low")
			}
			switch level {
			case "minimal", "low", "medium", "high", "max":
			default:
				return nil, errors.New("honcho reasoning_level 无效")
			}
			return n.request(ctx, space, http.MethodPost, base+"/peers/a-"+space+"/chat", nil, map[string]any{"target": peer, "query": query, "reasoning_level": level, "session_id": "s-" + session, "include_evidence": true})
		case "honcho_conclude":
			content, _ := args["conclusion"].(string)
			id, _ := args["delete_id"].(string)
			list, _ := args["list"].(bool)
			count := 0
			if content != "" {
				count++
			}
			if id != "" {
				count++
			}
			if list {
				count++
			}
			if count != 1 {
				return nil, errors.New("conclusion/delete_id/list 必须且只能指定一种")
			}
			if content != "" {
				return n.honchoConclusion(ctx, space, session, peer, content)
			}
			if id != "" {
				return n.honchoDeleteConclusion(ctx, space, peer, id)
			}
			query, _ := args["query"].(string)
			filters := map[string]any{"observed_id": peer, "observer_id": "a-" + space}
			if query != "" {
				return n.request(ctx, space, http.MethodPost, base+"/conclusions/query", nil, map[string]any{"query": query, "top_k": 20, "filters": filters})
			}
			cursor, _ := args["cursor"].(string)
			return n.request(ctx, space, http.MethodPost, base+"/conclusions/list", url.Values{"size": {"20"}, "cursor": {cursor}}, map[string]any{"filters": filters})
		default:
			return nil, fmt.Errorf("未知 Honcho tool %q", name)
		}
	default:
		return nil, fmt.Errorf("honcho 未实现 operation %q", operation)
	}
}

func (n *NativeBackend) honchoConclusion(ctx context.Context, space, session, peer, content string) (any, error) {
	if strings.TrimSpace(content) == "" {
		return nil, ErrEmptyContent
	}
	return n.request(ctx, space, http.MethodPost, n.honchoBase()+"/conclusions", nil, map[string]any{"conclusions": []any{map[string]any{"content": content, "observer_id": "a-" + space, "observed_id": peer, "session_id": "s-" + session}}})
}
func (n *NativeBackend) honchoDeleteConclusion(ctx context.Context, space, peer, id string) (any, error) {
	endpoint := n.honchoBase() + "/conclusions/" + url.PathEscape(id)
	value, err := n.request(ctx, space, http.MethodGet, endpoint, nil, nil)
	if err != nil {
		return nil, err
	}
	item, ok := value.(map[string]any)
	if !ok || item["observer_id"] != "a-"+space || item["observed_id"] != peer {
		return nil, errors.New("结论不属于当前可信记忆空间")
	}
	return n.request(ctx, space, http.MethodDelete, endpoint, nil, nil)
}
func (n *NativeBackend) honchoMirror(ctx context.Context, space, session string, payload map[string]any) (any, error) {
	id := int64(nativeInt(payload, "entry_id", 0, int(^uint(0)>>1)))
	action, _ := payload["action"].(string)
	content, _ := payload["content"].(string)
	if id <= 0 {
		return nil, errors.New("memory mirror 缺少权威 entry_id")
	}
	if action == ActionReplace || action == ActionRemove {
		ids, err := n.links(ctx, space, id)
		if err != nil {
			return nil, err
		}
		for _, remote := range ids {
			if _, err = n.honchoDeleteConclusion(ctx, space, "u-"+space, remote); err != nil {
				return nil, err
			}
		}
		if err = n.saveLinks(ctx, space, id, nil); err != nil {
			return nil, err
		}
		if action == ActionRemove {
			return map[string]any{"status": "deleted", "ids": ids}, nil
		}
	}
	if action != ActionAdd && action != ActionReplace {
		return nil, errors.New("未知 memory mirror action")
	}
	value, err := n.honchoConclusion(ctx, space, session, "u-"+space, content)
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
		return nil, errors.New("honcho 未确认 conclusion ID，不能建立来源映射")
	}
	return value, n.saveLinks(ctx, space, id, ids)
}
