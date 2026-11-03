package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
)

// BudgetDecision 是评估模型给出的可观察建议；追加量始终由程序决定。
type BudgetDecision struct {
	Continue       bool     `json:"continue"`
	Reason         string   `json:"reason"`
	RemainingSteps []string `json:"remaining_steps"`
}

// BudgetProgress 是评估器可见的最小事实集合。
type BudgetProgress struct {
	State            TaskState     `json:"state"`
	Recent           []Round       `json:"recent"`
	RoundsUsed       int           `json:"rounds_used"`
	SoftLimit        int           `json:"soft_limit"`
	HardLimit        int           `json:"hard_limit"`
	AccountedTokens  int           `json:"accounted_tokens"`
	Elapsed          time.Duration `json:"elapsed"`
	RunningProcesses int           `json:"running_processes"`
}

// BudgetEvaluator 判断是否追加轮数；只把 Continue 当建议。
type BudgetEvaluator func(context.Context, BudgetProgress) (BudgetDecision, error)

// BudgetStats 是公开到事件与任务终态的预算快照。
type BudgetStats struct {
	RoundsUsed      int          `json:"rounds_used"`
	SoftLimit       int          `json:"soft_limit"`
	HardLimit       int          `json:"hard_limit"`
	Extensions      int          `json:"extensions"`
	AccountedTokens int          `json:"accounted_tokens"`
	ReportedUsage   openai.Usage `json:"reported_usage"`
	MainUsage       openai.Usage `json:"main_usage"`
	AuxUsage        openai.Usage `json:"aux_usage"`
	SummaryUsage    openai.Usage `json:"summary_usage"`
	EvaluationUsage openai.Usage `json:"evaluation_usage"`
	Estimated       bool         `json:"estimated"`
	StopReason      string       `json:"stop_reason,omitempty"`
}

// AttemptGate 在每个真实模型请求前预留输出预算、完成后记账。
type AttemptGate interface {
	Admit(Request) (Request, error)
	Record(Request, Response)
}

// bytesPerToken 是预算预留使用的保守比率：4 个 UTF-8 字节视为 1 token。
// 它只用于请求前的额度预留；真实消耗以提供商返回的 usage 为准。
const bytesPerToken = 4

// chargeSink 是嵌套运行向父账本预留额度、上推实际消耗的最小接口。
// 子运行用 reserve/release 让父账本在请求前就锁定并发额度，只读余额无法阻止
// 多个子运行同时看到同一份剩余量。
type chargeSink interface {
	charge(tokens int, estimated bool, usage openai.Usage, purpose string)
	reserve(tokens int) error
	release(tokens int)
	remaining() int
}

type gateKey struct{}

// WithAttemptGate 把同一个账本附着到模型、摘要与评估请求。
func WithAttemptGate(ctx context.Context, gate AttemptGate) context.Context {
	return context.WithValue(ctx, gateKey{}, gate)
}

// AttemptGateFromContext 返回当前运行共享的预算入口。
func AttemptGateFromContext(ctx context.Context) (AttemptGate, bool) {
	gate, ok := ctx.Value(gateKey{}).(AttemptGate)
	return gate, ok
}

// ledger 是 Runtime 私有账本，记录预留、实际 charge 与按用途的原始用量。
// 所有计数都以 token 为单位：预留用估算，记账优先用提供商 usage。
type ledger struct {
	mu           sync.Mutex
	max          int
	accounted    int
	reservations int
	estimated    bool
	reported     openai.Usage
	main         openai.Usage
	summary      openai.Usage
	evaluation   openai.Usage
	// parent 是嵌套运行的父账本；子运行的每笔实际消耗都上推到这里。
	parent chargeSink
}

func newLedger(max int) *ledger { return &ledger{max: max} }

