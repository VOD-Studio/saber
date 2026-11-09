package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/toolkit"
)

func TestRuntimeToolOutput_TypedMediaDoesNotEnterModelJSON(t *testing.T) {
	attachment := chat.Attachment{Kind: "audio", MIMEType: "audio/wav", Data: []byte("private audio bytes"), URL: "data:audio/wav;base64,cHJpdmF0ZSBhdWRpbyBieXRlcw=="}
	original := toolkit.Result{Data: map[string]any{"attachment": attachment, "literal": map[string]any{"data": "ordinary structured text"}}, Files: []string{"/private/audio.wav"}}
	output := runtimeToolOutput(original)
	encoded, err := json.Marshal(output.Value)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "base64")
	require.NotContains(t, string(encoded), "cHJpdmF0ZQ")
	require.Contains(t, string(encoded), "ordinary structured text")
	require.Contains(t, string(encoded), `"mime_type":"audio/wav"`)
	require.Equal(t, original.Files, output.Files)
	raw := original.Data.(map[string]any)["attachment"].(chat.Attachment)
	require.Equal(t, attachment.Data, raw.Data)
	require.Equal(t, attachment.URL, raw.URL)
}

func TestService_DelegateWorkerModelReadsAuthorizedFile(t *testing.T) {
	var parentCalls, workerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model    string                         `json:"model"`
			Messages []openai.ChatCompletionMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var response string
		switch request.Model {
		case "parent":
			if parentCalls.Add(1) == 1 {
				response = `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"delegate-one","type":"function","function":{"name":"delegate","arguments":"{\"goal\":\"Read source.txt in the authorized workspace and report its content\",\"worker\":\"review\",\"tools\":[\"read_file\"]}"}}]},"finish_reason":"tool_calls"}]}`
			} else {
				require.Contains(t, request.Messages[len(request.Messages)-1].Content, "actual workspace content")
				response = `{"choices":[{"message":{"role":"assistant","content":"workflow complete"},"finish_reason":"stop"}]}`
			}
		case "worker":
			if workerCalls.Add(1) == 1 {
				require.NotContains(t, toolNamesFromRequest(request.Messages), "parent history sentinel")
				response = `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"read-one","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"source.txt\"}"}}]},"finish_reason":"tool_calls"}]}`
			} else {
				require.Contains(t, request.Messages[len(request.Messages)-1].Content, "actual workspace content")
				response = `{"choices":[{"message":{"role":"assistant","content":"Read the authorized file"},"finish_reason":"stop"}]}`
			}
		default:
			t.Errorf("unconfigured route %q", request.Model)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprint(w, response); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: server.URL, APIKey: "test", Models: map[string]config.ModelConfig{"parent": {Model: "parent"}, "worker": {Model: "worker"}}}}
	cfg.AI.DefaultModel = "openai.parent"
	cfg.Agent.Context.Enabled = false
	cfg.Agent.Budget.Enabled = false
	cfg.Agent.Subagents.Mode = "inline"
	cfg.Agent.Subagents.Workers = map[string]config.SubagentWorkerConfig{"review": {Model: "openai.worker"}}
	service, err := NewService(&cfg)
	require.NoError(t, err)
	ctx := authorizedWorkspace(t, service)
	_, dir, ok := execution.TaskScope(ctx)
	require.True(t, ok)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "source.txt"), []byte("actual workspace content"), 0600))
	tools, _ := service.toolExecutor.PrepareTools(ctx)
	result, err := service.RunAgent(ctx, agent.Request{Model: cfg.AI.DefaultModel, Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "parent history sentinel"}}, Tools: tools}, nil)
	require.NoError(t, err)
	require.Equal(t, agent.Completed, result.Status)
	require.EqualValues(t, 2, parentCalls.Load())
	require.EqualValues(t, 2, workerCalls.Load())
}

func toolNamesFromRequest(messages []openai.ChatCompletionMessage) string {
	var b strings.Builder
	for _, message := range messages {
		b.WriteString(message.Content)
	}
	return b.String()
}

func TestService_SubagentRouteRejectsArbitraryCredentials(t *testing.T) {
	service := testSubagentService(t, nil)
	for _, args := range []map[string]any{{"worker": "missing"}, {"provider": "attacker"}, {"base_url": "http://attacker"}, {"api_key": "forged"}, {"images": []any{"file:///etc/passwd"}}} {
		child := agent.Request{Model: service.config.AI.DefaultModel, Messages: []openai.ChatCompletionMessage{{Role: "system"}, {Role: "user", Content: "read"}}}
		require.Error(t, service.routeSubagent(context.Background(), args, &child))
	}
}
