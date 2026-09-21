package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/config"
)

func TestResponses_ConfigAndRequest(t *testing.T) {
	var ai config.AIConfig
	require.NoError(t, yaml.Unmarshal([]byte(`
default_model: podlink-responses.gpt-5.6-sol
max_tokens: 8192
temperature: 0.7
providers:
  podlink-responses:
    type: openai
    api: openai-responses
    base_url: http://localhost:8317/v1
    api_key: test-key
    models:
      gpt-5.6-sol:
        model: gpt-5.6-sol
        reasoning_effort: medium
        max_tokens: 65536
models:
  fast:
    provider: podlink-responses
    model: gpt-5.6-sol
`), &ai))
	for _, id := range []string{ai.DefaultModel, "fast"} {
		cfg, found := ai.GetModelConfig(id)
		require.True(t, found)
		require.Equal(t, "openai-responses", cfg.API)
		require.Equal(t, "test-key", cfg.APIKey)
		client, err := NewClientWithModel(&cfg)
		require.NoError(t, err)
		require.True(t, client.usesResponses())
	}
	cfg, _ := ai.GetModelConfig(ai.DefaultModel)
	client, err := NewClientWithModel(&cfg)
	require.NoError(t, err)
	choice := "required"
	req := agent.Request{Model: ai.DefaultModel, MaxTokens: cfg.MaxTokens, Temperature: cfg.Temperature, ToolChoice: &choice,
		Messages: []openai.ChatCompletionMessage{
			{Role: "system", Content: "system"}, {Role: "developer", Content: "developer"},
			{Role: "user", MultiContent: []openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: "look"}, {Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: "data:image/png;base64,eA==", Detail: "low"}}}},
			{Role: "assistant", Content: "checking", ToolCalls: []openai.ToolCall{{ID: "call1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "lookup", Arguments: `{}`}}}},
			{Role: "tool", ToolCallID: "call1", Content: "result"},
		}, Tools: []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}}}},
	}
	request, err := client.responseRequest(req)
	require.NoError(t, err)
	data, err := json.Marshal(request)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(data, &body))
	require.Equal(t, "gpt-5.6-sol", body["model"])
	require.EqualValues(t, 65536, body["max_output_tokens"])
	require.Equal(t, false, body["store"])
	require.NotContains(t, body, "temperature")
	require.NotContains(t, body, "messages")
	require.Equal(t, "required", body["tool_choice"])
	require.Equal(t, map[string]any{"effort": "medium"}, body["reasoning"])
	input := body["input"].([]any)
	require.Len(t, input, 6)
	require.Equal(t, "input_image", input[2].(map[string]any)["content"].([]any)[1].(map[string]any)["type"])
	require.Equal(t, "function_call", input[4].(map[string]any)["type"])
	require.Equal(t, "function_call_output", input[5].(map[string]any)["type"])
	tool := body["tools"].([]any)[0].(map[string]any)
	require.Equal(t, false, tool["strict"])
	require.Equal(t, "lookup", tool["name"])
	require.NotContains(t, tool, "function")
	req.ReasoningEffort = "none"
	request, err = client.responseRequest(req)
	require.NoError(t, err)
	require.Equal(t, float32(.7), *request.Temperature)
	require.Equal(t, "none", request.Reasoning.Effort)
	require.Equal(t, "medium", cfg.ReasoningEffort)
	for _, api := range []string{"anthropic-messages", "typo"} {
		cfg.API = api
		_, err = NewClientWithModel(&cfg)
		require.ErrorContains(t, err, "unsupported api")
	}
}

