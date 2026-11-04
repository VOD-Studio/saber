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

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	chatmemory "rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
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
		if last == "私人问候" || last == "群里问候" {
			mu.Lock()
			requests[last] = req
			mu.Unlock()
		}
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
		_, privateOK := requests["私人问候"]
		_, groupOK := requests["群里问候"]
		return privateOK && groupOK
	}, 3*time.Second, 20*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	privateReq, ok := requests["私人问候"]
	require.True(t, ok)
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

// TestTasks_MatrixPrivateMemory 验证私聊命令保存的记忆进入自动回复，但不泄露给其他用户或群聊。
func TestTasks_MatrixPrivateMemory(t *testing.T) {
	ctx := context.Background()
	requests := make(chan agent.Request, 3)
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Messages[len(req.Messages)-1].Content == "请使用已保存的偏好" {
			requests <- req
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer modelServer.Close()
	matrixServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/state"):
			members := []string{"@bot:test", "@alice:test"}
			if strings.Contains(r.URL.Path, "!bob:test") {
				members[1] = "@bob:test"
			}
			if strings.Contains(r.URL.Path, "!group:test") {
				members = append(members, "@bob:test")
			}
			state := make([]map[string]any, 0, len(members))
			for _, member := range members {
				state = append(state, map[string]any{"type": "m.room.member", "state_key": member, "content": map[string]any{"membership": "join"}})
			}
			if err := json.NewEncoder(w).Encode(state); err != nil {
				t.Error(err)
			}
		case strings.Contains(r.URL.Path, "/send/"):
			if err := json.NewEncoder(w).Encode(map[string]string{"event_id": "$reply"}); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer matrixServer.Close()
	client, err := mautrix.NewClient(matrixServer.URL, "@bot:test", "test")
	require.NoError(t, err)
	commands := matrix.NewCommandService(client, "@bot:test", nil)
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: modelServer.URL}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.StreamEnabled = false
	mem := newTestMemory(t, memory.Config{})
	service, err := NewService(&cfg, WithMemory(mem))
	require.NoError(t, err)
	defer service.Stop()
	require.NoError(t, service.EnableTasks(filepath.Join(t.TempDir(), "tasks.db")))
	adapter := matrix.NewChatAdapter(commands, nil, config.MediaConfig{}, false, service.HandleChat)
	require.NoError(t, service.RegisterTaskDelivery("matrix", adapter))
	saved := chat.Message{Session: adapter.Session(ctx, "!alice:test"), ID: "$save", SenderID: "@alice:test", Direct: true}
	require.NoError(t, service.MemoryCommand(ctx, saved, adapter, "add", "Alice 的个人暗号是紫色海豚"))
	entries, err := mem.List(ctx, chat.Identity{Session: saved.Session, SenderID: saved.SenderID, Direct: true})
	require.NoError(t, err)
	require.Len(t, entries, 1)

	for _, tc := range []struct {
		name   string
		room   id.RoomID
		sender id.UserID
		want   bool
	}{
		{"本人私聊", "!alice:test", "@alice:test", true},
		{"其他用户私聊", "!bob:test", "@bob:test", false},
		{"本人在群聊", "!group:test", "@alice:test", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messageCtx := matrix.WithEventID(ctx, id.EventID("$"+tc.room))
			_, err := adapter.Receive(messageCtx, tc.sender, tc.room, "请使用已保存的偏好")
			require.NoError(t, err)
			select {
			case req := <-requests:
				found := false
				for _, message := range req.Messages {
					found = found || strings.Contains(message.Content, "Alice 的个人暗号是紫色海豚")
				}
				require.Equal(t, tc.want, found, "个人记忆注入必须与可信私聊身份一致")
			case <-time.After(3 * time.Second):
				t.Fatal("未收到模型请求")
			}
		})
	}
}
