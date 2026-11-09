package memoryproviders

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

func TestMemori_OfficialCaptureRecallAndTools(t *testing.T) {
	ctx := nativeMemoryTestContext("alice")
	scope, err := ScopedIdentity(ctx)
	require.NoError(t, err)
	session, err := nativeLocalSession(ctx)
	require.NoError(t, err)
	var mu sync.Mutex
	var turn, augmentation map[string]any
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/v1/agent/augmentation", r.URL.Path)
		require.Equal(t, "Bearer test-memori", r.Header.Get("Authorization"))
		require.NotEmpty(t, r.Header.Get("X-Memori-API-Key"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		augmentation = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer collector.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer test-memori", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/sdk/quota":
			require.Equal(t, "GET", r.Method)
			_, _ = w.Write([]byte(`{"memories":{"num":1,"max":100}}`))
		case "/v1/agent/conversation/turn":
			require.Equal(t, "POST", r.Method)
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			mu.Lock()
			turn = body
			mu.Unlock()
			_, _ = w.Write([]byte(`{"success":true,"id":"turn_1"}`))
		case "/v1/agent/recall":
			require.Equal(t, "GET", r.Method)
			require.Equal(t, scope, r.URL.Query().Get("entity_id"))
			require.Equal(t, scope, r.URL.Query().Get("project_id"))
			require.Equal(t, "jasmine", r.URL.Query().Get("query"))
			_, _ = w.Write([]byte(`{"memories":[{"content":"User prefers jasmine tea","source":"fact","signal":"verification"}]}`))
		case "/v1/agent/recall/summary":
			require.Equal(t, scope, r.URL.Query().Get("project_id"))
			_, _ = w.Write([]byte(`{"summary":"Tea preference recorded"}`))
		case "/v1/agent/compaction":
			require.Equal(t, scope, r.URL.Query().Get("project_id"))
			require.Equal(t, session, r.URL.Query().Get("session_id"))
			_, _ = w.Write([]byte(`{"summary":"Continue current scoped task"}`))
		case "/v1/sdk/account":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "reader@example.org", body["email"])
			_, _ = w.Write([]byte(`{"content":"Signup email sent"}`))
		case "/v1/agent/feedback":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "Recall was useful", body["content"])
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected Memori route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	backend, err := NewMemori(config.MemoryBackendConfig{Endpoint: server.URL, APIKey: "test-memori", AllowPrivate: true, Options: map[string]any{"collector_endpoint": collector.URL}}, toolkit.Host{})
	require.NoError(t, err)
	defer func() { require.NoError(t, backend.Close()) }()
	_, err = backend.Call(ctx, "initialize", nil)
	require.NoError(t, err)
	messages := []map[string]any{
		{"role": "user", "content": "I prefer jasmine tea."},
		{"role": "assistant", "content": "", "tool_calls": []map[string]any{{"id": "call_1", "function": map[string]any{"name": "read_file", "arguments": `{"path":"tea.md"}`}}}},
		{"role": "tool", "tool_call_id": "call_1", "content": "jasmine is available"},
		{"role": "assistant", "content": "I recorded your preference."},
	}
	_, err = backend.Call(ctx, "sync_turn", nativeMemoryEnvelope(map[string]any{"messages": messages, "status": "completed"}))
	require.NoError(t, err)
	mu.Lock()
	captured := turn
	augmented := augmentation
	mu.Unlock()
	require.Equal(t, scope, captured["attribution"].(map[string]any)["entity"].(map[string]any)["id"])
	require.Equal(t, scope, captured["project"].(map[string]any)["id"])
	require.Equal(t, session, captured["session"].(map[string]any)["id"])
	trace := augmented["trace"].(map[string]any)["tools"].([]any)[0].(map[string]any)
	require.Equal(t, "read_file", trace["name"])
	require.Equal(t, "tea.md", trace["args"].(map[string]any)["path"])
	require.Equal(t, "jasmine is available", trace["result"])
	_, err = backend.Call(ctx, "context", nativeMemoryEnvelope(map[string]any{"query": "jasmine"}))
	require.NoError(t, err)
	tools, err := backend.Call(ctx, "tools", nil)
	require.NoError(t, err)
	require.Len(t, tools, 6)
	for _, call := range []struct {
		name string
		args map[string]any
	}{{"memori_recall_summary", map[string]any{}}, {"memori_compaction", map[string]any{}}, {"memori_signup", map[string]any{"email": "reader@example.org"}}, {"memori_feedback", map[string]any{"content": "Recall was useful"}}} {
		_, err = backend.Call(ctx, "tool", nativeMemoryEnvelope(map[string]any{"tool_name": call.name, "arguments": call.args}))
		require.NoError(t, err)
	}
}

func TestMemori_CancellationScopeAndPartialWriteErrors(t *testing.T) {
	var turns int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/agent/conversation/turn" {
			turns++
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		http.Error(w, "private credential must never be returned", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	backend, err := NewMemori(config.MemoryBackendConfig{Endpoint: server.URL, APIKey: "private-credential", AllowPrivate: true}, toolkit.Host{})
	require.NoError(t, err)
	defer func() { require.NoError(t, backend.Close()) }()
	ctx := nativeMemoryTestContext("alice")
	_, err = backend.Call(ctx, "sync_turn", nativeMemoryEnvelope(map[string]any{"user_content": "I prefer tea", "assistant_content": "Noted", "status": "completed"}))
	require.ErrorContains(t, err, "turn was captured")
	require.NotContains(t, err.Error(), "private-credential")
	require.Equal(t, 1, turns)
	_, err = backend.Call(ctx, "tool", nativeMemoryEnvelope(map[string]any{"tool_name": "memori_recall", "arguments": map[string]any{"entity_id": "someone-else", "query": "tea"}}))
	require.ErrorContains(t, err, "trusted memory scope")
	_, err = backend.Call(ctx, "memori_recall", nativeMemoryEnvelope(map[string]any{"source": "fact", "signal": "failure"}))
	require.ErrorContains(t, err, "classification pair")
	_, err = backend.Call(ctx, "pre_compress", nil)
	require.ErrorContains(t, err, "does not support")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = backend.Call(canceled, "initialize", nil)
	require.ErrorIs(t, err, context.Canceled)
}