func TestResponses_ToolLoop(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var requests atomic.Int32
			var tools atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1/responses", r.URL.Path)
				require.Equal(t, "POST", r.Method)
				require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, "gpt-5.6-sol", body["model"])
				require.Equal(t, false, body["store"])
				require.Equal(t, streaming, body["stream"] == true)
				n := requests.Add(1)
				output := `[ {"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]} ]`
				if n == 1 {
					output = `[{"id":"rs_1","type":"reasoning","encrypted_content":"opaque","summary":[]},
      {"id":"fc_1","type":"function_call","call_id":"call1","name":"lookup","arguments":"{\"n\":1}"},
      {"id":"fc_2","type":"function_call","call_id":"call2","name":"lookup","arguments":"{\"n\":2}"}]`
				} else {
					require.EqualValues(t, 2, tools.Load())
					input := body["input"].([]any)
					require.Len(t, input, 6)
					require.Equal(t, "opaque", input[1].(map[string]any)["encrypted_content"])
					require.Equal(t, "call1", input[4].(map[string]any)["call_id"])
					require.Equal(t, "call2", input[5].(map[string]any)["call_id"])
					require.Equal(t, "\"ok\"", input[5].(map[string]any)["output"])
				}
				resp := fmt.Sprintf(`{"status":"completed","model":"gpt-5.6-sol","output":%s,"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`, output)
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					if n == 1 {
						// output_index 0 是推理，工具碎片与完整终态不得重复拼接。
						writeResponseSSE(t, w, `{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call1","name":"lookup","arguments":""}}`)
						writeResponseSSE(t, w, `{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"n\":"}`)
						writeResponseSSE(t, w, `{"type":"response.function_call_arguments.delta","output_index":1,"delta":"1}"}`)
					} else {
						writeResponseSSE(t, w, `{"type":"response.output_text.delta","delta":"do"}`)
						writeResponseSSE(t, w, `{"type":"response.output_text.delta","delta":"ne"}`)
					}
					writeResponseSSE(t, w, `{"type":"response.completed","response":`+resp+`}`)
				} else {
					_, err := fmt.Fprint(w, resp)
					require.NoError(t, err)
				}
			}))
			defer server.Close()
			client, err := NewClientWithModel(&config.ModelConfig{API: "openai-responses", BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "gpt-5.6-sol"})
			require.NoError(t, err)
			runtime := agent.Runtime{
				Model: AgentModel(func(string) (*Client, error) { return client, nil }, &RetryConfigWrapper{}),
				Execute: func(_ context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
					n := tools.Add(1)
					require.Equal(t, "lookup", name)
					require.EqualValues(t, n, args["n"])
					return agent.ToolOutput{Value: "ok"}, nil
				},
			}
			var text strings.Builder
			result, err := runtime.Run(context.Background(), agent.Request{Model: "podlink-responses.gpt-5.6-sol", Stream: streaming, Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "lookup"}}, Tools: []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "lookup"}}}}, func(e agent.Event) {
				if e.Kind == agent.TextDelta {
					text.WriteString(e.Text)
				}
			})
			require.NoError(t, err)
			require.Equal(t, agent.Completed, result.Status)
			require.Equal(t, "done", result.Content)
			require.EqualValues(t, 2, requests.Load())
			require.Equal(t, 30, result.Usage.TotalTokens)
			require.Len(t, result.Rounds[0].Response.ResponsesOutput, 3)
			if streaming {
				require.Equal(t, "done", text.String())
			}
		})
	}
}

func writeResponseSSE(t *testing.T, w http.ResponseWriter, data string) {
	t.Helper()
	if json.Valid([]byte(data)) {
		var compact bytes.Buffer
		require.NoError(t, json.Compact(&compact, []byte(data)))
		data = compact.String()
	}
	_, err := fmt.Fprintf(w, "data: %s\n\n", data)
	require.NoError(t, err)
}

