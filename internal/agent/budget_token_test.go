package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
)

func TestEstimateTextTokens(t *testing.T) {
	require.Zero(t, estimateTextTokens(""))
	require.Equal(t, 1, estimateTextTokens("ab"))
	require.Equal(t, 1, estimateTextTokens("abcd"))
	require.Equal(t, 2, estimateTextTokens("abcde"))
	// 中文按 UTF-8 字节计：6 个汉字为 18 字节，向上取整为 5 token。
	require.Equal(t, 5, estimateTextTokens("六个中文字符"))
}

func TestEstimateInputTokensWalksRequest(t *testing.T) {
	base := Request{Messages: []openai.ChatCompletionMessage{{Role: "user", Content: strings.Repeat("x", 400)}}}
	require.Equal(t, 101, estimateInputTokens(base), "角色与正文逐字段计数")
	require.Less(t, estimateInputTokens(base), 400, "token 估算不能退化为字节数")

	withTool := base
	withTool.Tools = []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name:        "echo",
		Description: strings.Repeat("d", 40),
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
	}}}
	require.Greater(t, estimateInputTokens(withTool), estimateInputTokens(base), "工具名称、描述与 schema 必须计入")

	withHistory := base
	withHistory.ResponsesHistory = map[string][]json.RawMessage{"call-1": {json.RawMessage(strings.Repeat("h", 400))}}
	require.Greater(t, estimateInputTokens(withHistory), estimateInputTokens(base), "保留的推理历史必须计入")
}

func TestLedger_AdmitErrorIsTokenTruthful(t *testing.T) {
	led := newLedger(100)
	_, err := led.Admit(Request{Messages: []openai.ChatCompletionMessage{{Role: "user", Content: strings.Repeat("x", 800)}}})
	require.ErrorIs(t, err, ErrBudgetExhausted)
	require.ErrorContains(t, err, "estimated input of 201 tokens exceeds remaining token budget of 100")
}

func TestLedger_RecordChargesBeyondReservation(t *testing.T) {
	led := newLedger(1000)
	admitted, err := led.Admit(Request{Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hi"}}, MaxTokens: 10})
	require.NoError(t, err)
	require.Equal(t, 10, admitted.MaxTokens)
	// 实际用量超过预留时仍完整记账，不回滚已完成的请求。
	led.Record(admitted, Response{Usage: openai.Usage{TotalTokens: 900}})
	require.Equal(t, 900, led.accountedTokens())
	require.Equal(t, 100, led.remaining(), "预留必须释放，剩余额度只按实际消耗扣减")
	admitted, err = led.Admit(Request{Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hi"}}, MaxTokens: 1000})
	require.NoError(t, err)
	require.Equal(t, 98, admitted.MaxTokens, "输出上限按剩余 token 收紧")
}

func TestLedger_SplitsMainAndAuxUsage(t *testing.T) {
	led := newLedger(10000)
	admit := func(purpose string) Request {
		req, err := led.Admit(Request{Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hi"}}, MaxTokens: 10, Purpose: purpose})
		require.NoError(t, err)
		return req
	}
	led.Record(admit(""), Response{Usage: openai.Usage{PromptTokens: 30, CompletionTokens: 10, TotalTokens: 40}})
	led.Record(admit("summary"), Response{Usage: openai.Usage{TotalTokens: 5}})
	led.Record(admit("evaluation"), Response{Usage: openai.Usage{TotalTokens: 3}})
	stats := led.stats(1, 1, 1, 0, "")
	require.Equal(t, 40, stats.MainUsage.TotalTokens)
	require.Equal(t, 8, stats.AuxUsage.TotalTokens)
	require.Equal(t, 48, stats.ReportedUsage.TotalTokens, "总用量包含主请求与辅助请求")
	require.Equal(t, 48, stats.AccountedTokens)
}

// TestRuntime_NestedLedgerForwardsChildCharge 验证子运行的每笔消耗都计入父账本，
// 且父账本耗尽后子运行在 Admit 阶段就被拒绝。
func TestRuntime_NestedLedgerForwardsChildCharge(t *testing.T) {
	parent := newLedger(1000)
	parentCtx := WithAttemptGate(context.Background(), parent)
	runChild := func() (Result, error) {
		child := Runtime{
			Limits: Limits{MaxRounds: 2, Timeout: 5 * time.Second, MaxToolOutputBytes: 1024, MaxTotalTokens: 200},
			Model: func(ctx context.Context, req Request, _ func(Event)) (Response, error) {
				gate, ok := AttemptGateFromContext(ctx)
				require.True(t, ok)
				childLedger, ok := gate.(*ledger)
				require.True(t, ok, "子运行必须使用自己的账本")
				require.NotSame(t, parent, childLedger, "子运行不能替换父账本")
				admitted, err := gate.Admit(req)
				if err != nil {
					return Response{}, err
				}
				response := Response{FinishReason: "stop", Content: "ok", Usage: openai.Usage{PromptTokens: 120, CompletionTokens: 80, TotalTokens: 200}}
				gate.Record(admitted, response)
				return response, nil
			},
		}
		return child.Run(parentCtx, Request{Model: "test", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hi"}}}, nil)
	}
	for i := range 5 {
		result, err := runChild()
		require.NoError(t, err, "child %d", i+1)
		require.Equal(t, Completed, result.Status)
		require.Equal(t, 200, result.Usage.TotalTokens, "Result.Usage 只统计主请求")
		require.Equal(t, 200, result.Budget.MainUsage.TotalTokens)
		if i == 3 {
			require.Equal(t, 200, parent.remaining(), "4 个子运行后父账本应剩 200")
		}
	}
	require.Zero(t, parent.remaining())

	sixth, err := runChild()
	require.ErrorIs(t, err, ErrBudgetExhausted)
	require.Equal(t, BudgetExhausted, sixth.Status)
	require.Zero(t, sixth.Budget.AccountedTokens)
}
