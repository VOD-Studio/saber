package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
)

func budgetRequest() Request {
	return Request{Model: "test", Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "目标"}}, Tools: []openai.Tool{functionTool("echo")}}
}

func alwaysToolModel(calls *int, usage openai.Usage) ModelFunc {
	return func(_ context.Context, _ Request, _ func(Event)) (Response, error) {
		*calls++
		return Response{FinishReason: "tool_calls", Usage: usage, ToolCalls: []openai.ToolCall{{ID: fmt.Sprintf("call-%d", *calls), Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "echo", Arguments: `{}`}}}}, nil
	}
}

func TestRuntime_DefaultInitialRounds(t *testing.T) {
	calls := 0
	runtime := Runtime{
		Limits:  Limits{MaxRounds: 10, Timeout: 5 * time.Second, MaxToolOutputBytes: 1024},
		Model:   alwaysToolModel(&calls, openai.Usage{}),
		Execute: func(context.Context, string, map[string]any) (ToolOutput, error) { return ToolOutput{Value: "ok"}, nil },
		Evaluate: func(context.Context, BudgetProgress) (BudgetDecision, error) {
			return BudgetDecision{Continue: false, Reason: "默认软限已到"}, nil
		},
	}
	result, err := runtime.Run(context.Background(), budgetRequest(), nil)
	require.ErrorIs(t, err, ErrBudgetExhausted)
	require.Equal(t, 5, calls)
	require.Equal(t, 5, result.Budget.RoundsUsed)
	require.Equal(t, 5, result.Budget.SoftLimit)
	require.Equal(t, 10, result.Budget.HardLimit)
}

func TestRuntime_EvaluatorExtendsUntilHardLimit(t *testing.T) {
	calls := 0
	var extended int
	evaluations := 0
	runtime := Runtime{
		Limits: Limits{MaxRounds: 6, InitialRounds: 2, ExtensionRounds: 2, Timeout: 5 * time.Second, MaxToolOutputBytes: 1024},
		Model:  alwaysToolModel(&calls, openai.Usage{}),
		Execute: func(context.Context, string, map[string]any) (ToolOutput, error) {
			return ToolOutput{Value: fmt.Sprintf("ok-%d", calls)}, nil
		},
		Evaluate: func(context.Context, BudgetProgress) (BudgetDecision, error) {
			evaluations++
			return BudgetDecision{Continue: true, Reason: "还有下一步", RemainingSteps: []string{"继续"}}, nil
		},
	}
	result, err := runtime.Run(context.Background(), budgetRequest(), func(event Event) {
		if event.Kind == BudgetExtended {
			extended++
			require.NotNil(t, event.Budget)
		}
	})
	require.ErrorIs(t, err, ErrBudgetExhausted)
	require.Equal(t, 2, evaluations)
	require.Equal(t, 2, extended)
	require.Equal(t, 6, calls)
	require.Equal(t, 2, result.Budget.Extensions)
	require.Equal(t, 6, result.Budget.SoftLimit)
	require.Contains(t, result.Budget.StopReason, "hard round")
}

func TestRuntime_EvaluatorStopIsObservable(t *testing.T) {
	calls := 0
	var stopped *BudgetStats
	runtime := Runtime{
		Limits:  Limits{MaxRounds: 10, InitialRounds: 2, ExtensionRounds: 5, Timeout: 5 * time.Second, MaxToolOutputBytes: 1024},
		Model:   alwaysToolModel(&calls, openai.Usage{}),
		Execute: func(context.Context, string, map[string]any) (ToolOutput, error) { return ToolOutput{Value: "ok"}, nil },
		Evaluate: func(context.Context, BudgetProgress) (BudgetDecision, error) {
			return BudgetDecision{Continue: false, Reason: "没有新的可验证步骤"}, nil
		},
	}
	_, err := runtime.Run(context.Background(), budgetRequest(), func(event Event) {
		if event.Kind == BudgetStopped {
			stopped = event.Budget
		}
	})
	require.ErrorIs(t, err, ErrBudgetExhausted)
	require.ErrorContains(t, err, "没有新的可验证步骤")
	require.NotNil(t, stopped)
	require.Equal(t, "没有新的可验证步骤", stopped.StopReason)
	require.Equal(t, 2, calls)
}

func TestRuntime_IdenticalToolLoopStops(t *testing.T) {
	calls := 0
	runtime := Runtime{
		Limits: Limits{MaxRounds: 10, InitialRounds: 2, ExtensionRounds: 1, Timeout: 5 * time.Second, MaxToolOutputBytes: 1024},
		Model:  alwaysToolModel(&calls, openai.Usage{}),
		Execute: func(context.Context, string, map[string]any) (ToolOutput, error) {
			return ToolOutput{Value: map[string]any{"summary": "unchanged", "output_id": fmt.Sprintf("t1-%d", calls)}}, nil
		},
		Evaluate: func(context.Context, BudgetProgress) (BudgetDecision, error) {
			return BudgetDecision{Continue: true, Reason: "继续"}, nil
		},
	}
	_, err := runtime.Run(context.Background(), budgetRequest(), nil)
	require.ErrorIs(t, err, ErrBudgetExhausted)
	require.ErrorContains(t, err, "identical results")
	require.Equal(t, 4, calls)
}

