package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
)

func functionTool(name string) openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: name, Parameters: map[string]any{"type": "object"}}}
}

func TestRenderRequest_PreservesInstructionsOnce(t *testing.T) {
	state := &TaskState{Instructions: []Instruction{{TaskID: 1, Text: "原始目标：只修改 internal/cli"}, {TaskID: 2, Text: "补充：不要新增依赖"}}}
	req := Request{
		Messages:       []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: "system"}, {Role: openai.ChatMessageRoleUser, Content: "原始目标：只修改 internal/cli"}, {Role: openai.ChatMessageRoleAssistant, Content: "开始"}, {Role: openai.ChatMessageRoleUser, Content: "补充：不要新增依赖"}},
		State:          state,
		Summary:        "已完成搜索",
		RuntimeContext: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: "记忆快照"}},
	}
	rendered := renderRequest(req)
	require.Nil(t, rendered.State)
	require.Empty(t, rendered.Summary)
	require.Equal(t, 4, len(req.Messages), "原始请求不能被修改")
	joined := ""
	for _, message := range rendered.Messages {
		joined += message.Content + "\n"
	}
	require.Equal(t, 1, strings.Count(joined, "原始目标：只修改 internal/cli"))
	require.Equal(t, 1, strings.Count(joined, "补充：不要新增依赖"))
	require.Equal(t, 1, strings.Count(joined, "记忆快照"))
	require.Equal(t, 1, strings.Count(joined, "已完成搜索"))
	last := rendered.Messages[len(rendered.Messages)-1]
	require.Equal(t, openai.ChatMessageRoleUser, last.Role)
	require.Equal(t, "补充：不要新增依赖", last.Content)
	// 工作状态以 assistant 资料出现，但排在最新用户消息之前。
	require.Equal(t, openai.ChatMessageRoleAssistant, rendered.Messages[len(rendered.Messages)-2].Role)
}

func TestRenderRequest_AddsMissingParentInstructions(t *testing.T) {
	state := &TaskState{Instructions: []Instruction{{TaskID: 1, Text: "根任务约束"}, {TaskID: 2, Text: "当前补充"}}}
	req := Request{Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "当前补充"}}, State: state}
	rendered := renderRequest(req)
	require.Contains(t, rendered.Messages[0].Content, "根任务约束")
	require.Equal(t, openai.ChatMessageRoleUser, rendered.Messages[0].Role)
	require.Equal(t, "当前补充", rendered.Messages[len(rendered.Messages)-1].Content)
}

func TestRuntime_CompactsAndKeepsConstraintsAndToolProtocol(t *testing.T) {
	constraint := "CONSTRAINT-7439 不得修改用户原始指令"
	state := &TaskState{Instructions: []Instruction{{TaskID: 1, Text: constraint}}}
	request := Request{Model: "test", Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: constraint}}, Tools: []openai.Tool{functionTool("echo")}, State: state}
	var captured []Request
	calls := 0
	summaries := 0
	var compactEvents int
	runtime := Runtime{
		Limits:  Limits{MaxRounds: 12, Timeout: 10 * time.Second, MaxToolOutputBytes: 4096},
		Context: ContextPolicy{Enabled: true, MaxMessages: 1000, MaxInputTokens: 3000, Compaction: CompactionPolicy{Enabled: true, TriggerRatio: 0.5, TargetRatio: 0.2}},
		Summarize: func(_ context.Context, input SummaryInput) (SummaryOutput, error) {
			summaries++
			require.NotEmpty(t, input.Messages)
			return SummaryOutput{Text: "滚动摘要：已执行若干命令"}, nil
		},
		Model: func(_ context.Context, req Request, _ func(Event)) (Response, error) {
			captured = append(captured, req)
			calls++
			if calls <= 5 {
				return Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{{ID: "call-" + string(rune('a'+calls)), Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "echo", Arguments: `{}`}}}}, nil
			}
			return Response{FinishReason: "stop", Content: "完成"}, nil
		},
		Execute: func(context.Context, string, map[string]any) (ToolOutput, error) {
			return ToolOutput{Value: map[string]string{"output": strings.Repeat("x", 600)}}, nil
		},
	}
	result, err := runtime.Run(context.Background(), request, func(event Event) {
		if event.Kind == ContextCompacted {
			compactEvents++
			require.NotNil(t, event.Checkpoint)
		}
	})
	require.NoError(t, err)
	require.Equal(t, Completed, result.Status)
	require.Greater(t, summaries, 0)
	require.Greater(t, compactEvents, 0)
	require.NotNil(t, result.Checkpoint)
	require.Equal(t, constraint, result.Checkpoint.Request.State.Instructions[0].Text)
	for _, sent := range captured {
		joined := ""
		for _, message := range sent.Messages {
			joined += message.Content + "\n"
		}
		require.Contains(t, joined, constraint, "压缩后原始约束仍必须逐字可见")
		// 每条 assistant 工具调用都带齐匹配结果，协议不被压缩拆开。
		for i, message := range sent.Messages {
			if message.Role != openai.ChatMessageRoleAssistant || len(message.ToolCalls) == 0 {
				continue
			}
			pending := map[string]bool{}
			for _, call := range message.ToolCalls {
				pending[call.ID] = true
			}
			for j := i + 1; j < len(sent.Messages) && sent.Messages[j].Role == openai.ChatMessageRoleTool; j++ {
				delete(pending, sent.Messages[j].ToolCallID)
			}
			require.Empty(t, pending)
		}
	}
}

