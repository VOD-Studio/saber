package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "test-server-token"

// syncBuffer 是并发安全的 stderr 缓冲，slog 与测试断言可能同时访问。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write 实现 io.Writer。
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String 返回已写入内容。
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeState 记录假上游收到的消息，并控制响应形态。
type fakeState struct {
	mu       sync.Mutex
	requests [][]chatMessage
	status   int
	block    bool
}

// record 保存一次请求的消息快照。
func (s *fakeState) record(messages []chatMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, append([]chatMessage(nil), messages...))
}

// last 返回最近一次请求的消息。
func (s *fakeState) last() []chatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return nil
	}
	return s.requests[len(s.requests)-1]
}

// requestCount 返回已记录的请求数。
func (s *fakeState) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// newFakeServer 创建实现 /v1/models 与 /v1/chat/completions 的假上游。
// status 非零时聊天接口返回该状态；block 为真时写出一段文本后等待客户端断开。
func newFakeServer(t *testing.T, status int, block bool) (*httptest.Server, *fakeState) {
	t.Helper()
	state := &fakeState{status: status, block: block}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []chatMessage `json:"messages"`
			Stream   bool          `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		state.record(body.Messages)
		if state.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(state.status)
			_, _ = io.WriteString(w, `{"error":{"message":"需要 Saber 服务端令牌","type":"invalid_request_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		writeTestChunk(w, flusher, "你")
		writeTestChunk(w, flusher, "好")
		if state.block {
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, state
}

// writeTestChunk 写出一段带内容的 SSE 数据块。
func writeTestChunk(w io.Writer, flusher http.Flusher, text string) {
	payload, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}},
	})
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	if flusher != nil {
		flusher.Flush()
	}
}

// writeTestConfig 写入最小配置与 0600 令牌文件，返回配置路径。
func writeTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  listen: 127.0.0.1:8320\n  token_file: .saber-token\n"), 0o600); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".saber-token"), []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatalf("写入令牌失败: %v", err)
	}
	return cfgPath
}

// harness 用管道驱动 run，并逐行读取输出。
type harness struct {
	in     *io.PipeWriter
	lines  chan []byte
	stderr *syncBuffer
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu            sync.Mutex
	runErr        error
	notifications []map[string]any
	pending       map[int]map[string]any
	nextID        int
}

// startHarness 启动 acp 并等待其开始读取 stdin。
func startHarness(t *testing.T, serverURL string) *harness {
	t.Helper()
	cfgPath := writeTestConfig(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	stderr := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())

	h := &harness{
		in:      inW,
		lines:   make(chan []byte, 128),
		stderr:  stderr,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		pending: make(map[int]map[string]any),
	}
	go func() {
		err := run(ctx, []string{"-c", cfgPath, "--server", serverURL}, inR, outW, stderr)
		h.mu.Lock()
		h.runErr = err
		h.mu.Unlock()
		close(h.done)
		_ = outW.Close()
	}()
	go func() {
		reader := bufio.NewReader(outR)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				h.lines <- line
			}
			if err != nil {
				close(h.lines)
				return
			}
		}
	}()
	t.Cleanup(h.stop)
	return h
}

// stop 取消进程并等待退出，避免测试残留 goroutine。
func (h *harness) stop() {
	h.cancel()
	_ = h.in.Close()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
	}
}

// wait 关闭 stdin 并等待 acp 自然退出，返回退出错误。
func (h *harness) wait(t *testing.T) error {
	t.Helper()
	_ = h.in.Close()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("等待 acp 退出超时")
	}
	h.cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runErr
}

// send 写出一个 JSON 值。
func (h *harness) send(t *testing.T, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("编码请求失败: %v", err)
	}
	data = append(data, '\n')
	if _, err := h.in.Write(data); err != nil {
		t.Fatalf("写入 stdin 失败: %v", err)
	}
}

// sendRequest 发送带自增 id 的请求并返回 id。
func (h *harness) sendRequest(t *testing.T, method string, params any) int {
	t.Helper()
	h.nextID++
	message := map[string]any{"jsonrpc": "2.0", "id": h.nextID, "method": method}
	if params != nil {
		message["params"] = params
	}
	h.send(t, message)
	return h.nextID
}

// call 发送请求并等待对应响应。
func (h *harness) call(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	return h.await(t, h.sendRequest(t, method, params))
}

// nextLine 读取一行解析后的消息。
func (h *harness) nextLine(t *testing.T) map[string]any {
	t.Helper()
	select {
	case line, ok := <-h.lines:
		if !ok {
			t.Fatal("ACP 输出已关闭")
		}
		var message map[string]any
		if err := json.Unmarshal(line, &message); err != nil {
			t.Fatalf("输出不是合法 JSON: %v (%s)", err, line)
		}
		return message
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ACP 输出超时")
		return nil
	}
}