func TestRuntime_LedgerStopsAfterUsageCap(t *testing.T) {
	calls := 0
	runtime := Runtime{
		Limits: Limits{MaxRounds: 10, Timeout: 5 * time.Second, MaxToolOutputBytes: 1024, MaxTotalTokens: 1200},
		Model: func(ctx context.Context, req Request, _ func(Event)) (Response, error) {
			gate, ok := AttemptGateFromContext(ctx)
			if !ok {
				t.Fatal("ledger gate must be available to the model adapter")
			}
			admitted, err := gate.Admit(req)
			if err != nil {
				return Response{}, err
			}
			calls++
			response := Response{FinishReason: "tool_calls", Usage: openai.Usage{PromptTokens: 600, CompletionTokens: 600, TotalTokens: 1200}, ToolCalls: []openai.ToolCall{{ID: fmt.Sprintf("call-%d", calls), Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "echo", Arguments: `{}`}}}}
			gate.Record(admitted, response)
			return response, nil
		},
		Execute: func(context.Context, string, map[string]any) (ToolOutput, error) { return ToolOutput{Value: "ok"}, nil },
	}
	result, err := runtime.Run(context.Background(), budgetRequest(), nil)
	require.ErrorIs(t, err, ErrBudgetExhausted)
	require.Equal(t, 1, calls)
	require.NotNil(t, result.Budget)
	require.Equal(t, 1200, result.Budget.AccountedTokens)
	require.Equal(t, 1200, result.Usage.TotalTokens)
	require.False(t, result.Budget.Estimated)
}

func TestRuntime_EvaluationFailureDoesNotExtend(t *testing.T) {
	calls := 0
	runtime := Runtime{
		Limits:  Limits{MaxRounds: 6, InitialRounds: 1, ExtensionRounds: 2, Timeout: 5 * time.Second, MaxToolOutputBytes: 1024},
		Model:   alwaysToolModel(&calls, openai.Usage{}),
		Execute: func(context.Context, string, map[string]any) (ToolOutput, error) { return ToolOutput{Value: "ok"}, nil },
		Evaluate: func(context.Context, BudgetProgress) (BudgetDecision, error) {
			return BudgetDecision{}, errors.New("evaluation backend down")
		},
	}
	_, err := runtime.Run(context.Background(), budgetRequest(), nil)
	require.ErrorIs(t, err, ErrBudgetExhausted)
	require.ErrorContains(t, err, "budget evaluation failed")
	require.Equal(t, 1, calls)
}

func TestLedger_AdmitClampsAndRejects(t *testing.T) {
	led := newLedger(1000)
	admitted, err := led.Admit(Request{Messages: []openai.ChatCompletionMessage{{Role: "user", Content: strings.Repeat("x", 200)}}, MaxTokens: 2000})
	require.NoError(t, err)
	require.Less(t, admitted.MaxTokens, 2000)
	require.Positive(t, admitted.MaxTokens)
	led.Record(admitted, Response{Usage: openai.Usage{TotalTokens: 1000}})
	_, err = led.Admit(Request{Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "x"}}})
	require.ErrorIs(t, err, ErrBudgetExhausted)
}

func TestLedger_UnlimitedOutputKeepsRequestUncapped(t *testing.T) {
	led := newLedger(1000)
	admitted, err := led.Admit(Request{Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "x"}}})
	require.NoError(t, err)
	require.Zero(t, admitted.MaxTokens)
	led.Record(admitted, Response{Content: "ok", Usage: openai.Usage{TotalTokens: 900}})
	require.Zero(t, led.reservations)
	require.Equal(t, 900, led.accountedTokens())
	// 不设输出上限仍受任务总量约束：剩余不足输入估算时拒绝，不再放大输出预留。
	_, err = led.Admit(Request{Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: strings.Repeat("x", 400)}}})
	require.ErrorIs(t, err, ErrBudgetExhausted)
}

func TestLedger_UsesConservativeEstimateWithoutUsage(t *testing.T) {
	led := newLedger(100000)
	request, err := led.Admit(Request{Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hello"}}, MaxTokens: 10})
	require.NoError(t, err)
	led.Record(request, Response{Content: "world"})
	stats := led.stats(1, 1, 1, 0, "")
	require.True(t, stats.Estimated)
	require.Positive(t, stats.AccountedTokens)
}

func TestParseBudgetDecision(t *testing.T) {
	decision, err := ParseBudgetDecision("```json\n{\"continue\":true,\"reason\":\"ok\",\"remaining_steps\":[\"a\"]}\n```")
	require.NoError(t, err)
	require.True(t, decision.Continue)
	require.Equal(t, "ok", decision.Reason)
	_, err = ParseBudgetDecision("not json")
	require.Error(t, err)
}

func TestRuntime_UnlimitedTotalTokensSkipsLedger(t *testing.T) {
	calls := 0
	runtime := Runtime{
		Limits: Limits{MaxRounds: 3, Timeout: 5 * time.Second, MaxToolOutputBytes: 1024},
		Model: func(ctx context.Context, _ Request, _ func(Event)) (Response, error) {
			if _, gated := AttemptGateFromContext(ctx); gated {
				t.Error("max_total_tokens: 0 must not attach an attempt gate")
			}
			calls++
			if calls == 3 {
				return Response{FinishReason: "stop", Content: "done", Usage: openai.Usage{TotalTokens: 1000}}, nil
			}
			return Response{FinishReason: "tool_calls", Usage: openai.Usage{TotalTokens: 1000}, ToolCalls: []openai.ToolCall{{ID: fmt.Sprintf("call-%d", calls), Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "echo", Arguments: `{}`}}}}, nil
		},
		Execute: func(context.Context, string, map[string]any) (ToolOutput, error) { return ToolOutput{Value: "ok"}, nil },
	}
	result, err := runtime.Run(context.Background(), budgetRequest(), nil)
	require.NoError(t, err)
	require.Equal(t, Completed, result.Status)
	require.Equal(t, 3, calls)
	require.Zero(t, result.Budget.AccountedTokens)
	require.Equal(t, 3000, result.Usage.TotalTokens)
}
