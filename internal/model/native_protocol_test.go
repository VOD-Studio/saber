package model

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/config"
)

func TestNativeAnthropic_AgentLoopPreservesSignedThinking(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var calls, tools atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1/messages", r.URL.Path)
				require.Equal(t, "test-key", r.Header.Get("x-api-key"))
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, "claude-opus-4-7", body["model"])
				require.Greater(t, body["max_tokens"].(float64), float64(0))
				require.NotContains(t, body, "temperature")
				n := calls.Add(1)
				if n > 1 {
					messages := body["messages"].([]any)
					blocks := messages[1].(map[string]any)["content"].([]any)
					require.Equal(t, "opaque-signature", blocks[0].(map[string]any)["signature"])
					require.Equal(t, "tool1", messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)["tool_use_id"])
					require.EqualValues(t, 1, tools.Load())
				}
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					writeResponseSSE(t, w, `{"type":"message_start","message":{"model":"claude-opus-4-7","usage":{"input_tokens":10}}}`)
					if n == 1 {
						writeResponseSSE(t, w, `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"private chain","signature":"opaque-signature"}}`)
						writeResponseSSE(t, w, `{"type":"content_block_stop","index":0}`)
						writeResponseSSE(t, w, `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool1","name":"lookup","input":{}}}`)
						writeResponseSSE(t, w, `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"n\":1}"}}`)
						writeResponseSSE(t, w, `{"type":"content_block_stop","index":1}`)
						writeResponseSSE(t, w, `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`)
					} else {
						writeResponseSSE(t, w, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
						writeResponseSSE(t, w, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`)
						writeResponseSSE(t, w, `{"type":"content_block_stop","index":0}`)
						writeResponseSSE(t, w, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
					}
					writeResponseSSE(t, w, `{"type":"message_stop"}`)
				} else if n == 1 {
					_, err := fmt.Fprint(w, `{"model":"claude-opus-4-7","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"private chain","signature":"opaque-signature"},{"type":"tool_use","id":"tool1","name":"lookup","input":{"n":1}}],"usage":{"input_tokens":10,"output_tokens":5}}`)
					require.NoError(t, err)
				} else {
					_, err := fmt.Fprint(w, `{"model":"claude-opus-4-7","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":5}}`)
					require.NoError(t, err)
				}
			}))
			defer server.Close()
			client, err := NewClientWithModel(&config.ModelConfig{Provider: "anthropic", BaseURL: server.URL, APIKey: "test-key", Model: "claude-opus-4-7"})
			require.NoError(t, err)
			runtime := agent.Runtime{Model: AgentModel(func(string) (*Client, error) { return client, nil }, &RetryConfigWrapper{}), Execute: func(_ context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
				require.Equal(t, "lookup", name)
				require.EqualValues(t, 1, args["n"])
				tools.Add(1)
				return agent.ToolOutput{Value: 42}, nil
			}}
			result, err := runtime.Run(context.Background(), agent.Request{Model: "anthropic.claude-opus-4-7", Stream: streaming, Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "lookup"}}, Tools: []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}}, nil)
			require.NoError(t, err)
			require.Equal(t, "done", result.Content)
			require.Equal(t, "", result.Rounds[0].Response.Thinking)
			require.EqualValues(t, 2, calls.Load())
		})
	}
}

