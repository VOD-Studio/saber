package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sashabaranov/go-openai"
)

func TestTrimContext(t *testing.T) {
	call := openai.ToolCall{ID: "old", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "lookup", Arguments: "{}"}}
	req := Request{Messages: []openai.ChatCompletionMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "old question"},
		{Role: "assistant", ToolCalls: []openai.ToolCall{call}},
		{Role: "tool", ToolCallID: "old", Content: "old result"},
		{Role: "assistant", Content: "old answer"},
		{Role: "user", Content: "new question"},
	}, ResponsesHistory: map[string][]json.RawMessage{"old": {json.RawMessage(`{"type":"reasoning"}`)}}}
	for _, tc := range []struct {
		name   string
		policy ContextPolicy
	}{
		{"messages", ContextPolicy{Enabled: true, MaxMessages: 4}},
		{"tokens", ContextPolicy{Enabled: true, MaxInputTokens: 200}},
		{"disabled_history", ContextPolicy{MaxMessages: 50}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TrimContext(req, tc.policy)
			if err != nil || len(got.Messages) != 2 || got.Messages[0].Content != "system" || got.Messages[1].Content != "new question" || len(got.ResponsesHistory) != 0 {
				t.Fatalf("incomplete turn pruning: %+v, %v", got, err)
			}
			if len(req.Messages) != 6 || len(req.ResponsesHistory) != 1 {
				t.Fatal("mutated persisted input")
			}
		})
	}
	req.Messages = req.Messages[:4]
	got, err := TrimContext(req, ContextPolicy{Enabled: true, MaxMessages: 4})
	if err != nil || len(got.Messages) != 4 || len(got.ResponsesHistory) != 1 {
		t.Fatalf("lost current tool round or reasoning: %+v, %v", got, err)
	}
}

func TestRuntime_ContextBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool bool
	}{{"initial input", false}, {"tool result", true}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			r := Runtime{Context: ContextPolicy{Enabled: true, MaxInputTokens: 1000}, Model: func(context.Context, Request, func(Event)) (Response, error) {
				calls++
				return Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{{ID: "call", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "lookup", Arguments: "{}"}}}}, nil
			}, Execute: func(context.Context, string, map[string]any) (ToolOutput, error) {
				return ToolOutput{Value: strings.Repeat("x", 2000)}, nil
			}}
			req := Request{Messages: []openai.ChatCompletionMessage{{Role: "user", Content: strings.Repeat("x", 2000)}}}
			wantCalls := 0
			if tc.tool {
				req.Messages[0].Content = "lookup"
				req.Tools = []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "lookup"}}}
				wantCalls = 1
			}
			result, err := r.Run(context.Background(), req, nil)
			if !errors.Is(err, ErrBudgetExhausted) || result.Status != BudgetExhausted || calls != wantCalls {
				t.Fatalf("budget not enforced before model request: calls=%d, result=%+v, err=%v", calls, result, err)
			}
		})
	}
}
