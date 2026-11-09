package memoryproviders

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"time"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

// 协议来源：RetainDB/RetainDB packages/server/src/api/{memory,files}.ts。
// memory 的按 ID 操作只有组织级鉴权，因此删除前必须再次核对 user_id；文件按路径隔离。
type retainDBBackend struct {
	nativeLocalState
	project string
}

// NewRetainDB 接入官方 v1 云 API，同时强制个人/群作用域与文件命名空间隔离。
func NewRetainDB(cfg config.MemoryBackendConfig, host toolkit.Host) (Backend, error) {
	cfg.Type = "retaindb"
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.retaindb.com"
	}
	if cfg.APIKey == "" && cfg.APIKeyEnv == "" {
		cfg.APIKeyEnv = "RETAINDB_API_KEY"
	}
	if err := (config.MemoryBackendsConfig{Backends: map[string]config.MemoryBackendConfig{"retaindb": cfg}}).Validate(); err != nil {
		return nil, err
	}
	project := nativeLocalString(cfg.Options, "project")
	if project == "" {
		project = "saber"
	}
	return &retainDBBackend{nativeLocalState: newNativeLocalState(cfg, host), project: project}, nil
}

func (b *retainDBBackend) request(ctx context.Context, method, route string, body any) (map[string]any, error) {
	if _, err := nativeLocalCredential(b.cfg); err != nil {
		return nil, err
	}
	raw, err := HTTPJSON(ctx, b.cfg, method, route, body, nil)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, errors.New("RetainDB returned a non-object response")
	}
	if success, ok := result["success"].(bool); ok && !success {
		return nil, errors.New("RetainDB rejected the operation")
	}
	if result["error"] != nil {
		return nil, errors.New("RetainDB returned an API error")
	}
	return result, nil
}

