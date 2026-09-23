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
			isResult := strings.HasPrefix(content.Body, "finished ")
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
			if _, err := fmt.Fprint(w, `{"event_id":"$source","sender":"@bot:test","type":"m.room.message","content":{"msgtype":"m.text","body":"original"}}`); err != nil {
				t.Error(err)
			}
		}
	}))
	defer matrixServer.Close()
	client, err := mautrix.NewClient(matrixServer.URL, "@bot:test", "test")
	require.NoError(t, err)
	client.DefaultHTTPRetries = 0
	commands := matrix.NewCommandService(client, "@bot:test", nil)
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: modelServer.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.StreamEnabled = false
	cfg.Agent.TaskReceiptEnabled = true
	cfg.AI.SystemPrompt = "task system"
	service, err := NewService(&cfg, WithMatrix(commands, nil))
	require.NoError(t, err)
	defer service.Stop()
	defer close(release)
	path := filepath.Join(t.TempDir(), "tasks.db")
	require.NoError(t, service.EnableTasks(path))
	require.Error(t, service.EnableTasks(path))
	wireMatrixPlatform(t, service, commands, nil)
	commands.RegisterCommand("ai", NewAICommand(service))
	commands.SetReplyAIHandler(NewAICommand(service))
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
	cancelEvent := eventFor("$cancel", "@bob:test", fmt.Sprintf("> <@bot:test> 已接收，任务 #%d\n\n取消任务 #%d", b.ID, b.ID))
	cancelEvent.Content.AsMessage().RelatesTo.InReplyTo = &event.InReplyTo{EventID: "$receipt"}
	require.NoError(t, commands.HandleEvent(context.Background(), cancelEvent))
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
	service2, err := NewService(&cfg, WithMatrix(commands, nil))
	require.NoError(t, err)
	defer service2.Stop()
	require.NoError(t, service2.EnableTasks(path))
	wireMatrixPlatform(t, service2, commands, nil)
	status, err := service2.taskOperation(context.Background(), identity, "status", a.ID)
	require.NoError(t, err)
	require.Contains(t, status, "completed")
	require.Contains(t, status, "https://matrix.to/#/!room:test/$alice")
	followCtx := matrix.WithMessageRelations(matrix.WithEventID(context.Background(), "$follow"), "$alice", "$thread")
	require.NoError(t, service2.handleAICommand(followCtx, "@alice:test", "!room:test", service2.GetModelRegistry().GetDefault(), []string{"再检查一下"}))
	release <- struct{}{}
	require.Eventually(t, func() bool {
		ts, err := service2.tasks.List(context.Background(), session)
		return err == nil && len(ts) == 3 && ts[0].Status == "completed"
	}, 3*time.Second, 10*time.Millisecond)
	mu.Lock()
	require.Len(t, requests, 2)
	require.Equal(t, "alice private input", requests[1].Messages[1].Content)
	require.Equal(t, "finished alice private input", requests[1].Messages[2].Content)
	require.Equal(t, "再检查一下", requests[1].Messages[len(requests[1].Messages)-1].Content)
	mu.Unlock()
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

