// Package codeexec 实现由 Go 控制、按真实任务隔离的持久代码单元与工具 RPC。
package codeexec

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/toolkit"
)

//go:embed kernel.py
var kernelSource string

type kernelFrame struct {
	Type     string         `json:"type"`
	Token    string         `json:"token"`
	Tool     string         `json:"tool"`
	Args     map[string]any `json:"args"`
	Data     string         `json:"data"`
	Stream   string         `json:"stream"`
	ExitCode int            `json:"exit_code"`
	Error    string         `json:"error"`
}
type frameEvent struct {
	frame kernelFrame
	err   error
}
type kernel struct {
	process  toolkit.Process
	frames   chan frameEvent
	mu       sync.Mutex
	lastUsed time.Time
	once     sync.Once
	lost     atomic.Bool
	stop     chan struct{}
}

func (k *kernel) close() error {
	var err error
	k.once.Do(func() { k.lost.Store(true); close(k.stop); err = k.process.Close() })
	return err
}

// Service 持有有界任务内核；每个单元的 RPC 始终使用当前调用的可信身份和预算。
type Service struct {
	cfg     config.CodeExecutionConfig
	host    toolkit.Host
	mu      sync.Mutex
	kernels map[string]*kernel
	closed  bool
	stop    chan struct{}
}

// New 构造实际代码执行提供者，启用时缺少授权执行端口即明确失败。
func New(cfg config.CodeExecutionConfig, host toolkit.Host) (toolkit.Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	d := config.DefaultCodeExecutionConfig()
	if cfg.Mode == "" {
		cfg.Mode = d.Mode
	}
	if cfg.Python == "" {
		cfg.Python = d.Python
	}
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = d.TimeoutSeconds
	}
	if cfg.MaxToolCalls == 0 {
		cfg.MaxToolCalls = d.MaxToolCalls
	}
	if cfg.MaxOutputBytes == 0 {
		cfg.MaxOutputBytes = d.MaxOutputBytes
	}
	if cfg.MaxSessionKernels == 0 {
		cfg.MaxSessionKernels = d.MaxSessionKernels
	}
	if cfg.KernelIdleSeconds == 0 {
		cfg.KernelIdleSeconds = d.KernelIdleSeconds
	}
	if cfg.Tools == nil {
		cfg.Tools = slices.Clone(d.Tools)
	}
	if cfg.Enabled && (host.StartProcess == nil || host.Call == nil || host.Check == nil || host.SaveOutput == nil) {
		return nil, errors.New("code_execution requires authorized process, tool dispatch, permission and output archive ports")
	}
	s := &Service{cfg: cfg, host: host, kernels: map[string]*kernel{}, stop: make(chan struct{})}
	if cfg.Enabled {
		go s.reap()
	}
	return s, nil
}

// Definitions 声明独立 execute_code 授权与实际 exec 依赖，不扩大单元内工具名单。
func (s *Service) Definitions() []toolkit.Definition {
	if !s.cfg.Enabled {
		return nil
	}
	return []toolkit.Definition{{Permission: "execute_code", Requires: []string{"exec"}, Tool: openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "execute_code", Description: "Run persistent Python code in the authorized task backend. Variables/imports survive cells; reset=true discards state. Import saber_tools.call or named tools, json_parse, shell_quote, retry. Tool RPC is restricted by configured allowlist and current authorization; no interactive/background/recursive tools. Only stdout, stderr and requested tool artifacts enter the model context. Timeout or cancellation destroys this kernel, without replaying side effects.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}, "reset": map[string]any{"type": "boolean"}}, "required": []string{"code"}, "additionalProperties": false}}}}}
}

func kernelKey(ctx context.Context) (string, error) {
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return "", errors.New("execute_code requires trusted identity")
	}
	id, dir, ok := execution.TaskScope(ctx)
	if !ok {
		return "", errors.New("execute_code requires a persistent task")
	}
	encoded, _ := json.Marshal([]any{identity.Session.Platform, identity.Session.Account, identity.Session.Conversation, identity.SenderID, id, dir})
	return string(encoded), nil
}

func (s *Service) getKernel(ctx context.Context, key string, reset bool) (*kernel, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false, errors.New("code execution service is closed")
	}
	if old := s.kernels[key]; old != nil {
		if !reset && !old.lost.Load() {
			return old, true, nil
		}
		_ = old.close()
		delete(s.kernels, key)
	}
	if len(s.kernels) >= s.cfg.MaxSessionKernels {
		return nil, false, fmt.Errorf("code execution kernel limit reached (%d); reset or finish idle tasks", s.cfg.MaxSessionKernels)
	}
	process, err := s.host.StartProcess(ctx, []string{s.cfg.Python, "-I", "-u", "-c", kernelSource}, nil)
	if err != nil {
		return nil, false, fmt.Errorf("start code_execution Python kernel: %w", err)
	}
	k := &kernel{process: process, frames: make(chan frameEvent, 32), lastUsed: time.Now(), stop: make(chan struct{})}
	s.kernels[key] = k
	go func() {
		scanner := bufio.NewScanner(process)
		scanner.Buffer(make([]byte, 64<<10), 24<<20)
		publish := func(event frameEvent) bool {
			select {
			case k.frames <- event:
				return true
			case <-k.stop:
				return false
			}
		}
		for scanner.Scan() {
			var frame kernelFrame
			if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
				publish(frameEvent{err: fmt.Errorf("invalid kernel protocol: %w", err)})
				return
			}
			if !publish(frameEvent{frame: frame}) {
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = process.Wait()
		}
		if err == nil {
			err = io.EOF
		}
		publish(frameEvent{err: err})
	}()
	return k, false, nil
}