func TestNativeAnthropic_InterruptedStreamNeverCommitsTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeResponseSSE(t, w, `{"type":"message_start","message":{"model":"claude-opus-4-7","usage":{"input_tokens":1}}}`)
		writeResponseSSE(t, w, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool1","name":"lookup","input":{}}}`)
		writeResponseSSE(t, w, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"n\":"}}`)
	}))
	defer server.Close()
	client, err := NewClientWithModel(&config.ModelConfig{Provider: "anthropic", BaseURL: server.URL, Model: "claude-opus-4-7"})
	require.NoError(t, err)
	var tools atomic.Int32
	runtime := agent.Runtime{Model: AgentModel(func(string) (*Client, error) { return client, nil }, &RetryConfigWrapper{}), Execute: func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
		tools.Add(1)
		return agent.ToolOutput{}, nil
	}}
	_, err = runtime.Run(context.Background(), agent.Request{Model: "a.model", Stream: true, Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "lookup"}}, Tools: []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}}, nil)
	require.Error(t, err)
	require.Zero(t, tools.Load())
}

func TestNativeGemini_LongDecodePreservesRequestAndSumsUsage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1beta/models/gemini-4-argon:generateContent", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		n := calls.Add(1)
		if n == 1 {
			require.NotContains(t, body, "continuationToken")
			_, err := fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"first "}]},"finishReason":"CONTINUATION","continuationToken":"opaque-hop"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}}`)
			require.NoError(t, err)
		} else {
			require.Equal(t, "opaque-hop", body["continuationToken"])
			require.Equal(t, "explain", body["contents"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"])
			_, err := fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"second"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`)
			require.NoError(t, err)
		}
	}))
	defer server.Close()
	client, err := NewClientWithModel(&config.ModelConfig{Provider: "gemini", Model: "gemini-4-argon", BaseURL: server.URL + "/v1beta"})
	require.NoError(t, err)
	response, err := client.CreateChatCompletion(context.Background(), agent.Request{Model: "google.argon", MaxTokens: 65535, Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "explain"}}})
	require.NoError(t, err)
	require.Equal(t, "first second", response.Content)
	require.Equal(t, 12, response.Usage.TotalTokens)
	require.EqualValues(t, 2, calls.Load())
}

func awsEvent(t *testing.T, name, payload string) []byte {
	t.Helper()
	var headers bytes.Buffer
	for _, header := range [][2]string{{":message-type", "event"}, {":event-type", name}, {":content-type", "application/json"}} {
		require.NoError(t, headers.WriteByte(byte(len(header[0]))))
		_, err := headers.WriteString(header[0])
		require.NoError(t, err)
		require.NoError(t, headers.WriteByte(7))
		require.NoError(t, binary.Write(&headers, binary.BigEndian, uint16(len(header[1]))))
		_, err = headers.WriteString(header[1])
		require.NoError(t, err)
	}
	length := 16 + headers.Len() + len(payload)
	frame := make([]byte, length)
	binary.BigEndian.PutUint32(frame[:4], uint32(length))
	binary.BigEndian.PutUint32(frame[4:8], uint32(headers.Len()))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[12:], headers.Bytes())
	copy(frame[12+headers.Len():], payload)
	binary.BigEndian.PutUint32(frame[length-4:], crc32.ChecksumIEEE(frame[:length-4]))
	return frame
}

func TestNativeBedrock_ConverseSignedStreamAndCRC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/model/anthropic.claude/converse-stream", r.URL.Path)
		require.Contains(t, r.Header.Get("Authorization"), "/us-east-1/bedrock/aws4_request")
		require.Equal(t, "session", r.Header.Get("X-Amz-Security-Token"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "question", body["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])
		for _, event := range [][2]string{{"messageStart", `{"role":"assistant"}`}, {"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"answer"}}`}, {"contentBlockStop", `{"contentBlockIndex":0}`}, {"messageStop", `{"stopReason":"end_turn"}`}, {"metadata", `{"usage":{"inputTokens":2,"outputTokens":3,"totalTokens":5}}`}} {
			_, err := w.Write(awsEvent(t, event[0], event[1]))
			require.NoError(t, err)
		}
	}))
	defer server.Close()
	client, err := NewClientWithModel(&config.ModelConfig{Provider: "bedrock", Model: "anthropic.claude", BaseURL: server.URL})
	require.NoError(t, err)
	client.provider.AWS = config.AWSProviderConfig{Region: "us-east-1", AccessKeyID: "access", SecretAccessKey: "secret", SessionToken: "session"}
	response, err := client.CreateChatCompletion(context.Background(), agent.Request{Model: "aws.claude", Stream: true, Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "question"}}})
	require.NoError(t, err)
	require.Equal(t, "answer", response.Content)
	require.Equal(t, 5, response.Usage.TotalTokens)
	corrupt := awsEvent(t, "messageStop", `{"stopReason":"end_turn"}`)
	corrupt[len(corrupt)-1] ^= 1
	called := false
	err = readAWSEvents(bytes.NewReader(corrupt), func(string, []byte) error { called = true; return nil })
	require.Error(t, err)
	require.False(t, called)
}

func TestNativeProtocols_CancelBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, provider := range []string{"anthropic", "gemini"} {
		client, err := NewClientWithModel(&config.ModelConfig{Provider: provider, Model: "native-model", BaseURL: server.URL})
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = client.CreateChatCompletion(ctx, agent.Request{Model: "p.native", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "stop"}}})
		require.True(t, errors.Is(err, context.Canceled), "%v", err)
	}
	require.Zero(t, calls.Load())
}

func TestNativeHistory_DoesNotReplayForeignSignatures(t *testing.T) {
	client, err := NewClientWithModel(&config.ModelConfig{Provider: "anthropic", Model: "model-b", BaseURL: "https://provider.example"})
	require.NoError(t, err)
	response := agent.Request{Model: "p.b", ResponsesHistory: map[string][]json.RawMessage{"call": {json.RawMessage(`{"protocol":"anthropic","origin":"https://different.example","model":"model-a","content":[{"type":"thinking","thinking":"secret","signature":"foreign"}]}`)}}, Messages: []openai.ChatCompletionMessage{{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "lookup", Arguments: "{}"}}}}}}
	body, err := client.anthropicRequest(response)
	require.NoError(t, err)
	data, err := json.Marshal(body)
	require.NoError(t, err)
	require.False(t, strings.Contains(string(data), "foreign"))
	require.False(t, strings.Contains(string(data), "secret"))
}
