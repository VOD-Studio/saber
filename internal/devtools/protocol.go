package devtools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// Process 是经过执行器授权且处于实际任务后端中的双向进程；Close 必须终止并回收进程。
type Process interface {
	io.Reader
	io.Writer
	CloseWrite() error
	Close() error
	Wait() error
}

// Backend 启动经过身份、任务、exec 和 read_file 权限检查的后端进程，禁止隐式宿主回退。
type Backend interface {
	StartLanguageServer(context.Context, string, []string, map[string]string) (Process, error)
}

// Position 与 LSP 一致，行列从零开始，character 是 UTF-16 代码单元偏移。
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range 表示语言服务返回的源文件区间。
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Diagnostic 保留真实语言服务诊断及其来源。
type Diagnostic struct {
	File     string `json:"file,omitempty"`
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Code     any    `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
	Tags     []int  `json:"tags,omitempty"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type diagnosticEvent struct {
	version     int
	diagnostics []Diagnostic
}
type rpcClient struct {
	process       Process
	writeMu       sync.Mutex
	mu            sync.Mutex
	next          int64
	pending       map[string]chan rpcMessage
	push          map[string]diagnosticEvent
	changed       chan struct{}
	done          chan struct{}
	failure       error
	pull          bool
	workspacePull bool
	serverID      string
	settings      map[string]any
	versions      map[string]int
	texts         map[string]string
	opMu          sync.Mutex
	closeOnce     sync.Once
	closeErr      error
	cold          bool
}

func newRPCClient(p Process, id string, settings map[string]any) *rpcClient {
	c := &rpcClient{process: p, pending: make(map[string]chan rpcMessage), push: make(map[string]diagnosticEvent), changed: make(chan struct{}), done: make(chan struct{}), serverID: id, settings: settings, versions: make(map[string]int), texts: make(map[string]string)}
	go c.readLoop()
	return c
}
func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: strings.ReplaceAll(path, "\\", "/")}).String()
}
func (c *rpcClient) path(path string) string {
	if mapper, ok := c.process.(interface{ BackendPath(string) string }); ok {
		return mapper.BackendPath(path)
	}
	return path
}
func (c *rpcClient) send(message any) error {
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err = fmt.Fprintf(c.process, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	for len(body) > 0 {
		n, e := c.process.Write(body)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		body = body[n:]
	}
	return nil
}
func (c *rpcClient) notify(method string, params any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}
func (c *rpcClient) request(ctx context.Context, method string, params any, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stopWatch := c.watchContext(ctx)
	defer stopWatch()
	c.mu.Lock()
	c.next++
	id := c.next
	key := strconv.FormatInt(id, 10)
	ch := make(chan rpcMessage, 1)
	c.pending[key] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, key); c.mu.Unlock() }()
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return errors.Join(ctx.Err(), err)
	}
	select {
	case <-ctx.Done():
		return errors.Join(ctx.Err(), c.notify("$/cancelRequest", map[string]any{"id": id}))
	case <-c.done:
		c.mu.Lock()
		err := c.failure
		c.mu.Unlock()
		return errors.Join(ctx.Err(), err)
	case reply := <-ch:
		if reply.Error != nil {
			return fmt.Errorf("LSP %s (%d): %s", method, reply.Error.Code, reply.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(reply.Result, result)
		}
		return nil
	}
}
func (c *rpcClient) readLoop() {
	reader := bufio.NewReaderSize(c.process, 8192)
	var failure error
	defer func() {
		if failure == nil {
			failure = io.EOF
		}
		c.mu.Lock()
		c.failure = failure
		close(c.done)
		close(c.changed)
		c.mu.Unlock()
	}()
	for {
		length := -1
		for {
			lineBytes, err := reader.ReadSlice('\n')
			if err != nil {
				failure = err
				return
			}
			line := string(lineBytes)
			if line == "\r\n" || line == "\n" {
				break
			}
			key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
			if ok && strings.EqualFold(key, "Content-Length") {
				length, err = strconv.Atoi(strings.TrimSpace(value))
				if err != nil {
					failure = err
					return
				}
			}
		}
		if length < 0 || length > 8<<20 {
			failure = errors.New("invalid LSP Content-Length")
			return
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(reader, body); err != nil {
			failure = err
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			failure = err
			return
		}
		if msg.JSONRPC != "2.0" {
			failure = errors.New("invalid language server JSON-RPC version")
			return
		}
		if msg.Method != "" {
			if len(msg.ID) > 0 {
				if err := c.answer(msg); err != nil {
					failure = err
					return
				}
			} else if msg.Method == "textDocument/publishDiagnostics" {
				var p struct {
					URI         string       `json:"uri"`
					Version     *int         `json:"version"`
					Diagnostics []Diagnostic `json:"diagnostics"`
				}
				if json.Unmarshal(msg.Params, &p) == nil {
					c.mu.Lock()
					version := -1
					if p.Version != nil {
						version = *p.Version
					}
					c.push[p.URI] = diagnosticEvent{version, p.Diagnostics}
					close(c.changed)
					c.changed = make(chan struct{})
					c.mu.Unlock()
				}
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[string(msg.ID)]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- msg:
			default:
			}
		}
	}
}
func (c *rpcClient) answer(msg rpcMessage) error {
	var result any
	switch msg.Method {
	case "workspace/configuration":
		var p struct {
			Items []struct {
				Section string `json:"section"`
			} `json:"items"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		items := make([]any, len(p.Items))
		for i, item := range p.Items {
			var value any = c.settings
			for _, part := range strings.Split(item.Section, ".") {
				if part == "" {
					continue
				}
				object, ok := value.(map[string]any)
				if !ok {
					value = nil
					break
				}
				value = object[part]
			}
			items[i] = value
		}
		result = items
	case "client/registerCapability", "client/unregisterCapability", "window/workDoneProgress/create":
		result = nil
	case "workspace/workspaceFolders":
		result = nil
	case "workspace/applyEdit":
		result = map[string]any{"applied": false, "failureReason": "language servers cannot mutate authorized files"}
	default:
		return c.send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32601, "message": "unsupported client method"}})
	}
	return c.send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
}
func (c *rpcClient) initialize(ctx context.Context, root string, options map[string]any) error {
	var reply struct {
		Capabilities struct {
			DiagnosticProvider json.RawMessage `json:"diagnosticProvider"`
		} `json:"capabilities"`
	}
	uri := fileURI(c.path(root))
	if err := c.request(ctx, "initialize", map[string]any{"processId": nil, "rootUri": uri, "workspaceFolders": []any{map[string]any{"uri": uri, "name": root}}, "clientInfo": map[string]any{"name": "Saber", "version": "1"}, "capabilities": map[string]any{"workspace": map[string]any{"configuration": true, "applyEdit": false}, "textDocument": map[string]any{"publishDiagnostics": map[string]any{"versionSupport": true}, "diagnostic": map[string]any{"dynamicRegistration": false}}}, "initializationOptions": options}, &reply); err != nil {
		return err
	}
	c.pull = len(reply.Capabilities.DiagnosticProvider) > 0 && string(reply.Capabilities.DiagnosticProvider) != "null" && string(reply.Capabilities.DiagnosticProvider) != "false"
	var provider struct {
		WorkspaceDiagnostics bool `json:"workspaceDiagnostics"`
	}
	_ = json.Unmarshal(reply.Capabilities.DiagnosticProvider, &provider)
	c.workspacePull = provider.WorkspaceDiagnostics
	if err := c.notify("initialized", map[string]any{}); err != nil {
		return err
	}
	if len(c.settings) > 0 {
		return c.notify("workspace/didChangeConfiguration", map[string]any{"settings": c.settings})
	}
	return nil
}
func (c *rpcClient) syncDocument(path, language, text string, save bool) (int, error) {
	uri := fileURI(c.path(path))
	version := c.versions[uri] + 1
	c.mu.Lock()
	delete(c.push, uri)
	c.mu.Unlock()
	var err error
	if c.versions[uri] == 0 {
		err = c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": language, "version": version, "text": text}})
	} else {
		err = c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": version}, "contentChanges": []any{map[string]any{"text": text}}})
	}
	if err != nil {
		return 0, err
	}
	c.versions[uri] = version
	c.texts[uri] = text
	if save {
		err = c.notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}, "text": text})
	}
	return version, err
}
func (c *rpcClient) verdict(ctx context.Context, path string, version int, mode string) ([]Diagnostic, error) {
	uri := fileURI(c.path(path))
	if mode == "full" {
		if !c.workspacePull {
			return nil, errors.New("server does not support full-workspace diagnostic pull; use devtools.wait_mode=document")
		}
		var report struct {
			Items []struct {
				URI   string       `json:"uri"`
				Kind  string       `json:"kind"`
				Items []Diagnostic `json:"items"`
			} `json:"items"`
		}
		if err := c.request(ctx, "workspace/diagnostic", map[string]any{"previousResultIds": []any{}}, &report); err != nil {
			return nil, err
		}
		var diagnostics []Diagnostic
		for _, item := range report.Items {
			if item.Kind != "full" {
				return nil, errors.New("workspace server returned unchanged diagnostics without a requested baseline")
			}
			for _, d := range item.Items {
				d.File = item.URI
				diagnostics = append(diagnostics, d)
			}
		}
		return diagnostics, nil
	}
	if c.pull || c.serverID == "pyright" {
		var report struct {
			Kind  string       `json:"kind"`
			Items []Diagnostic `json:"items"`
		}
		if err := c.request(ctx, "textDocument/diagnostic", map[string]any{"textDocument": map[string]any{"uri": uri}}, &report); err == nil && report.Kind == "full" {
			return report.Items, nil
		} else if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if c.serverID == "typescript" {
		var diagnostics []Diagnostic
		supported := true
		for _, command := range []string{"syntacticDiagnosticsSync", "semanticDiagnosticsSync"} {
			var reply struct {
				Success *bool           `json:"success"`
				Body    json.RawMessage `json:"body"`
			}
			if err := c.request(ctx, "workspace/executeCommand", map[string]any{"command": "typescript.tsserverRequest", "arguments": []any{command, map[string]any{"file": c.path(path), "includeLinePosition": true}, map[string]any{"executionTarget": 0}}}, &reply); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				supported = false
				break
			}
			var items []struct {
				Start struct {
					Line   int `json:"line"`
					Offset int `json:"offset"`
				} `json:"startLocation"`
				End struct {
					Line   int `json:"line"`
					Offset int `json:"offset"`
				} `json:"endLocation"`
				Message  string `json:"message"`
				Category string `json:"category"`
				Source   string `json:"source"`
				Code     any    `json:"code"`
			}
			if reply.Success != nil && !*reply.Success || len(reply.Body) == 0 || json.Unmarshal(reply.Body, &items) != nil {
				supported = false
				break
			}
			for _, item := range items {
				severity := 1
				switch item.Category {
				case "warning":
					severity = 2
				case "message":
					severity = 3
				case "suggestion":
					severity = 4
				}
				source := item.Source
				if source == "" {
					source = "typescript"
				}
				diagnostics = append(diagnostics, Diagnostic{Range: Range{Start: Position{item.Start.Line - 1, item.Start.Offset - 1}, End: Position{item.End.Line - 1, item.End.Offset - 1}}, Message: item.Message, Severity: severity, Source: source, Code: item.Code})
			}
		}
		if supported {
			return diagnostics, nil
		}
	}
	for {
		c.mu.Lock()
		event, ok := c.push[uri]
		changed := c.changed
		c.mu.Unlock()
		// 不带版本的异步推送不能证明属于哪次编辑，绝不当作当前内容的新鲜诊断。
		if ok && event.version == version {
			return event.diagnostics, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.done:
			c.mu.Lock()
			err := c.failure
			c.mu.Unlock()
			return nil, err
		case <-changed:
		}
	}
}
func (c *rpcClient) close() error {
	c.closeOnce.Do(func() { c.closeErr = c.process.Close() })
	return c.closeErr
}
func (c *rpcClient) watchContext(ctx context.Context) func() {
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_ = c.close()
		case <-stop:
		}
	}()
	return func() { close(stop); <-stopped }
}
