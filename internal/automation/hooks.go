package automation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

// ErrApprovalRequired 表示可信 hook 要求先取得持久人工批准，不能当作自动允许。
var ErrApprovalRequired = errors.New("automation hook requires human approval")

type hookKey struct{}
type directive struct {
	Action    string         `json:"action"`
	Decision  string         `json:"decision"`
	Message   string         `json:"message"`
	Reason    string         `json:"reason"`
	Context   string         `json:"context"`
	Args      map[string]any `json:"args"`
	ToolInput map[string]any `json:"tool_input"`
}

func matchesEvent(events []string, event string) bool {
	for _, match := range events {
		if match == event || strings.HasSuffix(match, "*") && strings.HasPrefix(event, strings.TrimSuffix(match, "*")) {
			return true
		}
	}
	return false
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

// Emit 执行管理员配置的回调；外部通知绝不跟随重定向或盲重试副作用。
func (s *Service) Emit(ctx context.Context, event string, payload map[string]any) ([]map[string]any, error) {
	if !s.cfg.Enabled || ctx.Value(hookKey{}) != nil {
		return nil, nil
	}
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return nil, errors.New("hook requires trusted identity")
	}
	ctx = context.WithValue(ctx, hookKey{}, true)
	fields := map[string]any{}
	for k, v := range payload {
		fields[k] = v
	}
	fields["hook_event_name"] = event
	fields["session_id"] = i.Session.Key()
	fields["platform"] = i.Session.Platform
	fields["user_id"] = i.SenderID
	fields["chat_id"] = i.Session.Conversation
	fields["thread_id"] = i.Session.Thread
	fields["task_id"] = task.ID(ctx)
	var out []map[string]any
	for _, hook := range s.cfg.Hooks {
		if !matchesEvent(hook.Events, event) {
			continue
		}
		if hook.Matcher != "" {
			match, err := regexp.MatchString("^(?:"+hook.Matcher+")$", fmt.Sprint(fields["tool_name"]))
			if err != nil {
				return nil, err
			}
			if !match {
				continue
			}
		}
		timeout := hook.TimeoutSeconds
		if timeout == 0 {
			timeout = 60
		}
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		var value any
		var err error
		if hook.URL != "" {
			if s.host.Check == nil {
				err = errors.New("hook authorization unavailable")
			} else {
				err = s.host.Check(callCtx, "hooks_outbound", true)
			}
			if err == nil {
				err = sendHook(callCtx, hook.URL, os.Getenv(hook.SecretEnv), event, fields)
			}
		} else {
			args := map[string]any{}
			for k, v := range hook.Args {
				args[k] = v
			}
			encoded, e := json.Marshal(fields)
			if e != nil {
				err = e
			} else {
				args["hook_payload"] = fields
				if hook.Tool == "exec" {
					command, valid := args["command"].(string)
					if !valid || command == "" {
						err = errors.New("exec hook requires configured command")
					} else {
						args["command"] = "printf '%s' " + shellQuote(string(encoded)) + " | " + command
						delete(args, "hook_payload")
					}
				}
				if err == nil {
					if s.host.Call == nil {
						err = errors.New("hook tool dispatcher unavailable")
					} else {
						value, err = s.host.Call(callCtx, hook.Tool, args)
					}
				}
			}
		}
		cancel()
		var parsed directive
		var raw []byte
		if value != nil {
			raw, _ = json.Marshal(value)
			obj, decodeErr := toolObject(value)
			if decodeErr == nil {
				for _, key := range []string{"summary", "output", "stdout"} {
					if stdout, valid := obj[key].(string); valid {
						raw = []byte(stdout)
					}
				}
				if event == "pre_tool_call" {
					if exit, valid := obj["exit_code"].(float64); valid && exit == 2 {
						parsed.Action = "block"
						parsed.Message = "hook exited 2"
					}
				}
			}
		}
		parseErr := error(nil)
		if len(raw) > 0 {
			var decoded directive
			parseErr = json.Unmarshal(raw, &decoded)
			if parseErr == nil {
				if decoded.Action != "" || decoded.Decision != "" || decoded.Context != "" {
					parsed = decoded
				}
			}
		}
		if parsed.Action == "" {
			parsed.Action = parsed.Decision
		}
		if parsed.Message == "" {
			parsed.Message = parsed.Reason
		}
		blocking := event == "pre_tool_call" || event == "pre_llm_call"
		if err != nil {
			if blocking && hook.FailClosed {
				return out, fmt.Errorf("hook %s failed closed: %w", hook.Name, err)
			}
			slog.Warn("自动化 hook 失败", "hook", hook.Name, "event", event, "error", err)
			continue
		}
		if blocking && hook.FailClosed && parseErr != nil {
			return out, fmt.Errorf("hook %s invalid directive: %w", hook.Name, parseErr)
		}
		if blocking {
			switch parsed.Action {
			case "block":
				return out, fmt.Errorf("hook %s blocked: %s", hook.Name, parsed.Message)
			case "approve":
				return out, fmt.Errorf("%w: %s", ErrApprovalRequired, parsed.Message)
			}
		}
		if parsed.Args == nil {
			parsed.Args = parsed.ToolInput
		}
		encoded, _ := json.Marshal(parsed)
		var result map[string]any
		if json.Unmarshal(encoded, &result) == nil {
			out = append(out, result)
		}
	}
	return out, nil
}

