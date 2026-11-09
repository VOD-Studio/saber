package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

func runtimeCore(t *testing.T, providers map[string]config.ProviderConfig, defaultModel string, runtime config.ModelRuntimeConfig) *Core {
	t.Helper()
	cfg := config.DefaultAIConfig()
	cfg.Providers, cfg.DefaultModel, cfg.Models = providers, defaultModel, nil
	core, err := NewCore(&cfg)
	require.NoError(t, err)
	if runtime.StateDir == "" {
		runtime.StateDir = t.TempDir()
	}
	require.NoError(t, core.ConfigureRuntime(runtime))
	return core
}

func completionJSON(w http.ResponseWriter, model, content string) error {
	return json.NewEncoder(w).Encode(map[string]any{"model": model, "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
}

func TestModelRuntime_RoutesFallbackAndRejectsPermissionBypass(t *testing.T) {
	var fallbackCalls atomic.Int32
	var status atomic.Int32
	status.Store(429)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, err := fmt.Fprint(w, `{"error":{"code":"rate_limit","message":"denied"}}`)
		require.NoError(t, err)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		require.NoError(t, completionJSON(w, "fallback", "recovered"))
	}))
	defer fallback.Close()
	runtime := config.DefaultModelRuntimeConfig()
	runtime.Routes = map[string]config.ModelRouteConfig{"p.primary": {Targets: []string{"p.primary"}, Fallbacks: []string{"f.fallback"}}}
	core := runtimeCore(t, map[string]config.ProviderConfig{"p": {Type: "openai", BaseURL: primary.URL + "/v1", Models: map[string]config.ModelConfig{"primary": {}}}, "f": {Type: "openai", BaseURL: fallback.URL + "/v1", Models: map[string]config.ModelConfig{"fallback": {}}}}, "p.primary", runtime)
	client, err := core.GetClient("p.primary")
	require.NoError(t, err)
	response, err := client.CreateChatCompletion(context.Background(), agent.Request{Model: "p.primary", MaxTokens: 64, Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "question"}}})
	require.NoError(t, err)
	require.Equal(t, "recovered", response.Content)
	status.Store(403)
	_, err = client.CreateChatCompletion(context.Background(), agent.Request{Model: "p.primary", MaxTokens: 64, Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "another"}}})
	require.Error(t, err)
	require.EqualValues(t, 1, fallbackCalls.Load())
	_, err = core.GetClient("p.not-allowlisted")
	require.Error(t, err)
	runtime.Routes["f.fallback"] = config.ModelRouteConfig{Targets: []string{"p.primary"}}
	require.Error(t, core.ConfigureRuntime(runtime))
}

func TestModelRuntime_CacheIsTrustedIdentityScopedAndUnbilled(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		require.NoError(t, completionJSON(w, "model", "summary"))
	}))
	defer server.Close()
	runtime := config.DefaultModelRuntimeConfig()
	runtime.Cache.Enabled = true
	core := runtimeCore(t, map[string]config.ProviderConfig{"p": {Type: "openai", BaseURL: server.URL + "/v1", Models: map[string]config.ModelConfig{"model": {}}}}, "p.model", runtime)
	client, err := core.GetClient("p.model")
	require.NoError(t, err)
	identity := chat.Identity{Session: chat.Session{Platform: "test", Account: "account", Conversation: "room"}, SenderID: "alice"}
	ctx := chat.WithIdentity(context.Background(), identity)
	request := agent.Request{Model: "p.model", Purpose: "summary", MaxTokens: 64, Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "summarize"}}}
	first, err := client.CreateChatCompletion(ctx, request)
	require.NoError(t, err)
	require.Equal(t, 5, first.Usage.TotalTokens)
	cached, err := client.CreateChatCompletion(ctx, request)
	require.NoError(t, err)
	require.Zero(t, cached.Usage.TotalTokens)
	require.EqualValues(t, 1, calls.Load())
	identity.SenderID = "bob"
	_, err = client.CreateChatCompletion(chat.WithIdentity(context.Background(), identity), request)
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load())
	_, err = client.CreateChatCompletion(context.Background(), request)
	require.NoError(t, err)
	require.EqualValues(t, 3, calls.Load())
}