func (b *retainDBBackend) Call(ctx context.Context, operation string, args map[string]any) (any, error) {
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
		return retainDBTools(), nil
	case "initialize", "retaindb_profile":
		return b.request(ctx, "GET", "/v1/memory/profile/"+url.PathEscape(scope)+"?"+url.Values{"project": {b.project}, "include_pending": {"true"}}.Encode(), nil)
	case "context", "prefetch", "turn_start", "retaindb_search", "retaindb_context":
		query := nativeLocalString(payload, "query")
		if query == "" {
			query = nativeLocalString(payload, "message")
		}
		if query == "" || len(query) > 5000 {
			return nil, errors.New("RetainDB query must be 1..5000 bytes")
		}
		n, err := nativeLocalInt(payload, "limit", 5, 50)
		if err != nil {
			return nil, err
		}
		result, err := b.request(ctx, "POST", "/v1/memory/search", map[string]any{"project": b.project, "user_id": scope, "query": query, "top_k": n, "scope_targets": []string{"USER"}, "include_pending": true, "include_chunks": false, "include_relations": false})
		if err != nil {
			return nil, err
		}
		if operation == "retaindb_search" {
			return result, nil
		}
		var contextText strings.Builder
		results, _ := result["results"].([]any)
		for _, item := range results {
			row, _ := item.(map[string]any)
			memory, _ := row["memory"].(map[string]any)
			content := nativeLocalString(memory, "content")
			if content != "" {
				contextText.WriteString("- " + content + "\n")
			}
		}
		return map[string]any{"context": contextText.String(), "results": result}, nil
	case "retaindb_remember", "memory_write":
		if operation == "memory_write" && nativeLocalString(payload, "action") != "add" {
			return nil, errors.New("RetainDB mirror supports add only; use scoped forget for deletion")
		}
		content := nativeLocalString(payload, "content")
		if content == "" || len(content) > 10000 {
			return nil, errors.New("RetainDB content must be 1..10000 bytes")
		}
		memoryType := nativeLocalString(payload, "memory_type")
		if memoryType == "" {
			memoryType = "factual"
		}
		allowed := map[string]bool{"factual": true, "episodic": true, "semantic": true, "procedural": true, "preference": true, "event": true, "relationship": true, "opinion": true, "goal": true, "instruction": true, "decision": true, "constraint": true, "solution": true, "project_state": true, "correction": true, "workflow": true}
		if !allowed[memoryType] {
			return nil, errors.New("invalid RetainDB memory_type")
		}
		body := map[string]any{"project": b.project, "user_id": scope, "session_id": session, "content": content, "memory_type": memoryType, "scope_target": "USER", "write_mode": "async"}
		if value, ok := payload["importance"]; ok {
			n, err := nativeLocalNumber(value)
			if err != nil || n < 0 || n > 1 {
				return nil, errors.New("importance must be 0..1")
			}
			body["importance"] = n
		}
		return b.request(ctx, "POST", "/v1/memory", body)
	case "sync_turn", "pre_compress":
		user, assistant, messages, err := nativeLocalTurn(payload)
		if err != nil {
			return nil, err
		}
		if operation == "pre_compress" {
			data, err := json.Marshal(payload["messages"])
			if err != nil {
				return nil, err
			}
			if err = json.Unmarshal(data, &messages); err != nil {
				return nil, err
			}
		}
		transcript := make([]map[string]any, 0, len(messages)+2)
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		for _, message := range messages {
			role := nativeLocalString(message, "role")
			content := nativeLocalString(message, "content")
			if content == "" {
				continue
			}
			transcript = append(transcript, map[string]any{"role": role, "content": content, "timestamp": stamp})
		}
		if len(transcript) == 0 {
			transcript = []map[string]any{{"role": "user", "content": user, "timestamp": stamp}, {"role": "assistant", "content": assistant, "timestamp": stamp}}
		}
		body := map[string]any{"project": b.project, "user_id": scope, "session_id": session, "messages": transcript, "write_mode": "async"}
		if operation == "pre_compress" {
			body["write_mode"] = "sync"
		}
		result, err := b.request(ctx, "POST", "/v1/memory/ingest/session", body)
		if err != nil {
			return nil, err
		}
		if operation == "pre_compress" {
			mode := nativeLocalString(result, "mode")
			if mode == "async" {
				return result, errors.New("RetainDB did not acknowledge a synchronous checkpoint")
			}
			id := nativeLocalString(result, "trace_id")
			if id == "" {
				return result, errors.New("RetainDB checkpoint response lacks trace_id")
			}
			return map[string]any{"checkpointed": true, "checkpoint_id": id, "result": result}, nil
		}
		return result, nil
	case "retaindb_forget":
		id := nativeLocalString(payload, "memory_id")
		if !nativeLocalResourceID(id) {
			return nil, errors.New("valid memory_id is required")
		}
		detail, err := b.request(ctx, "GET", "/v1/memory/"+url.PathEscape(id), nil)
		if err != nil {
			return nil, err
		}
		memory, _ := detail["memory"].(map[string]any)
		if nativeLocalString(memory, "user_id") != scope {
			return nil, errors.New("RetainDB memory is outside current trusted scope")
		}
		return b.request(ctx, "DELETE", "/v1/memory/"+url.PathEscape(id), nil)
	case "retaindb_upload_file":
		return b.upload(ctx, scope, payload)
	case "retaindb_list_files":
		n, err := nativeLocalInt(payload, "limit", 50, 200)
		if err != nil {
			return nil, err
		}
		prefix, err := retainDBFilePath(scope, nativeLocalString(payload, "prefix"), true)
		if err != nil {
			return nil, err
		}
		result, err := b.request(ctx, "GET", "/v1/files?"+url.Values{"prefix": {prefix}, "limit": {fmt.Sprint(n)}}.Encode(), nil)
		if err != nil {
			return nil, err
		}
		files, _ := result["files"].([]any)
		filtered := make([]any, 0, len(files))
		for _, item := range files {
			file, _ := item.(map[string]any)
			if retainDBOwnsFile(scope, file) {
				filtered = append(filtered, item)
			}
		}
		result["files"] = filtered
		return result, nil
	case "retaindb_read_file", "retaindb_ingest_file", "retaindb_delete_file":
		id := nativeLocalString(payload, "file_id")
		if !nativeLocalResourceID(id) {
			return nil, errors.New("valid file_id is required")
		}
		detail, err := b.request(ctx, "GET", "/v1/files/"+url.PathEscape(id), nil)
		if err != nil {
			return nil, err
		}
		file, _ := detail["file"].(map[string]any)
		if !retainDBOwnsFile(scope, file) {
			return nil, errors.New("RetainDB file is outside current trusted scope")
		}
		if operation == "retaindb_delete_file" {
			return b.request(ctx, "DELETE", "/v1/files/"+url.PathEscape(id)+"?hard=true", nil)
		}
		if operation == "retaindb_ingest_file" {
			return b.request(ctx, "POST", "/v1/files/"+url.PathEscape(id)+"/ingest", map[string]any{"project": b.project, "user_id": scope})
		}
		data, err := b.fileContent(ctx, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"file": file, "content_base64": base64.StdEncoding.EncodeToString(data), "size": len(data)}, nil
	case "session_end", "session_switch":
		return b.request(ctx, "GET", "/v1/memory/session/"+url.PathEscape(session)+"?"+url.Values{"project": {b.project}}.Encode(), nil)
	case "shutdown":
		return nil, b.Close()
	default:
		return nil, fmt.Errorf("RetainDB does not support operation %q", operation)
	}
}

