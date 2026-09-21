package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/config"
)

// TestClient_ReasoningEffortWire 校验所有请求入口的真实 HTTP 字段，防止只改配置却未传递参数。
func TestClient_ReasoningEffortWire(t *testing.T) {
	for _, api := range []string{"openai-completions", "openai-responses"} {
		for _, effort := range []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max"} {
			for _, mode := range []string{"plain", "collect", "callback", "tools"} {
				t.Run(api+"/"+effort+"/"+mode, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var body map[string]any
						require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
						responses := api == "openai-responses"
						field := "reasoning_effort"
						if responses {
							field = "reasoning"
							require.Equal(t, "/v1/responses", r.URL.Path)
						} else {
							require.Equal(t, "/v1/chat/completions", r.URL.Path)
						}
						if effort == "" {
							require.NotContains(t, body, field)
						} else if responses {
							require.Equal(t, map[string]any{"effort": effort}, body[field])
						} else {
							require.Equal(t, effort, body[field])
						}
						resp := `{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
						if responses {
							resp = `{"model":"m","status":"completed","output_text":"ok"}`
						}
						if mode == "plain" {
							_, err := fmt.Fprint(w, resp)
							require.NoError(t, err)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						if responses {
							writeResponseSSE(t, w, `{"type":"response.completed","response":`+resp+`}`)
						} else {
							writeResponseSSE(t, w, `{"model":"m","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
							writeResponseSSE(t, w, `[DONE]`)
						}
					}))
					defer server.Close()
					cfg := config.AIConfig{Providers: map[string]config.ProviderConfig{"relay": {API: api, BaseURL: server.URL + "/v1", ReasoningEffort: effort}}}
					mc, _ := cfg.GetModelConfig("relay.m")
					client, err := NewClientWithModel(&mc)
					require.NoError(t, err)
					ctx := context.Background()
					handler := newAgentStreamHandler(func(agent.Event) {})
					switch mode {
					case "plain", "collect":
						result, err := client.CreateChatCompletion(ctx, agent.Request{Stream: mode == "collect"})
						require.NoError(t, err)
						require.Equal(t, "ok", result.Content)
					case "callback":
						require.NoError(t, client.CreateStreamingChatCompletion(ctx, agent.Request{}, handler))
					case "tools":
						require.NoError(t, client.CreateStreamingChatCompletionWithTools(ctx, agent.Request{}, handler))
					}
				})
			}
		}
	}
}
