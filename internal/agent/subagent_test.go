package agent_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// delegateCall 构造一个 delegate 工具调用。
func delegateCall(id, goal string, tools []string) openai.ToolCall {
	args := fmt.Sprintf(`{"goal":%q`, goal)
	if tools != nil {
		args += fmt.Sprintf(`,"tools":["%s"]`, strings.Join(tools, `","`))
	}
	args += "}"
	return openai.ToolCall{ID: id, Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: agent.DelegateTool, Arguments: args}}
}

// toolCall 构造一个普通工具调用。
func toolCall(id, name string) openai.ToolCall {
	return openai.ToolCall{ID: id, Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: name, Arguments: `{}`}}
}

// subagentTool 构造一个最小工具定义。
func subagentTool(name string) openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: name, Parameters: map[string]any{"type": "object"}}}
}

// gatedModel 模拟 model.AgentModel 的预算准入：请求前 Admit，完成后 Record。
func gatedModel(inner func(context.Context, agent.Request) (agent.Response, error)) agent.ModelFunc {
	return func(ctx context.Context, req agent.Request, _ func(agent.Event)) (agent.Response, error) {
		gate, ok := agent.AttemptGateFromContext(ctx)
		admitted, admittedOK := req, false
		if ok {
			var err error
			admitted, err = gate.Admit(req)
			if err != nil {
				return agent.Response{}, err
			}
			admittedOK = true
		}
		response, err := inner(ctx, admitted)
		if admittedOK {
			gate.Record(admitted, response)
		}
		return response, err
	}
}

// isSubagent 判断请求是否来自子运行：只有一条 system 与一条 user。
func isSubagent(req agent.Request) bool {
	return len(req.Messages) == 2 && req.Messages[0].Role == openai.ChatMessageRoleSystem &&
		strings.Contains(req.Messages[0].Content, "子代理")
}

// toolContents 返回一轮中的工具结果正文。
func toolContents(round agent.Round) []string {
	contents := make([]string, 0, len(round.Tools))
	for _, record := range round.Tools {
		contents = append(contents, record.Content)
	}
	return contents
}

// newParallelDelegator 组装一个使用共享事件出口的委托器。
func newParallelDelegator(req agent.Request, model agent.ModelFunc, execute agent.ToolFunc) *agent.Delegator {
	return &agent.Delegator{
		Policy:   agent.SubagentPolicy{MaxRounds: 4, MaxParallel: 2},
		Model:    model,
		Execute:  execute,
		Parent:   req,
		Tools:    req.Tools,
		Lease:    &agent.WorkspaceLease{},
		LeaseKey: "/workspace",
	}
}