func TestResponses_StreamFailuresAndCompletion(t *testing.T) {
	for _, tc := range []struct{ name, body, wantError, content string }{
		{"text", `{"type":"response.output_text.delta","delta":"hello"}` + "\n" + `{"type":"response.completed","response":{"status":"completed","model":"m","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}}`, "", "hello"},
		{"refusal", `{"type":"response.refusal.delta","delta":"no"}` + "\n" + `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"no"}]}]}}`, "", "no"},
		{"final_only", `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}}`, "", "hello"},
		{"early_eof", `{"type":"response.output_text.delta","delta":"partial"}`, "before response.completed", ""},
		{"done_only", `[DONE]`, "before response.completed", ""},
		{"failed", `{"type":"response.failed","response":{"status":"failed","error":{"code":"bad","message":"failed upstream"}}}`, "failed upstream", ""},
		{"incomplete", `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, "max_output_tokens", ""},
		{"error", `{"type":"error","code":"bad","message":"bad input"}`, "bad input", ""},
		{"nested_error", `{"type":"error","error":{"code":"bad","message":"nested error"}}`, "nested error", ""},
		{"missing_response", `{"type":"response.completed"}`, "has no response", ""},
		{"missing_failure", `{"type":"response.failed"}`, "stream failed", ""},
		{"mismatch", `{"type":"response.output_text.delta","delta":"bad"}` + "\n" + `{"type":"response.completed","response":{"status":"completed","output_text":"good"}}`, "does not match", ""},
		{"malformed", `{invalid`, "read Responses stream", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, line := range strings.Split(tc.body, "\n") {
					writeResponseSSE(t, w, line)
				}
			}))
			defer server.Close()
			client, err := NewClientWithModel(&config.ModelConfig{Provider: "openai-responses", BaseURL: server.URL + "/v1"})
			require.NoError(t, err)
			handler := newAgentStreamHandler(func(agent.Event) {})
			err = client.CreateStreamingChatCompletionWithTools(context.Background(), agent.Request{}, handler)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Empty(t, handler.response().FinishReason)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.content, handler.response().Content)
				require.Equal(t, "stop", handler.response().FinishReason)
			}
			// 普通回调和直接收集流走相同协议入口。
			if tc.wantError == "" {
				handler = newAgentStreamHandler(func(agent.Event) {})
				require.NoError(t, client.CreateStreamingChatCompletion(context.Background(), agent.Request{}, handler))
				resp, err := client.CreateChatCompletion(context.Background(), agent.Request{Stream: true})
				require.NoError(t, err)
				require.Equal(t, tc.content, resp.Content)
			}
		})
	}
}

func TestResponses_RejectUnsupportedInput(t *testing.T) {
	client, err := NewClientWithModel(&config.ModelConfig{API: "openai-responses"})
	require.NoError(t, err)
	for _, req := range []agent.Request{
		{Messages: []openai.ChatCompletionMessage{{Role: "tool"}}},
		{Messages: []openai.ChatCompletionMessage{{Role: "function"}}},
		{Messages: []openai.ChatCompletionMessage{{Role: "assistant", FunctionCall: &openai.FunctionCall{Name: "legacy"}}}},
		{Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "text", MultiContent: []openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: "text"}}}}},
		{Messages: []openai.ChatCompletionMessage{{Role: "user", MultiContent: []openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeImageURL}}}}},
		{Messages: []openai.ChatCompletionMessage{{Role: "user", MultiContent: []openai.ChatMessagePart{{Type: "audio"}}}}},
		{Messages: []openai.ChatCompletionMessage{{Role: "assistant", ToolCalls: []openai.ToolCall{{Type: openai.ToolTypeFunction}}}}},
		{Tools: []openai.Tool{{Type: openai.ToolTypeFunction}}},
	} {
		_, err = client.CreateChatCompletion(context.Background(), req)
		require.Error(t, err)
	}
	for _, output := range []string{
		`{"type":"function_call","status":"in_progress","call_id":"c","name":"f","arguments":"{}"}`,
		`{"type":"function_call","call_id":"","name":"f","arguments":"{}"}`,
		`{"type":"message","content":[{"type":"audio"}]}`,
		`{"type":"custom_tool_call"}`,
		`5`,
	} {
		_, err = responseResult(openai.CreateResponseResponse{Status: openai.ResponseStatusCompleted, Output: []any{json.RawMessage(output)}})
		require.Error(t, err)
	}
}

func TestResponses_HTTPErrorAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"quota","type":"quota"}}`, http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, err := NewClientWithModel(&config.ModelConfig{API: "openai-responses", BaseURL: server.URL + "/v1"})
	require.NoError(t, err)
	for _, stream := range []bool{false, true} {
		_, err = client.CreateChatCompletion(context.Background(), agent.Request{Stream: stream})
		var apiErr *openai.APIError
		require.ErrorAs(t, err, &apiErr)
		require.Equal(t, 429, apiErr.HTTPStatusCode)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = client.CreateChatCompletion(ctx, agent.Request{Stream: stream})
		require.ErrorIs(t, err, context.Canceled)
	}
}

// TestResponses_LivePodlink 仅在显式指定私有配置路径时调用真实中继，不访问 Matrix。
func TestResponses_LivePodlink(t *testing.T) {
	path := os.Getenv("SABER_RESPONSES_CONFIG")
	if path == "" {
		t.Skip("set SABER_RESPONSES_CONFIG to opt into real Podlink requests")
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var cfg config.Config
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	modelID := os.Getenv("SABER_RESPONSES_MODEL")
	if modelID == "" {
		modelID = "podlink-responses.gpt-5.6-sol"
	}
	mc, found := cfg.AI.GetModelConfig(modelID)
	require.True(t, found)
	client, err := NewClientWithModel(&mc)
	require.NoError(t, err)
	require.True(t, client.usesResponses())
	req := agent.Request{Model: modelID, MaxTokens: 1024, Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "Reply with exactly SABER_RESPONSES_OK."}}}
	resp, err := client.CreateChatCompletion(context.Background(), req)
	require.NoError(t, err)
	require.Contains(t, resp.Content, "SABER_RESPONSES_OK")
	t.Logf("non-stream: model=%s finish=%s tokens=%d", resp.Model, resp.FinishReason, resp.Usage.TotalTokens)
	calls := 0
	model := AgentModel(func(string) (*Client, error) { return client, nil }, &RetryConfigWrapper{})
	runtime := agent.Runtime{
		Model: func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Response, error) {
			choice := "auto"
			if calls > 0 {
				choice = "none"
			}
			req.ToolChoice = &choice
			return model(ctx, req, emit)
		},
		Execute: func(_ context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
			require.Equal(t, "saber_protocol_probe", name)
			calls++
			return agent.ToolOutput{Value: "SABER_TOOL_OK"}, nil
		},
	}
	req.Stream = true
	req.Messages = []openai.ChatCompletionMessage{{Role: "user", Content: "Call saber_protocol_probe once, then reply with its exact returned text."}}
	req.Tools = []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "saber_protocol_probe", Description: "Return a fixed protocol test marker without side effects.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}}
	result, err := runtime.Run(context.Background(), req, nil)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Contains(t, result.Content, "SABER_TOOL_OK")
	t.Logf("stream tool loop: rounds=%d calls=%d status=%s tokens=%d", len(result.Rounds), calls, result.Status, result.Usage.TotalTokens)
}
