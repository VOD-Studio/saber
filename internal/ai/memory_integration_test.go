package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	chatmemory "rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/memory"
)

// TestTasks_MemorySnapshotInjectedAndScoped 验证快照在任务运行前注入，并只覆盖当前可信作用域。
func TestTasks_MemorySnapshotInjectedAndScoped(t *testing.T) {
	var mu sync.Mutex
	requests := map[string]agent.Request{}
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		last := req.Messages[len(req.Messages)-1].Content
		mu.Lock()
		requests[last] = req
		mu.Unlock()
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok " + last}, "finish_reason": "stop"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer modelServer.Close()

	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: modelServer.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.StreamEnabled = false
	cfg.AI.SystemPrompt = "base prompt"

	mem := newTestMemory(t, memory.Config{})
	// 记忆保存的真实作用域必须与任务运行身份一致：同一平台、账号、发送者。
	private := chat.Identity{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "dm"}, SenderID: "@alice:test", Direct: true}
	if _, err := mem.Add(chat.WithIdentity(context.Background(), private), private, "她希望被称呼为 Alice", memory.Source{}, true); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	service, err := NewService(&cfg, WithMemory(mem))
	require.NoError(t, err)
	defer service.Stop()
	require.NoError(t, service.EnableTasks(filepath.Join(t.TempDir(), "tasks.db")))
	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)

	send := func(message chat.Message) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := service.HandleChat(ctx, message, adapter)
		require.NoError(t, err)
	}
	send(chat.Message{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "dm"}, ID: "$private", SenderID: "@alice:test", Direct: true, Text: "私人问候"})
	send(chat.Message{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "!room"}, ID: "$group", SenderID: "@alice:test", Text: "群里问候"})

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(requests) == 2
	}, 3*time.Second, 20*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	privateReq, ok := requests["私人问候"]
	require.True(t, ok)
	require.Len(t, privateReq.Messages, 3)
	require.Equal(t, "system", privateReq.Messages[1].Role)
	require.Contains(t, privateReq.Messages[1].Content, "她希望被称呼为 Alice")
	require.Contains(t, privateReq.Messages[1].Content, "个人空间")
	// 群里不能读到同一用户的私聊记忆。
	groupReq, ok := requests["群里问候"]
	require.True(t, ok)
	for _, message := range groupReq.Messages {
		require.NotContains(t, message.Content, "她希望被称呼为 Alice")
	}
	require.False(t, strings.Contains(groupReq.Messages[len(groupReq.Messages)-1].Content, "【长期记忆"))
}