// TestDelegator_ParallelKeepsCallOrder 验证同一轮多个 delegate 并发执行，结果按调用顺序回填。
func TestDelegator_ParallelKeepsCallOrder(t *testing.T) {
	req := agent.Request{
		Model:    "test-model",
		Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "并行处理"}},
		Tools:    []openai.Tool{subagentTool(agent.DelegateTool), subagentTool("work")},
	}
	entered := make(chan string, 2)
	release := make(chan struct{})
	var parentRounds atomic.Int32
	model := gatedModel(func(ctx context.Context, req agent.Request) (agent.Response, error) {
		if isSubagent(req) {
			goal := req.Messages[1].Content
			if depth := agent.SubagentDepth(ctx); depth != 1 {
				return agent.Response{}, fmt.Errorf("子代理深度应为 1，实际 %d", depth)
			}
			for _, tool := range req.Tools {
				if tool.Function.Name == agent.DelegateTool || tool.Function.Name == "saber_task_state" {
					return agent.Response{}, fmt.Errorf("子代理不应看到 %s", tool.Function.Name)
				}
			}
			entered <- goal
			select {
			case <-release:
			case <-time.After(3 * time.Second):
				return agent.Response{}, errors.New("子代理没有并发启动")
			}
			// alpha 后完成，证明结果顺序不是由完成顺序决定。
			if goal == "alpha" {
				time.Sleep(40 * time.Millisecond)
			}
			return agent.Response{Content: "done:" + goal, FinishReason: "stop"}, nil
		}
		if parentRounds.Add(1) == 1 {
			return agent.Response{
				FinishReason: "tool_calls",
				ToolCalls:    []openai.ToolCall{delegateCall("d1", "alpha", nil), delegateCall("d2", "beta", nil)},
			}, nil
		}
		return agent.Response{Content: "父完成", FinishReason: "stop"}, nil
	})
	var hookMu sync.Mutex
	started, finished := 0, 0
	var finishStatus []agent.Status
	delegator := newParallelDelegator(req, model, func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
		return agent.ToolOutput{Value: "ok"}, nil
	})
	delegator.Hooks = agent.SubagentHooks{
		Start: func(context.Context, string) (int64, error) {
			hookMu.Lock()
			defer hookMu.Unlock()
			started++
			return int64(started), nil
		},
		Finish: func(_ context.Context, id int64, status agent.Status, _ string, _ openai.Usage) error {
			hookMu.Lock()
			defer hookMu.Unlock()
			finished++
			finishStatus = append(finishStatus, status)
			if id <= 0 {
				t.Errorf("结束记录缺少编号: %d", id)
			}
			return nil
		},
	}
	runtime := agent.Runtime{
		Model:        model,
		Limits:       agent.Limits{MaxRounds: 4},
		ParallelTool: func(name string) int { return map[bool]int{true: 2, false: 1}[name == agent.DelegateTool] },
	}
	runtime.Execute = func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
		if name == agent.DelegateTool {
			return delegator.Delegate(ctx, args)
		}
		return agent.ToolOutput{Value: "ok"}, nil
	}
	go func() { <-entered; <-entered; close(release) }()
	var subagentEvents []agent.Event
	result, err := runtime.Run(context.Background(), req, func(event agent.Event) {
		switch event.Kind {
		case agent.SubagentStarted, agent.SubagentFinished:
			subagentEvents = append(subagentEvents, event)
		}
	})
	if err != nil {
		t.Fatalf("运行失败: %v", err)
	}
	if len(result.Rounds) < 2 || len(result.Rounds[0].Tools) != 2 {
		t.Fatalf("工具记录数量异常: %+v", result.Rounds)
	}
	contents := toolContents(result.Rounds[0])
	if !strings.Contains(contents[0], `"content":"done:alpha"`) || !strings.Contains(contents[1], `"content":"done:beta"`) {
		t.Fatalf("结果未按调用顺序回填: %v", contents)
	}
	hookMu.Lock()
	defer hookMu.Unlock()
	if started != 2 || finished != 2 || len(finishStatus) != 2 {
		t.Fatalf("生命周期记录不完整: start=%d finish=%d", started, finished)
	}
	for _, status := range finishStatus {
		if status != agent.Completed {
			t.Fatalf("子代理终态异常: %s", status)
		}
	}
	startedEvents, finishedEvents := 0, 0
	for _, event := range subagentEvents {
		if event.Subagent == nil || event.Round != 1 {
			t.Fatalf("子代理事件缺少事实或轮次: %+v", event)
		}
		if event.Kind == agent.SubagentStarted {
			startedEvents++
		} else {
			finishedEvents++
		}
	}
	if startedEvents != 2 || finishedEvents != 2 {
		t.Fatalf("子代理事件数量异常: start=%d finish=%d", startedEvents, finishedEvents)
	}
}

