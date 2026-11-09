package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

type rpcMessage struct {
	Version string          `json:"jsonrpc"`
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
type pendingCall struct {
	ctx   context.Context
	reply chan rpcMessage
}
type process struct {
	manifest  Manifest
	host      toolkit.Host
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	writeMu   sync.Mutex
	mu        sync.Mutex
	pending   map[string]pendingCall
	done      chan struct{}
	exit      error
	next      atomic.Uint64
	closeOnce sync.Once
	cfg       config.PluginsConfig
	ctx       context.Context
	cancel    context.CancelFunc
}

func startProcess(ctx context.Context, m Manifest, dir string, cfg config.PluginsConfig, host toolkit.Host) (*process, error) {
	life, cancel := context.WithCancel(context.Background())
	args := append([]string{}, m.Command...)
	if len(cfg.Launcher) > 0 {
		launcher := make([]string, len(cfg.Launcher))
		for i, value := range cfg.Launcher {
			launcher[i] = strings.ReplaceAll(value, "{plugin_dir}", dir)
		}
		args = append(launcher, args...)
	}
	if !filepath.IsAbs(args[0]) && strings.ContainsAny(args[0], "/\\") {
		args[0] = filepath.Join(dir, args[0])
	}
	cmd := exec.CommandContext(life, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "SABER_PLUGIN_ID=" + m.Name, "SABER_PLUGIN_PROTOCOL=1"}
	for _, key := range m.Environment {
		if value, ok := cfg.Environment[key]; ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	started, exited := processSupervisor(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		return nil, fmt.Errorf("plugin %s executable/launcher unavailable: %w", m.Name, err)
	}
	if started != nil {
		if err := started(); err != nil {
			cancel()
			_ = stdin.Close()
			_ = cmd.Wait()
			exited()
			return nil, fmt.Errorf("plugin process supervision failed: %w", err)
		}
	}
	p := &process{manifest: m, host: host, cmd: cmd, stdin: stdin, pending: map[string]pendingCall{}, done: make(chan struct{}), cfg: cfg, ctx: life, cancel: cancel}
	go p.readLoop(stdout)
	go func() {
		err := cmd.Wait()
		exited()
		p.mu.Lock()
		p.exit = err
		p.mu.Unlock()
		cancel()
		close(p.done)
	}()
	load, stop := context.WithTimeout(ctx, time.Duration(cfg.LoadTimeoutSeconds)*time.Second)
	defer stop()
	var result struct {
		ProtocolVersion int    `json:"protocol_version"`
		Name            string `json:"name"`
		Version         string `json:"version"`
	}
	if err := p.call(load, "initialize", map[string]any{"protocol_version": 1, "name": m.Name, "version": m.Version, "capabilities": m.Capabilities}, &result); err != nil {
		_ = p.close()
		return nil, err
	}
	if result.ProtocolVersion != 1 || result.Name != m.Name || result.Version != m.Version {
		_ = p.close()
		return nil, errors.New("plugin initialize identity/version/protocol mismatch")
	}
	return p, nil
}
func (p *process) send(message rpcMessage) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.ctx.Err() != nil {
		return errors.New("plugin process stopped")
	}
	return json.NewEncoder(p.stdin).Encode(message)
}
func (p *process) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), p.cfg.MaxOutputBytes)
	for scanner.Scan() {
		var msg rpcMessage
		if json.Unmarshal(scanner.Bytes(), &msg) != nil || msg.Version != "2.0" {
			p.cancel()
			return
		}
		if msg.Method != "" {
			go p.hostRequest(msg)
			continue
		}
		p.mu.Lock()
		call, ok := p.pending[string(msg.ID)]
		p.mu.Unlock()
		if ok {
			select {
			case call.reply <- msg:
			default:
			}
		}
	}
	p.cancel()
}
func (p *process) call(ctx context.Context, method string, params any, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := fmt.Sprintf("%d", p.next.Add(1))
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	pending := pendingCall{ctx: ctx, reply: make(chan rpcMessage, 1)}
	p.mu.Lock()
	p.pending[id] = pending
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, id); p.mu.Unlock() }()
	if err := p.send(rpcMessage{Version: "2.0", ID: json.RawMessage(id), Method: method, Params: raw}); err != nil {
		return err
	}
	select {
	case msg := <-pending.reply:
		if msg.Error != nil {
			return fmt.Errorf("plugin %s RPC %s failed (code %d)", p.manifest.Name, method, msg.Error.Code)
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(msg.Result, result); err != nil {
			return errors.New("plugin returned invalid JSON result")
		}
		return nil
	case <-ctx.Done():
		notification, _ := json.Marshal(map[string]any{"id": json.RawMessage(id)})
		_ = p.send(rpcMessage{Version: "2.0", Method: "$/cancelRequest", Params: notification})
		grace := time.NewTimer(250 * time.Millisecond)
		defer grace.Stop()
		select {
		case <-pending.reply:
		case <-p.ctx.Done():
		case <-grace.C:
			_ = p.close()
		}
		return ctx.Err()
	case <-p.ctx.Done():
		return errors.New("plugin process exited or protocol stream failed")
	}
}

