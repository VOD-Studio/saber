package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/config"
)

// newOpenAITestService 创建指向假上游的 AI 服务。
func newOpenAITestService(t *testing.T, upstream string) *Service {
	t.Helper()
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: upstream, APIKey: "secret-test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.Retry.MaxRetries = 0
	service, err := NewService(&cfg)
	require.NoError(t, err)
	t.Cleanup(service.Stop)
	return service
}

func TestResolveOpenAIModel(t *testing.T) {
	service := newOpenAITestService(t, "http://127.0.0.1:1")
	for _, tc := range []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "空名称使用默认模型", input: "", want: "openai.local"},
		{name: "显式模型", input: "openai.local", want: "openai.local"},
		{name: "未知模型", input: "openai.missing", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := service.ResolveOpenAIModel(tc.input)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrOpenAIModelNotFound)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestRunOpenAIChat(t *testing.T) {
	var received map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		w.Header().Set("Content-Type", "application/json")
		_, err := fmt.Fprint(w, `{"id":"x","object":"chat.completion","created":1,"model":"local","choices":[{"index":0,"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
		require.NoError(t, err)
	}))
	defer upstream.Close()
	service := newOpenAITestService(t, upstream.URL)

	temperature := 0.25
	result, err := service.RunOpenAIChat(t.Context(), OpenAIChatRequest{
		Model:       "openai.local",
		Messages:    []OpenAIMessage{{Role: "system", Content: "你是助手"}, {Role: "user", Content: "你好"}},
		Temperature: &temperature,
		MaxTokens:   64,
	}, nil)
	require.NoError(t, err)
	require.Equal(t, agent.Completed, result.Status)
	require.Equal(t, "你好", result.Content)
	require.Equal(t, "openai.local", result.Model)
	require.Equal(t, 5, result.Usage.TotalTokens)
	require.Equal(t, 0.25, received["temperature"])
	require.EqualValues(t, 64, received["max_tokens"])
}
