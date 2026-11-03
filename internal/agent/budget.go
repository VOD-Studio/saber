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
type ledger struct {
	mu           sync.Mutex
	max          int
	accounted    int
	reservations int
	estimated    bool
	reported     openai.Usage
	summary      openai.Usage
	evaluation   openai.Usage
}

func newLedger(max int) *ledger { return &ledger{max: max} }

func (l *ledger) Admit(req Request) (Request, error) {
	if l == nil || l.max <= 0 {
		return req, nil
	}
	estimate := requestInputBytes(req)
	l.mu.Lock()
	defer l.mu.Unlock()
	remaining := l.max - l.accounted - l.reservations
	if remaining <= 0 {
		return req, fmt.Errorf("%w: token budget exhausted", ErrBudgetExhausted)
	}
	output := req.MaxTokens
	if estimate >= remaining {
		return req, fmt.Errorf("%w: input estimate exceeds remaining token budget", ErrBudgetExhausted)
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
	req.MaxTokens = output
	l.reservations += estimate + output
	return req, nil
}

func (l *ledger) Record(req Request, response Response) {
	if l == nil || l.max <= 0 {
		return
	}
	charge, estimated := chargeFor(req, response)
	l.mu.Lock()
	defer l.mu.Unlock()
	reserved := requestInputBytes(req) + req.MaxTokens
	l.reservations = max(0, l.reservations-reserved)
	l.accounted += charge
	l.estimated = l.estimated || estimated
	addBudgetUsage(&l.reported, response.Usage)
	switch req.Purpose {
	case "summary":
		addBudgetUsage(&l.summary, response.Usage)
	case "evaluation":
		addBudgetUsage(&l.evaluation, response.Usage)
	}
}

func (l *ledger) stats(rounds, soft, hard, extensions int, stopReason string) *BudgetStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return &BudgetStats{
		RoundsUsed:      rounds,
		SoftLimit:       soft,
		HardLimit:       hard,
		Extensions:      extensions,
		AccountedTokens: l.accounted,
		ReportedUsage:   l.reported,
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

func (l *ledger) reportedUsage() openai.Usage {
	if l == nil {
		return openai.Usage{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reported
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
	defer l.mu.Unlock()
	return l.max - l.accounted - l.reservations
}

func requestInputBytes(req Request) int {
	encoded, err := json.Marshal(struct {
		Messages []openai.ChatCompletionMessage
		Tools    []openai.Tool
		History  map[string][]json.RawMessage
	}{req.Messages, req.Tools, req.ResponsesHistory})
	if err != nil {
		return 0
	}
	return len(encoded)
}

// chargeFor 优先使用提供商原始 total，其次 prompt+completion，最后按字节保守估算。
func chargeFor(req Request, response Response) (int, bool) {
	usage := response.Usage
	if usage.TotalTokens > 0 || usage.PromptTokens > 0 || usage.CompletionTokens > 0 {
		return max(usage.TotalTokens, usage.PromptTokens+usage.CompletionTokens), false
	}
	charge := requestInputBytes(req) + len(response.Content) + len(response.Thinking)
	for _, call := range response.ToolCalls {
		charge += len(call.Function.Name) + len(call.Function.Arguments)
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
