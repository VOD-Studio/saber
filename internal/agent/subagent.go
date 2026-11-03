package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sashabaranov/go-openai"
)

// DelegateTool 是内置子代理工具名；只有顶层运行会向模型暴露它。
const DelegateTool = "delegate"

// 子代理默认值与上限；config 包的同名常量必须保持一致。
const (
	// DefaultSubagentMaxRounds 是子代理的默认轮数上限。
	DefaultSubagentMaxRounds = 12
	// MaxSubagentMaxRounds 是子代理轮数上限的配置天花板。
	MaxSubagentMaxRounds = 40
	// DefaultSubagentMaxParallel 是同一轮并发子代理数的默认值。
	DefaultSubagentMaxParallel = 4
	// MaxSubagentMaxParallel 是同一轮并发子代理数的配置天花板。
	MaxSubagentMaxParallel = 16
	// MaxSubagentGoalBytes 是单个 goal 的最大字节数。
	MaxSubagentGoalBytes = 8192
)

// subagentSystemPrompt 是子代理唯一的系统消息。
// 子代理不继承父聊天的提示词与历史，避免把父任务的计划当成自己的约束。
const subagentSystemPrompt = `你是 Saber 的子代理，只完成用户消息给出的单一目标。
- 你看不到父任务的历史、其他子代理的工作和父任务的工具结果；需要的事实请自己取得。
- 只做当前目标，不要扩大任务范围，不要复述或续写父任务的计划。
- 禁止再次委派：delegate 工具不存在，不要请求它。
- 只使用本次提供的工具；完成后用最终答复简洁总结结果、改动的文件和未完成的点。`

// SubagentPolicy 控制子代理的轮数、用量与并发上限。
type SubagentPolicy struct {
	// MaxRounds 是子运行的硬轮数上限；零值用 DefaultSubagentMaxRounds，上限 MaxSubagentMaxRounds。
	MaxRounds int
	// MaxTotalTokens 是子运行自己的 token 账本上限；零值表示只受父账本约束。
	MaxTotalTokens int
	// MaxParallel 是同一轮并发子代理数上限；零值用 DefaultSubagentMaxParallel，上限 MaxSubagentMaxParallel。
	MaxParallel int
}

// Normalize 补齐零值并把越界值钳制到允许范围；负数按零值处理。
func (p SubagentPolicy) Normalize() SubagentPolicy {
	if p.MaxRounds <= 0 {
		p.MaxRounds = DefaultSubagentMaxRounds
	}
	p.MaxRounds = min(p.MaxRounds, MaxSubagentMaxRounds)
	if p.MaxParallel <= 0 {
		p.MaxParallel = DefaultSubagentMaxParallel
	}
	p.MaxParallel = min(p.MaxParallel, MaxSubagentMaxParallel)
	if p.MaxTotalTokens < 0 {
		p.MaxTotalTokens = 0
	}
	return p
}

// SubagentHooks 让调用方把子代理生命周期写入父任务的持久化存储。
// 两个回调都会在子代理并发执行时被调用，实现必须并发安全；ctx 已脱离父取消。
type SubagentHooks struct {
	// Start 写入启动记录并返回可用于 Finish 的编号；返回 0 表示没有持久化记录。
	Start func(ctx context.Context, goal string) (int64, error)
	// Finish 写入终态、结果与用量；Start 返回 0 时不会被调用。
	Finish func(ctx context.Context, id int64, status Status, result string, usage openai.Usage)
}

// SubagentInfo 是子代理生命周期事件的最小事实，供事件消费者渲染或持久化。
type SubagentInfo struct {
	// ID 是父任务存储中的子代理编号；未持久化时为 0。
	ID int64 `json:"id"`
	// Goal 是父模型给出的目标原文。
	Goal string `json:"goal"`
	// Status 只在结束事件中设置。
	Status Status `json:"status,omitempty"`
	// Result 是子代理的最终答复；失败时为空。
	Result string `json:"result,omitempty"`
	// Usage 是子运行报告的主请求用量。
	Usage openai.Usage `json:"usage,omitempty"`
	// Error 是子运行终止原因；正常完成时为空。
	Error string `json:"error,omitempty"`
}

// SubagentResult 是 delegate 返回给父模型的结构化结果。
// 子代理失败也是可读结果，父模型据此决定下一步，而不是让父运行失败。
type SubagentResult struct {
	ID      int64        `json:"id"`
	Status  Status       `json:"status"`
	Error   string       `json:"error,omitempty"`
	Content string       `json:"content,omitempty"`
	Usage   openai.Usage `json:"usage"`
	Rounds  int          `json:"rounds"`
}