func (l *ledger) Admit(req Request) (Request, error) {
	if l == nil || l.max <= 0 {
		return req, nil
	}
	estimate := estimateInputTokens(req)
	l.mu.Lock()
	defer l.mu.Unlock()
	remaining := l.max - l.accounted - l.reservations
	if remaining <= 0 {
		return req, fmt.Errorf("%w: token budget exhausted", ErrBudgetExhausted)
	}
	output := req.MaxTokens
	if estimate >= remaining {
		return req, fmt.Errorf("%w: estimated input of %d tokens exceeds remaining token budget of %d", ErrBudgetExhausted, estimate, remaining)
	}
	// MaxTokens 为 0 表示不发送输出上限：只预留输入，实际用量仍由 Record 记账。
	// 单次输出无法在请求前封顶，超量只会在后续请求被拒。
	if output < 0 {
		output = 0
	}
	if output > 0 {
		if allowed := remaining - estimate; output > allowed {
			output = allowed
		}
	}
	reserved := estimate + output
	// 父账本必须与本账本一起原子预留：只读父余额时，并发子运行会同时看到同一份额度，
	// 各自放行后在 Record 阶段共同超额。锁顺序恒为子账本 → 父账本，父账本不会反向持锁。
	if l.parent != nil {
		if err := l.parent.reserve(reserved); err != nil {
			return req, err
		}
	}
	req.MaxTokens = output
	req.reservedTokens = reserved
	l.reservations += reserved
	return req, nil
}

// Record 先释放 Admit 为本次请求预留的额度，再记录真实消耗。
// 释放量按同一估算器与 Admit 返回的 MaxTokens 重算；请求在 Admit 之后被修改时两者
// 允许不同，此时以重算值为准，reservations 只保证不为负。实际消耗永远记账，
// 即使超过预留也不回滚已完成的请求，只让后续 Admit 失败。
func (l *ledger) Record(req Request, response Response) {
	if l == nil || l.max <= 0 {
		return
	}
	charge, estimated := chargeFor(req, response)
	reserved := req.reservedTokens
	l.mu.Lock()
	l.reservations = max(0, l.reservations-reserved)
	l.mu.Unlock()
	// 父账本按同一预留量归还，再按真实消耗记账。Admit 成功但请求始终没走到 Record
	// 时，预留会一直占着额度；这与自身账本的既有行为一致，靠父运行结束回收。
	if l.parent != nil && reserved > 0 {
		l.parent.release(reserved)
	}
	l.charge(charge, estimated, response.Usage, req.Purpose)
}

// reserve 在账本内原子预留额度，失败时返回包装后的 ErrBudgetExhausted。
func (l *ledger) reserve(tokens int) error {
	if l == nil || l.max <= 0 || tokens <= 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.max-l.accounted-l.reservations < tokens {
		return fmt.Errorf("%w: token budget exhausted", ErrBudgetExhausted)
	}
	l.reservations += tokens
	return nil
}

// release 归还 reserve 预留但未实际消耗的额度，下限归零。
func (l *ledger) release(tokens int) {
	if l == nil || l.max <= 0 || tokens <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reservations = max(0, l.reservations-tokens)
}

// charge 记录一笔已在别处预留过的实际消耗，并把同一笔消耗上推给父账本。
func (l *ledger) charge(tokens int, estimated bool, usage openai.Usage, purpose string) {
	if l == nil || l.max <= 0 {
		return
	}
	l.mu.Lock()
	l.accounted += tokens
	l.estimated = l.estimated || estimated
	addBudgetUsage(&l.reported, usage)
	switch purpose {
	case "summary":
		addBudgetUsage(&l.summary, usage)
	case "evaluation":
		addBudgetUsage(&l.evaluation, usage)
	default:
		addBudgetUsage(&l.main, usage)
	}
	parent := l.parent
	l.mu.Unlock()
	if parent != nil {
		parent.charge(tokens, estimated, usage, purpose)
	}
}

func (l *ledger) stats(rounds, soft, hard, extensions int, stopReason string) *BudgetStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	aux := l.summary
	addBudgetUsage(&aux, l.evaluation)
	return &BudgetStats{
		RoundsUsed:      rounds,
		SoftLimit:       soft,
		HardLimit:       hard,
		Extensions:      extensions,
		AccountedTokens: l.accounted,
		ReportedUsage:   l.reported,
		MainUsage:       l.main,
		AuxUsage:        aux,
		SummaryUsage:    l.summary,
		EvaluationUsage: l.evaluation,
		Estimated:       l.estimated,
		StopReason:      stopReason,
	}
}

