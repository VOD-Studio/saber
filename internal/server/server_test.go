package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/config"
)

func TestServer_ChatReconnectHistoryAndIsolation(t *testing.T) {
	var calls atomic.Int32
	var received []map[string]any
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		received = append(received, req)
		mu.Unlock()
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, err := fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n")
		if err != nil {
			t.Error(err)
			return
		}
		w.(http.Flusher).Flush()
		time.Sleep(80 * time.Millisecond)
		_, err = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"，世界\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		if err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	cfg := config.DefaultAIConfig()
	cfg.Enabled, cfg.Provider, cfg.BaseURL, cfg.APIKey, cfg.DefaultModel = true, "openai", upstream.URL, "secret-test", "local"
	cfg.ReasoningEffort = "medium"
	cfg.Retry.MaxRetries = 0
	service, err := ai.NewService(&cfg, nil, nil, nil)
	require.NoError(t, err)
	defer service.Stop()
	require.NoError(t, service.EnableTasks(filepath.Join(t.TempDir(), "tasks.db")))
	endpoint := httptest.NewServer(New(service, "test-token"))
	defer endpoint.Close()
	call := func(method, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, endpoint.URL+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer test-token")
		response, err := endpoint.Client().Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
		return response
	}
	var first Turn
	response := call("POST", "/v1/sessions/demo/messages", `{"id":"first","text":"记住世界","model":"openai.local","effort":"high"}`)
	require.Equal(t, 200, response.StatusCode)
	require.NoError(t, json.NewDecoder(response.Body).Decode(&first))
	streamPath := fmt.Sprintf("/v1/sessions/demo/tasks/%d/events", first.ID)
	stream := call("GET", streamPath, "")
	require.Equal(t, 200, stream.StatusCode)
	require.NoError(t, stream.Body.Close()) // 断开订阅不取消任务。
	replay := call("GET", streamPath, "")
	data, err := io.ReadAll(replay.Body)
	require.NoError(t, err)
	require.Contains(t, string(data), `"text":"你好"`)
	require.Contains(t, string(data), `"status":"completed"`)
	require.Contains(t, string(data), `"content":"你好，世界"`)
	require.EqualValues(t, 1, calls.Load())
	duplicate := call("POST", "/v1/sessions/demo/messages", `{"id":"first","text":"记住世界"}`)
	var same Turn
	require.NoError(t, json.NewDecoder(duplicate.Body).Decode(&same))
	require.Equal(t, first.ID, same.ID)
	second := call("POST", "/v1/sessions/demo/messages", `{"id":"second","text":"继续","effort":"none"}`)
	var next Turn
	require.NoError(t, json.NewDecoder(second.Body).Decode(&next))
	finished := call("GET", fmt.Sprintf("/v1/sessions/demo/tasks/%d/events", next.ID), "")
	_, err = io.Copy(io.Discard, finished.Body)
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load())
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "high", received[0]["reasoning_effort"])
	require.Equal(t, "none", received[1]["reasoning_effort"])
	history, err := json.Marshal(received[1]["messages"])
	require.NoError(t, err)
	require.Contains(t, string(history), "记住世界")
	require.Contains(t, string(history), "你好，世界")
	require.Equal(t, 404, call("GET", fmt.Sprintf("/v1/sessions/other/tasks/%d/events", first.ID), "").StatusCode)
	info, err := io.ReadAll(call("GET", "/v1/info", "").Body)
	require.NoError(t, err)
	require.NotContains(t, string(info), "secret-test")
	require.Equal(t, 400, call("POST", "/v1/sessions/demo/messages", `{"id":"third","text":"x","model":"unknown"}`).StatusCode)
}

func TestServer_AuthDisabledAndToken(t *testing.T) {
	h := New(nil, "secret")
	for _, tc := range []struct {
		token, origin, path string
		status              int
	}{
		{"", "", "/v1/info", 401}, {"secret", "https://example.com", "/v1/info", 403},
		{"secret", "", "/v1/info", 200}, {"secret", "", "/v1/sessions", 503},
	} {
		req := httptest.NewRequest("GET", tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("Origin", tc.origin)
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, req)
		require.Equal(t, tc.status, recorder.Code)
	}
	path := filepath.Join(t.TempDir(), "token")
	_, err := Token(path, false)
	require.Error(t, err)
	token, err := Token(path, true)
	require.NoError(t, err)
	again, err := Token(path, false)
	require.NoError(t, err)
	require.Equal(t, token, again)
}

func TestServe_Cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, Serve(ctx, &http.Server{Addr: "127.0.0.1:0", Handler: New(nil, "token"), ReadHeaderTimeout: time.Second}))
}

func TestMessage_UnknownFields(t *testing.T) {
	// JSON 边界不接受伪造身份字段。
	decoder := json.NewDecoder(bytes.NewBufferString(`{"id":"a","text":"b","sender":"admin"}`))
	decoder.DisallowUnknownFields()
	require.Error(t, decoder.Decode(new(Message)))
}