// WorkspaceLease 按工作区串行化工具执行。
// 锁只包住真实工具调用，模型请求不加锁，因此多个子代理可以并行思考，但不会并发操作
// 同一环境。零值可用；同一个 Lease 应在一次父运行内的所有子代理之间共享。
type WorkspaceLease struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Lock 取得 key 对应的互斥锁并返回解锁函数；key 为空时直接返回空解锁。
func (l *WorkspaceLease) Lock(key string) func() {
	if l == nil || key == "" {
		return func() {}
	}
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*sync.Mutex)
	}
	lock, ok := l.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		l.locks[key] = lock
	}
	l.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// Delegator 在父运行内执行 delegate 调用；每个调用都是一次完整子 Runtime。
// 构造完成后字段不应修改；Delegate 可以被多个并行工具调用同时调用。
type Delegator struct {
	// Policy 是子运行上限；零值字段由 Normalize 补齐。
	Policy SubagentPolicy
	// Model 是父运行使用的模型入口；子运行沿用同一模型名与重试策略。
	Model ModelFunc
	// Execute 是父工具执行器，已携带可信身份与任务作用域。
	Execute ToolFunc
	// Parent 是父请求快照，子运行沿用它选择模型与采样设置。
	Parent Request
	// Tools 是父请求的工具快照；子代理只能从这里取工具。
	Tools []openai.Tool
	// Context 与 Summarize 沿用父运行的上下文策略。
	Context   ContextPolicy
	Summarize SummaryFunc
	// Limits 提供时限与单条工具输出上限。
	Limits Limits
	// Lease 按工作区串行化子代理的工具执行；为空表示不串行。
	Lease *WorkspaceLease
	// LeaseKey 是工作区租约键，通常是规范化后的任务目录。
	LeaseKey string
	// Hooks 持久化子代理生命周期；为空表示只在事件中可见。
	Hooks SubagentHooks
}

type subagentDepthKey struct{}
type emitterKey struct{}

// WithSubagentDepth 标注当前运行所在的委派深度；顶层运行为 0。
func WithSubagentDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, subagentDepthKey{}, depth)
}

// SubagentDepth 返回当前运行的委派深度；没有标注时为 0。
func SubagentDepth(ctx context.Context) int {
	depth, _ := ctx.Value(subagentDepthKey{}).(int)
	return depth
}

// withEmitter 把运行事件出口放进 ctx，供工具（delegate）发出子代理生命周期事件。
// 出口必须并发安全：Runtime 会在并发工具执行前把它串行化。
func withEmitter(ctx context.Context, emit func(Event)) context.Context {
	return context.WithValue(ctx, emitterKey{}, emit)
}

// emitFromContext 在工具内发送运行事件；没有事件出口时静默丢弃。
func emitFromContext(ctx context.Context, event Event) {
	if emit, ok := ctx.Value(emitterKey{}).(func(Event)); ok && emit != nil {
		emit(event)
	}
}

// Delegate 执行一次 delegate 工具调用。
// 参数错误返回 Go error，让 Runtime 记录为工具失败；子代理自身的失败和超时返回
// 结构化结果，父模型可以继续决策。
func (d *Delegator) Delegate(ctx context.Context, args map[string]any) (ToolOutput, error) {
	if d == nil {
		return ToolOutput{}, errors.New("子代理委托器未配置")
	}
	if SubagentDepth(ctx) > 0 {
		return ToolOutput{}, errors.New("delegate 不能在子代理内调用（最大委派深度 1）")
	}
	goal, err := delegateGoal(args)
	if err != nil {
		return ToolOutput{}, err
	}
	requested, err := delegateToolNames(args["tools"])
	if err != nil {
		return ToolOutput{}, err
	}
	tools, err := d.childTools(requested)
	if err != nil {
		return ToolOutput{}, err
	}
	if d.Model == nil {
		return ToolOutput{}, errors.New("子代理缺少模型入口")
	}

	// 生命周期事件与持久化必须越过父取消，否则取消时查不到已完成的子代理；
	// 子运行本身仍使用父 ctx，父取消会立即终止所有子代理。
	safe := context.WithoutCancel(ctx)
	id := d.recordStart(safe, goal)
	emitFromContext(safe, Event{Kind: SubagentStarted, Subagent: &SubagentInfo{ID: id, Goal: goal}})

	childContext := WithSubagentDepth(ctx, SubagentDepth(ctx)+1)
	child := Request{
		ReasoningEffort: d.Parent.ReasoningEffort,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: subagentSystemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: goal},
		},
		Stream:      d.Parent.Stream,
		MaxTokens:   d.Parent.MaxTokens,
		Temperature: d.Parent.Temperature,
		Model:       d.Parent.Model,
		Tools:       tools,
	}
	policy := d.Policy.Normalize()
	runtime := Runtime{
		Model:     d.Model,
		Execute:   d.childExecute(),
		Limits:    d.childLimits(policy),
		Context:   d.Context,
		Summarize: d.Summarize,
	}
	result, runErr := runtime.Run(childContext, child, nil)
	status := result.Status
	if status == "" {
		status = Failed
	}
	message := ""
	if runErr != nil {
		message = runErr.Error()
	}
	info := &SubagentInfo{ID: id, Goal: goal, Status: status, Result: result.Content, Usage: result.Usage, Error: message}
	emitFromContext(safe, Event{Kind: SubagentFinished, Subagent: info})
	d.recordFinish(safe, id, status, result.Content, result.Usage)
	return ToolOutput{Value: SubagentResult{
		ID: id, Status: status, Error: message, Content: result.Content, Usage: result.Usage, Rounds: len(result.Rounds),
	}}, nil
}