func TestTasks_MatrixReplyKeepsQuoteUnlessContinuingTask(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprintf("direct=%t", direct), func(t *testing.T) {
			seen := make(chan agent.Request, 4)
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req agent.Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				seen <- req
				if _, err := fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"解释完成"},"finish_reason":"stop"}]}`); err != nil {
					t.Error(err)
				}
			}))
			defer model.Close()
			quotes := map[string]string{"$proactive": "今天适合读书", "$help": "使用 !task 查看任务", "$legacy": "旧机器人消息", "$known": "展示摘要不应再次进入续接输入"}
			homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/state") {
					if _, err := fmt.Fprint(w, `[{"type":"m.room.member","state_key":"@bot:test","content":{"membership":"join"}},{"type":"m.room.member","state_key":"@alice:test","content":{"membership":"join"}}]`); err != nil {
						t.Error(err)
					}
					return
				}
				if r.Method == http.MethodGet {
					if err := json.NewEncoder(w).Encode(map[string]any{"event_id": filepath.Base(r.URL.Path), "sender": "@bot:test", "type": "m.room.message", "content": map[string]string{"msgtype": "m.text", "body": quotes[filepath.Base(r.URL.Path)]}}); err != nil {
						t.Error(err)
					}
					return
				}
				if err := json.NewEncoder(w).Encode(map[string]string{"event_id": "$" + filepath.Base(r.URL.Path)}); err != nil {
					t.Error(err)
				}
			}))
			defer homeserver.Close()
			client, err := mautrix.NewClient(homeserver.URL, "@bot:test", "test")
			require.NoError(t, err)
			commands := matrix.NewCommandService(client, "@bot:test", nil)
			cfg := *config.DefaultConfig()
			cfg.AI.Enabled = true
			cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: model.URL, APIKey: "test"}}
			cfg.AI.DefaultModel = "openai.local"
			cfg.Agent.StreamEnabled = false
			cfg.AI.SystemPrompt = "system"
			service, err := NewService(&cfg, WithMatrix(commands, nil))
			require.NoError(t, err)
			defer service.Stop()
			require.NoError(t, service.EnableTasks(filepath.Join(t.TempDir(), "tasks.db")))
			wireMatrixPlatform(t, service, commands, nil)
			commands.SetReplyAIHandler(NewAICommand(service))
			if direct {
				commands.SetDirectChatAIHandler(NewAICommand(service))
			}
			session := chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test"}
			for _, replyTo := range []string{"$proactive", "$help", "$legacy", "$known"} {
				t.Run(replyTo, func(t *testing.T) {
					incoming := &event.Event{Type: event.EventMessage, ID: id.EventID("$reply-" + replyTo), Sender: "@alice:test", RoomID: "!room:test", Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "> <@bot:test> " + quotes[replyTo] + "\n\n解释刚才这句话", RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: id.EventID(replyTo)}}}}}
					require.NoError(t, commands.HandleEvent(context.Background(), incoming))
					var req agent.Request
					select {
					case req = <-seen:
					case <-time.After(3 * time.Second):
						t.Fatal("model request missing")
					}
					last := req.Messages[len(req.Messages)-1].Content
					if replyTo == "$known" {
						require.Equal(t, "解释刚才这句话", last)
						require.Contains(t, fmt.Sprint(req.Messages), quotes["$legacy"])
						require.NotContains(t, fmt.Sprint(req.Messages), quotes[replyTo])
					} else {
						expected := "[引用消息]\n" + quotes[replyTo] + "\n\n[回复]\n解释刚才这句话"
						if direct {
							expected = incoming.Content.AsMessage().Body
						}
						require.Equal(t, expected, last)
					}
					tasks, err := service.tasks.List(context.Background(), session)
					require.NoError(t, err)
					require.Equal(t, last, tasks[0].Message.Text)
					if replyTo == "$legacy" {
						require.NoError(t, service.tasks.RememberMessage(context.Background(), tasks[0].ID, "$known"))
					}
				})
			}
			if direct {
				tasks, err := service.tasks.List(context.Background(), session)
				require.NoError(t, err)
				cancelEvent := &event.Event{Type: event.EventMessage, ID: "$cancel", Sender: "@alice:test", RoomID: "!room:test", Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: fmt.Sprintf("> <@bot:test> 回执\n\n取消任务 #%d", tasks[0].ID), RelatesTo: &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: "$known"}}}}}
				require.NoError(t, commands.HandleEvent(context.Background(), cancelEvent))
				tasks, err = service.tasks.List(context.Background(), session)
				require.NoError(t, err)
				require.Len(t, tasks, 4)
			}
		})
	}
}

func TestService_EnableTasksWithoutMatrix(t *testing.T) {
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.DefaultModel = "openai.local"
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", APIKey: "test", BaseURL: "http://127.0.0.1:1/v1"}}
	service, err := NewService(&cfg)
	require.NoError(t, err)
	defer service.Stop()
	require.NoError(t, service.EnableTasks(filepath.Join(t.TempDir(), "tasks.db")))
	require.NotNil(t, service.tasks)
}