// await 等待指定 id 的响应，其他消息依次记录。
func (h *harness) await(t *testing.T, id int) map[string]any {
	t.Helper()
	if message, ok := h.pending[id]; ok {
		delete(h.pending, id)
		return message
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-h.lines:
			if !ok {
				t.Fatal("ACP 输出已关闭")
			}
			var message map[string]any
			if err := json.Unmarshal(line, &message); err != nil {
				t.Fatalf("输出不是合法 JSON: %v (%s)", err, line)
			}
			if got, ok := message["id"].(float64); ok {
				if int(got) == id {
					return message
				}
				h.pending[int(got)] = message
				continue
			}
			if _, hasID := message["id"]; hasID {
				continue
			}
			h.notifications = append(h.notifications, message)
		case <-deadline:
			t.Fatalf("等待响应 id=%d 超时", id)
			return nil
		}
	}
}

// nextNotification 等待下一条通知，响应暂存供 await 使用。
func (h *harness) nextNotification(t *testing.T) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-h.lines:
			if !ok {
				t.Fatal("ACP 输出已关闭")
			}
			var message map[string]any
			if err := json.Unmarshal(line, &message); err != nil {
				t.Fatalf("输出不是合法 JSON: %v (%s)", err, line)
			}
			if got, ok := message["id"].(float64); ok {
				h.pending[int(got)] = message
				continue
			}
			if _, hasID := message["id"]; hasID {
				continue
			}
			h.notifications = append(h.notifications, message)
			return message
		case <-deadline:
			t.Fatal("等待通知超时")
			return nil
		}
	}
}

// newSession 创建会话并返回 sessionId。
func (h *harness) newSession(t *testing.T) string {
	t.Helper()
	resp := h.call(t, "session/new", map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("session/new 响应缺少 result: %v", resp)
	}
	id, _ := result["sessionId"].(string)
	if id == "" {
		t.Fatalf("session/new 未返回 sessionId: %v", resp)
	}
	return id
}

// prompt 发起一次提示并返回请求 id 与响应。
func (h *harness) prompt(t *testing.T, sessionID, text string) (int, map[string]any) {
	t.Helper()
	id := h.sendRequest(t, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": text}},
	})
	return id, h.await(t, id)
}

