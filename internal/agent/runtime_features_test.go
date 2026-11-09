package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
)

func TestRuntime_MultimodalResultsFollowWholeToolBatch(t *testing.T) {
	image := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jZ1sAAAAASUVORK5CYII="
	calls := 0
	runtime := agent.Runtime{Limits: agent.Limits{MaxRounds: 3}, ParallelTool: func(string) int { return 2 }}
	runtime.Execute = func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
		return agent.ToolOutput{Value: map[string]any{"width": 1}, Images: []string{image}, Files: []string{"/private/artifact.png"}}, nil
	}
	runtime.Model = func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Response, error) {
		calls++
		if calls == 1 {
			return agent.Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{toolCall("one", "capture"), toolCall("two", "capture")}}, nil
		}
		require.Len(t, req.Messages, 6)
		require.Equal(t, "assistant", req.Messages[1].Role)
		require.Equal(t, "tool", req.Messages[2].Role)
		require.Equal(t, "one", req.Messages[2].ToolCallID)
		require.Equal(t, "tool", req.Messages[3].Role)
		require.Equal(t, "two", req.Messages[3].ToolCallID)
		for _, message := range req.Messages[4:] {
			require.Equal(t, "user", message.Role)
			require.Equal(t, image, message.MultiContent[1].ImageURL.URL)
		}
		require.NotContains(t, req.Messages[2].Content, "base64")
		return agent.Response{Content: "finished", FinishReason: "stop"}, nil
	}
	result, err := runtime.Run(context.Background(), agent.Request{Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "capture both"}}, Tools: []openai.Tool{subagentTool("capture")}}, nil)
	require.NoError(t, err)
	require.Equal(t, agent.Completed, result.Status)
	for _, record := range result.Rounds[0].Tools {
		require.Equal(t, []string{image}, record.Images)
		require.Equal(t, []string{"/private/artifact.png"}, record.Files)
	}
}

func TestRuntime_InvalidToolImageIsRecoverableToolFailure(t *testing.T) {
	calls := 0
	runtime := agent.Runtime{Limits: agent.Limits{MaxRounds: 3}, Execute: func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
		return agent.ToolOutput{Value: "image", Images: []string{"file:///etc/passwd"}}, nil
	}}
	runtime.Model = func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Response, error) {
		calls++
		if calls == 1 {
			return agent.Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{toolCall("image", "capture")}}, nil
		}
		require.Contains(t, req.Messages[len(req.Messages)-1].Content, "invalid_tool_image")
		require.Empty(t, req.Messages[len(req.Messages)-1].MultiContent)
		return agent.Response{FinishReason: "stop"}, nil
	}
	result, err := runtime.Run(context.Background(), agent.Request{Tools: []openai.Tool{subagentTool("capture")}}, nil)
	require.NoError(t, err)
	require.Empty(t, result.Rounds[0].Tools[0].Images)
	require.Equal(t, "invalid_tool_image", result.Rounds[0].Tools[0].ErrorCode)
}

func TestRuntime_OutputSchemaCorrectionIsBounded(t *testing.T) {
	for _, validSecond := range []bool{true, false} {
		t.Run(map[bool]string{true: "corrected", false: "rejected"}[validSecond], func(t *testing.T) {
			calls := 0
			runtime := agent.Runtime{Limits: agent.Limits{MaxRounds: 8}, Model: func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Response, error) {
				calls++
				text := `{"count":"wrong"}`
				if calls > 1 {
					require.Contains(t, req.Messages[len(req.Messages)-1].Content, "JSON Schema")
					if validSecond {
						text = `{"count":2}`
					}
				}
				return agent.Response{Content: text, FinishReason: "stop"}, nil
			}}
			result, err := runtime.Run(context.Background(), agent.Request{OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"count": map[string]any{"type": "integer"}}, "required": []string{"count"}}}, nil)
			require.Equal(t, 2, calls)
			if validSecond {
				require.NoError(t, err)
				require.Equal(t, agent.Completed, result.Status)
			} else {
				require.Error(t, err)
				require.Equal(t, agent.Failed, result.Status)
			}
		})
	}
}