// childExecute 返回子运行的工具入口：只在父工具执行外包一层工作区租约，模型请求不加锁。
func (d *Delegator) childExecute() ToolFunc {
	if d.Lease == nil || d.LeaseKey == "" {
		return d.Execute
	}
	return func(ctx context.Context, name string, args map[string]any) (ToolOutput, error) {
		unlock := d.Lease.Lock(d.LeaseKey)
		defer unlock()
		return d.Execute(ctx, name, args)
	}
}

// childLimits 由父限制派生：时间与输出上限沿用，轮数与 token 上限来自子代理策略。
func (d *Delegator) childLimits(policy SubagentPolicy) Limits {
	return Limits{
		MaxRounds:          policy.MaxRounds,
		Timeout:            d.Limits.Timeout,
		MaxToolOutputBytes: d.Limits.MaxToolOutputBytes,
		MaxTotalTokens:     policy.MaxTotalTokens,
	}
}

// childTools 返回子代理可见的工具集：父工具集去掉 delegate 与状态工具，再按请求筛选。
func (d *Delegator) childTools(requested []string) ([]openai.Tool, error) {
	available := make(map[string]openai.Tool, len(d.Tools))
	order := make([]string, 0, len(d.Tools))
	for _, tool := range d.Tools {
		if tool.Function == nil {
			continue
		}
		name := tool.Function.Name
		if name == "" || name == DelegateTool || name == "saber_task_state" {
			continue
		}
		if _, exists := available[name]; exists {
			continue
		}
		available[name] = tool
		order = append(order, name)
	}
	if len(requested) == 0 {
		tools := make([]openai.Tool, 0, len(order))
		for _, name := range order {
			tools = append(tools, available[name])
		}
		return tools, nil
	}
	tools := make([]openai.Tool, 0, len(requested))
	seen := make(map[string]bool, len(requested))
	for _, name := range requested {
		if seen[name] {
			continue
		}
		tool, ok := available[name]
		if !ok {
			return nil, fmt.Errorf("tools 中的 %q 不在父请求的可用工具集内", name)
		}
		seen[name] = true
		tools = append(tools, tool)
	}
	return tools, nil
}

// recordStart 写入子代理启动记录；持久化失败只记日志，不阻止子代理运行。
func (d *Delegator) recordStart(ctx context.Context, goal string) int64 {
	if d.Hooks.Start == nil {
		return 0
	}
	id, err := d.Hooks.Start(ctx, goal)
	if err != nil {
		slog.Error("记录子代理启动失败", "error", err)
		return 0
	}
	return id
}

// recordFinish 写入子代理终态；启动记录缺失时跳过。
func (d *Delegator) recordFinish(ctx context.Context, id int64, status Status, result string, usage openai.Usage) {
	if id <= 0 || d.Hooks.Finish == nil {
		return
	}
	d.Hooks.Finish(ctx, id, status, result, usage)
}

// delegateGoal 提取并校验必填的 goal。
func delegateGoal(args map[string]any) (string, error) {
	goal, ok := args["goal"].(string)
	if !ok || strings.TrimSpace(goal) == "" {
		return "", errors.New("goal 是必填字符串")
	}
	goal = strings.TrimSpace(goal)
	if len(goal) > MaxSubagentGoalBytes {
		return "", fmt.Errorf("goal 超过 %d 字节上限（%d 字节）", MaxSubagentGoalBytes, len(goal))
	}
	if !utf8.ValidString(goal) {
		return "", errors.New("goal 必须是合法的 UTF-8 文本")
	}
	return goal, nil
}

// delegateToolNames 解析可选的 tools 参数；JSON 解码后是 []any，直接调用时可能是 []string。
func delegateToolNames(value any) ([]string, error) {
	switch raw := value.(type) {
	case nil:
		return nil, nil
	case []string:
		return raw, nil
	case []any:
		names := make([]string, 0, len(raw))
		for _, item := range raw {
			name, ok := item.(string)
			if !ok || strings.TrimSpace(name) == "" {
				return nil, errors.New("tools 必须是非空字符串数组")
			}
			names = append(names, name)
		}
		return names, nil
	default:
		return nil, errors.New("tools 必须是非空字符串数组")
	}
}