// chunkTexts 提取当前记录的通知中的文本增量。
func (h *harness) chunkTexts(t *testing.T) []string {
	t.Helper()
	texts := make([]string, 0, len(h.notifications))
	for _, notification := range h.notifications {
		if notification["method"] != "session/update" {
			continue
		}
		params, _ := notification["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		content, _ := update["content"].(map[string]any)
		text, _ := content["text"].(string)
		if text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

// resultOf 取出响应中的 result 对象。
func resultOf(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	if rpcErr, ok := resp["error"]; ok && rpcErr != nil {
		t.Fatalf("响应包含错误: %v", rpcErr)
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 result 对象: %v", resp)
	}
	return result
}

// errorOf 取出响应中的错误码与消息。
func errorOf(t *testing.T, resp map[string]any) (int, string) {
	t.Helper()
	rpcErr, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 error: %v", resp)
	}
	code, _ := rpcErr["code"].(float64)
	message, _ := rpcErr["message"].(string)
	return int(code), message
}

func TestRun_Initialize(t *testing.T) {
	server, _ := newFakeServer(t, 0, false)
	h := startHarness(t, server.URL)

	result := resultOf(t, h.call(t, "initialize", map[string]any{"protocolVersion": 1}))
	if got := result["protocolVersion"]; got != float64(1) {
		t.Fatalf("protocolVersion = %v，期望 1", got)
	}
	caps, ok := result["agentCapabilities"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 agentCapabilities: %v", result)
	}
	if caps["loadSession"] != false {
		t.Fatalf("loadSession = %v，期望 false", caps["loadSession"])
	}
	promptCaps, ok := caps["promptCapabilities"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 promptCapabilities: %v", caps)
	}
	for _, key := range []string{"image", "audio", "embeddedContext"} {
		if _, ok := promptCaps[key]; !ok {
			t.Fatalf("promptCapabilities 缺少 %s: %v", key, promptCaps)
		}
	}
	authMethods, ok := result["authMethods"].([]any)
	if !ok || len(authMethods) != 0 {
		t.Fatalf("authMethods = %v，期望空数组", result["authMethods"])
	}
}

func TestRun_SessionNew(t *testing.T) {
	server, _ := newFakeServer(t, 0, false)
	h := startHarness(t, server.URL)

	first := h.newSession(t)
	second := h.newSession(t)
	if first == second {
		t.Fatalf("两次生成的 sessionId 相同: %s", first)
	}
	if len(first) != 64 {
		t.Fatalf("sessionId 长度 = %d，期望 64: %s", len(first), first)
	}

	resp := h.call(t, "session/new", map[string]any{"cwd": "/tmp", "mcpServers": []any{map[string]any{"name": "demo"}}})
	if id := resultOf(t, resp)["sessionId"]; id == "" {
		t.Fatalf("带 mcpServers 的 session/new 未返回 sessionId: %v", resp)
	}
	if !strings.Contains(h.stderr.String(), "mcp_servers") {
		t.Fatalf("未记录被忽略的 mcpServers: %s", h.stderr.String())
	}
}

func TestRun_PromptStreamsText(t *testing.T) {
	server, state := newFakeServer(t, 0, false)
	h := startHarness(t, server.URL)
	sessionID := h.newSession(t)

	_, resp := h.prompt(t, sessionID, "你好")
	if stop := resultOf(t, resp)["stopReason"]; stop != "end_turn" {
		t.Fatalf("stopReason = %v，期望 end_turn", stop)
	}
	texts := h.chunkTexts(t)
	if strings.Join(texts, "") != "你好" || len(texts) != 2 {
		t.Fatalf("文本增量 = %v，期望 [你 好]", texts)
	}
	for _, notification := range h.notifications {
		params, _ := notification["params"].(map[string]any)
		if params["sessionId"] != sessionID {
			t.Fatalf("session/update 的 sessionId = %v，期望 %s", params["sessionId"], sessionID)
		}
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] != "agent_message_chunk" {
			t.Fatalf("sessionUpdate = %v", update["sessionUpdate"])
		}
		content, _ := update["content"].(map[string]any)
		if content["type"] != "text" {
			t.Fatalf("content.type = %v", content["type"])
		}
	}

	messages := state.last()
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "你好" {
		t.Fatalf("上游消息 = %+v，期望单条用户消息", messages)
	}
}

func TestRun_PromptKeepsHistory(t *testing.T) {
	server, state := newFakeServer(t, 0, false)
	h := startHarness(t, server.URL)
	sessionID := h.newSession(t)

	if _, resp := h.prompt(t, sessionID, "第一问"); resultOf(t, resp)["stopReason"] != "end_turn" {
		t.Fatalf("第一轮未正常结束: %v", resp)
	}
	if _, resp := h.prompt(t, sessionID, "第二问"); resultOf(t, resp)["stopReason"] != "end_turn" {
		t.Fatalf("第二轮未正常结束: %v", resp)
	}
	if state.requestCount() != 2 {
		t.Fatalf("上游请求次数 = %d，期望 2", state.requestCount())
	}
	messages := state.last()
	want := []chatMessage{
		{Role: "user", Content: "第一问"},
		{Role: "assistant", Content: "你好"},
		{Role: "user", Content: "第二问"},
	}
	if len(messages) != len(want) {
		t.Fatalf("第二轮消息 = %+v，期望 %+v", messages, want)
	}
	for i := range want {
		if messages[i] != want[i] {
			t.Fatalf("第二轮消息[%d] = %+v，期望 %+v", i, messages[i], want[i])
		}
	}
}

func TestRun_CancelPrompt(t *testing.T) {
	server, _ := newFakeServer(t, 0, true)
	h := startHarness(t, server.URL)
	sessionID := h.newSession(t)

	promptID := h.sendRequest(t, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": "慢慢说"}},
	})
	notification := h.nextNotification(t)
	params, _ := notification["params"].(map[string]any)
	if params["sessionId"] != sessionID {
		t.Fatalf("取消前的通知 sessionId = %v", params["sessionId"])
	}

	cancelResp := h.call(t, "session/cancel", map[string]any{"sessionId": sessionID})
	if result := resultOf(t, cancelResp); len(result) != 0 {
		t.Fatalf("session/cancel 结果 = %v，期望空对象", result)
	}

	promptResp := h.await(t, promptID)
	if stop := resultOf(t, promptResp)["stopReason"]; stop != "cancelled" {
		t.Fatalf("取消后 stopReason = %v，期望 cancelled", stop)
	}
}

