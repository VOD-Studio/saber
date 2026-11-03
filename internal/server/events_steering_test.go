package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// TestServer_EventsExposeSteering 验证排队补充要求生效后，SSE 历史重放能看到 steered 记录。
func TestServer_EventsExposeSteering(t *testing.T) {
	var once sync.Once
	firstArrived := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(firstArrived) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": "完成"}, "finish_reason": "stop",
		}}}); err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: upstream.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.StreamEnabled = false
	service, err := ai.NewService(&cfg)
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
	first := call("POST", "/v1/sessions/demo/messages", `{"id":"first","text":"长任务","model":"openai.local"}`)
	require.Equal(t, 200, first.StatusCode)
	select {
	case <-firstArrived:
	case <-time.After(5 * time.Second):
		t.Fatal("首个任务未开始")
	}
	// 运行中的任务占住工作目录，同目录的第二个任务保持排队。
	dir, err := os.Getwd()
	require.NoError(t, err)
	dir, err = filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session := chat.Session{Platform: "terminal", Account: "saber", Conversation: "demo"}
	sender := chat.Identity{Session: session, SenderID: strconv.Itoa(os.Getuid())}
	queuedMessage := chat.Message{ID: "$queued", SenderID: sender.SenderID, Text: "排队任务", Session: session}
	queued, err := service.Tasks().Submit(ctx, queuedMessage, dir, agent.Request{
		Model:    "openai.local",
		Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: queuedMessage.Text}},
	})
	require.NoError(t, err)
	require.Equal(t, "queued", queued.Status)
	_, err = service.Tasks().Redirect(ctx, sender, queued.ID, "优先检查配置")
	require.NoError(t, err)
	close(release)
	require.Eventually(t, func() bool {
		got, getErr := service.Tasks().Get(ctx, session, queued.ID)
		return getErr == nil && got.Status == "completed"
	}, 10*time.Second, 20*time.Millisecond)

	events := call("GET", fmt.Sprintf("/v1/sessions/demo/tasks/%d/events", queued.ID), "")
	require.Equal(t, 200, events.StatusCode)
	data, err := io.ReadAll(events.Body)
	require.NoError(t, err)
	require.Contains(t, string(data), `"kind":"steered"`)
	require.Contains(t, string(data), `"text":"优先检查配置"`)
	require.Contains(t, string(data), `"status":"completed"`)
}