func TestRuntime_CompactionFailureDoesNotCommitCheckpoint(t *testing.T) {
	state := &TaskState{Instructions: []Instruction{{TaskID: 1, Text: "约束"}}}
	request := Request{Model: "test", Messages: []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "开始"},
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{ID: "call-1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "exec", Arguments: `{}`}}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "call-1", Content: "结果"},
		{Role: openai.ChatMessageRoleUser, Content: strings.Repeat("内容", 300)},
	}, State: state}
	var checkpoint *Checkpoint
	runtime := Runtime{
		Limits:  Limits{MaxRounds: 4, Timeout: 5 * time.Second, MaxToolOutputBytes: 4096},
		Context: ContextPolicy{Enabled: true, MaxMessages: 1000, MaxInputTokens: 600, Compaction: CompactionPolicy{Enabled: true, TriggerRatio: 0.5, TargetRatio: 0.2}},
		Summarize: func(context.Context, SummaryInput) (SummaryOutput, error) {
			return SummaryOutput{}, errors.New("summary backend down")
		},
		Model: func(context.Context, Request, func(Event)) (Response, error) {
			t.Fatal("压缩失败后不得继续请求模型")
			return Response{}, nil
		},
	}
	_, err := runtime.Run(context.Background(), request, func(event Event) {
		if event.Checkpoint != nil {
			checkpoint = event.Checkpoint
		}
	})
	require.ErrorContains(t, err, "context_compaction_failed")
	require.Nil(t, checkpoint)
}

func TestRuntime_StateToolUpdatesWorkFieldsOnly(t *testing.T) {
	state := &TaskState{Instructions: []Instruction{{TaskID: 1, Text: "原始约束"}}}
	request := Request{Model: "test", Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "原始约束"}}, State: state}
	calls := 0
	var updated *TaskState
	runtime := Runtime{
		Limits: Limits{MaxRounds: 4, Timeout: 5 * time.Second, MaxToolOutputBytes: 4096},
		Model: func(_ context.Context, req Request, _ func(Event)) (Response, error) {
			calls++
			if calls == 1 {
				return Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{{ID: "state-1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "saber_task_state", Arguments: `{"action":"update","plan":[{"text":"修改 CLI","status":"in_progress"}],"findings":["定位到参数校验"]}`}}}}, nil
			}
			return Response{FinishReason: "stop", Content: "ok"}, nil
		},
		Execute: func(context.Context, string, map[string]any) (ToolOutput, error) {
			t.Fatal("状态工具必须由 Runtime 内部执行")
			return ToolOutput{}, nil
		},
	}
	result, err := runtime.Run(context.Background(), request, func(event Event) {
		if event.Kind == TaskStateUpdated && event.State != nil {
			updated = event.State
		}
	})
	require.NoError(t, err)
	require.NotNil(t, result.State)
	require.Equal(t, int64(1), result.State.Revision)
	require.Len(t, result.State.Plan, 1)
	require.Equal(t, "原始约束", result.State.Instructions[0].Text)
	require.NotNil(t, updated)

	// 模型不能通过状态工具篡改原始指令。
	_, _, err = ApplyTaskStateTool(result.State, map[string]any{"action": "update", "instructions": []any{}})
	require.Error(t, err)
	// 纯 read 与相同内容更新不递增 revision。
	_, changed, err := ApplyTaskStateTool(result.State, map[string]any{"action": "read"})
	require.NoError(t, err)
	require.False(t, changed)
	_, changed, err = ApplyTaskStateTool(result.State, map[string]any{"action": "update", "plan": []any{map[string]any{"text": "修改 CLI", "status": "in_progress"}}})
	require.NoError(t, err)
	require.False(t, changed)
}

func TestCloneTaskStateIsDeep(t *testing.T) {
	state := &TaskState{Plan: []PlanStep{{Text: "a", Status: "pending"}}, Findings: []string{"f"}}
	clone := CloneTaskState(state)
	clone.Plan[0].Text = "b"
	clone.Findings[0] = "g"
	require.Equal(t, "a", state.Plan[0].Text)
	require.Equal(t, "f", state.Findings[0])
}

func TestContextSizeUsesRenderedInput(t *testing.T) {
	state := &TaskState{Instructions: []Instruction{{TaskID: 1, Text: "约束"}}}
	req := Request{Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "约束"}}, State: state, RuntimeContext: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: "运行时资料"}}}
	messages, tokens, err := ContextSize(req)
	require.NoError(t, err)
	require.Equal(t, 2, messages)
	encoded, _ := json.Marshal("约束")
	require.Greater(t, tokens, len(encoded))
}