// TestDelegator_ChildToolsetAndDepth 验证子代理工具子集、参数校验与深度 1 拒绝递归。
func TestDelegator_ChildToolsetAndDepth(t *testing.T) {
	tools := []openai.Tool{
		subagentTool(agent.DelegateTool), subagentTool("search"), subagentTool("exec"),
		subagentTool("saber_task_state"), subagentTool("saber_task"),
	}
	req := agent.Request{Model: "test-model", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "parent"}}, Tools: tools}
	cases := []struct {
		name   string
		args   map[string]any
		depth  int
		expect []string
		err    bool
	}{
		{name: "默认沿用父工具", args: map[string]any{"goal": "查资料"}, expect: []string{"search", "exec", "saber_task"}},
		{name: "显式子集", args: map[string]any{"goal": "查资料", "tools": []any{"search"}}, expect: []string{"search"}},
		{name: "请求 delegate", args: map[string]any{"goal": "再委派", "tools": []any{agent.DelegateTool}}, err: true},
		{name: "请求未知工具", args: map[string]any{"goal": "查资料", "tools": []any{"missing"}}, err: true},
		{name: "tools 类型错误", args: map[string]any{"goal": "查资料", "tools": []any{1}}, err: true},
		{name: "goal 为空", args: map[string]any{"goal": "   "}, err: true},
		{name: "goal 超长", args: map[string]any{"goal": strings.Repeat("长", agent.MaxSubagentGoalBytes)}, err: true},
		{name: "深度 1 拒绝", args: map[string]any{"goal": "查资料"}, depth: 1, err: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var runs atomic.Int32
			model := gatedModel(func(_ context.Context, req agent.Request) (agent.Response, error) {
				runs.Add(1)
				var names []string
				for _, tool := range req.Tools {
					names = append(names, tool.Function.Name)
				}
				if strings.Join(names, ",") != strings.Join(testCase.expect, ",") {
					return agent.Response{}, fmt.Errorf("子代理工具集错误: %v", names)
				}
				return agent.Response{Content: "done", FinishReason: "stop"}, nil
			})
			delegator := &agent.Delegator{Policy: agent.SubagentPolicy{MaxRounds: 2}, Model: model, Parent: req, Tools: tools}
			ctx := agent.WithSubagentDepth(context.Background(), testCase.depth)
			_, err := delegator.Delegate(ctx, testCase.args)
			if testCase.err {
				if err == nil {
					t.Fatal("应拒绝该参数")
				}
				if runs.Load() != 0 {
					t.Fatal("参数错误不应启动子运行")
				}
				return
			}
			if err != nil {
				t.Fatalf("委托失败: %v", err)
			}
			if runs.Load() != 1 {
				t.Fatalf("子运行次数异常: %d", runs.Load())
			}
		})
	}
}

// TestDelegator_ChargesParentLedger 验证子运行用量上推父账本。
func TestDelegator_ChargesParentLedger(t *testing.T) {
	req := agent.Request{
		Model:    "test-model",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "记账"}},
		Tools:    []openai.Tool{subagentTool(agent.DelegateTool)},
	}
	var parentRounds atomic.Int32
	model := gatedModel(func(_ context.Context, req agent.Request) (agent.Response, error) {
		if isSubagent(req) {
			return agent.Response{Content: "子结果", FinishReason: "stop", Usage: openai.Usage{PromptTokens: 1200, CompletionTokens: 800, TotalTokens: 2000}}, nil
		}
		if parentRounds.Add(1) == 1 {
			return agent.Response{
				FinishReason: "tool_calls",
				ToolCalls:    []openai.ToolCall{delegateCall("d1", "记账子任务", nil)},
				Usage:        openai.Usage{TotalTokens: 100},
			}, nil
		}
		return agent.Response{Content: "父最终", FinishReason: "stop", Usage: openai.Usage{TotalTokens: 100}}, nil
	})
	delegator := newParallelDelegator(req, model, nil)
	delegator.Execute = func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
		return agent.ToolOutput{Value: "ok"}, nil
	}
	runtime := agent.Runtime{Model: model, Limits: agent.Limits{MaxRounds: 4, MaxTotalTokens: 5000}}
	runtime.Execute = func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
		return delegator.Delegate(ctx, args)
	}
	result, err := runtime.Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("运行失败: %v", err)
	}
	if result.Budget == nil || result.Budget.AccountedTokens != 2200 {
		t.Fatalf("父账本未包含子运行消耗: %+v", result.Budget)
	}
	contents := toolContents(result.Rounds[0])
	if len(contents) != 1 || !strings.Contains(contents[0], `"total_tokens":2000`) {
		t.Fatalf("子运行用量未回填: %v", contents)
	}
}

