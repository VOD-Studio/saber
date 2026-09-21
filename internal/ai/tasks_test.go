package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/task"
)

func TestTasks_MatrixReceiptIsolationCommandsAndRetry(t *testing.T) {
	var mu sync.Mutex
	var receipts, results []event.MessageEventContent
	var resultPaths []string
	var requests []agent.Request
	var failResult atomic.Bool
	failResult.Store(true)
	release := make(chan struct{})
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "finished " + req.Messages[len(req.Messages)-1].Content}, "finish_reason": "stop"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer modelServer.Close()
	matrixServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/send/") {
			var content event.MessageEventContent
			if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			isResult := strings.Contains(content.Body, "：completed")
			if isResult {
				results = append(results, content)
				resultPaths = append(resultPaths, r.URL.Path)
			} else {
				receipts = append(receipts, content)
			}
			mu.Unlock()
			if isResult && failResult.Swap(false) {
				w.WriteHeader(http.StatusServiceUnavailable)
				if _, err := fmt.Fprint(w, `{"errcode":"M_UNKNOWN","error":"offline"}`); err != nil {
					t.Error(err)
				}
				return
			}
			if _, err := fmt.Fprint(w, `{"event_id":"$delivered"}`); err != nil {
				t.Error(err)
			}
		} else {
			if _, err := fmt.Fprint(w, `{"event_id":"$source","sender":"@alice:test","type":"m.room.message","content":{"msgtype":"m.text","body":"original"}}`); err != nil {
				t.Error(err)
			}
		}
	}))
	defer matrixServer.Close()
	client, err := mautrix.NewClient(matrixServer.URL, "@bot:test", "test")
	require.NoError(t, err)
	client.DefaultHTTPRetries = 0
	commands := matrix.NewCommandService(client, "@bot:test", nil)
	cfg := config.DefaultAIConfig()
	cfg.Enabled = true
	cfg.Provider = "openai"
	cfg.BaseURL = modelServer.URL
	cfg.APIKey = "test"
	cfg.DefaultModel = "local"
	cfg.StreamEnabled = false
	cfg.SystemPrompt = "task system"
	service, err := NewService(&cfg, commands, nil, nil)
	require.NoError(t, err)
	defer service.Stop()
	defer close(release)
	path := filepath.Join(t.TempDir(), "tasks.db")
	require.NoError(t, service.EnableTasks(path))
	require.Error(t, service.EnableTasks(path))
	commands.RegisterCommand("ai", NewAICommand(service))
	eventFor := func(eventID, sender, body string) *event.Event {
		return &event.Event{Type: event.EventMessage, ID: id.EventID(eventID), Sender: id.UserID(sender), RoomID: "!room:test", Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: body, RelatesTo: &event.RelatesTo{Type: event.RelThread, EventID: "$thread"}}}}
	}
	send := func(eventID, sender, body string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, commands.HandleEvent(ctx, eventFor(eventID, sender, body)))
	}
	// 模型被阻塞时，两人的命令仍应立即返回回执。
	send("$alice", "@alice:test", "!task run alice private input")
	send("$bob", "@bob:test", "!ai bob private input")
	send("$alice", "@alice:test", "!task run alice private input")
	session := chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test", Thread: "$thread"}
	tasks, err := service.tasks.List(context.Background(), session)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	b, a := tasks[0], tasks[1]
	require.Equal(t, "@alice:test", a.Message.SenderID)
	require.Equal(t, "@bob:test", b.Message.SenderID)
	require.Len(t, a.Request.Messages, 2)
	require.Len(t, b.Request.Messages, 2)
	require.Equal(t, "alice private input", a.Request.Messages[1].Content)
	require.Equal(t, "bob private input", b.Request.Messages[1].Content)
	mu.Lock()
	require.Len(t, receipts, 3)
	require.Contains(t, receipts[0].Body, fmt.Sprintf("已接收，任务 #%d", a.ID))
	require.Equal(t, id.EventID("$alice"), receipts[0].RelatesTo.GetReplyTo())
	require.Equal(t, id.EventID("$thread"), receipts[0].RelatesTo.GetThreadParent())
	mu.Unlock()
	send("$list", "@alice:test", "!task list")
	send("$status", "@bob:test", fmt.Sprintf("!task status %d", a.ID))
	send("$nl", "@alice:test", "!ai 查看任务列表")
	send("$denied", "@alice:test", fmt.Sprintf("!task cancel %d", b.ID))
	identity := chat.Identity{Session: session, SenderID: "@bob:test"}
	toolCtx := chat.WithIdentity(context.Background(), identity)
	tools, ok := service.toolExecutor.PrepareTools()
	require.True(t, ok)
	require.Equal(t, "saber_task", tools[0].Function.Name)
	output, err := service.toolExecutor.ExecuteToolCall(toolCtx, "saber_task", map[string]any{"action": "status", "id": float64(b.ID)})
	require.NoError(t, err)
	require.Contains(t, output, "bob")
	_, err = service.toolExecutor.ExecuteToolCall(context.Background(), "saber_task", map[string]any{"action": "list"})
	require.Error(t, err)
	_, err = service.toolExecutor.ExecuteToolCall(toolCtx, "saber_task", map[string]any{"action": "cancel", "id": 1.5})
	require.Error(t, err)
	// 取消入队任务的自然语言入口不需要等待模型或工作目录。
	send("$cancel", "@bob:test", fmt.Sprintf("!ai 取消任务 #%d", b.ID))
	b, err = service.tasks.Get(context.Background(), session, b.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", b.Status)
	// 解除模型阻塞；重复发送结果仍必须使用同一个 Matrix 事务路径。
	release <- struct{}{}
	require.Eventually(t, func() bool {
		got, getErr := service.tasks.Get(context.Background(), session, a.ID)
		return getErr == nil && got.Delivery == "sent"
	}, 5*time.Second, 10*time.Millisecond)
	mu.Lock()
	require.Len(t, requests, 1)
	require.Len(t, results, 2)
	require.Equal(t, resultPaths[0], resultPaths[1])
	require.Equal(t, id.EventID("$alice"), results[1].RelatesTo.GetReplyTo())
	require.Equal(t, id.EventID("$thread"), results[1].RelatesTo.GetThreadParent())
	mu.Unlock()
	service.Stop()
	// 重启接回同一数据库，命令仍能查到旧任务及原消息。
	service2, err := NewService(&cfg, commands, nil, nil)
	require.NoError(t, err)
	defer service2.Stop()
	require.NoError(t, service2.EnableTasks(path))
	status, err := service2.taskOperation(context.Background(), identity, "status", a.ID)
	require.NoError(t, err)
	require.Contains(t, status, "completed")
	require.Contains(t, status, "https://matrix.to/#/!room:test/$alice")
}