// Execute 在有界单元内重查每次真实 RPC，错误与取消销毁状态且不重发调用。
func (s *Service) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	if name != "execute_code" || !s.cfg.Enabled {
		return nil, errors.New("execute_code is disabled or unknown")
	}
	if err := s.host.Check(ctx, "execute_code", false); err != nil {
		return nil, err
	}
	if err := s.host.Check(ctx, "exec", false); err != nil {
		return nil, err
	}
	for name := range args {
		if name != "code" && name != "reset" {
			return nil, fmt.Errorf("unexpected execute_code argument %q", name)
		}
	}
	code, ok := args["code"].(string)
	if !ok || strings.TrimSpace(code) == "" || len(code) > 1<<20 {
		return nil, errors.New("execute_code requires Python source of 1..1048576 bytes")
	}
	reset := false
	if value, exists := args["reset"]; exists {
		reset, ok = value.(bool)
		if !ok {
			return nil, errors.New("reset must be boolean")
		}
	}
	key, err := kernelKey(ctx)
	if err != nil {
		return nil, err
	}
	k, reused, err := s.getKernel(ctx, key, reset)
	if err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.lost.Load() {
		return nil, errors.New("code execution kernel state was lost; retry with reset=true")
	}
	cellCtx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(random[:])
	allowed := map[string]bool{}
	tools := []string{}
	for _, tool := range s.cfg.Tools {
		if s.host.Check(cellCtx, tool, false) == nil {
			allowed[tool] = true
			tools = append(tools, tool)
		}
	}
	payload, err := json.Marshal(map[string]any{"type": "cell", "token": token, "tools": tools, "code": code, "mode": s.cfg.Mode})
	if err != nil {
		return nil, err
	}
	if _, err = k.process.Write(append(payload, '\n')); err != nil {
		_ = k.close()
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	calls := 0
	logs := []map[string]any{}
	toolErrors := []map[string]any{}
	images := []string{}
	files := []string{}
	result := toolkit.Result{}
	finish := func(code int, cause error) (any, error) {
		k.lastUsed = time.Now()
		data := map[string]any{"exit_code": code, "stdout": truncateOutput(stdout.String(), s.cfg.MaxOutputBytes), "stderr": truncateOutput(stderr.String(), s.cfg.MaxOutputBytes), "tool_calls_made": calls, "tool_calls": logs, "kernel_reused": reused, "state_lost": k.lost.Load(), "tools": tools}
		if len(toolErrors) > 0 {
			data["tool_errors"] = toolErrors
			result.IsError = true
		}
		if cause != nil {
			data["error"] = cause.Error()
			result.IsError = true
		}
		if code != 0 {
			result.IsError = true
		}
		full := append(bytes.Clone(stdout.Bytes()), stderr.Bytes()...)
		if len(full) > 0 {
			outputID, archiveErr := s.host.SaveOutput(ctx, full)
			if archiveErr != nil {
				data["archive_error"] = archiveErr.Error()
				result.IsError = true
			} else {
				result.OutputID = outputID
			}
		}
		result.Data = data
		result.Images = images
		result.Files = files
		return result, nil
	}
	for {
		select {
		case <-cellCtx.Done():
			_ = k.close()
			return finish(-1, cellCtx.Err())
		case <-k.stop:
			return finish(-1, errors.New("code kernel was stopped; state was lost"))
		case event := <-k.frames:
			if event.err != nil {
				_ = k.close()
				return finish(-1, event.err)
			}
			frame := event.frame
			if subtle.ConstantTimeCompare([]byte(frame.Token), []byte(token)) != 1 {
				_ = k.close()
				return finish(-1, errors.New("kernel frame has stale or invalid cell authority"))
			}
			switch frame.Type {
			case "output":
				chunk, decodeErr := base64.StdEncoding.DecodeString(frame.Data)
				if decodeErr != nil {
					_ = k.close()
					return finish(-1, decodeErr)
				}
				if stdout.Len()+stderr.Len()+len(chunk) > 16<<20 {
					_ = k.close()
					return finish(-1, errors.New("code output exceeds 16 MiB; kernel state was discarded"))
				}
				if frame.Stream == "stderr" {
					stderr.Write(chunk)
				} else {
					stdout.Write(chunk)
				}
			case "call":
				start := time.Now()
				var value any
				var callErr error
				if !allowed[frame.Tool] {
					callErr = fmt.Errorf("tool %q is not allowed in this code cell", frame.Tool)
				} else if calls >= s.cfg.MaxToolCalls {
					callErr = fmt.Errorf("code tool call limit reached (%d)", s.cfg.MaxToolCalls)
				} else {
					calls++
					value, callErr = s.host.Call(cellCtx, frame.Tool, frame.Args)
				}
				if media, ok := value.(toolkit.Result); ok {
					images = append(images, media.Images...)
					files = append(files, media.Files...)
				}
				if callErr == nil {
					callErr = toolResultError(value)
				}
				log := map[string]any{"tool": frame.Tool, "duration_ms": time.Since(start).Milliseconds()}
				response := map[string]any{"type": "response", "token": token, "result": value}
				if callErr != nil {
					response["error"] = callErr.Error()
					log["error"] = callErr.Error()
					if len(toolErrors) < 20 {
						toolErrors = append(toolErrors, map[string]any{"tool": frame.Tool, "error": callErr.Error()})
					}
				}
				if len(logs) < 200 {
					logs = append(logs, log)
				}
				payload, marshalErr := json.Marshal(response)
				if marshalErr != nil {
					_ = k.close()
					return finish(-1, marshalErr)
				}
				if _, err = k.process.Write(append(payload, '\n')); err != nil {
					_ = k.close()
					return finish(-1, err)
				}
			case "done":
				if frame.Error != "" {
					return finish(frame.ExitCode, errors.New(frame.Error))
				}
				return finish(frame.ExitCode, nil)
			default:
				_ = k.close()
				return finish(-1, fmt.Errorf("unknown code kernel frame %q", frame.Type))
			}
		}
	}
}