func (p *process) hostRequest(msg rpcMessage) {
	var args struct {
		ParentID  json.RawMessage                `json:"parent_id"`
		Name      string                         `json:"name"`
		Arguments map[string]any                 `json:"arguments"`
		Model     string                         `json:"model"`
		Messages  []openai.ChatCompletionMessage `json:"messages"`
	}
	response := rpcMessage{Version: "2.0", ID: msg.ID}
	fail := func(code int) {
		response.Error = &rpcError{code, "host request denied or failed"}
		_ = p.send(response)
	}
	if json.Unmarshal(msg.Params, &args) != nil {
		fail(-32602)
		return
	}
	p.mu.Lock()
	parent, ok := p.pending[string(args.ParentID)]
	p.mu.Unlock()
	if !ok || parent.ctx.Err() != nil {
		fail(-32001)
		return
	}
	if _, ok := chat.IdentityFromContext(parent.ctx); !ok {
		fail(-32001)
		return
	}
	if p.host.Check == nil || p.host.Check(parent.ctx, pluginPermission(p.manifest.Name), false) != nil || p.host.Check(parent.ctx, "exec", false) != nil {
		fail(-32001)
		return
	}
	var result any
	var err error
	switch msg.Method {
	case "host.tool_call":
		if !contains(p.manifest.Capabilities, "tool_call") || p.host.Call == nil || args.Name == "" || strings.HasPrefix(args.Name, "plugin__"+strings.ReplaceAll(p.manifest.Name, "-", "_")+"__") {
			fail(-32001)
			return
		}
		result, err = p.host.Call(parent.ctx, args.Name, args.Arguments)
	case "host.complete":
		if !contains(p.manifest.Capabilities, "complete") || p.host.Complete == nil {
			fail(-32001)
			return
		}
		result, err = p.host.Complete(parent.ctx, args.Model, args.Messages)
	case "host.workspace":
		if !contains(p.manifest.Capabilities, "workspace") || p.host.Workspace == nil {
			fail(-32001)
			return
		}
		result, err = p.host.Workspace(parent.ctx)
	default:
		fail(-32601)
		return
	}
	if err != nil {
		fail(-32000)
		return
	}
	response.Result, err = json.Marshal(result)
	if err != nil {
		fail(-32603)
		return
	}
	_ = p.send(response)
}
func (p *process) close() error {
	var err error
	p.closeOnce.Do(func() {
		p.cancel()
		err = p.stdin.Close()
		<-p.done
		p.mu.Lock()
		exit := p.exit
		p.mu.Unlock()
		if exit != nil {
			var e *exec.ExitError
			// exec.CommandContext 在主动取消时让 Wait 返回 ctx 错误，那不是插件故障。
			cancelled := p.ctx.Err() != nil && errors.Is(exit, p.ctx.Err())
			if !errors.As(exit, &e) && !cancelled {
				err = errors.Join(err, exit)
			}
		}
	})
	return err
}
