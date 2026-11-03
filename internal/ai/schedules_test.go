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
	"rua.plus/saber/internal/memory"
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
			if content.Body == "服务正常" {
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
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled, cfg.Agent.StreamEnabled = true, false
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: model.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.AI.SystemPrompt = "system"
	workdir, logs, dbdir := t.TempDir(), t.TempDir(), t.TempDir()
	execCfg := config.ExecutionConfig{Enabled: true, Backend: "local", LogDir: logs, Workspaces: map[string]config.WorkspaceConfig{"project": {Path: workdir}}, Grants: []config.ExecutionGrant{{Platform: "matrix", Account: "@bot:test", Room: "!room:test", Users: []string{"@alice:test"}, Workspace: "project", Tools: []string{"read_file"}}}}
	start := func() *Service {
		s, err := NewService(&cfg, WithMatrix(commands, nil))
		require.NoError(t, err)
		// 此测试不执行容器工具；容器生命周期有独立真实 Docker 验收。
		s.executor, err = execution.New(execCfg, nil, nil)
		require.NoError(t, err)
		require.NoError(t, s.EnableTasks(filepath.Join(dbdir, "tasks.db")))
		wireMatrixPlatform(t, s, commands, nil)
		return s
	}
	s := start()
	defer func() { s.Stop() }()
	ctx := matrix.WithMessageRelations(matrix.WithEventID(context.Background(), "$source"), "", "$thread")
	args := []string{"once", time.Now().Add(3 * time.Second).Format(time.RFC3339), "Asia/Shanghai", "检查服务"}
	adapter := matrix.NewChatAdapter(commands, nil, cfg.Matrix.Media, false, nil)
	message := adapter.Message(ctx, "@alice:test", "!room:test", "!schedule "+strings.Join(args, " "))
	require.NoError(t, s.ScheduleCommand(ctx, message, adapter, args[0], strings.Join(args[1:], " ")))
	require.NoError(t, s.ScheduleCommand(ctx, message, adapter, args[0], strings.Join(args[1:], " ")))
	session := chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test", Thread: "$thread"}
	plans, err := s.tasks.ListSchedules(ctx, session)
	require.NoError(t, err)
	require.Len(t, plans, 1)
	require.True(t, plans[0].RequiresExecution, "持有执行工具的创建者应保留授权要求")
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
	require.NoError(t, runMatrixChat(s, naturalCtx, "@alice:test", "!room:test", s.GetModelRegistry().GetDefault(), "用自然语言建立计划"))
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
	// 没有执行授权的成员也能创建纯聊天计划：不申请工作区，目标末尾 --silent 即静默投递。
	chatMsg := msg
	chatMsg.ID, chatMsg.SenderID = "$bob-chat", "@bob:test"
	bobText, err := s.scheduleOperation(ctx, chatMsg, scheduleInput{Action: "create", Goal: "纯聊天提醒 --silent", Spec: task.ScheduleSpec{Kind: "every", Timezone: "UTC", Every: "1h"}})
	require.NoError(t, err)
	require.Contains(t, bobText, "纯聊天")
	bobPlans, err := s.tasks.ListSchedules(ctx, session)
	require.NoError(t, err)
	require.NotEmpty(t, bobPlans)
	require.Equal(t, "@bob:test", bobPlans[0].Message.SenderID)
	require.False(t, bobPlans[0].RequiresExecution)
	require.Equal(t, "silent", bobPlans[0].Delivery)
	require.Equal(t, s.taskDir, bobPlans[0].WorkDir)
	msg.Session.Conversation = "!elsewhere:test"
	_, err = s.scheduleOperation(ctx, msg, scheduleInput{Action: "status", ID: plan.ID})
	require.Error(t, err)
}

// silentAdapter 只记录发送次数，用于验证静默计划不向聊天投递。
type silentAdapter struct{ sends atomic.Int32 }

func (a *silentAdapter) Capabilities() chat.Capabilities { return chat.Capabilities{} }

func (a *silentAdapter) Send(context.Context, chat.Reply) (string, error) {
	a.sends.Add(1)
	return "$sent", nil
}

func (a *silentAdapter) Edit(context.Context, string, chat.Reply) error { return nil }

func (a *silentAdapter) SetTyping(context.Context, chat.Session, bool) error { return nil }

