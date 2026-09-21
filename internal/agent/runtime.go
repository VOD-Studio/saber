package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sashabaranov/go-openai"
)

// Runtime 将统一执行循环与模型传输、工具实现及聊天平台隔离。
// 配置完成后不应修改字段；并发 Run 要求依赖本身支持并发调用。
type Runtime struct {
	// Model 取得一轮响应，不能为空。
	Model ModelFunc
	// Execute 执行工具，无工具运行时允许为空。
	Execute ToolFunc
	// Limits 是本次运行的资源限制。
	Limits Limits
}

// Run 顺序执行模型与工具，任何终态都返回已有记录并发出一次 RunFinished。
// 取消后不再调度新请求；已派发操作依赖 adapter 响应 context，无法撤销远端副作用。
// emit 可为空；非空时必须及时返回，否则会阻塞当前运行。
func (r Runtime) Run(ctx context.Context, req Request, emit func(Event)) (result Result, err error) {
	started := time.Now()
	if emit == nil {
		emit = func(Event) {}
	}
	result.Status = Failed
	defer func() {
		result.Duration = time.Since(started)
		emit(Event{Kind: RunFinished, Round: len(result.Rounds), Status: result.Status, Text: result.Content, Error: err})
	}()
	emit(Event{Kind: RunStarted})
	limits := r.Limits
	if limits.MaxRounds == 0 {
		limits.MaxRounds = 5
	}
	if limits.Timeout == 0 {
		limits.Timeout = 120 * time.Second
	}
	if limits.MaxToolOutputBytes == 0 {
		limits.MaxToolOutputBytes = 32 * 1024
	}
	if limits.MaxRounds < 1 || limits.Timeout < 0 || limits.MaxToolOutputBytes < 128 {
		return result, errors.New("invalid agent limits: require positive rounds/timeout and at least 128 output bytes")
	}
	ctx, cancel := context.WithDeadline(ctx, started.Add(limits.Timeout))
	defer cancel()
	stop := func(cause error) (Result, error) {
		if ctx.Err() != nil {
			cause = ctx.Err()
		}
		switch {
		case errors.Is(cause, context.DeadlineExceeded):
			result.Status = TimedOut
		case errors.Is(cause, context.Canceled):
			result.Status = Cancelled
		case errors.Is(cause, ErrBudgetExhausted):
			result.Status = BudgetExhausted
		default:
			result.Status = Failed
		}
		if len(result.Rounds) > 0 {
			result.Rounds[len(result.Rounds)-1].Error = cause.Error()
		}
		return result, cause
	}
	if err := ctx.Err(); err != nil {
		return stop(err)
	}
	if r.Model == nil {
		return stop(errors.New("agent model is not configured"))
	}
	// 新建切片防止追加历史覆盖调用方预留的容量。
	req.Messages = append([]openai.ChatCompletionMessage(nil), req.Messages...)
	allowed := make(map[string]bool, len(req.Tools))
	for _, tool := range req.Tools {
		if tool.Function != nil {
			allowed[tool.Function.Name] = true
		}
	}
	for number := 1; number <= limits.MaxRounds; number++ {
		if err := ctx.Err(); err != nil {
			return stop(err)
		}
		emit(Event{Kind: ModelStarted, Round: number})
		if err := ctx.Err(); err != nil {
			return stop(err)
		}
		round := Round{}
		response, modelErr := r.Model(ctx, req, func(event Event) {
			event.Round = number
			// adapter 只能发布模型事件，不能自行宣布运行终态。
			switch event.Kind {
			case AttemptFinished:
				round.Attempts = append(round.Attempts, event.Attempt)
			case AttemptStarted, TextDelta:
				if ctx.Err() != nil {
					return
				}
			default:
				return
			}
			emit(event)
		})
		round.Response = response
		result.Rounds = append(result.Rounds, round)
		if len(round.Attempts) == 0 {
			addUsage(&result.Usage, response.Usage)
		} else {
			for _, attempt := range round.Attempts {
				addUsage(&result.Usage, attempt.Response.Usage)
			}
		}
		if err := ctx.Err(); err != nil {
			return stop(err)
		}
		if modelErr != nil {
			return stop(modelErr)
		}
		emit(Event{Kind: ModelCompleted, Round: number, Response: response})
		if err := ctx.Err(); err != nil {
			return stop(err)
		}
		if err := ValidateResponse(response); err != nil {
			return stop(err)
		}
		if len(response.ToolCalls) == 0 {
			result.Status, result.Content = Completed, response.Content
			return result, nil
		}
		if number == limits.MaxRounds {
			return stop(ErrBudgetExhausted)
		}
		req.Messages = append(req.Messages, openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant, Content: response.Content, ToolCalls: response.ToolCalls,
		})
		for _, call := range response.ToolCalls {
			if err := ctx.Err(); err != nil {
				return stop(err)
			}
			emit(Event{Kind: ToolStarted, Round: number, Tool: ToolRecord{Call: call}})
			if err := ctx.Err(); err != nil {
				return stop(err)
			}
			record := r.callTool(ctx, call, allowed, limits.MaxToolOutputBytes)
			result.Rounds[number-1].Tools = append(result.Rounds[number-1].Tools, record)
			req.Messages = append(req.Messages, openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleTool, ToolCallID: call.ID, Content: record.Content,
			})
			emit(Event{Kind: ToolFinished, Round: number, Tool: record})
			if err := ctx.Err(); err != nil {
				return stop(err)
			}
		}
	}
	return stop(ErrBudgetExhausted)
}