func nativeLocalNumber(value any) (float64, error) {
	switch v := value.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case json.Number:
		return v.Float64()
	default:
		return 0, errors.New("value must be a number")
	}
}

func nativeLocalResourceID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func retainDBFilePath(scope, name string, prefix bool) (string, error) {
	root := "/saber/" + scope + "/"
	if name == "" && prefix {
		return root, nil
	}
	if strings.ContainsRune(name, 0) || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return "", errors.New("file path must be relative to scoped storage")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", errors.New("file path cannot escape scoped storage")
		}
	}
	clean := path.Clean(name)
	if clean == "." || clean == "" {
		return "", errors.New("file path is required")
	}
	return root + clean, nil
}

func retainDBOwnsFile(scope string, file map[string]any) bool {
	metadata, _ := file["metadata"].(map[string]any)
	return strings.HasPrefix(nativeLocalString(file, "path"), "/saber/"+scope+"/") && nativeLocalString(metadata, "saber_scope") == scope
}

func (b *retainDBBackend) upload(ctx context.Context, scope string, args map[string]any) (any, error) {
	name := nativeLocalString(args, "name")
	if name == "" || path.Base(name) != name || strings.ContainsAny(name, "\\\x00") {
		return nil, errors.New("upload name must be a filename")
	}
	remote, err := retainDBFilePath(scope, nativeLocalString(args, "path"), false)
	if err != nil {
		return nil, err
	}
	content := nativeLocalString(args, "content_base64")
	if len(content) > 12*1024*1024 {
		return nil, errors.New("upload exceeds 8 MiB")
	}
	data, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return nil, errors.New("upload requires valid content_base64")
	}
	if len(data) > 8*1024*1024 {
		return nil, errors.New("upload exceeds 8 MiB")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", name)
	if err != nil {
		return nil, err
	}
	if _, err = file.Write(data); err != nil {
		return nil, err
	}
	for key, value := range map[string]string{"path": remote, "scope": "USER", "is_public": "false", "metadata": `{"saber_scope":"` + scope + `"}`} {
		if err = writer.WriteField(key, value); err != nil {
			return nil, err
		}
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	resp, err := b.raw(ctx, "POST", "/v1/files", bytes.NewReader(body.Bytes()), writer.FormDataContentType())
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err = retainDBReadResponse(resp)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, errors.New("RetainDB upload returned invalid JSON")
	}
	stored, _ := result["file"].(map[string]any)
	if !retainDBOwnsFile(scope, stored) {
		return nil, errors.New("RetainDB upload did not confirm scoped file metadata")
	}
	return result, nil
}