// 纯聊天计划没有执行授权也能创建和运行：静默投递只落库，记忆按计划来源作用域注入。
func TestSchedules_PureChatSilentRunAndMemoryScope(t *testing.T) {
	type captured struct {
		goal  string
		text  string
		tools []string
	}
	var mu sync.Mutex
	var requests []captured
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []openai.ChatCompletionMessage `json:"messages"`
			Tools    []openai.Tool                  `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		var item captured
		for i, message := range body.Messages {
			item.text += message.Content + "\n"
			if i == len(body.Messages)-1 {
				item.goal = message.Content
			}
		}
		for _, tool := range body.Tools {
			item.tools = append(item.tools, tool.Function.Name)
		}
		mu.Lock()
		requests = append(requests, item)
		mu.Unlock()
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": "到点了"}, "finish_reason": "stop",
		}}}))
	}))
	defer model.Close()
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled, cfg.Agent.StreamEnabled = true, false
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: model.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.AI.SystemPrompt = "system"
	mem := newTestMemory(t, memory.Config{GroupWriter: func(chat.Identity) bool { return true }})
	dm := chat.Identity{Session: chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!dm:test"}, SenderID: "@alice:test", Direct: true}
	group := chat.Identity{Session: chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test"}, SenderID: "@alice:test"}
	_, err := mem.Add(chat.WithIdentity(context.Background(), dm), dm, "私聊偏好：只喝美式", memory.Source{}, true)
	require.NoError(t, err)
	_, err = mem.Add(chat.WithIdentity(context.Background(), group), group, "群约定：周五发周报", memory.Source{}, true)
	require.NoError(t, err)
	svc, err := NewService(&cfg, WithMemory(mem))
	require.NoError(t, err)
	defer svc.Stop()
	require.NoError(t, svc.EnableTasks(filepath.Join(t.TempDir(), "tasks.db")))
	adapter := &silentAdapter{}
	require.NoError(t, svc.RegisterTaskDelivery("matrix", adapter))
	ctx := context.Background()
	at := time.Now().Add(3 * time.Second).Format(time.RFC3339)
	dmMessage := chat.Message{Session: dm.Session, ID: "$dm-chat", SenderID: dm.SenderID, Direct: true, Text: "dm"}
	_, err = svc.scheduleOperation(ctx, dmMessage, scheduleInput{Action: "create", Goal: "私聊晚安", Delivery: "silent", Spec: task.ScheduleSpec{Kind: "once", Timezone: "UTC", At: at}})
	require.NoError(t, err)
	groupMessage := chat.Message{Session: group.Session, ID: "$group-chat", SenderID: group.SenderID, Text: "group"}
	_, err = svc.scheduleOperation(ctx, groupMessage, scheduleInput{Action: "create", Goal: "群里周报", Delivery: "silent", Spec: task.ScheduleSpec{Kind: "once", Timezone: "UTC", At: at}})
	require.NoError(t, err)
	plans, err := svc.tasks.ListSchedules(ctx, dm.Session)
	require.NoError(t, err)
	require.Len(t, plans, 1)
	require.False(t, plans[0].RequiresExecution)
	require.Equal(t, "silent", plans[0].Delivery)
	require.Equal(t, svc.taskDir, plans[0].WorkDir)
	var dmTask, groupTask task.Task
	require.Eventually(t, func() bool {
		dmTasks, dmErr := svc.tasks.List(ctx, dm.Session)
		groupTasks, groupErr := svc.tasks.List(ctx, group.Session)
		if dmErr != nil || groupErr != nil || len(dmTasks) != 1 || len(groupTasks) != 1 {
			return false
		}
		dmTask, groupTask = dmTasks[0], groupTasks[0]
		return dmTask.Status == "completed" && groupTask.Status == "completed" && dmTask.Delivery == "sent" && groupTask.Delivery == "sent"
	}, 15*time.Second, 50*time.Millisecond)
	require.True(t, dmTask.Silent)
	require.True(t, groupTask.Silent)
	require.Zero(t, adapter.sends.Load(), "静默计划不应向聊天投递")
	require.Equal(t, "到点了", dmTask.Result.Content)
	// 私聊计划只注入个人记忆，群计划只注入群记忆；工具集合不含执行或 MCP 能力。
	mu.Lock()
	defer mu.Unlock()
	seen := map[string]bool{}
	for _, item := range requests {
		switch item.goal {
		case "私聊晚安":
			seen["dm"] = true
			require.Contains(t, item.text, "私聊偏好：只喝美式")
			require.NotContains(t, item.text, "群约定：周五发周报")
		case "群里周报":
			seen["group"] = true
			require.Contains(t, item.text, "群约定：周五发周报")
			require.NotContains(t, item.text, "私聊偏好：只喝美式")
		default:
			continue
		}
		require.Contains(t, item.text, "不是本轮指令")
		for _, name := range item.tools {
			require.True(t, chatToolNames[name], "纯聊天计划拿到了工具 %s", name)
		}
	}
	require.True(t, seen["dm"] && seen["group"], "两次计划的模型请求都应到达")
}

// 静默计划在投递层直接成功返回，不触发任何平台发送。
func TestSchedules_SilentDeliverySkipsSend(t *testing.T) {
	adapter := &silentAdapter{}
	svc := &Service{}
	id, err := svc.deliverTask(context.Background(), adapter, task.Task{ID: 7, Status: "completed", Silent: true})
	require.NoError(t, err)
	require.Empty(t, id)
	require.Zero(t, adapter.sends.Load())
}

// 定时运行的记忆注入只信任计划保存的来源快照：私聊注入个人记忆，群计划只注入群记忆，
// 快照与当前可信身份不一致时跳过。
func TestSchedules_ScheduledMemoryScope(t *testing.T) {
	mem := newTestMemory(t, memory.Config{GroupWriter: func(chat.Identity) bool { return true }})
	ctx := context.Background()
	dm := chat.Identity{Session: chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!dm:test"}, SenderID: "@alice:test", Direct: true}
	group := chat.Identity{Session: chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test"}, SenderID: "@alice:test"}
	_, err := mem.Add(chat.WithIdentity(ctx, dm), dm, "私聊偏好：只喝美式", memory.Source{}, true)
	require.NoError(t, err)
	_, err = mem.Add(chat.WithIdentity(ctx, group), group, "群约定：周五发周报", memory.Source{}, true)
	require.NoError(t, err)
	svc := newMemoryAIService(t, mem, 0)
	req := agent.Request{Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: "base"}}}
	text := func(got agent.Request) string {
		var joined strings.Builder
		for _, message := range got.RuntimeContext {
			joined.WriteString(message.Content)
		}
		return joined.String()
	}
	// 私聊快照与当前身份一致：注入个人记忆，不注入群记忆。
	dmMessage := chat.Message{Session: dm.Session, ID: "$dm", SenderID: dm.SenderID, Direct: true, Text: "提醒"}
	_, got := svc.scheduledMemoryRequest(chat.WithIdentity(ctx, dm), dmMessage, req)
	require.Contains(t, text(got), "私聊偏好：只喝美式")
	require.NotContains(t, text(got), "群约定：周五发周报")
	// 群快照：只注入群记忆，个人记忆不跨群。
	groupMessage := chat.Message{Session: group.Session, ID: "$group", SenderID: group.SenderID, Text: "周报"}
	_, got = svc.scheduledMemoryRequest(chat.WithIdentity(ctx, group), groupMessage, req)
	require.Contains(t, text(got), "群约定：周五发周报")
	require.NotContains(t, text(got), "私聊偏好：只喝美式")
	// 快照私聊标记与当前身份不一致：无法证明作用域，跳过注入。
	forged := groupMessage
	forged.Direct = true
	_, got = svc.scheduledMemoryRequest(chat.WithIdentity(ctx, group), forged, req)
	require.Empty(t, got.RuntimeContext)
	// 发送者不一致：跳过注入。
	_, got = svc.scheduledMemoryRequest(chat.WithIdentity(ctx, dm), chat.Message{Session: dm.Session, ID: "$x", SenderID: "@bob:test", Direct: true, Text: "x"}, req)
	require.Empty(t, got.RuntimeContext)
}

// 没有执行授权的用户也必须能通过模型调用 saber_schedule 创建纯聊天计划；
// 计划是否申请执行授权仍由创建时冻结的工具集合决定，不因工具可见而提升权限。
func TestPrepareTools_ScheduleVisibleWithoutExecutionGrant(t *testing.T) {
	var mu sync.Mutex
	var advertised [][]string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		last := req.Messages[len(req.Messages)-1]
		answer := map[string]any{"role": "assistant", "content": "已完成"}
		finish := "stop"
		if last.Role != openai.ChatMessageRoleTool {
			mu.Lock()
			advertised = append(advertised, toolNames(req.Tools))
			mu.Unlock()
			goal := "每天提醒喝水"
			if strings.Contains(last.Content, "目录") {
				goal = "运行 ls 并汇报目录"
			}
			answer = map[string]any{"role": "assistant", "tool_calls": []openai.ToolCall{{ID: "schedule", Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{Name: "saber_schedule", Arguments: fmt.Sprintf(`{"action":"create","goal":%q,"delivery":"silent","spec":{"kind":"every","timezone":"UTC","every":"24h"}}`, goal)}}}}
			finish = "tool_calls"
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": answer, "finish_reason": finish}}}))
	}))
	defer model.Close()
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled, cfg.Agent.StreamEnabled = true, false
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: model.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.AI.SystemPrompt = "system"
	svc, err := NewService(&cfg)
	require.NoError(t, err)
	defer svc.Stop()
	workdir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	execCfg := config.ExecutionConfig{Enabled: true, Backend: "local", LogDir: t.TempDir(), Workspaces: map[string]config.WorkspaceConfig{"project": {Path: workdir}}, Grants: []config.ExecutionGrant{{Platform: "matrix", Account: "@bot:test", Room: "!room:test", Users: []string{"@alice:test"}, Workspace: "project", Tools: []string{"read_file"}}}}
	svc.executor, err = execution.New(execCfg, nil, nil)
	require.NoError(t, err)
	require.NoError(t, svc.EnableTasks(filepath.Join(t.TempDir(), "tasks.db")))
	require.NoError(t, svc.RegisterTaskDelivery("matrix", &silentAdapter{}))

	session := chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test"}
	bob := chat.Identity{Session: session, SenderID: "@bob:test"}
	tools, ok := svc.toolExecutor.PrepareTools(chat.WithIdentity(context.Background(), bob))
	require.True(t, ok)
	require.Contains(t, toolNames(tools), "saber_schedule")
	require.NotContains(t, toolNames(tools), "read_file", "无授权用户不应看到执行工具")

	// 真实运行任务：模型拿到 saber_schedule 才能通过运行时校验并创建计划。
	for eventID, text := range map[string]string{"$bob-tool": "每天提醒喝水", "$bob-exec": "运行 ls 并汇报目录"} {
		msg := chat.Message{Session: session, ID: eventID, SenderID: bob.SenderID, Text: text}
		_, err = svc.tasks.Submit(context.Background(), msg, svc.taskDir, agent.Request{
			Model: cfg.AI.DefaultModel, Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: text}},
		})
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool {
		plans, listErr := svc.tasks.ListSchedules(context.Background(), session)
		return listErr == nil && len(plans) == 2
	}, 10*time.Second, 20*time.Millisecond)
	plans, err := svc.tasks.ListSchedules(context.Background(), session)
	require.NoError(t, err)
	require.Len(t, plans, 2)
	for _, plan := range plans {
		require.False(t, plan.RequiresExecution, "无授权用户的计划不得申请执行授权")
		require.Equal(t, "silent", plan.Delivery)
		require.Equal(t, svc.taskDir, plan.WorkDir)
	}
	mu.Lock()
	require.NotEmpty(t, advertised)
	for _, names := range advertised {
		require.Contains(t, names, "saber_schedule")
		require.NotContains(t, names, "exec")
		require.NotContains(t, names, "read_file")
	}
	mu.Unlock()

	// 即便以后获得授权，纯聊天计划运行时仍只保留聊天工具。
	grantedCtx := execution.WithTask(chat.WithIdentity(context.Background(), chat.Identity{Session: session, SenderID: "@alice:test"}), 1, workdir)
	require.True(t, scheduleNeedsExecution(svc.executor.Tools(grantedCtx)))
	require.NotContains(t, toolNames(filterChatTools(append(tools, svc.executor.Tools(grantedCtx)...))), "exec")
}