// ValidateResponse 校验模型响应的完整性及工具调用身份，运行和历史恢复共用同一协议规则。
func ValidateResponse(response Response) error {
	if len(response.ToolCalls) == 0 {
		if response.FinishReason != "stop" {
			return fmt.Errorf("incomplete model response: finish_reason=%q", response.FinishReason)
		}
		return nil
	}
	if response.FinishReason != "tool_calls" {
		return fmt.Errorf("incomplete tool calls: finish_reason=%q", response.FinishReason)
	}
	seen := make(map[string]bool, len(response.ToolCalls))
	for _, call := range response.ToolCalls {
		if call.ID == "" || seen[call.ID] || call.Type != openai.ToolTypeFunction || call.Function.Name == "" {
			return errors.New("invalid tool call identity or type")
		}
		seen[call.ID] = true
	}
	return nil
}

func (r Runtime) callTool(ctx context.Context, call openai.ToolCall, allowed map[string]bool, limit int) ToolRecord {
	started := time.Now()
	record := ToolRecord{Call: call}
	var args map[string]any
	var output ToolOutput
	var err error
	switch {
	case !allowed[call.Function.Name]:
		record.ErrorCode, err = "unknown_tool", fmt.Errorf("tool %q is not available in this run", call.Function.Name)
	case r.Execute == nil:
		record.ErrorCode, err = "tool_unavailable", errors.New("tool executor is not configured")
	default:
		err = json.Unmarshal([]byte(call.Function.Arguments), &args)
		if err != nil || args == nil {
			record.ErrorCode = "invalid_arguments"
			if err == nil {
				err = errors.New("tool arguments must be a JSON object")
			}
		} else if err = ctx.Err(); err != nil {
			record.ErrorCode = "cancelled"
		} else {
			output, err = r.Execute(ctx, call.Function.Name, args)
			if err != nil || output.IsError {
				record.ErrorCode = "tool_failed"
			}
		}
	}
	var data []byte
	if err != nil {
		data = errorContent(record.ErrorCode, err.Error())
	} else {
		data, err = json.Marshal(output.Value)
		if err != nil {
			record.ErrorCode = "serialization_failed"
			data = errorContent(record.ErrorCode, err.Error())
		} else if output.IsError {
			data = errorContent(record.ErrorCode, string(data))
		}
	}
	record.OriginalBytes = len(data)
	record.Content = string(data)
	if len(data) > limit {
		suffix := fmt.Sprintf("\n[truncated: original_bytes=%d]", len(data))
		end := limit - len(suffix)
		for end > 0 && !utf8.Valid(data[:end]) {
			end--
		}
		record.Content = string(data[:end]) + suffix
		record.Truncated = true
	}
	record.Duration = time.Since(started)
	return record
}

func errorContent(code, message string) []byte {
	// 仅序列化字符串字段，不存在不支持的值类型。
	var b strings.Builder
	enc := json.NewEncoder(&b)
	if err := enc.Encode(map[string]any{"ok": false, "error": map[string]string{"code": code, "message": message}}); err != nil {
		return []byte(`{"ok":false,"error":{"code":"serialization_failed","message":"cannot encode tool error"}}`)
	}
	return []byte(strings.TrimSuffix(b.String(), "\n"))
}

func addUsage(total *openai.Usage, usage openai.Usage) {
	total.PromptTokens += usage.PromptTokens
	total.CompletionTokens += usage.CompletionTokens
	total.TotalTokens += usage.TotalTokens
}