func toolResultError(value any) error {
	if result, ok := value.(execution.Result); ok {
		if result.Error != "" {
			return errors.New(result.Error)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("tool exited with code %d", result.ExitCode)
		}
	}
	if result, ok := value.(toolkit.Result); ok && result.IsError {
		return errors.New("nested tool reported an error")
	}
	if object, ok := value.(map[string]any); ok {
		if text, ok := object["error"].(string); ok && text != "" {
			return errors.New(text)
		}
		if flag, ok := object["is_error"].(bool); ok && flag {
			return errors.New("nested tool reported an error")
		}
	}
	return nil
}
func truncateOutput(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	head := limit * 2 / 5
	tail := limit - head
	return strings.ToValidUTF8(text[:head], "�") + "\n[output truncated; full output is in output_id]\n" + strings.ToValidUTF8(text[len(text)-tail:], "�")
}
func (s *Service) reap() {
	ticker := time.NewTicker(time.Second * 30)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.mu.Lock()
			for key, k := range s.kernels {
				if k.mu.TryLock() {
					if k.lost.Load() || time.Since(k.lastUsed) > time.Duration(s.cfg.KernelIdleSeconds)*time.Second {
						_ = k.close()
						delete(s.kernels, key)
					}
					k.mu.Unlock()
				}
			}
			s.mu.Unlock()
		}
	}
}

// Close 停止空闲清理并终止所有已拥有的内核；不重启任何代码单元。
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	kernels := s.kernels
	s.kernels = map[string]*kernel{}
	s.mu.Unlock()
	var errs []error
	for _, k := range kernels {
		errs = append(errs, k.close())
	}
	return errors.Join(errs...)
}