func TestModelRuntime_CredentialCooldownIsModelScopedAndPersistent(t *testing.T) {
	var keyAForModelB atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if r.Header.Get("Authorization") == "Bearer key-a" && body.Model == "model-a" {
			w.WriteHeader(429)
			_, err := fmt.Fprint(w, `{"error":{"code":"rate_limit","message":"rate limited"}}`)
			require.NoError(t, err)
			return
		}
		if r.Header.Get("Authorization") == "Bearer key-a" && body.Model == "model-b" {
			keyAForModelB.Add(1)
		}
		require.NoError(t, completionJSON(w, body.Model, "ok"))
	}))
	defer server.Close()
	providers := map[string]config.ProviderConfig{"p": {Type: "openai", BaseURL: server.URL + "/v1", Credentials: []config.ProviderCredential{{ID: "a", APIKey: "key-a"}, {ID: "b", APIKey: "key-b"}}, Models: map[string]config.ModelConfig{"model-a": {}, "model-b": {}}}}
	runtime := config.DefaultModelRuntimeConfig()
	runtime.StateDir = t.TempDir()
	core := runtimeCore(t, providers, "p.model-a", runtime)
	client, err := core.GetClient("p.model-a")
	require.NoError(t, err)
	_, err = client.CreateChatCompletion(context.Background(), agent.Request{Model: "p.model-a", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "a"}}})
	require.NoError(t, err)
	core = runtimeCore(t, providers, "p.model-b", runtime)
	client, err = core.GetClient("p.model-b")
	require.NoError(t, err)
	_, err = client.CreateChatCompletion(context.Background(), agent.Request{Model: "p.model-b", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "b"}}})
	require.NoError(t, err)
	require.EqualValues(t, 1, keyAForModelB.Load())
	status, err := core.AuthCommand(context.Background(), []string{"pool-status", "p"})
	require.NoError(t, err)
	encoded, err := json.Marshal(status)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "key-a")
	require.NotContains(t, string(encoded), "key-b")
}

type observedModelGate struct {
	mu     sync.Mutex
	models []string
	total  int
	deny   bool
}

func (g *observedModelGate) Admit(req agent.Request) (agent.Request, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.deny {
		return req, agent.ErrBudgetExhausted
	}
	g.models = append(g.models, req.Model)
	return req, nil
}
func (g *observedModelGate) Record(_ agent.Request, response agent.Response) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.total += response.Usage.TotalTokens
}

func TestModelRuntime_MoAAdvisorsOnceAndEveryActualCallGated(t *testing.T) {
	var refs, acting, tools atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if body["model"] == "reference" {
			refs.Add(1)
			require.NotContains(t, body, "tools")
			require.NoError(t, completionJSON(w, "reference", "Consider checking the value."))
			return
		}
		require.Equal(t, "acting", body["model"])
		if acting.Add(1) == 1 {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"model": "acting", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call1", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}}))
		} else {
			require.EqualValues(t, 1, tools.Load())
			require.NoError(t, completionJSON(w, "acting", "42"))
		}
	}))
	defer server.Close()
	runtimeCfg := config.DefaultModelRuntimeConfig()
	runtimeCfg.MoA.Enabled = true
	runtimeCfg.MoA.DefaultPreset = "review"
	runtimeCfg.MoA.Presets = map[string]config.MoAPresetConfig{"review": {Enabled: true, References: []config.MoASlot{{Model: "p.reference"}}, Aggregator: config.MoASlot{Model: "p.acting"}, Fanout: "user_turn"}}
	core := runtimeCore(t, map[string]config.ProviderConfig{"p": {Type: "openai", BaseURL: server.URL + "/v1", Models: map[string]config.ModelConfig{"reference": {}, "acting": {}}}, "moa": {Type: "moa", BaseURL: "moa://local", Models: map[string]config.ModelConfig{"review": {}}}}, "moa.review", runtimeCfg)
	gate := &observedModelGate{}
	identity := chat.Identity{Session: chat.Session{Platform: "test", Account: "account", Conversation: "room"}, SenderID: "alice"}
	ctx := agent.WithAttemptGate(chat.WithIdentity(context.Background(), identity), gate)
	runtime := agent.Runtime{Model: AgentModel(core.GetClient, &RetryConfigWrapper{}), Execute: func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
		tools.Add(1)
		return agent.ToolOutput{Value: 42}, nil
	}}
	result, err := runtime.Run(ctx, agent.Request{Model: "moa.review", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "find value"}}, Tools: []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}}, nil)
	require.NoError(t, err)
	require.Equal(t, "42", result.Content)
	require.EqualValues(t, 1, refs.Load())
	require.EqualValues(t, 2, acting.Load())
	require.Len(t, gate.models, 3)
	require.Equal(t, 15, gate.total)
	gate.deny = true
	_, err = AgentModel(core.GetClient, &RetryConfigWrapper{})(ctx, agent.Request{Model: "p.acting", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "denied"}}}, nil)
	require.ErrorIs(t, err, agent.ErrBudgetExhausted)
	require.EqualValues(t, 2, acting.Load())
}