// TestDelegator_ParentBalanceRejectsChild 验证父余额耗尽时子运行被拒绝。
func TestDelegator_ParentBalanceRejectsChild(t *testing.T) {
	req := agent.Request{
		Model:    "test-model",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "耗尽的余额"}},
		Tools:    []openai.Tool{subagentTool(agent.DelegateTool)},
	}
	var childRuns atomic.Int32
	model := gatedModel(func(_ context.Context, req agent.Request) (agent.Response, error) {
		if isSubagent(req) {
			childRuns.Add(1)
			return agent.Response{Content: "不该到达", FinishReason: "stop"}, nil
		}
		return agent.Response{
			FinishReason: "tool_calls",
			ToolCalls:    []openai.ToolCall{delegateCall("d1", "余额不足仍被调用", nil)},
			Usage:        openai.Usage{TotalTokens: 100},
		}, nil
	})
	delegator := newParallelDelegator(req, model, nil)
	runtime := agent.Runtime{Model: model, Limits: agent.Limits{MaxRounds: 2, MaxTotalTokens: 100}}
	runtime.Execute = func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
		return delegator.Delegate(ctx, args)
	}
	result, err := runtime.Run(context.Background(), req, nil)
	if !errors.Is(err, agent.ErrBudgetExhausted) {
		t.Fatalf("父运行应以预算耗尽结束: %v", err)
	}
	if childRuns.Load() != 0 {
		t.Fatal("父余额耗尽后不应发起子模型请求")
	}
	contents := toolContents(result.Rounds[0])
	if len(contents) != 1 || !strings.Contains(contents[0], `"status":"budget_exhausted"`) || !strings.Contains(contents[0], "budget") {
		t.Fatalf("子代理应返回结构化预算失败: %v", contents)
	}
}

// TestDelegator_ParentCancelStopsChild 验证父取消立即停止子代理并保留终态事件。
func TestDelegator_ParentCancelStopsChild(t *testing.T) {
	req := agent.Request{
		Model:    "test-model",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "取消"}},
		Tools:    []openai.Tool{subagentTool(agent.DelegateTool)},
	}
	childStarted := make(chan struct{})
	var startOnce sync.Once
	model := gatedModel(func(ctx context.Context, req agent.Request) (agent.Response, error) {
		if isSubagent(req) {
			startOnce.Do(func() { close(childStarted) })
			<-ctx.Done()
			return agent.Response{}, ctx.Err()
		}
		return agent.Response{
			FinishReason: "tool_calls",
			ToolCalls:    []openai.ToolCall{delegateCall("d1", "慢子任务", nil)},
		}, nil
	})
	delegator := newParallelDelegator(req, model, nil)
	runtime := agent.Runtime{Model: model, Limits: agent.Limits{MaxRounds: 3}}
	runtime.Execute = func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
		return delegator.Delegate(ctx, args)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var finished atomic.Int32
	type outcome struct {
		result agent.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := runtime.Run(ctx, req, func(event agent.Event) {
			if event.Kind == agent.SubagentFinished {
				finished.Add(1)
			}
		})
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-childStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("子代理没有启动")
	}
	cancel()
	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) || got.result.Status != agent.Cancelled {
			t.Fatalf("父运行终态异常: %v %s", got.err, got.result.Status)
		}
		contents := toolContents(got.result.Rounds[0])
		if len(contents) != 1 || !strings.Contains(contents[0], `"status":"cancelled"`) {
			t.Fatalf("子代理结果未标记取消: %v", contents)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("父取消未停止子代理")
	}
	if finished.Load() != 1 {
		t.Fatalf("取消后仍应持久化子代理终态: %d", finished.Load())
	}
}

