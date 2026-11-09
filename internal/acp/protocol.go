package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

const (
	// jsonrpcVersion 是本实现唯一接受的 JSON-RPC 版本。
	jsonrpcVersion = "2.0"
	// protocolVersion 是 ACP 初始化响应的协议版本。
	protocolVersion = 1
)

// JSON-RPC 2.0 错误码；-32000 起为应用自定义。
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	codeUpstreamError  = -32000
	codeUnauthorized   = -32001
)

// rpcRequest 是单行 JSON-RPC 请求；ID 为空表示通知。
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcError 是 JSON-RPC 错误对象，不含令牌等敏感信息。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcResponse 是 JSON-RPC 响应；ID 为 null 时用于无法识别请求的解析错误。
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcNotification 是无需响应的 JSON-RPC 通知。
type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// promptCapabilities 声明本 Agent 接受的提示内容类型。
type promptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

// agentCapabilities 声明可从服务端持久历史恢复会话。
type agentCapabilities struct {
	LoadSession        bool               `json:"loadSession"`
	PromptCapabilities promptCapabilities `json:"promptCapabilities"`
}

// initializeResult 是 initialize 的响应；authMethods 为空表示令牌由外部配置提供。
type initializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities agentCapabilities `json:"agentCapabilities"`
	AuthMethods       []any             `json:"authMethods"`
}

// sessionUpdateParams 是 session/update 通知的参数。
type sessionUpdateParams struct {
	SessionID string       `json:"sessionId"`
	Update    sessionDelta `json:"update"`
}

// sessionDelta 描述一次增量更新；当前只有文本消息块。
type sessionDelta struct {
	SessionUpdate string       `json:"sessionUpdate"`
	Content       contentBlock `json:"content"`
}

// contentBlock 包含文本、图片和编辑器提供的嵌入文本资源。
type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Resource *struct {
		URI  string `json:"uri"`
		Text string `json:"text"`
	} `json:"resource,omitempty"`
}

// newSessionResult 是 session/new 的响应。
type newSessionResult struct {
	SessionID string `json:"sessionId"`
}

// promptResult 是 session/prompt 的响应。
type promptResult struct {
	StopReason string `json:"stopReason"`
}

// conn 维护一条 ACP stdio 连接及其内存会话。
type conn struct {
	ctx    context.Context
	cancel context.CancelFunc
	in     io.Reader
	out    *bufio.Writer
	client *chatClient
	log    *slog.Logger

	sessions *sessionStore

	writeMu         sync.Mutex
	failMu          sync.Mutex
	failure         error
	requestMu       sync.Mutex
	requestSequence uint64
	pendingRequests map[string]chan clientReply
}

// newConn 包装输入输出并准备内存会话表。
func newConn(ctx context.Context, client *chatClient, in io.Reader, out io.Writer, logger *slog.Logger) *conn {
	ctx, cancel := context.WithCancel(ctx)
	return &conn{
		ctx:      ctx,
		cancel:   cancel,
		in:       in,
		out:      bufio.NewWriterSize(out, 16*1024),
		client:   client,
		log:      logger,
		sessions: newSessionStore(),
	}
}

// serve 逐行读取请求；提示请求在独立 goroutine 中运行，以便同一连接内的取消
// 通知能立即生效。stdin 结束时会等待在途请求完成，上下文取消则中止它们。
func (c *conn) serve() error {
	lines := make(chan []byte, 16)
	readErr := make(chan error, 1)
	go c.readLines(lines, readErr)

	var wg sync.WaitGroup
	for {
		select {
		case <-c.ctx.Done():
			wg.Wait()
			return c.firstFailure()
		case line := <-lines:
			c.dispatch(line, &wg)
			if err := c.firstFailure(); err != nil {
				c.cancel()
				wg.Wait()
				return err
			}
		case err := <-readErr:
			wg.Wait()
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("读取 stdin 失败: %w", err)
			}
			return c.firstFailure()
		}
	}
}

// readLines 把非空行送入处理循环；上下文取消后停止发送。
func (c *conn) readLines(lines chan<- []byte, failed chan<- error) {
	reader := bufio.NewReaderSize(c.in, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			select {
			case lines <- line:
			case <-c.ctx.Done():
				return
			}
		}
		if err != nil {
			failed <- err
			return
		}
	}
}

