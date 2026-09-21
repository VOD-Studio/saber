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

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/task"
)

func TestSchedules_CommandsRestartReportAndNaturalTool(t *testing.T) {
	var calls atomic.Int32
	var failReport atomic.Bool
	failReport.Store(true)
	var mu sync.Mutex
	var reports []event.MessageEventContent
	var paths []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req agent.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		last := req.Messages[len(req.Messages)-1]
		answer := map[string]any{"role": "assistant", "content": "服务正常"}
		finish := "stop"
		if last.Content == "用自然语言建立计划" {
			answer = map[string]any{"role": "assistant", "tool_calls": []openai.ToolCall{{ID: "schedule", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "saber_schedule", Arguments: `{"action":"create","goal":"独立目标","spec":{"kind":"weekdays","at":"09:00","timezone":"Asia/Shanghai"}}`}}}}
			finish = "tool_calls"
		} else if last.Role == openai.ChatMessageRoleTool {
			require.Contains(t, last.Content, "下次执行")
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": answer, "finish_reason": finish}}}))
	}))
	defer model.Close()
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/send/") {
			var content event.MessageEventContent
			require.NoError(t, json.NewDecoder(r.Body).Decode(&content))
			if strings.Contains(content.Body, "：completed") {
				mu.Lock()
				reports = append(reports, content)
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				if failReport.Swap(false) {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, err := fmt.Fprint(w, `{"errcode":"M_UNKNOWN","error":"offline"}`)
					require.NoError(t, err)
					return
				}
			}
			_, err := fmt.Fprint(w, `{"event_id":"$reply"}`)
			require.NoError(t, err)
			return
		}
		_, err := fmt.Fprint(w, `{"event_id":"$source","sender":"@alice:test","type":"m.room.message","content":{"msgtype":"m.text","body":"original"}}`)
		require.NoError(t, err)
	}))
	defer homeserver.Close()
	client, err := mautrix.NewClient(homeserver.URL, "@bot:test", "test")
	require.NoError(t, err)
	client.DefaultHTTPRetries = 0
	commands := matrix.NewCommandService(client, "@bot:test", nil)
	cfg := config.DefaultAIConfig()
	cfg.Enabled, cfg.StreamEnabled = true, false
	cfg.Provider, cfg.BaseURL, cfg.APIKey, cfg.DefaultModel, cfg.SystemPrompt = "openai", model.URL, "test", "local", "system"
	workdir, logs, dbdir := t.TempDir(), t.TempDir(), t.TempDir()
	execCfg := config.ExecutionConfig{Enabled: true, LogDir: logs, Workspaces: map[string]config.WorkspaceConfig{"project": {Path: workdir}}, Grants: []config.ExecutionGrant{{Platform: "matrix", Account: "@bot:test", Room: "!room:test", Users: []string{"@alice:test"}, Workspace: "project", Tools: []string{"read_file"}}}}
	start := func() *Service {
		s, err := NewService(&cfg, commands, nil, nil)
		require.NoError(t, err)
		// 此测试不执行容器工具；容器生命周期有独立真实 Docker 验收。
		s.executor, err = execution.New(execCfg, nil, nil)
		require.NoError(t, err)
		require.NoError(t, s.EnableTasks(filepath.Join(dbdir, "tasks.db")))
		return s
	}
	s := start()
	defer func() { s.Stop() }()
	ctx := matrix.WithMessageRelations(matrix.WithEventID(context.Background(), "$source"), "", "$thread")
	command, ok := commands.GetCommand("schedule")
	require.True(t, ok)
	args := []string{"once", time.Now().Add(3 * time.Second).Format(time.RFC3339), "Asia/Shanghai", "检查服务"}
	require.NoError(t, command.Handler.Handle(ctx, "@alice:test", "!room:test", args))
	require.NoError(t, command.Handler.Handle(ctx, "@alice:test", "!room:test", args))
	session := chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test", Thread: "$thread"}
	plans, err := s.tasks.ListSchedules(ctx, session)
	require.NoError(t, err)
	require.Len(t, plans, 1)
	require.Contains(t, scheduleText(plans[0]), "Asia/Shanghai")
	require.Contains(t, scheduleText(plans[0]), "+08:00")
	require.Zero(t, calls.Load()) // 确定性创建入口不访问模型。
	s.Stop()
	s = start() // 不再发消息，重启后的调度器自行执行并回群。
	var finished task.Task
	require.Eventually(t, func() bool {
		items, e := s.tasks.List(ctx, session)
		if e != nil || len(items) != 1 {
			return false
		}
		finished = items[0]
		return finished.Delivery == "sent"
	}, 8*time.Second, 20*time.Millisecond)
	require.Equal(t, "completed", finished.Status)
	require.EqualValues(t, 1, calls.Load())
	mu.Lock()
	require.Len(t, reports, 2)
	require.Equal(t, paths[0], paths[1])
	require.Equal(t, "$source", string(reports[1].RelatesTo.GetReplyTo()))
	require.Equal(t, "$thread", string(reports[1].RelatesTo.GetThreadParent()))
	mu.Unlock()
	// 不同发生使用不同发送幂等键，但始终引用真实原消息。
	other := finished
	other.EventKey += ":next"
	require.NotEqual(t, taskReply(finished, "result", "").TransactionID, taskReply(other, "result", "").TransactionID)
	naturalCtx := matrix.WithEventID(ctx, "$natural")
	require.NoError(t, s.handleAICommand(naturalCtx, "@alice:test", "!room:test", s.GetModelRegistry().GetDefault(), []string{"用自然语言建立计划"}))
	require.Eventually(t, func() bool {
		p, e := s.tasks.ListSchedules(ctx, session)
		return e == nil && len(p) == 2
	}, 5*time.Second, 10*time.Millisecond)
	plans, err = s.tasks.ListSchedules(ctx, session)
	require.NoError(t, err)
	plan := plans[0]
	require.Equal(t, "$natural", plan.Message.ID)
	require.Equal(t, "@alice:test", plan.Message.SenderID)
	require.Equal(t, "独立目标", plan.Message.Text)
	require.Len(t, plan.Request.Messages, 2)
	require.Equal(t, "独立目标", plan.Request.Messages[1].Content)
	require.Empty(t, plan.Request.Tools) // 运行前按当前权限重新构建。
	msg := plan.Message
	msg.SenderID = "@bob:test"
	_, err = s.scheduleOperation(ctx, msg, scheduleInput{Action: "delete", ID: plan.ID})
	require.Error(t, err)
	_, err = s.scheduleOperation(ctx, msg, scheduleInput{Action: "create", Goal: "bad", Spec: plan.Spec})
	require.Error(t, err)
	msg.SenderID = "@alice:test"
	text, err := s.scheduleOperation(ctx, msg, scheduleInput{Action: "status", ID: plan.ID})
	require.NoError(t, err)
	require.Contains(t, text, "工作目录")
	text, err = s.scheduleOperation(ctx, msg, scheduleInput{Action: "pause", ID: plan.ID})
	require.NoError(t, err)
	require.Contains(t, text, "paused")
	text, err = s.scheduleOperation(ctx, msg, scheduleInput{Action: "delete", ID: plan.ID})
	require.NoError(t, err)
	require.Contains(t, text, "deleted")
	text, err = s.scheduleOperation(ctx, msg, scheduleInput{Action: "list"})
	require.NoError(t, err)
	require.NotContains(t, text, "独立目标")
	_, err = s.toolExecutor.ExecuteToolCall(context.Background(), "saber_schedule", map[string]any{"action": "list"})
	require.Error(t, err)
	msg.Session.Conversation = "!elsewhere:test"
	_, err = s.scheduleOperation(ctx, msg, scheduleInput{Action: "status", ID: plan.ID})
	require.Error(t, err)
}