// TestDelegator_WorkspaceLeaseAndContextIdentity 验证工作区互斥与父上下文传递。
func TestDelegator_WorkspaceLeaseAndContextIdentity(t *testing.T) {
	req := agent.Request{
		Model:    "test-model",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "并行写入"}},
		Tools:    []openai.Tool{subagentTool(agent.DelegateTool), subagentTool("work")},
	}
	type taskKey struct{}
	entered := make(chan string, 2)
	release := make(chan struct{})
	var active, peak atomic.Int32
	var roundMu sync.Mutex
	rounds := map[string]int{}
	model := gatedModel(func(_ context.Context, req agent.Request) (agent.Response, error) {
		goal := req.Messages[1].Content
		roundMu.Lock()
		rounds[goal]++
		count := rounds[goal]
		roundMu.Unlock()
		if count == 1 {
			entered <- goal
			select {
			case <-release:
			case <-time.After(3 * time.Second):
				return agent.Response{}, errors.New("子代理没有并发启动")
			}
			return agent.Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{toolCall("w-"+goal, "work")}}, nil
		}
		return agent.Response{Content: "done:" + goal, FinishReason: "stop"}, nil
	})
	execute := func(ctx context.Context, name string, _ map[string]any) (agent.ToolOutput, error) {
		identity, ok := chat.IdentityFromContext(ctx)
		if !ok || identity.SenderID != "alice" {
			return agent.ToolOutput{}, fmt.Errorf("子代理丢失可信身份: %+v", identity)
		}
		if got, _ := ctx.Value(taskKey{}).(int64); got != 42 {
			return agent.ToolOutput{}, fmt.Errorf("子代理丢失任务作用域: %v", got)
		}
		current := active.Add(1)
		for {
			previous := peak.Load()
			if current <= previous || peak.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		active.Add(-1)
		return agent.ToolOutput{Value: "ok"}, nil
	}
	delegator := newParallelDelegator(req, model, execute)
	runtime := agent.Runtime{
		Model:        model,
		Limits:       agent.Limits{MaxRounds: 4},
		ParallelTool: func(name string) int { return map[bool]int{true: 2, false: 1}[name == agent.DelegateTool] },
	}
	runtime.Execute = func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
		if name == agent.DelegateTool {
			return delegator.Delegate(ctx, args)
		}
		return execute(ctx, name, args)
	}
	// 先让父模型返回两个 delegate 调用，再验证工具执行不重叠。
	parentModel := runtime.Model
	var parentRounds atomic.Int32
	runtime.Model = func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Response, error) {
		if isSubagent(req) {
			return parentModel(ctx, req, emit)
		}
		if parentRounds.Add(1) == 1 {
			return agent.Response{
				FinishReason: "tool_calls",
				ToolCalls:    []openai.ToolCall{delegateCall("d1", "left", nil), delegateCall("d2", "right", nil)},
			}, nil
		}
		return agent.Response{Content: "父完成", FinishReason: "stop"}, nil
	}
	ctx := chat.WithIdentity(context.Background(), chat.Identity{
		Session:  chat.Session{Platform: "matrix", Account: "bot", Conversation: "!room"},
		SenderID: "alice",
	})
	ctx = context.WithValue(ctx, taskKey{}, int64(42))
	go func() { <-entered; <-entered; close(release) }()
	result, err := runtime.Run(ctx, req, nil)
	if err != nil {
		t.Fatalf("运行失败: %v", err)
	}
	if peak.Load() != 1 {
		t.Fatalf("同一工作区的工具执行发生重叠，峰值 %d", peak.Load())
	}
	contents := toolContents(result.Rounds[0])
	if len(contents) != 2 || !strings.Contains(contents[0], `"content":"done:left"`) || !strings.Contains(contents[1], `"content":"done:right"`) {
		t.Fatalf("子代理没有完成全部轮次: %v", contents)
	}
}

// TestDelegator_ResultTruncated 验证 delegate 结果受 MaxToolOutputBytes 截断。
func TestDelegator_ResultTruncated(t *testing.T) {
	req := agent.Request{
		Model:    "test-model",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "截断"}},
		Tools:    []openai.Tool{subagentTool(agent.DelegateTool)},
	}
	var parentRounds atomic.Int32
	model := gatedModel(func(_ context.Context, req agent.Request) (agent.Response, error) {
		if isSubagent(req) {
			return agent.Response{Content: strings.Repeat("长", 2048), FinishReason: "stop"}, nil
		}
		if parentRounds.Add(1) == 1 {
			return agent.Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{delegateCall("d1", "长结果", nil)}}, nil
		}
		return agent.Response{Content: "父完成", FinishReason: "stop"}, nil
	})
	delegator := &agent.Delegator{Policy: agent.SubagentPolicy{MaxRounds: 2}, Model: model, Parent: req, Tools: req.Tools}
	runtime := agent.Runtime{Model: model, Limits: agent.Limits{MaxRounds: 3, MaxToolOutputBytes: 256}}
	runtime.Execute = func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
		return delegator.Delegate(ctx, args)
	}
	result, err := runtime.Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("运行失败: %v", err)
	}
	record := result.Rounds[0].Tools[0]
	if !record.Truncated || record.OriginalBytes <= 256 || len(record.Content) > 256 {
		t.Fatalf("delegate 结果未按输出上限截断: truncated=%v bytes=%d len=%d", record.Truncated, record.OriginalBytes, len(record.Content))
	}
}
