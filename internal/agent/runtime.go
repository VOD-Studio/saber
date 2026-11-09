package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
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
	// Context 在每轮模型请求前裁剪历史，包含后续工具结果。
	Context ContextPolicy
	// Summarize 在带任务状态的运行中压缩超预算上下文；为空时不做压缩。
	Summarize SummaryFunc
	// Evaluate 在软预算将尽时评估是否追加轮数；为空时软限等于硬限。
	Evaluate BudgetEvaluator
	// RunningProcesses 返回当前任务仍在运行的后台作业数。
	RunningProcesses func() int
	// ParallelTool 返回同名工具在同一轮的并发上限；<=1（默认）表示按模型顺序串行执行。
	// 只有返回 >1 的工具会被批量并发执行，结果仍按模型给出的调用顺序回填；
	// 其余工具保持原有串行语义与事件顺序。
	ParallelTool func(name string) int
	// Steering 在每轮上下文压缩完成后、发送模型请求前返回本轮补充要求。
	// 每条文本以 user 消息追加到本轮请求并发出一条 Steered 事件；空文本忽略，
	// 同一运行内相同文本只注入一次。为空时改用 WithSteering 绑定的来源。
	Steering func() []string
	// ChildResults 在每轮请求前领取已持久化的子任务结果；失败会停止运行而不是丢失结果。
	ChildResults func(context.Context) ([]ChildCompletion, error)
	// Completion 读取任务权威完成请求；只在完整工具批次之后进入正常完成终态。
	Completion func(context.Context) (string, bool, error)
	// Notifications 领取真实后台通知；最终回答前 wait=true 等待已经发起的诊断。
	Notifications func(context.Context, bool) ([]string, error)
	// BeforeRun 在预算和时限已经生效后注入受信生命周期上下文。
	BeforeRun func(context.Context, Request) (Request, error)
	// AfterRun 在环境仍活跃且父预算仍有效时完成持久质量门与记忆同步。
	AfterRun func(context.Context, Request, Result, error) error
	// OnCompaction 在成功压缩提交前撤销已遗忘的全文覆盖授权。
	OnCompaction func(context.Context) error
}

