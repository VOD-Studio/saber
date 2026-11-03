package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
)

func toolCall(name string) openai.ToolCall {
	return openai.ToolCall{ID: "call-1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: name, Arguments: `{}`}}
}

func TestCallTool_StructuredOverBudgetUsesOutputHandle(t *testing.T) {
	r := Runtime{Execute: func(context.Context, string, map[string]any) (ToolOutput, error) {
		return ToolOutput{Value: map[string]any{"text": strings.Repeat("x", 4096)}, OutputID: "t1-0123456789abcdef0123456789abcdef", Structured: true}, nil
	}}
	record := r.callTool(context.Background(), toolCall("read_file"), map[string]bool{"read_file": true}, 128)
	require.True(t, record.Truncated)
	require.Equal(t, "t1-0123456789abcdef0123456789abcdef", record.OutputID)
	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(record.Content), &payload), "structured fallback must stay valid JSON")
	require.Equal(t, "tool_result_exceeds_context_budget", payload["error"])
	require.Equal(t, record.OutputID, payload["output_id"])
	require.NotContains(t, record.Content, "[truncated:")
}

func TestCallTool_StructuredOverBudgetWithoutHandleFails(t *testing.T) {
	r := Runtime{Execute: func(context.Context, string, map[string]any) (ToolOutput, error) {
		return ToolOutput{Value: strings.Repeat("x", 4096), Structured: true}, nil
	}}
	record := r.callTool(context.Background(), toolCall("read_file"), map[string]bool{"read_file": true}, 128)
	require.Equal(t, "tool_result_exceeds_context_budget", record.ErrorCode)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(record.Content), &payload), "budget error must stay valid JSON")
	require.NotContains(t, record.Content, "[truncated:")
}

func TestCallTool_StructuredWithinBudgetIsUnchanged(t *testing.T) {
	r := Runtime{Execute: func(context.Context, string, map[string]any) (ToolOutput, error) {
		return ToolOutput{Value: map[string]string{"text": "short"}, OutputID: "t1-0123456789abcdef0123456789abcdef", Structured: true}, nil
	}}
	record := r.callTool(context.Background(), toolCall("read_file"), map[string]bool{"read_file": true}, 4096)
	require.False(t, record.Truncated)
	require.Contains(t, record.Content, `"short"`)
	require.Equal(t, "t1-0123456789abcdef0123456789abcdef", record.OutputID)
}