func (l *ledger) accountedTokens() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.accounted
}

// mainUsage 只返回主请求用量，摘要与评估单独统计。
func (l *ledger) mainUsage() openai.Usage {
	if l == nil {
		return openai.Usage{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.main
}

func budgetSnapshot(led *ledger, rounds, soft, hard, extensions int, reason string) *BudgetStats {
	if led == nil {
		return &BudgetStats{RoundsUsed: rounds, SoftLimit: soft, HardLimit: hard, Extensions: extensions, StopReason: reason}
	}
	return led.stats(rounds, soft, hard, extensions, reason)
}

func (l *ledger) remaining() int {
	if l == nil || l.max <= 0 {
		return math.MaxInt
	}
	l.mu.Lock()
	left := l.max - l.accounted - l.reservations
	l.mu.Unlock()
	// 子运行的可用量还受父账本余额限制；父账本先读后释放本账本锁。
	if l.parent != nil {
		if parentLeft := l.parent.remaining(); parentLeft < left {
			left = parentLeft
		}
	}
	return left
}

// estimateTextTokens 按 UTF-8 字节数向上取整估算 token 数。
func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + bytesPerToken - 1) / bytesPerToken
}

// estimateSchemaTokens 估算单个 schema 值；字符串与原始 JSON 直接计数，
// 其他类型只序列化这一个字段，不重新序列化整个请求。
func estimateSchemaTokens(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case string:
		return estimateTextTokens(typed)
	case json.RawMessage:
		return estimateTextTokens(string(typed))
	case []byte:
		return estimateTextTokens(string(typed))
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return 0
		}
		return estimateTextTokens(string(encoded))
	}
}

// estimateMessageTokens 逐字段估算一条消息的 token 数。
func estimateMessageTokens(message openai.ChatCompletionMessage) int {
	tokens := estimateTextTokens(message.Role) + estimateTextTokens(message.Content) +
		estimateTextTokens(message.Refusal) + estimateTextTokens(message.Name) +
		estimateTextTokens(message.ReasoningContent) + estimateTextTokens(message.ToolCallID)
	for _, part := range message.MultiContent {
		tokens += estimateTextTokens(string(part.Type)) + estimateTextTokens(part.Text)
		if part.ImageURL != nil {
			tokens += estimateTextTokens(part.ImageURL.URL) + estimateTextTokens(string(part.ImageURL.Detail))
		}
	}
	if message.FunctionCall != nil {
		tokens += estimateTextTokens(message.FunctionCall.Name) + estimateTextTokens(message.FunctionCall.Arguments)
	}
	for _, call := range message.ToolCalls {
		tokens += estimateTextTokens(string(call.Type)) + estimateTextTokens(call.ID) +
			estimateTextTokens(call.Function.Name) + estimateTextTokens(call.Function.Arguments)
	}
	return tokens
}

// estimateToolTokens 估算一个工具定义（名称、描述与参数 schema）。
func estimateToolTokens(tool openai.Tool) int {
	tokens := estimateTextTokens(string(tool.Type))
	if tool.Function != nil {
		tokens += estimateTextTokens(tool.Function.Name) + estimateTextTokens(tool.Function.Description) +
			estimateSchemaTokens(tool.Function.Parameters)
	}
	for key, value := range tool.Parameters {
		tokens += estimateTextTokens(key) + estimateSchemaTokens(value)
	}
	return tokens
}

// estimateMessagesTokens 估算一组消息的 token 数。
func estimateMessagesTokens(messages []openai.ChatCompletionMessage) int {
	tokens := 0
	for _, message := range messages {
		tokens += estimateMessageTokens(message)
	}
	return tokens
}

// estimateInputTokens 估算一次请求的输入 token 数，覆盖消息、工具定义与保留的推理历史。
func estimateInputTokens(req Request) int {
	tokens := estimateMessagesTokens(req.Messages)
	for _, tool := range req.Tools {
		tokens += estimateToolTokens(tool)
	}
	for key, payloads := range req.ResponsesHistory {
		tokens += estimateTextTokens(key)
		for _, payload := range payloads {
			tokens += estimateTextTokens(string(payload))
		}
	}
	return tokens
}

