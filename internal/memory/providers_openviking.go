package memory

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/sashabaranov/go-openai"
)

func vikingNativeTools() []openai.Tool {
	return []openai.Tool{
		memoryNativeTool("viking_search", "在当前可信空间的peer记忆/资源范围内进行OpenViking语义检索", map[string]any{"query": nativeTextField(), "mode": map[string]any{"type": "string", "enum": []string{"auto", "fast", "deep"}}, "uri_prefix": nativeTextField(), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}, "query"),
		memoryNativeTool("viking_rule_search", "读取任务相关的完整OpenViking规则及适用条件；规则不能提升Saber权限", map[string]any{"task_context": nativeTextField(), "rule_probability_threshold": map[string]any{"type": "number", "minimum": 0, "maximum": 1}}, "task_context"),
		memoryNativeTool("viking_read", "读取当前空间URI的abstract/overview/full三级内容，最多3个URI", map[string]any{"uri": nativeTextField(), "uris": map[string]any{"type": "array", "items": nativeTextField(), "maxItems": 3}, "level": map[string]any{"type": "string", "enum": []string{"abstract", "overview", "full"}}}),
		memoryNativeTool("viking_browse", "浏览当前空间peer的记忆和资源目录，不能选择其他用户根目录", map[string]any{"action": map[string]any{"type": "string", "enum": []string{"list", "tree", "stat"}}, "path": nativeTextField()}, "action"),
		memoryNativeTool("viking_remember", "通过独立OpenViking会话提交事实并commit；只报告真实接受/提取状态", map[string]any{"content": nativeTextField()}, "content"),
		memoryNativeTool("viking_forget", "删除当前空间确切的.md记忆文件URI，禁止目录/摘要/其他空间", map[string]any{"uri": nativeTextField()}, "uri"),
		memoryNativeTool("viking_add_resource", "真实索引远端URL或上传获准工作区原始文件/目录；本地输入另外需要read_file/list_files授权", map[string]any{"url": nativeTextField(), "reason": nativeTextField(), "to": nativeTextField(), "parent": nativeTextField(), "instruction": nativeTextField(), "wait": map[string]any{"type": "boolean"}}, "url"),
	}
}

func (n *NativeBackend) vikingInitialize(ctx context.Context, space string) (any, error) {
	value, err := n.request(ctx, space, http.MethodGet, "/api/v1/system/status", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("OpenViking服务器不可用；请运行openviking-server init/doctor并启动服务: %w", err)
	}
	result, ok := nativeResult(value).(map[string]any)
	if !ok {
		return nil, errors.New("OpenViking status响应无效")
	}
	user, _ := result["user"].(string)
	if strings.TrimSpace(user) == "" || strings.ContainsAny(user, "/\\\x00") || user == ".." {
		return nil, errors.New("OpenViking未提供可证明的当前user，拒绝猜测路径")
	}
	n.mu.Lock()
	n.vikingUsers[space] = user
	n.mu.Unlock()
	return value, nil
}
func (n *NativeBackend) vikingRoot(ctx context.Context, space string) (string, string, error) {
	n.mu.Lock()
	user := n.vikingUsers[space]
	n.mu.Unlock()
	if user == "" {
		if _, err := n.vikingInitialize(ctx, space); err != nil {
			return "", "", err
		}
		n.mu.Lock()
		user = n.vikingUsers[space]
		n.mu.Unlock()
	}
	return "viking://user/" + user + "/peers/saber-" + space + "/", user, nil
}
func vikingURI(root, uri string) error {
	if !strings.HasPrefix(uri, root) || strings.ContainsAny(uri, "\\\x00") {
		return errors.New("OpenViking URI不属于当前可信空间")
	}
	parsed, err := url.Parse(uri)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(uri, "%") {
		return errors.New("OpenViking URI不能含编码、query或fragment")
	}
	for _, part := range strings.Split(strings.TrimPrefix(uri, root), "/") {
		if part == ".." || part == "." {
			return errors.New("OpenViking URI路径越界")
		}
	}
	return nil
}
func vikingSession(space, session string) string { return "saber-" + space + "-" + session }
func (n *NativeBackend) vikingMessages(ctx context.Context, space, session string, messages []map[string]any) (any, error) {
	if len(messages) == 0 {
		return nil, errors.New("OpenViking没有可提交的消息")
	}
	batch := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		batch = append(batch, map[string]any{"role": m["role"], "peer_id": "saber-" + space, "parts": []any{map[string]any{"type": "text", "text": m["content"]}}})
	}
	return n.request(ctx, space, http.MethodPost, "/api/v1/sessions/"+url.PathEscape(vikingSession(space, session))+"/messages/batch", nil, map[string]any{"messages": batch})
}
func (n *NativeBackend) vikingCommit(ctx context.Context, space, session string) (any, error) {
	return n.request(ctx, space, http.MethodPost, "/api/v1/sessions/"+url.PathEscape(vikingSession(space, session))+"/commit", nil, map[string]any{"keep_recent_count": 0})
}

