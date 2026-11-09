package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/sashabaranov/go-openai"
)

// Handler 是插件工具、命令、钩子和 provider 共享的实际业务函数。
type Handler func(context.Context, map[string]any) (any, error)

// Handlers 将 manifest 声明绑定到实际函数；声明而未实现的能力不能启动。
type Handlers struct {
	Tools     map[string]Handler
	Commands  map[string]Handler
	Hooks     map[string]Handler
	Providers map[string]map[string]Handler
}

// Runtime 供插件请求宿主的受控工具、模型和工作目录，宿主总会复核父请求权限。
type Runtime struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	writer  io.Writer
	pending map[string]chan rpcMessage
	active  map[string]context.CancelFunc
	next    atomic.Uint64
}
type runtimeKey struct{}

// RuntimeFromContext 返回当前 handler 的宿主请求端口；脱离 handler 不能伪造父请求。
func RuntimeFromContext(ctx context.Context) (*Runtime, error) {
	r, ok := ctx.Value(runtimeKey{}).(*Runtime)
	if !ok {
		return nil, errors.New("SDK runtime unavailable outside handler")
	}
	return r, nil
}

type requestIDKey struct{}

func (r *Runtime) send(msg rpcMessage) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return json.NewEncoder(r.writer).Encode(msg)
}
func (r *Runtime) hostCall(ctx context.Context, method string, args map[string]any, result any) error {
	parent, ok := ctx.Value(requestIDKey{}).(json.RawMessage)
	if !ok {
		return errors.New("SDK host calls require an active handler request")
	}
	args["parent_id"] = parent
	id := fmt.Sprintf("\"host-%d\"", r.next.Add(1))
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	channel := make(chan rpcMessage, 1)
	r.mu.Lock()
	r.pending[id] = channel
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, id); r.mu.Unlock() }()
	if err := r.send(rpcMessage{Version: "2.0", ID: json.RawMessage(id), Method: method, Params: raw}); err != nil {
		return err
	}
	select {
	case response := <-channel:
		if response.Error != nil {
			return errors.New("host denied or failed plugin request")
		}
		return json.Unmarshal(response.Result, result)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// CallTool 借用宿主原始 ToolExecutor，不能自行添加用户身份或越过子任务范围。
func (r *Runtime) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
	var result any
	err := r.hostCall(ctx, "host.tool_call", map[string]any{"name": name, "arguments": args}, &result)
	return result, err
}

// Complete 使用宿主白名单模型和既有辅助请求策略，不接收 API key 或服务 URL。
func (r *Runtime) Complete(ctx context.Context, model string, messages []openai.ChatCompletionMessage) (string, error) {
	var result string
	err := r.hostCall(ctx, "host.complete", map[string]any{"model": model, "messages": messages}, &result)
	return result, err
}

// Workspace 获取当前任务授权的目录；插件自己的 cwd 不代表用户授权目录。
func (r *Runtime) Workspace(ctx context.Context) (string, error) {
	var result string
	err := r.hostCall(ctx, "host.workspace", map[string]any{}, &result)
	return result, err
}

// Serve 在标准输入输出上提供 JSONRPC SDK；所有声明必须具备真实 handler，取消请求传递 context。
func Serve(ctx context.Context, manifest Manifest, handlers Handlers, input io.Reader, output io.Writer) (*Runtime, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	for _, tool := range manifest.Tools {
		if handlers.Tools[tool.Name] == nil {
			return nil, fmt.Errorf("tool %s has no handler", tool.Name)
		}
	}
	for _, cmd := range manifest.Commands {
		if handlers.Commands[cmd.Name] == nil {
			return nil, fmt.Errorf("command %s has no handler", cmd.Name)
		}
	}
	for _, hook := range manifest.Hooks {
		if handlers.Hooks[hook] == nil {
			return nil, fmt.Errorf("hook %s has no handler", hook)
		}
	}
	for kind, ops := range manifest.Providers {
		for _, op := range ops {
			if handlers.Providers[kind][op] == nil {
				return nil, fmt.Errorf("provider %s.%s has no handler", kind, op)
			}
		}
	}
	runtime := &Runtime{writer: output, pending: map[string]chan rpcMessage{}, active: map[string]context.CancelFunc{}}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 64<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return runtime, err
		}
		var msg rpcMessage
		if json.Unmarshal(scanner.Bytes(), &msg) != nil || msg.Version != "2.0" {
			return runtime, errors.New("invalid SDK JSONRPC input")
		}
		if msg.Method == "" {
			runtime.mu.Lock()
			pending := runtime.pending[string(msg.ID)]
			runtime.mu.Unlock()
			if pending != nil {
				select {
				case pending <- msg:
				default:
				}
			}
			continue
		}
		if msg.Method == "$/cancelRequest" {
			var params struct {
				ID json.RawMessage `json:"id"`
			}
			if json.Unmarshal(msg.Params, &params) == nil {
				runtime.mu.Lock()
				cancel := runtime.active[string(params.ID)]
				runtime.mu.Unlock()
				if cancel != nil {
					cancel()
				}
			}
			continue
		}
		request, cancel := context.WithCancel(context.WithValue(context.WithValue(ctx, runtimeKey{}, runtime), requestIDKey{}, msg.ID))
		runtime.mu.Lock()
		runtime.active[string(msg.ID)] = cancel
		runtime.mu.Unlock()
		go func(msg rpcMessage) {
			defer cancel()
			defer func() { runtime.mu.Lock(); delete(runtime.active, string(msg.ID)); runtime.mu.Unlock() }()
			result, err := sdkDispatch(request, manifest, handlers, msg)
			response := rpcMessage{Version: "2.0", ID: msg.ID}
			if err != nil {
				response.Error = &rpcError{-32000, "plugin handler failed"}
			} else {
				response.Result, err = json.Marshal(result)
				if err != nil {
					response.Error = &rpcError{-32603, "handler returned non-JSON result"}
				}
			}
			_ = runtime.send(response)
		}(msg)
	}
	runtime.mu.Lock()
	for _, cancel := range runtime.active {
		cancel()
	}
	runtime.mu.Unlock()
	return runtime, scanner.Err()
}
func sdkDispatch(ctx context.Context, manifest Manifest, handlers Handlers, msg rpcMessage) (any, error) {
	if msg.Method == "initialize" {
		return map[string]any{"protocol_version": 1, "name": manifest.Name, "version": manifest.Version}, nil
	}
	var params struct {
		Name      string         `json:"name"`
		Kind      string         `json:"kind"`
		Operation string         `json:"operation"`
		Event     string         `json:"event"`
		Arguments map[string]any `json:"arguments"`
		Session   string         `json:"session"`
		User      string         `json:"user"`
	}
	if json.Unmarshal(msg.Params, &params) != nil {
		return nil, errors.New("invalid RPC arguments")
	}
	var handler Handler
	switch msg.Method {
	case "tools.call":
		handler = handlers.Tools[params.Name]
	case "commands.call":
		handler = handlers.Commands[params.Name]
	case "hooks.call":
		handler = handlers.Hooks[params.Event]
	case "provider.call":
		handler = handlers.Providers[params.Kind][params.Operation]
	}
	if handler == nil {
		return nil, errors.New("RPC method/handler not declared")
	}
	return handler(ctx, params.Arguments)
}