// Run 顺序执行模型与工具，任何终态都返回已有记录并发出一次 RunFinished。
// 取消后不再调度新请求；已派发操作依赖 adapter 响应 context，无法撤销远端副作用。
// emit 可为空；非空时必须及时返回，否则会阻塞当前运行。
func (r Runtime) Run(ctx context.Context, req Request, emit func(Event)) (result Result, err error) {
	started := time.Now()
	if req.SubagentDepth > 0 {
		ctx = WithSubagentDepth(ctx, req.SubagentDepth)
	}
	if req.ParentAttemptGate != nil {
		ctx = WithAttemptGate(ctx, req.ParentAttemptGate)
	}
	if req.ToolScope != nil || req.SubagentDepth > 0 {
		ctx = WithToolScope(ctx, req.ToolScope)
	}
	if emit == nil {
		emit = func(Event) {}
	}
	// 并行工具与子代理事件可能来自多个 goroutine，事件出口必须串行；
	// 串行化后仍按调用顺序回填工具结果，事件顺序可预测。
	raw := emit
	var emitMu sync.Mutex
	serialized := func(event Event) {
		emitMu.Lock()
		defer emitMu.Unlock()
		raw(event)
	}
	emit = serialized
	ctx = withEmitter(ctx, serialized)
	result.Status = Failed
	var finalState *TaskState
	var checkpoint *Checkpoint
	defer func() {
		result.Duration = time.Since(started)
		result.State = CloneTaskState(finalState)
		result.Checkpoint = checkpoint
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
	hard := limits.MaxRounds
	soft := hard
	if r.Evaluate != nil {
		soft = limits.InitialRounds
		if soft <= 0 {
			soft = min(5, hard)
		}
		if soft > hard {
			soft = hard
		}
	}
	extension := limits.ExtensionRounds
	if extension <= 0 {
		extension = 5
	}
	var led *ledger
	if limits.MaxTotalTokens > 0 {
		led = newLedger(limits.MaxTotalTokens)
		// 嵌套运行（子代理）不能替换 ctx 里的父账本，否则父运行看不到子运行消耗。
		// 子运行保留自己的上限，并把每笔实际消耗上推给父账本。
		if existing, ok := AttemptGateFromContext(ctx); ok {
			if parent, ok := existing.(chargeSink); ok {
				led.parent = parent
			}
		}
		ctx = WithAttemptGate(ctx, led)
	}
	extensions := 0
	stopReason := ""
	defer func() {
		result.Budget = budgetSnapshot(led, len(result.Rounds), soft, hard, extensions, stopReason)
		if led != nil {
			// Result.Usage 只表示主请求用量；摘要与评估用量保留在 Budget 中。
			result.Usage = led.mainUsage()
		}
	}()
	defer func() {
		if r.AfterRun != nil {
			snapshot := result
			snapshot.State, snapshot.Checkpoint = CloneTaskState(finalState), checkpoint
			snapshot.Duration = time.Since(started)
			snapshot.Budget = budgetSnapshot(led, len(result.Rounds), soft, hard, extensions, stopReason)
			if hookErr := r.AfterRun(ctx, req, snapshot, err); hookErr != nil {
				err = errors.Join(err, hookErr)
				if result.Status == Completed {
					result.Status = Failed
				}
			}
		}
	}()
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
	if r.BeforeRun != nil {
		req, err = r.BeforeRun(ctx, req)
		if err != nil {
			return stop(err)
		}
	}
	if r.Model == nil {
		return stop(errors.New("agent model is not configured"))
	}
	outputSchema, schemaErr := ResolveOutputSchema(req.OutputSchema)
	if schemaErr != nil {
		return stop(schemaErr)
	}
	schemaRetried := false
	req.Tools = scopedTools(ctx, req.Tools)
	choice, choiceErr := toolChoiceConstraint(req.ToolChoice, req.Tools)
	if choiceErr != nil {
		return stop(choiceErr)
	}
	if choice.mode == "none" {
		req.Tools = nil
	}
	// 新建切片防止追加历史覆盖调用方预留的容量。
	req.Messages = append([]openai.ChatCompletionMessage(nil), req.Messages...)
	req.ResponsesHistory = maps.Clone(req.ResponsesHistory)
	steering := r.Steering
	// 子代理沿用父 ctx，不能消费父任务的补充要求或复用其事件轮次。
	if SubagentDepth(ctx) == 0 {
		if steering == nil {
			steering, _ = SteeringFromContext(ctx)
		}
	} else {
		steering = nil
	}
	// injected 记录本次运行已注入的文本，回调未消费时也不会跨轮重复。
	injected := make(map[string]bool)
	finalState = req.State
	compacted := 0
	var pendingCheckpoint *Checkpoint
	allowed := make(map[string]bool, len(req.Tools))
	for _, tool := range req.Tools {
		if tool.Function != nil {
			allowed[tool.Function.Name] = true
		}
	}
	for number := 1; number <= hard; number++ {
		if err := ctx.Err(); err != nil {
			return stop(err)
		}
		if r.Completion != nil {
			content, completed, completeErr := r.Completion(ctx)
			if completeErr != nil {
				return stop(completeErr)
			}
			if completed {
				result.Status, result.Content = Completed, content
				return result, nil
			}
		}
		var roundCheckpoint *Checkpoint
		req, compacted, roundCheckpoint, err = r.prepareContext(ctx, req, number, limits.MaxRounds, emit, compacted)
		if err != nil {
			return stop(err)
		}
		if roundCheckpoint != nil {
			// 检查点只有在事件落盘且 ctx 仍有效后才成为运行终态。
			pendingCheckpoint = roundCheckpoint
		}
		finalState = req.State
		if err := ctx.Err(); err != nil {
			return stop(err)
		}
		if pendingCheckpoint != nil {
			checkpoint = pendingCheckpoint
			pendingCheckpoint = nil
		}
		// 压缩完成后再注入转向，补充要求不会被当作历史摘要的输入；
		// 注入位置固定在发送模型之前，子代理与并行工具的事件顺序保持不变。
		if steering != nil {
			req.Messages = injectSteering(req.Messages, number, steering(), injected, emit)
		}
		if r.ChildResults != nil {
			completions, drainErr := r.ChildResults(ctx)
			if drainErr != nil {
				return stop(drainErr)
			}
			for _, completion := range completions {
				for _, image := range completion.Images {
					if imageErr := ValidateImageReference(image); imageErr != nil {
						return stop(imageErr)
					}
				}
			}
			if len(completions) > 0 {
				result.ChildCompletions = append(result.ChildCompletions, completions...)
				emit(Event{Kind: ChildResultsReceived, Round: number, ChildCompletions: completions})
				req.Messages = AppendChildCompletionMessages(req.Messages, completions)
			}
		}
		if r.Notifications != nil {
			notices, noticeErr := r.Notifications(ctx, false)
			if len(notices) > 0 {
				req.Messages = AppendContextNotifications(req.Messages, notices)
				result.Notifications = append(result.Notifications, notices...)
				emit(Event{Kind: ContextNotificationsReceived, Round: number, Notifications: notices})
			}
			if noticeErr != nil {
				return stop(noticeErr)
			}
		}
		emit(Event{Kind: ModelStarted, Round: number})
		if err := ctx.Err(); err != nil {
			return stop(err)
		}
		round := Round{}
		// 真正发送渲染后的输入；持久化请求保持未渲染基线，避免重复注入。
		sendReq := renderRequest(req)
		response, modelErr := r.Model(ctx, sendReq, func(event Event) {
			event.Round = number
			// adapter 只能发布模型事件，不能自行宣布运行终态。
			switch event.Kind {
			case AttemptFinished:
				round.Attempts = append(round.Attempts, event.Attempt)
			case AttemptStarted, TextDelta, ThinkingDelta:
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
		if choiceErr := validateToolChoiceResponse(choice, response); choiceErr != nil {
			return stop(choiceErr)
		}
		if len(response.ToolCalls) == 0 {
			if r.Notifications != nil {
				notices, noticeErr := r.Notifications(ctx, true)
				if len(notices) > 0 {
					req.Messages = append(req.Messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: response.Content})
					req.Messages = AppendContextNotifications(req.Messages, notices)
					result.Notifications = append(result.Notifications, notices...)
					emit(Event{Kind: ContextNotificationsReceived, Round: number, Notifications: notices})
					if noticeErr != nil {
						return stop(noticeErr)
					}
					if number == hard {
						return stop(fmt.Errorf("%w: 待消费的最终诊断或进程通知", ErrBudgetExhausted))
					}
					continue
				}
				if noticeErr != nil {
					return stop(noticeErr)
				}
			}
			if schemaErr := validateOutput(outputSchema, response.Content); schemaErr != nil {
				if schemaRetried || number == hard {
					return stop(fmt.Errorf("output_schema 校验失败: %w", schemaErr))
				}
				schemaRetried = true
				req.Messages = append(req.Messages,
					openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: response.Content},
					openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "输出契约校验失败，请仅返回符合既有 JSON Schema 的 JSON。错误: " + schemaErr.Error()})
				continue
			}
			result.Status, result.Content = Completed, response.Content
			return result, nil
		}
		if number == hard {
			stopReason = "hard round limit reached"
			return stop(ErrBudgetExhausted)
		}
		if r.Evaluate != nil && number >= soft {
			progress := BudgetProgress{RoundsUsed: number, SoftLimit: soft, HardLimit: hard, Elapsed: time.Since(started)}
			if req.State != nil {
				progress.State = *CloneTaskState(req.State)
			}
			completed := result.Rounds
			if len(completed) > 0 {
				completed = completed[:len(completed)-1]
			}
			// 最近三轮已完成轨迹之后追加当前轮：本轮只带着模型刚请求的工具调用，
			// 结果尚未产生。评估器需要它判断「下一步」是否存在，不能把未执行的调用当成停滞。
			recent := completed[max(0, len(completed)-3):]
			progress.Recent = append(append([]Round(nil), recent...), Round{Response: Response{
				Content: response.Content, ToolCalls: response.ToolCalls,
			}})
			if led != nil {
				progress.AccountedTokens = led.accountedTokens()
			}
			if r.RunningProcesses != nil {
				progress.RunningProcesses = r.RunningProcesses()
			}
			if _, reason := r.evaluateBudget(ctx, progress, led); reason != "" {
				stopReason = reason
				emit(Event{Kind: BudgetStopped, Round: number, Budget: budgetSnapshot(led, number, soft, hard, extensions, reason)})
				return stop(fmt.Errorf("%w: %s", ErrBudgetExhausted, reason))
			}
			soft = min(hard, soft+extension)
			extensions++
			emit(Event{Kind: BudgetExtended, Round: number, Budget: budgetSnapshot(led, number, soft, hard, extensions, "")})
		}
		if choice.mode == "required" || choice.mode == "function" {
			// 首个模型回复满足调用约束后，内部续轮需能正常给出最终回答。
			req.ToolChoice, choice = "auto", choiceConstraint{mode: "auto"}
		}
		if len(response.ResponsesOutput) > 0 {
			if req.ResponsesHistory == nil {
				req.ResponsesHistory = make(map[string][]json.RawMessage)
			}
			req.ResponsesHistory[response.ToolCalls[0].ID] = response.ResponsesOutput
		}
		req.Messages = append(req.Messages, openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant, Content: response.Content, ToolCalls: response.ToolCalls,
		})
		// 子代理事件统一带上当前轮次，事件消费者无需推断所属轮。
		ctx = withEmitter(ctx, func(event Event) {
			if event.Round == 0 {
				event.Round = number
			}
			serialized(event)
		})
		for index := 0; index < len(response.ToolCalls); {
			if err := ctx.Err(); err != nil {
				return stop(err)
			}
			call := response.ToolCalls[index]
			parallel := r.parallelLimit(call.Function.Name)
			if parallel <= 1 {
				emit(Event{Kind: ToolStarted, Round: number, Tool: ToolRecord{Call: call}})
				if err := ctx.Err(); err != nil {
					return stop(err)
				}
				var record ToolRecord
				if call.Function.Name == "saber_task_state" && req.State != nil && allowed[call.Function.Name] && ToolAllowed(ctx, call.Function.Name) {
					record = r.callStateTool(ctx, call, &req.State, number, limits.MaxToolOutputBytes, emit)
				} else {
					record = r.callTool(ctx, call, allowed, limits.MaxToolOutputBytes)
				}
				finalState = req.State
				result.Rounds[number-1].Tools = append(result.Rounds[number-1].Tools, record)
				req.Messages = append(req.Messages, openai.ChatCompletionMessage{
					Role: openai.ChatMessageRoleTool, ToolCallID: call.ID, Content: record.Content,
				})
				emit(Event{Kind: ToolFinished, Round: number, Tool: record})
				if err := ctx.Err(); err != nil {
					return stop(err)
				}
				index++
				continue
			}
			// 同一轮连续的同类并行调用一起派发；结果按模型给出的顺序回填。
			end := index + 1
			for end < len(response.ToolCalls) && response.ToolCalls[end].Function.Name == call.Function.Name &&
				r.parallelLimit(response.ToolCalls[end].Function.Name) > 1 {
				end++
			}
			batch := response.ToolCalls[index:end]
			index = end
			for _, item := range batch {
				emit(Event{Kind: ToolStarted, Round: number, Tool: ToolRecord{Call: item}})
			}
			if err := ctx.Err(); err != nil {
				return stop(err)
			}
			records := r.callToolBatch(ctx, batch, allowed, limits.MaxToolOutputBytes, parallel)
			finalState = req.State
			for offset, record := range records {
				result.Rounds[number-1].Tools = append(result.Rounds[number-1].Tools, record)
				req.Messages = append(req.Messages, openai.ChatCompletionMessage{
					Role: openai.ChatMessageRoleTool, ToolCallID: batch[offset].ID, Content: record.Content,
				})
				emit(Event{Kind: ToolFinished, Round: number, Tool: record})
			}
			if err := ctx.Err(); err != nil {
				return stop(err)
			}
		}
		// 并行工具批次必须全部回复后再追加图片 user 消息，否则破坏 assistant/tool 配对。
		req.Messages = appendToolImages(req.Messages, result.Rounds[number-1].Tools)
	}
	return stop(ErrBudgetExhausted)
}