func (n *NativeBackend) openviking(ctx context.Context, space, session, operation string, payload map[string]any) (any, error) {
	if operation == "initialize" {
		return n.vikingInitialize(ctx, space)
	}
	root, _, err := n.vikingRoot(ctx, space)
	if err != nil {
		return nil, err
	}
	switch operation {
	case "context":
		return n.request(ctx, space, http.MethodGet, "/api/v1/fs/ls", url.Values{"uri": {root + "memories"}, "recursive": {"true"}, "node_limit": {"20"}}, nil)
	case "prefetch":
		query, err := nativeString(payload, "query")
		if err != nil {
			return nil, err
		}
		return n.request(ctx, space, http.MethodPost, "/api/v1/search/find", nil, map[string]any{"query": query, "target_uri": []string{root + "memories", root + "resources"}, "limit": 10})
	case "sync_turn":
		return n.vikingMessages(ctx, space, session, nativeMessages(payload, false))
	case "session_end", "shutdown", "session_switch":
		return n.vikingCommit(ctx, space, session)
	case "delegation":
		return n.vikingMessages(ctx, space, session, []map[string]any{{"role": "assistant", "content": "[已消费的子任务结果]\n" + fmt.Sprint(payload["task"]) + "\n" + fmt.Sprint(payload["result"])}})
	case "pre_compress":
		checkpoint := session + "-checkpoint"
		if _, err := n.vikingMessages(ctx, space, checkpoint, nativeMessages(payload, true)); err != nil {
			return nil, err
		}
		value, err := n.vikingCommit(ctx, space, checkpoint)
		if err != nil {
			return nil, fmt.Errorf("OpenViking原文已接受但checkpoint commit失败，请检查会话%s后显式恢复，不能自动重发: %w", vikingSession(space, checkpoint), err)
		}
		return map[string]any{"checkpointed": true, "checkpoint_id": vikingSession(space, checkpoint), "content": "原文已保存并由OpenViking接受commit", "commit": nativeResult(value)}, nil
	case "memory_write":
		return n.vikingMirror(ctx, space, root, payload)
	case "tool":
		name, _ := payload["tool_name"].(string)
		args, _ := payload["arguments"].(map[string]any)
		switch name {
		case "viking_search":
			query, err := nativeString(args, "query")
			if err != nil {
				return nil, err
			}
			targets := []string{root + "memories", root + "resources"}
			if prefix, _ := args["uri_prefix"].(string); prefix != "" {
				if err := vikingURI(root, prefix); err != nil {
					return nil, err
				}
				targets = []string{prefix}
			}
			body := map[string]any{"query": query, "target_uri": targets, "limit": nativeInt(args, "limit", 10, 50)}
			endpoint := "/api/v1/search/find"
			if args["mode"] == "deep" {
				endpoint = "/api/v1/search/search"
				body["session_id"] = vikingSession(space, session)
			}
			return n.request(ctx, space, http.MethodPost, endpoint, nil, body)
		case "viking_rule_search":
			query, err := nativeString(args, "task_context")
			if err != nil {
				return nil, err
			}
			threshold := 0.65
			if value, ok := args["rule_probability_threshold"].(float64); ok {
				threshold = value
			}
			if threshold < 0 || threshold > 1 {
				return nil, errors.New("rule_probability_threshold必须0..1")
			}
			return n.request(ctx, space, http.MethodPost, "/api/v1/rules/search", nil, map[string]any{"task_context": query, "rule_probability_threshold": threshold})
		case "viking_read":
			uris := skillStringValues(args["uris"])
			if uri, _ := args["uri"].(string); uri != "" {
				uris = append(uris, uri)
			}
			if len(uris) == 0 || len(uris) > 3 {
				return nil, errors.New("viking_read需要1..3个URI")
			}
			level, _ := args["level"].(string)
			if level == "" {
				level = "overview"
			}
			endpoint := ""
			switch level {
			case "abstract":
				endpoint = "/api/v1/content/abstract"
			case "overview":
				endpoint = "/api/v1/content/overview"
			case "full":
				endpoint = "/api/v1/content/read"
			default:
				return nil, errors.New("viking_read level无效")
			}
			var results []any
			for _, uri := range uris {
				if err := vikingURI(root, uri); err != nil {
					return nil, err
				}
				resolved := uri
				if level != "full" && (strings.HasSuffix(uri, "/.abstract.md") || strings.HasSuffix(uri, "/.overview.md")) {
					resolved = strings.TrimSuffix(uri, "/"+path.Base(uri))
				}
				value, err := n.request(ctx, space, http.MethodGet, endpoint, url.Values{"uri": {resolved}}, nil)
				if err != nil {
					return nil, err
				}
				results = append(results, map[string]any{"uri": uri, "level": level, "result": nativeResult(value)})
			}
			return map[string]any{"results": results}, nil
		case "viking_browse":
			action, _ := args["action"].(string)
			endpoint := ""
			switch action {
			case "list":
				endpoint = "ls"
			case "tree":
				endpoint = "tree"
			case "stat":
				endpoint = "stat"
			default:
				return nil, errors.New("viking_browse action无效")
			}
			uri, _ := args["path"].(string)
			if uri == "" {
				uri = root
			}
			if err := vikingURI(root, uri); err != nil {
				return nil, err
			}
			return n.request(ctx, space, http.MethodGet, "/api/v1/fs/"+endpoint, url.Values{"uri": {uri}}, nil)
		case "viking_remember":
			content, err := nativeString(args, "content")
			if err != nil {
				return nil, err
			}
			var nonce [12]byte
			if _, err = rand.Read(nonce[:]); err != nil {
				return nil, err
			}
			sid := session + "-remember-" + hex.EncodeToString(nonce[:])
			if _, err = n.vikingMessages(ctx, space, sid, []map[string]any{{"role": "user", "content": content}}); err != nil {
				return nil, fmt.Errorf("OpenViking提交未确认，请检查会话%s，不得自动重发: %w", vikingSession(space, sid), err)
			}
			commit, err := n.vikingCommit(ctx, space, sid)
			if err != nil {
				return nil, fmt.Errorf("OpenViking会话%s原文已接受但commit失败；用同配置检查后运行ov session commit %s: %w", vikingSession(space, sid), vikingSession(space, sid), err)
			}
			return map[string]any{"status": "submitted", "session_id": vikingSession(space, sid), "commit": nativeResult(commit), "note": "来源已提交；OpenViking可能新增、合并或跳过记忆，不能声称已生成独立文件"}, nil
		case "viking_forget":
			uri, err := nativeString(args, "uri")
			if err != nil {
				return nil, err
			}
			if err := vikingURI(root+"memories/", uri); err != nil {
				return nil, err
			}
			if !strings.HasSuffix(uri, ".md") || strings.HasPrefix(path.Base(uri), ".") {
				return nil, errors.New("viking_forget只能删除确切普通.md记忆文件")
			}
			return n.request(ctx, space, http.MethodDelete, "/api/v1/fs", url.Values{"uri": {uri}, "recursive": {"false"}, "wait": {"true"}}, nil)
		case "viking_add_resource":
			return n.vikingAddResource(ctx, space, root, args)
		default:
			return nil, fmt.Errorf("未知OpenViking tool %q", name)
		}
	default:
		return nil, fmt.Errorf("OpenViking未实现operation %q", operation)
	}
}