// dispatch 解析一行并交给对应方法；session/prompt 异步执行。
func (c *conn) dispatch(line []byte, wg *sync.WaitGroup) {
	line = bytes.TrimSpace(line)
	var raw json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		c.writeError(nil, codeParseError, "无法解析 JSON")
		return
	}
	if len(raw) == 0 || raw[0] != '{' {
		c.writeError(nil, codeInvalidRequest, "请求必须是单个 JSON 对象")
		return
	}
	if c.acceptClientReply(raw) {
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		c.writeError(nil, codeInvalidRequest, "请求格式无效")
		return
	}
	if req.JSONRPC != jsonrpcVersion {
		c.writeError(req.ID, codeInvalidRequest, "仅支持 JSON-RPC 2.0")
		return
	}
	if req.Method == "" {
		c.writeError(req.ID, codeInvalidRequest, "缺少 method")
		return
	}
	notification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		c.reply(req.ID, initialize(), nil, notification)
	case "session/new":
		result, rerr := c.newSession(req.Params)
		c.reply(req.ID, result, rerr, notification)
	case "session/load":
		result, rerr := c.loadSession(req.Params)
		c.reply(req.ID, result, rerr, notification)
	case "session/prompt":
		wg.Add(1)
		go func() {
			defer wg.Done()
			var params promptParams
			if rerr := decodeParams(req.Params, &params); rerr != nil {
				c.reply(req.ID, nil, rerr, notification)
				return
			}
			result, rerr := c.prompt(c.ctx, params)
			c.reply(req.ID, result, rerr, notification)
		}()
	case "session/cancel":
		result, rerr := c.cancelSession(req.Params)
		c.reply(req.ID, result, rerr, notification)
	default:
		c.reply(req.ID, nil, &rpcError{Code: codeMethodNotFound, Message: "未知方法: " + req.Method}, notification)
	}
}

// initialize 构造协议能力响应；本版本不支持认证流程和图片输入。
func initialize() initializeResult {
	return initializeResult{
		ProtocolVersion: protocolVersion,
		AgentCapabilities: agentCapabilities{
			LoadSession:        true,
			PromptCapabilities: promptCapabilities{Image: true, Audio: false, EmbeddedContext: true},
		},
		AuthMethods: []any{},
	}
}

// cancelParams 是 session/cancel 的参数。
type cancelParams struct {
	SessionID string `json:"sessionId"`
}

// cancelSession 取消会话中在途的 HTTP 请求；没有在途请求时也算成功。
func (c *conn) cancelSession(raw json.RawMessage) (any, *rpcError) {
	var params cancelParams
	if rerr := decodeParams(raw, &params); rerr != nil {
		return nil, rerr
	}
	s, ok := c.sessions.get(params.SessionID)
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: "会话不存在: " + params.SessionID}
	}
	s.cancelPrompt()
	return struct{}{}, nil
}

// decodeParams 解析方法参数；缺省参数按空对象处理。
func decodeParams(raw json.RawMessage, out any) *rpcError {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &rpcError{Code: codeInvalidParams, Message: "参数无效: " + err.Error()}
	}
	return nil
}

// reply 按请求类型写出结果或错误；通知不产生响应。
func (c *conn) reply(id json.RawMessage, result any, rerr *rpcError, notification bool) {
	if notification {
		return
	}
	if rerr != nil {
		c.writeError(id, rerr.Code, rerr.Message)
		return
	}
	c.writeResult(id, result)
}

// writeResult 写出成功响应；空结果编码为 {}。
func (c *conn) writeResult(id json.RawMessage, result any) {
	if result == nil {
		result = struct{}{}
	}
	c.write(rpcResponse{JSONRPC: jsonrpcVersion, ID: id, Result: result})
}

// writeError 写出错误响应；id 为空时编码为 null。
func (c *conn) writeError(id json.RawMessage, code int, message string) {
	c.write(rpcResponse{JSONRPC: jsonrpcVersion, ID: id, Error: &rpcError{Code: code, Message: message}})
}

// notifyChunk 转发一段助手文本；写入失败会终止连接。
func (c *conn) notifyChunk(sessionID, text string) error {
	c.write(rpcNotification{
		JSONRPC: jsonrpcVersion,
		Method:  "session/update",
		Params: sessionUpdateParams{
			SessionID: sessionID,
			Update: sessionDelta{
				SessionUpdate: "agent_message_chunk",
				Content:       contentBlock{Type: "text", Text: text},
			},
		},
	})
	return c.firstFailure()
}

// write 串行写出一行 JSON 并刷新缓冲区；失败时取消整条连接。
func (c *conn) write(value any) {
	data, err := json.Marshal(value)
	if err != nil {
		c.fail(fmt.Errorf("编码响应失败: %w", err))
		return
	}
	data = append(data, '\n')

	c.writeMu.Lock()
	_, err = c.out.Write(data)
	if err == nil {
		err = c.out.Flush()
	}
	c.writeMu.Unlock()
	if err != nil {
		c.fail(fmt.Errorf("写入 stdout 失败: %w", err))
	}
}

// fail 记录首个连接级错误并取消在途请求。
func (c *conn) fail(err error) {
	c.failMu.Lock()
	if c.failure == nil {
		c.failure = err
	}
	c.failMu.Unlock()
	c.cancel()
}

// firstFailure 返回首个连接级错误。
func (c *conn) firstFailure() error {
	c.failMu.Lock()
	defer c.failMu.Unlock()
	return c.failure
}
