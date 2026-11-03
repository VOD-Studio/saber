package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
)

// TestRuntime_SteeringInjectedOncePerBatch 验证补充要求在每轮模型请求前注入，
// 同一批文本不跨轮重复，空文本不产生事件，事件顺序稳定。
func TestRuntime_SteeringInjectedOncePerBatch(t *testing.T) {
	batches := [][]string{
		{"先检查配置", " "},
		{"先检查配置", "再检查日志"},
		{}, // 第三轮没有新补充要求。
	}
	var requests []agent.Request
	runtime := agent.Runtime{
		Model: func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Response, error) {
			requests = append(requests, req)
			if len(requests) == 3 {
				return final(), nil
			}
			return tools(call("step", `{"limit":1}`)), nil
		},
		Execute: func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
			return agent.ToolOutput{Value: map[string]string{"ok": "true"}}, nil
		},
		Steering: func() []string {
			if len(requests) >= len(batches) {
				return nil
			}
			return batches[len(requests)]
		},
	}
	var events []agent.Event
	result, err := runtime.Run(context.Background(), request(), func(event agent.Event) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if result.Status != agent.Completed {
		t.Fatalf("unexpected status: %s", result.Status)
	}
	if len(requests) != 3 {
		t.Fatalf("unexpected model calls: %d", len(requests))
	}
	// 第一轮注入「先检查配置」，空白文本被忽略。
	if len(requests[0].Messages) != 2 {
		t.Fatalf("round 1 messages: %+v", requests[0].Messages)
	}
	last := requests[0].Messages[1]
	if last.Role != openai.ChatMessageRoleUser || last.Content != "[运行中补充要求] 先检查配置" {
		t.Fatalf("round 1 steering missing: %+v", last)
	}
	// 第二轮只追加新文本，已注入的同批文本不重复。
	want2 := []string{"[运行中补充要求] 先检查配置", "[运行中补充要求] 再检查日志"}
	if got := steerings(requests[1].Messages); strings.Join(got, "|") != strings.Join(want2, "|") {
		t.Fatalf("round 2 steering wrong: %+v", got)
	}
	// 第三轮沿用历史，不再追加。
	if got := steerings(requests[2].Messages); len(got) != len(want2) {
		t.Fatalf("round 3 must not re-inject: %+v", got)
	}
	var order []string
	for _, event := range events {
		switch event.Kind {
		case agent.Steered, agent.ModelStarted, agent.ToolStarted, agent.ToolFinished:
			order = append(order, string(event.Kind))
		}
	}
	want := []string{
		"steered", "model_started", "tool_started", "tool_finished",
		"steered", "model_started", "tool_started", "tool_finished",
		"model_started",
	}
	if got := strings.Join(order, ","); got != strings.Join(want, ",") {
		t.Fatalf("unexpected event order: %s", got)
	}
	var steered []agent.Event
	for _, event := range events {
		if event.Kind == agent.Steered {
			steered = append(steered, event)
		}
	}
	if len(steered) != 2 {
		t.Fatalf("unexpected steered events: %d", len(steered))
	}
	if steered[0].Round != 1 || steered[0].Text != "先检查配置" {
		t.Fatalf("bad steered event: %+v", steered[0])
	}
	if steered[1].Round != 2 || steered[1].Text != "再检查日志" {
		t.Fatalf("bad steered event: %+v", steered[1])
	}
}

// TestRuntime_SteeringFromContext 验证没有显式字段时使用 ctx 绑定的来源，且子代理不消费。
func TestRuntime_SteeringFromContext(t *testing.T) {
	calls := 0
	steering := func() []string {
		calls++
		return []string{"来自上下文"}
	}
	var seen string
	runtime := agent.Runtime{
		Model: func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Response, error) {
			seen = req.Messages[len(req.Messages)-1].Content
			return final(), nil
		},
	}
	ctx := agent.WithSteering(context.Background(), steering)
	if _, err := runtime.Run(ctx, request(), nil); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if calls != 1 || seen != "[运行中补充要求] 来自上下文" {
		t.Fatalf("context steering not applied: calls=%d last=%q", calls, seen)
	}
	// 子代理沿用父 ctx：不读取、也不消费父任务的补充要求。
	calls = 0
	seen = ""
	if _, err := runtime.Run(agent.WithSubagentDepth(ctx, 1), request(), nil); err != nil {
		t.Fatalf("subagent run failed: %v", err)
	}
	if calls != 0 || strings.HasPrefix(seen, "[运行中补充要求] ") {
		t.Fatalf("subagent must not consume steering: calls=%d last=%q", calls, seen)
	}
}

// steerings 返回历史中的补充要求消息正文。
func steerings(messages []openai.ChatCompletionMessage) []string {
	var texts []string
	for _, message := range messages {
		if strings.HasPrefix(message.Content, "[运行中补充要求] ") {
			texts = append(texts, message.Content)
		}
	}
	return texts
}