func TestRuntime_ExplicitCompletionRunsAfterToolRecord(t *testing.T) {
	ready := false
	modelCalls := 0
	finished := false
	runtime := agent.Runtime{Limits: agent.Limits{MaxRounds: 3}, Completion: func(context.Context) (string, bool, error) {
		if ready {
			require.True(t, finished)
		}
		return "operator completed", ready, nil
	}, Execute: func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
		ready = true
		return agent.ToolOutput{Value: "recorded"}, nil
	}, Model: func(context.Context, agent.Request, func(agent.Event)) (agent.Response, error) {
		modelCalls++
		return agent.Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{toolCall("complete", "control")}}, nil
	}}
	result, err := runtime.Run(context.Background(), agent.Request{Tools: []openai.Tool{subagentTool("control")}}, func(event agent.Event) {
		if event.Kind == agent.ToolFinished {
			finished = true
		}
	})
	require.NoError(t, err)
	require.Equal(t, 1, modelCalls)
	require.Equal(t, "operator completed", result.Content)
	require.Len(t, result.Rounds[0].Tools, 1)
}

func TestDelegator_ExplicitEmptyToolsNeverInheritParent(t *testing.T) {
	modelCalls := 0
	executed := false
	delegator := agent.Delegator{Policy: agent.SubagentPolicy{MaxRounds: 3}, Parent: agent.Request{Model: "worker"}, Tools: []openai.Tool{subagentTool("exec")}, Execute: func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
		executed = true
		return agent.ToolOutput{}, nil
	}, Model: func(ctx context.Context, req agent.Request, _ func(agent.Event)) (agent.Response, error) {
		modelCalls++
		require.Empty(t, req.Tools)
		require.False(t, agent.ToolAllowed(ctx, "exec"))
		if modelCalls == 1 {
			return agent.Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{toolCall("denied", "exec")}}, nil
		}
		return agent.Response{Content: "refused", FinishReason: "stop"}, nil
	}}
	output, err := delegator.Delegate(context.Background(), map[string]any{"goal": "do not execute", "tools": []any{}})
	require.NoError(t, err)
	require.False(t, executed)
	result := output.Value.(agent.SubagentResult)
	require.Equal(t, "unknown_tool", result.ToolTrace[0].ErrorCode)
}

func TestDelegator_PersistenceFailurePreventsDispatch(t *testing.T) {
	calls := 0
	failure := errors.New("storage offline")
	delegator := agent.Delegator{Model: func(context.Context, agent.Request, func(agent.Event)) (agent.Response, error) {
		calls++
		return agent.Response{FinishReason: "stop"}, nil
	}, Hooks: agent.SubagentHooks{Start: func(context.Context, string) (int64, error) { return 0, failure }}}
	_, err := delegator.Delegate(context.Background(), map[string]any{"goal": "change repository"})
	require.ErrorIs(t, err, failure)
	require.Zero(t, calls)
}

func TestRuntime_ChildResultsInjectTypedImagesAndPersistReceipt(t *testing.T) {
	image := "data:image/png;base64,eA=="
	drained := false
	receipt := false
	runtime := agent.Runtime{ChildResults: func(context.Context) ([]agent.ChildCompletion, error) {
		if drained {
			return nil, nil
		}
		drained = true
		return []agent.ChildCompletion{{TaskID: 22, Goal: "capture", Status: "completed", Content: "captured", Images: []string{image}, Files: []string{"artifact.png"}}}, nil
	}, Model: func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Response, error) {
		require.True(t, receipt)
		last := req.Messages[len(req.Messages)-1]
		require.Equal(t, image, last.MultiContent[1].ImageURL.URL)
		require.True(t, strings.Contains(last.MultiContent[0].Text, `"task_id":22`))
		require.NotContains(t, last.MultiContent[0].Text, "base64")
		return agent.Response{FinishReason: "stop"}, nil
	}}
	result, err := runtime.Run(context.Background(), agent.Request{}, func(event agent.Event) {
		if event.Kind == agent.ChildResultsReceived {
			receipt = true
			require.EqualValues(t, 22, event.ChildCompletions[0].TaskID)
		}
	})
	require.NoError(t, err)
	require.Equal(t, []string{"artifact.png"}, result.ChildCompletions[0].Files)
}

func TestRuntime_ToolChoiceNoneCannotMutateTaskState(t *testing.T) {
	state := &agent.TaskState{Revision: 9}
	runtime := agent.Runtime{Model: func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Response, error) {
		require.Empty(t, req.Tools)
		return agent.Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{toolCall("state", "saber_task_state")}}, nil
	}, Execute: func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
		t.Fatal("none dispatched a tool")
		return agent.ToolOutput{}, nil
	}}
	result, err := runtime.Run(context.Background(), agent.Request{State: state, Tools: []openai.Tool{agent.TaskStateTool()}, ToolChoice: "none"}, nil)
	require.Error(t, err)
	require.Equal(t, agent.Failed, result.Status)
	require.EqualValues(t, 9, result.State.Revision)
	require.Empty(t, result.Rounds[0].Tools)
}