// injectSteering 把本轮补充要求追加到模型历史并发出一条 Steered 事件。
// 空文本不注入也不产生事件；同一运行内已注入的文本只注入一次。
func injectSteering(messages []openai.ChatCompletionMessage, round int, texts []string, injected map[string]bool, emit func(Event)) []openai.ChatCompletionMessage {
	for _, text := range texts {
		text = strings.TrimSpace(text)
		if text == "" || injected[text] {
			continue
		}
		injected[text] = true
		messages = append(messages, openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleUser, Content: "[运行中补充要求] " + text,
		})
		emit(Event{Kind: Steered, Round: round, Text: text})
	}
	return messages
}

type steeringKey struct{}

// WithSteering 给 ctx 绑定运行中补充要求的读取回调；Runtime.Steering 为空时使用。
// 用于只能在 ctx 上挂载依赖的调用方，多个运行互不共享状态。
func WithSteering(ctx context.Context, steering func() []string) context.Context {
	if steering == nil {
		return ctx
	}
	return context.WithValue(ctx, steeringKey{}, steering)
}

// SteeringFromContext 读取 WithSteering 绑定的回调。
func SteeringFromContext(ctx context.Context) (func() []string, bool) {
	steering, ok := ctx.Value(steeringKey{}).(func() []string)
	return steering, ok && steering != nil
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

// parallelLimit 返回工具在同一轮的并发上限；状态工具与未声明并行的工具始终串行。
func (r Runtime) parallelLimit(name string) int {
	if name == "saber_task_state" || r.ParallelTool == nil {
		return 1
	}
	return r.ParallelTool(name)
}

// callToolBatch 并发执行同名工具调用，最多 parallel 个同时运行；结果按入参顺序返回。
// 事件出口已由 Run 串行化，工具内部只能通过 ctx 携带的出口发事件。
func (r Runtime) callToolBatch(ctx context.Context, calls []openai.ToolCall, allowed map[string]bool, limit, parallel int) []ToolRecord {
	records := make([]ToolRecord, len(calls))
	semaphore := make(chan struct{}, max(1, parallel))
	var wait sync.WaitGroup
	for index, call := range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			records[index] = r.callTool(ctx, call, allowed, limit)
		}()
	}
	wait.Wait()
	return records
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
	record.OutputID = output.OutputID
	if err == nil {
		record.Files = append([]string(nil), output.Files...)
		for _, ref := range output.Images {
			if imageErr := ValidateImageReference(ref); imageErr != nil {
				record.ErrorCode, err = "invalid_tool_image", imageErr
				break
			}
		}
		if err == nil {
			record.Images = append([]string(nil), output.Images...)
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
	originalBytes := len(data)
	budgetError := false
	if output.Structured && err == nil && !output.IsError && originalBytes > limit {
		// 结构化结果超预算时用可回读句柄替代正文，绝不返回被截断的 JSON。
		budgetError = true
		if output.OutputID == "" {
			record.ErrorCode = "tool_result_exceeds_context_budget"
			data = []byte(`{"ok":false,"error":{"code":"tool_result_exceeds_context_budget","message":"use output_id"}}`)
		} else {
			fallback, marshalErr := json.Marshal(map[string]string{"error": "tool_result_exceeds_context_budget", "output_id": output.OutputID})
			if marshalErr != nil || len(fallback) > limit {
				record.ErrorCode = "tool_result_exceeds_context_budget"
				data = []byte(`{"ok":false,"error":{"code":"tool_result_exceeds_context_budget","message":"use output_id"}}`)
			} else {
				data = fallback
				record.Truncated = true
			}
		}
	}
	record.OriginalBytes = originalBytes
	record.Content = string(data)
	if len(data) > limit && !budgetError {
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