// chargeFor 优先使用提供商原始 total，其次 prompt+completion，最后按同一估算器估算。
func chargeFor(req Request, response Response) (int, bool) {
	usage := response.Usage
	if usage.TotalTokens > 0 || usage.PromptTokens > 0 || usage.CompletionTokens > 0 {
		return max(usage.TotalTokens, usage.PromptTokens+usage.CompletionTokens), false
	}
	charge := estimateInputTokens(req) + estimateTextTokens(response.Content) + estimateTextTokens(response.Thinking)
	for _, call := range response.ToolCalls {
		charge += estimateTextTokens(call.Function.Name) + estimateTextTokens(call.Function.Arguments)
	}
	return charge, true
}

func addBudgetUsage(total *openai.Usage, usage openai.Usage) {
	total.PromptTokens += usage.PromptTokens
	total.CompletionTokens += usage.CompletionTokens
	total.TotalTokens += usage.TotalTokens
}

// evaluateBudget 先做确定性否决，再请求一次无工具评估。
func (r Runtime) evaluateBudget(ctx context.Context, progress BudgetProgress, led *ledger) (BudgetDecision, string) {
	if err := ctx.Err(); err != nil {
		return BudgetDecision{}, err.Error()
	}
	if led != nil && led.remaining() <= 0 {
		return BudgetDecision{}, "token budget exhausted"
	}
	if progress.RunningProcesses == 0 && exhaustedToolLoop(progress.Recent) {
		return BudgetDecision{}, "recent tool calls produced identical results without progress"
	}
	decision, err := r.Evaluate(ctx, progress)
	if err != nil {
		return BudgetDecision{}, "budget evaluation failed: " + err.Error()
	}
	if decision.Reason == "" {
		return BudgetDecision{}, "budget evaluation returned no reason"
	}
	if !decision.Continue {
		return decision, decision.Reason
	}
	return decision, ""
}

// exhaustedToolLoop 检查最近三次同名、同参数且实质输出相同的工具调用。
func exhaustedToolLoop(rounds []Round) bool {
	var records []ToolRecord
	for _, round := range rounds {
		records = append(records, round.Tools...)
	}
	if len(records) < 3 {
		return false
	}
	last := records[len(records)-3:]
	key := toolSignature(last[0])
	if key == "" {
		return false
	}
	for _, record := range last[1:] {
		if toolSignature(record) != key {
			return false
		}
	}
	return true
}

func toolSignature(record ToolRecord) string {
	if record.Call.ID == "" || record.Call.Function.Name == "" {
		return ""
	}
	var args any
	if err := json.Unmarshal([]byte(record.Call.Function.Arguments), &args); err != nil {
		return ""
	}
	normalized, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	output := normalizeToolOutput(record.Content)
	return record.Call.Function.Name + "\x00" + string(normalized) + "\x00" + output
}

// normalizeToolOutput 去掉日志句柄、路径、耗时等易变元数据。
func normalizeToolOutput(content string) string {
	var value any
	if err := json.Unmarshal([]byte(content), &value); err != nil {
		return content
	}
	var strip func(any) any
	strip = func(node any) any {
		switch typed := node.(type) {
		case map[string]any:
			out := make(map[string]any, len(typed))
			for key, item := range typed {
				switch key {
				case "log_path", "output_id", "duration", "next_offset", "next_byte_offset", "offset", "started_at", "revision":
					continue
				}
				out[key] = strip(item)
			}
			return out
		case []any:
			out := make([]any, len(typed))
			for i, item := range typed {
				out[i] = strip(item)
			}
			return out
		default:
			return typed
		}
	}
	encoded, err := json.Marshal(strip(value))
	if err != nil {
		return content
	}
	return string(encoded)
}

// ParseBudgetDecision 从评估响应中提取严格 JSON；允许外层代码块。
func ParseBudgetDecision(content string) (BudgetDecision, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return BudgetDecision{}, errors.New("evaluation response has no JSON object")
	}
	var decision BudgetDecision
	if err := json.Unmarshal([]byte(content[start:end+1]), &decision); err != nil {
		return BudgetDecision{}, err
	}
	return decision, nil
}