func sendHook(ctx context.Context, url, secret, event string, fields map[string]any) error {
	if secret == "" {
		return errors.New("outbound hook signing credential missing")
	}
	id, err := newID()
	if err != nil {
		return err
	}
	body := map[string]any{}
	for k, v := range fields {
		body[k] = v
	}
	body["delivery_id"] = id
	body["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, err = mac.Write(encoded)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Saber-Automation")
	req.Header.Set("X-Hermes-Event", event)
	req.Header.Set("X-Hermes-Delivery", id)
	req.Header.Set("X-Hermes-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	err = errors.Join(readErr, resp.Body.Close())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = errors.Join(err, fmt.Errorf("outbound hook HTTP %d; no automatic retry", resp.StatusCode))
	}
	return err
}

// BeforeTurn 注入可信 hook 上下文并记录真正会话活动。
func (s *Service) BeforeTurn(ctx context.Context, req agent.Request) (agent.Request, error) {
	if err := s.observe(ctx); err != nil {
		return req, err
	}
	directives, err := s.Emit(ctx, "agent:start", map[string]any{})
	if err != nil {
		return req, err
	}
	for _, d := range directives {
		if extra, ok := d["context"].(string); ok && extra != "" {
			req.RuntimeContext = append(req.RuntimeContext, contextMessage(extra))
		}
	}
	return req, nil
}

// AfterTurn 发出真实运行结果事件；自动化完成由持久任务结果进行恢复判断。
func (s *Service) AfterTurn(ctx context.Context, result agent.Result, runErr error) error {
	boundaryErr := s.finishLiveTurn(ctx, result, runErr)
	_, err := s.Emit(ctx, "agent:end", map[string]any{"response": result.Content, "status": result.Status, "error": fmt.Sprint(runErr)})
	return errors.Join(boundaryErr, err)
}

// BeforeTool 支持精确 matcher 的阻止、修改与人工批准指令。
func (s *Service) BeforeTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	directives, err := s.Emit(ctx, "pre_tool_call", map[string]any{"tool_name": name, "args": args})
	if err != nil {
		return nil, err
	}
	for _, d := range directives {
		if d["action"] == "modify" {
			encoded, _ := json.Marshal(d["args"])
			var modified map[string]any
			if json.Unmarshal(encoded, &modified) == nil && modified != nil {
				args = modified
			}
		}
	}
	return args, nil
}

// AfterTool 通知 hook 真实结果，不能将通知错误改写成已执行工具失败。
func (s *Service) AfterTool(ctx context.Context, name string, args map[string]any, result any, callErr error) error {
	_, err := s.Emit(ctx, "post_tool_call", map[string]any{"tool_name": name, "args": args, "result": result, "error": fmt.Sprint(callErr)})
	return err
}

// BeforeModel 支持模型调用前的可信上下文注入和显式 veto。
func (s *Service) BeforeModel(ctx context.Context, req agent.Request) (agent.Request, error) {
	directives, err := s.Emit(ctx, "pre_llm_call", map[string]any{"model": req.Model})
	if err != nil {
		return req, err
	}
	for _, d := range directives {
		if extra, ok := d["context"].(string); ok && extra != "" {
			req.RuntimeContext = append(req.RuntimeContext, contextMessage(extra))
		}
	}
	return req, nil
}

// AfterModel 通知真实模型轮次与工具名称。
func (s *Service) AfterModel(ctx context.Context, response agent.Response) error {
	var names []string
	for _, call := range response.ToolCalls {
		names = append(names, call.Function.Name)
	}
	_, err := s.Emit(ctx, "agent:step", map[string]any{"response": response.Content, "model": response.Model, "tool_names": names})
	return err
}

func contextMessage(extra string) openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: extra}
}