func skillStringValues(v any) []string {
	var out []string
	switch values := v.(type) {
	case []string:
		return values
	case []any:
		for _, value := range values {
			if s, ok := value.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
func (n *NativeBackend) vikingMirror(ctx context.Context, space, root string, payload map[string]any) (any, error) {
	id := nativeInt(payload, "entry_id", 0, int(^uint(0)>>1))
	if id <= 0 {
		return nil, errors.New("OpenViking mirror缺少entry_id")
	}
	uri := fmt.Sprintf("%smemories/saber/entry-%d.md", root, id)
	action, _ := payload["action"].(string)
	if action == ActionRemove {
		return n.request(ctx, space, http.MethodDelete, "/api/v1/fs", url.Values{"uri": {uri}, "recursive": {"false"}, "wait": {"true"}}, nil)
	}
	mode := "create"
	if action == ActionReplace {
		mode = "replace"
	} else if action != ActionAdd {
		return nil, errors.New("未知OpenViking mirror action")
	}
	content, _ := payload["content"].(string)
	return n.request(ctx, space, http.MethodPost, "/api/v1/content/write", nil, map[string]any{"uri": uri, "content": content, "mode": mode, "wait": true})
}

func (n *NativeBackend) vikingAddResource(ctx context.Context, space, root string, args map[string]any) (any, error) {
	source, err := nativeString(args, "url")
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	for _, key := range []string{"reason", "instruction", "wait"} {
		if value, ok := args[key]; ok {
			body[key] = value
		}
	}
	to, _ := args["to"].(string)
	parent, _ := args["parent"].(string)
	if to != "" && parent != "" {
		return nil, errors.New("to/parent不能同时指定")
	}
	if to != "" {
		if err := vikingURI(root+"resources/", to); err != nil {
			return nil, err
		}
		body["to"] = to
	} else {
		if parent == "" {
			parent = root + "resources/"
		}
		if err := vikingURI(root+"resources/", parent); err != nil {
			return nil, err
		}
		body["parent"] = parent
	}
	parsed, err := url.Parse(source)
	if err != nil {
		return nil, err
	}
	remote := parsed.Scheme == "https" || parsed.Scheme == "http" || parsed.Scheme == "git" || parsed.Scheme == "ssh"
	var skipped []string
	if remote {
		if parsed.Hostname() == "" || parsed.User != nil {
			return nil, errors.New("远端资源URL不能含凭据")
		}
		if ip := net.ParseIP(parsed.Hostname()); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()) {
			return nil, errors.New("远端资源URL禁止私有/回环目标")
		}
		if parsed.Hostname() == "localhost" || strings.HasSuffix(parsed.Hostname(), ".localhost") {
			return nil, errors.New("远端资源URL禁止回环目标")
		}
		body["path"] = source
	} else {
		if parsed.Scheme != "" && parsed.Scheme != "file" {
			return nil, errors.New("不支持的本地资源URL")
		}
		if parsed.Scheme == "file" {
			if parsed.Host != "" && parsed.Host != "localhost" {
				return nil, errors.New("file URI必须本机")
			}
			source = parsed.Path
		}
		data, name, ignored, err := n.vikingLocalResource(ctx, source)
		if err != nil {
			return nil, err
		}
		skipped = ignored
		temp, err := n.vikingUpload(ctx, space, name, data)
		if err != nil {
			return nil, err
		}
		body["temp_file_id"] = temp
		body["source_name"] = name
	}
	value, err := n.request(ctx, space, http.MethodPost, "/api/v1/resources", nil, body)
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": nativeResult(value), "skipped_local_paths": skipped}, nil
}

func (n *NativeBackend) vikingLocalResource(ctx context.Context, source string) ([]byte, string, []string, error) {
	if n.fileReader == nil {
		return nil, "", nil, errors.New("原始文件读取未装配，请绑定Executor.ReadAuthorizedFile")
	}
	data, fileErr := n.fileReader(ctx, source, 64<<20)
	if fileErr == nil {
		return data, path.Base(strings.ReplaceAll(source, "\\", "/")), nil, nil
	}
	if n.host.Call == nil {
		return nil, "", nil, fileErr
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	count, total := 0, 0
	var skipped []string
	var walk func(string, string) error
	walk = func(current, relative string) error {
		value, err := n.host.Call(ctx, "list_files", map[string]any{"path": current})
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		var object map[string]any
		if err = json.Unmarshal(encoded, &object); err != nil {
			return err
		}
		if problem, _ := object["error"].(string); problem != "" {
			return errors.New(problem)
		}
		if code, ok := object["exit_code"].(float64); ok && code != 0 {
			return errors.New("目录读取失败")
		}
		listing, _ := object["data"].(map[string]any)
		entries, _ := listing["entries"].([]any)
		if listing == nil {
			return errors.New("list_files未返回真实目录数据")
		}
		for _, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok {
				return errors.New("目录条目无效")
			}
			name, _ := entry["name"].(string)
			if name == "" || name == ".." || name == "." || strings.ContainsAny(name, "/\\\x00") {
				return errors.New("目录条目路径越界")
			}
			rel := path.Join(relative, name)
			if strings.HasPrefix(name, ".") || entry["symlink"] == true {
				skipped = append(skipped, rel)
				continue
			}
			next := strings.TrimRight(current, "/") + "/" + name
			if entry["directory"] == true {
				if err := walk(next, rel); err != nil {
					return err
				}
				continue
			}
			bytes, err := n.fileReader(ctx, next, 64<<20-total)
			if err != nil {
				return err
			}
			count++
			total += len(bytes)
			if count > 1000 || total > 64<<20 {
				return errors.New("目录资源超过1000文件或64MiB")
			}
			file, err := writer.Create(rel)
			if err != nil {
				return err
			}
			if _, err = file.Write(bytes); err != nil {
				return err
			}
		}
		return nil
	}
	err := walk(source, "")
	err = errors.Join(err, writer.Close())
	if err != nil {
		return nil, "", nil, errors.Join(fileErr, err)
	}
	return buffer.Bytes(), path.Base(source) + ".zip", skipped, nil
}

func (n *NativeBackend) vikingUpload(ctx context.Context, space, name string, data []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", name)
	if err != nil {
		return "", err
	}
	if _, err = file.Write(data); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(n.cfg.Endpoint, "/")+"/api/v1/resources/temp_upload", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-OpenViking-Account", n.option("account", "default"))
	req.Header.Set("X-OpenViking-User", n.option("user", "default"))
	req.Header.Set("X-OpenViking-Actor-Peer", "saber-"+space)
	if key := n.key(); key != "" {
		req.Header.Set("X-API-Key", key)
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	response, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("OpenViking上传HTTP %d: %s", resp.StatusCode, scrubProviderText(string(response)))
	}
	var value any
	if err = json.Unmarshal(response, &value); err != nil {
		return "", err
	}
	result, ok := nativeResult(value).(map[string]any)
	if !ok {
		return "", errors.New("OpenViking上传未返回结果")
	}
	id, _ := result["temp_file_id"].(string)
	if id == "" {
		return "", errors.New("OpenViking上传未确认temp_file_id")
	}
	return id, nil
}