func (b *retainDBBackend) raw(ctx context.Context, method, route string, body io.Reader, contentType string) (*http.Response, error) {
	base, err := url.Parse(b.cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	ref, err := url.Parse(route)
	if err != nil || ref.IsAbs() || ref.Host != "" {
		return nil, errors.New("invalid RetainDB route")
	}
	key, err := nativeLocalCredential(b.cfg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, base.ResolveReference(ref).String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client.Do(req)
}

func retainDBReadResponse(resp *http.Response) ([]byte, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("RetainDB returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8*1024*1024 {
		return nil, errors.New("RetainDB file response exceeds 8 MiB")
	}
	return data, nil
}

func (b *retainDBBackend) fileContent(ctx context.Context, id string) ([]byte, error) {
	resp, err := b.raw(ctx, "GET", "/v1/files/"+url.PathEscape(id)+"/content", nil, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusFound {
		defer func() { _ = resp.Body.Close() }()
		return retainDBReadResponse(resp)
	}
	location := resp.Header.Get("Location")
	_ = resp.Body.Close()
	u, err := url.Parse(location)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("RetainDB signed download must be a credential-free HTTPS URL")
	}
	// 签名存储地址来自已核对作用域的官方响应；不转发 API 凭据，解析并固定公网 IP 防止 SSRF。
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil {
		return nil, errors.New("RetainDB signed download hostname cannot be resolved")
	}
	for _, address := range addresses {
		if !nativeLocalPublicIP(address) {
			return nil, errors.New("RetainDB signed download cannot target private networks")
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("RetainDB signed download has no address")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(call context.Context, network, address string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		return (&net.Dialer{}).DialContext(call, network, net.JoinHostPort(addresses[0].String(), port))
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "GET", location, nil)
	if err != nil {
		return nil, err
	}
	resp, err = client.Do(req)
	if err != nil {
		return nil, errors.New("RetainDB signed download failed")
	}
	defer func() { _ = resp.Body.Close() }()
	return retainDBReadResponse(resp)
}

func nativeLocalPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return false
		}
	}
	return true
}

func retainDBTools() []map[string]any {
	query := map[string]any{"query": nativeLocalField("string", "Task-relevant search query"), "limit": nativeLocalField("integer", "Maximum results, 1..50")}
	fileID := map[string]any{"file_id": nativeLocalField("string", "File ID previously returned within the current scope")}
	return []map[string]any{
		nativeLocalTool("retaindb_profile", "Read current scoped user profile", map[string]any{}),
		nativeLocalTool("retaindb_search", "Search current scoped memories", query, "query"),
		nativeLocalTool("retaindb_context", "Get task-relevant scoped context", query, "query"),
		nativeLocalTool("retaindb_remember", "Store a typed scoped memory", map[string]any{"content": nativeLocalField("string", "Durable memory"), "memory_type": nativeLocalField("string", "Official memory type"), "importance": nativeLocalField("number", "Importance 0..1")}, "content"),
		nativeLocalTool("retaindb_forget", "Delete a memory after checking current scope", map[string]any{"memory_id": nativeLocalField("string", "Memory ID")}, "memory_id"),
		nativeLocalTool("retaindb_upload_file", "Upload bounded bytes to current scoped storage", map[string]any{"name": nativeLocalField("string", "Filename"), "path": nativeLocalField("string", "Relative storage path"), "content_base64": nativeLocalField("string", "File bytes encoded as base64; maximum 8 MiB")}, "name", "path", "content_base64"),
		nativeLocalTool("retaindb_list_files", "List current scoped files", map[string]any{"prefix": nativeLocalField("string", "Relative prefix"), "limit": nativeLocalField("integer", "Maximum files, 1..200")}),
		nativeLocalTool("retaindb_read_file", "Read actual file bytes from scoped storage", fileID, "file_id"),
		nativeLocalTool("retaindb_ingest_file", "Extract memories from a scoped file", fileID, "file_id"),
		nativeLocalTool("retaindb_delete_file", "Permanently delete a scoped file", fileID, "file_id"),
	}
}