func TestNaturalTaskCommand(t *testing.T) {
	for _, tc := range []struct {
		text, action string
		id           int64
		ok           bool
	}{
		{"查看任务列表", "list", 0, true}, {"查询任务42的状态", "status", 42, true}, {"取消任务 #42", "cancel", 42, true},
		{"查询任务 42 的状态", "status", 42, true},
		{"查看任务 0", "", 0, false}, {"取消任务 99999999999999999999999999999", "", 0, false}, {"帮我写代码", "", 0, false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			action, id, ok := naturalTaskCommand(tc.text)
			require.Equal(t, tc.action, action)
			require.Equal(t, tc.id, id)
			require.Equal(t, tc.ok, ok)
		})
	}
}

func TestTaskReply_StableBoundedAndScoped(t *testing.T) {
	taskA := task.Task{ID: 1, Message: chat.Message{ID: "$source", SenderID: "alice", Session: chat.Session{Platform: "matrix", Account: "bot", Conversation: "room", Thread: "thread"}}}
	a := taskReply(taskA, "result", strings.Repeat("中", 15000))
	require.Less(t, len([]rune(a.Text)), 12100)
	require.Equal(t, a.TransactionID, taskReply(taskA, "result", "different content").TransactionID)
	require.NotEqual(t, a.TransactionID, taskReply(taskA, "received", "").TransactionID)
	taskA.Message.Session.Account = "other"
	require.NotEqual(t, a.TransactionID, taskReply(taskA, "result", "").TransactionID)
}