func TestRuntime_NamedKeywordToolChoiceIsNotPolicyNone(t *testing.T) {
	calls, executed := 0, 0
	runtime := agent.Runtime{Limits: agent.Limits{MaxRounds: 3}, Execute: func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
		executed++
		return agent.ToolOutput{Value: "actual call completed"}, nil
	}, Model: func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Response, error) {
		calls++
		if calls == 1 {
			require.IsType(t, map[string]any{}, req.ToolChoice)
			return agent.Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{toolCall("named", "none")}}, nil
		}
		require.Equal(t, "auto", req.ToolChoice)
		return agent.Response{FinishReason: "stop"}, nil
	}}
	result, err := runtime.Run(context.Background(), agent.Request{Tools: []openai.Tool{subagentTool("none")}, ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "none"}}}, nil)
	require.NoError(t, err)
	require.Equal(t, agent.Completed, result.Status)
	require.Equal(t, 1, executed)
	require.Equal(t, 2, calls)
}

func TestRuntime_EmptyRootScopeSurvivesPersistence(t *testing.T) {
	initial := agent.Request{Tools: []openai.Tool{subagentTool("exec")}, ToolScope: []string{}}
	encoded, err := json.Marshal(initial)
	require.NoError(t, err)
	var restored agent.Request
	require.NoError(t, json.Unmarshal(encoded, &restored))
	require.NotNil(t, restored.ToolScope)
	calls := 0
	executed := false
	runtime := agent.Runtime{Limits: agent.Limits{MaxRounds: 3}, Execute: func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
		executed = true
		return agent.ToolOutput{}, nil
	}, Model: func(ctx context.Context, req agent.Request, _ func(agent.Event)) (agent.Response, error) {
		calls++
		require.Empty(t, req.Tools)
		require.False(t, agent.ToolAllowed(ctx, "exec"))
		if calls == 1 {
			return agent.Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{toolCall("forbidden", "exec")}}, nil
		}
		return agent.Response{FinishReason: "stop"}, nil
	}}
	result, err := runtime.Run(context.Background(), restored, nil)
	require.NoError(t, err)
	require.False(t, executed)
	require.Equal(t, "unknown_tool", result.Rounds[0].Tools[0].ErrorCode)
}

func TestRuntime_PendingFinalDiagnosticsMustReachModel(t *testing.T) {
	models, finalDrains := 0, 0
	runtime := agent.Runtime{Limits: agent.Limits{MaxRounds: 5}, Notifications: func(_ context.Context, wait bool) ([]string, error) {
		if !wait {
			return nil, nil
		}
		finalDrains++
		switch finalDrains {
		case 1:
			return []string{"diagnostics pending, no fresh verdict"}, nil
		case 2:
			return []string{"semantic error: missing identifier"}, nil
		default:
			return nil, nil
		}
	}, Model: func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Response, error) {
		models++
		if models == 2 {
			require.Contains(t, req.Messages[len(req.Messages)-1].Content, "no fresh verdict")
		}
		if models == 3 {
			require.Contains(t, req.Messages[len(req.Messages)-1].Content, "missing identifier")
		}
		return agent.Response{Content: "answer", FinishReason: "stop"}, nil
	}}
	result, err := runtime.Run(context.Background(), agent.Request{}, nil)
	require.NoError(t, err)
	require.Equal(t, 3, models)
	require.Len(t, result.Notifications, 2)
}

func TestRuntime_FinalHooksShareLiveParentBudget(t *testing.T) {
	hooked := false
	runtime := agent.Runtime{Limits: agent.Limits{MaxTotalTokens: 100, MaxRounds: 2}, Model: gatedModel(func(context.Context, agent.Request) (agent.Response, error) {
		return agent.Response{FinishReason: "stop", Usage: openai.Usage{PromptTokens: 1, TotalTokens: 1}}, nil
	}), AfterRun: func(ctx context.Context, _ agent.Request, _ agent.Result, _ error) error {
		require.NoError(t, ctx.Err())
		gate, ok := agent.AttemptGateFromContext(ctx)
		require.True(t, ok)
		request, err := gate.Admit(agent.Request{MaxTokens: 3, Purpose: "evaluation"})
		require.NoError(t, err)
		gate.Record(request, agent.Response{Usage: openai.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}})
		hooked = true
		return nil
	}}
	result, err := runtime.Run(context.Background(), agent.Request{}, nil)
	require.NoError(t, err)
	require.True(t, hooked)
	require.Equal(t, 3, result.Budget.AccountedTokens)
	require.Equal(t, 2, result.Budget.EvaluationUsage.TotalTokens)
}