func TestRun_ProtocolErrors(t *testing.T) {
	server, _ := newFakeServer(t, 0, false)
	h := startHarness(t, server.URL)
	sessionID := h.newSession(t)

	tests := []struct {
		name      string
		payload   any
		code      int
		requireID bool
	}{
		{name: "无效 JSON", payload: `{"jsonrpc":"2.0","id":`, code: codeParseError},
		{name: "非对象请求", payload: `[]`, code: codeInvalidRequest},
		{name: "未知方法", payload: map[string]any{"jsonrpc": "2.0", "id": 900, "method": "session/fork"}, code: codeMethodNotFound, requireID: true},
		{name: "无效参数", payload: map[string]any{
			"jsonrpc": "2.0", "id": 901, "method": "session/prompt",
			"params": map[string]any{"sessionId": sessionID, "prompt": []any{map[string]any{"type": "image", "url": "x"}}},
		}, code: codeInvalidParams, requireID: true},
		{name: "会话不存在", payload: map[string]any{
			"jsonrpc": "2.0", "id": 902, "method": "session/prompt",
			"params": map[string]any{"sessionId": "missing", "prompt": []any{map[string]any{"type": "text", "text": "hi"}}},
		}, code: codeInvalidParams, requireID: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			switch value := tc.payload.(type) {
			case string:
				if _, err := h.in.Write([]byte(value + "\n")); err != nil {
					t.Fatalf("写入 stdin 失败: %v", err)
				}
			default:
				h.send(t, value)
			}
			message := h.nextLine(t)
			code, messageText := errorOf(t, message)
			if code != tc.code {
				t.Fatalf("错误码 = %d，期望 %d（%s）", code, tc.code, messageText)
			}
			if tc.requireID {
				if _, ok := message["id"].(float64); !ok {
					t.Fatalf("响应缺少 id: %v", message)
				}
			} else if id, ok := message["id"]; !ok || id != nil {
				t.Fatalf("解析错误响应的 id 应为 null: %v", message)
			}
		})
	}
}

func TestRun_NotificationHasNoResponse(t *testing.T) {
	server, _ := newFakeServer(t, 0, false)
	h := startHarness(t, server.URL)

	h.send(t, map[string]any{"jsonrpc": "2.0", "method": "initialize", "params": map[string]any{}})
	result := resultOf(t, h.call(t, "initialize", map[string]any{}))
	if result["protocolVersion"] != float64(1) {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	if len(h.notifications) != 0 {
		t.Fatalf("通知不应产生响应，实际收到: %v", h.notifications)
	}
}

func TestRun_UpstreamAuthFailure(t *testing.T) {
	server, _ := newFakeServer(t, http.StatusUnauthorized, false)
	h := startHarness(t, server.URL)
	sessionID := h.newSession(t)

	_, resp := h.prompt(t, sessionID, "你好")
	code, message := errorOf(t, resp)
	if code != codeUnauthorized {
		t.Fatalf("错误码 = %d，期望 %d", code, codeUnauthorized)
	}
	if strings.Contains(message, testToken) {
		t.Fatalf("错误信息泄露了令牌: %s", message)
	}
}

func TestRun_MissingToken(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server:\n  listen: 127.0.0.1:8320\n  token_file: .saber-token\n"), 0o600); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"-c", cfgPath}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("令牌缺失时应返回错误")
	}
	if !strings.Contains(stderr.String(), "令牌") {
		t.Fatalf("stderr 未给出令牌提示: %s", stderr.String())
	}
}

func TestRun_UnreachableServer(t *testing.T) {
	cfgPath := writeTestConfig(t)
	server := httptest.NewServer(http.NotFoundHandler())
	serverURL := server.URL
	server.Close()

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"-c", cfgPath, "--server", serverURL}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("服务不可达时应返回错误")
	}
	if !strings.Contains(stderr.String(), "无法连接") {
		t.Fatalf("stderr 未给出行连接提示: %s", stderr.String())
	}
}

func TestRun_ServerRejectsToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "需要 Saber 服务端令牌", http.StatusUnauthorized)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfgPath := writeTestConfig(t)
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"-c", cfgPath, "--server", server.URL}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("令牌被拒时应返回错误")
	}
	if strings.Contains(stderr.String(), testToken) {
		t.Fatalf("stderr 泄露了令牌: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "令牌") {
		t.Fatalf("stderr 未给出令牌提示: %s", stderr.String())
	}
}

func TestRun_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"--help"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("--help 不应返回错误: %v", err)
	}
	if !strings.Contains(stdout.String(), "用法: saber acp") {
		t.Fatalf("stdout 未输出用法: %s", stdout.String())
	}
}

func TestRun_StdinEOFExits(t *testing.T) {
	server, _ := newFakeServer(t, 0, false)
	h := startHarness(t, server.URL)
	if err := h.wait(t); err != nil {
		t.Fatalf("stdin EOF 后应正常退出，实际: %v", err)
	}
}
