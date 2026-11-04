package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/db"
	"rua.plus/saber/internal/task"
)

// traceHarness 装配真实任务管理器与执行器，让导出走与生产相同的工具调用路径。
type traceHarness struct {
	svc     *Service
	mgr     *task.Manager
	dbPath  string
	session chat.Session
	mu      sync.Mutex
	run     func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error)
	values  []any
	errs    []error
}

func newTraceHarness(t *testing.T) *traceHarness {
	t.Helper()
	h := &traceHarness{
		dbPath:  filepath.Join(t.TempDir(), "tasks.db"),
		session: chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test"},
	}
	svc, err := NewService(createTestMultiProviderAIConfig())
	require.NoError(t, err)
	require.NoError(t, svc.ConfigureExecution(config.ExecutionConfig{LogDir: t.TempDir()}, nil, nil))
	mgr, err := task.Open(h.dbPath, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		h.mu.Lock()
		run := h.run
		h.mu.Unlock()
		if run == nil {
			return agent.Result{Status: agent.Completed}, nil
		}
		return run(ctx, req, emit)
	}, nil)
	require.NoError(t, err)
	h.svc, h.mgr = svc, mgr
	svc.tasks, svc.taskDir = mgr, t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, mgr.Close())
		svc.Stop()
	})
	return h
}

// runOn 设置任务执行函数；返回的调用会在任务上下文里读取工具结果。
func (h *traceHarness) runOn(run func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.run = run
}

func (h *traceHarness) submit(t *testing.T, sender, event string) task.Task {
	t.Helper()
	submitted, err := h.mgr.Submit(context.Background(), chat.Message{
		ID: event, SenderID: sender, Text: "导出轨迹", Session: h.session,
	}, t.TempDir(), agent.Request{Model: "test"})
	require.NoError(t, err)
	return submitted
}

func (h *traceHarness) wait(t *testing.T, id int64) task.Task {
	t.Helper()
	var got task.Task
	require.Eventually(t, func() bool {
		var err error
		got, err = h.mgr.Get(context.Background(), h.session, id)
		return err == nil && got.Status != "queued" && got.Status != "running"
	}, 10*time.Second, 10*time.Millisecond)
	return got
}

func (h *traceHarness) record(value any, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.values = append(h.values, value)
	h.errs = append(h.errs, err)
}

func (h *traceHarness) captured(t *testing.T) (any, error) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	require.Equal(t, 1, len(h.values), "每次用例只应捕获一次工具调用")
	return h.values[0], h.errs[0]
}

func (h *traceHarness) captureAll(t *testing.T) ([]any, []error) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]any(nil), h.values...), append([]error(nil), h.errs...)
}

// insertTraceRecords 直接写入原始记录，用于构造损坏、未知与含加密推理的真实数据库状态。
func insertTraceRecords(t *testing.T, dbPath string, taskID int64, records []string) {
	t.Helper()
	handle, err := sql.Open(db.DriverName, dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, handle.Close()) }()
	for _, record := range records {
		_, err := handle.Exec(`INSERT INTO task_events(task_id,created_at,record) VALUES(?,?,?)`, taskID, time.Now().UnixMilli(), record)
		require.NoError(t, err)
	}
}

// readTraceLines 读取导出产物并按行解码，任何半行都会让用例失败。
func readTraceLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	if len(data) == 0 {
		return nil
	}
	require.Equal(t, byte('\n'), data[len(data)-1], "末行必须是完整 JSON 行")
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var line map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &line), "每一行都必须是完整 JSON: %q", raw)
		lines = append(lines, line)
	}
	return lines
}

func TestTraceExport_SelfExportSkipsBadRecordsAndKeepsUnknownKind(t *testing.T) {
	h := newTraceHarness(t)
	args := `{"command":"ls","api_key":"sk-live-secret"}`
	h.runOn(func(ctx context.Context, _ agent.Request, emit func(agent.Event)) (agent.Result, error) {
		taskID := task.ID(ctx)
		emit(agent.Event{Kind: agent.ToolStarted, Round: 2, Tool: agent.ToolRecord{
			Call: openai.ToolCall{ID: "call-1", Function: openai.FunctionCall{Name: "exec", Arguments: args}},
		}})
		emit(agent.Event{Kind: "future_kind", Round: 3})
		emit(agent.Event{Kind: agent.TextDelta, Round: 4, Text: `{"authorization":"Bearer secret","note":"hi"}`})
		insertTraceRecords(t, h.dbPath, taskID, []string{
			`{"Kind":"run_started"`,
			`{}`,
			`{"Kind":"context_compacted","Round":5,"Checkpoint":{"Request":{"ResponsesHistory":{"call-1":["ZW5jcnlwdGVk"]}}}}`,
		})
		value, err := h.svc.toolExecutor.ExecuteToolCall(ctx, "saber_trace", map[string]any{"action": "export", "task_id": float64(taskID)})
		h.record(value, err)
		return agent.Result{Status: agent.Completed}, nil
	})
	submitted := h.submit(t, "@alice:test", "trace-self")
	require.Equal(t, "completed", h.wait(t, submitted.ID).Status)
	value, err := h.captured(t)
	require.NoError(t, err)
	result, ok := value.(traceExportResult)
	require.True(t, ok)
	// 损坏记录与空对象被跳过并计数，其余合法事件全部导出。
	require.EqualValues(t, 2, result.Skipped)
	require.GreaterOrEqual(t, result.Records, 4)
	require.False(t, result.Truncated)
	artifact := h.svc.executor.ArtifactDir(submitted.ID)
	lines := readTraceLines(t, filepath.Join(artifact, result.Artifact))
	require.Len(t, lines, result.Records)
	info, err := os.Stat(filepath.Join(artifact, result.Artifact))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dirInfo, err := os.Stat(artifact)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
	for i, line := range lines {
		require.EqualValues(t, i+1, line["seq"])
		require.EqualValues(t, submitted.ID, line["task_id"])
		require.Equal(t, "", line["batch_id"])
		require.NotEmpty(t, line["at"])
	}
	byKind := map[string]map[string]any{}
	for _, line := range lines {
		byKind[line["kind"].(string)] = line
	}
	toolLine := byKind["tool_started"]
	tool := toolLine["tool"].(map[string]any)
	require.Equal(t, "exec", tool["name"])
	require.Equal(t, "call-1", tool["call_id"])
	require.EqualValues(t, len(args), tool["args_bytes"])
	require.NotContains(t, toolLine, "content")
	// 未知 kind 原样保留并附原始长度，不因为不认识而丢弃。
	unknown := byKind["future_kind"]
	require.NotNil(t, unknown)
	require.Greater(t, unknown["raw_bytes"].(float64), float64(0))
	require.NotContains(t, byKind["task_state_updated"], "raw_bytes")
	raw, err := os.ReadFile(filepath.Join(artifact, result.Artifact))
	require.NoError(t, err)
	for _, forbidden := range []string{"ResponsesHistory", "ZW5jcnlwdGVk", "sk-live-secret", "Bearer secret", "hello"} {
		require.NotContains(t, string(raw), forbidden, "默认导出不得包含正文或加密推理")
	}
}

func TestTraceExport_IncludeContentRequiresRoleAndRedacts(t *testing.T) {
	h := newTraceHarness(t)
	h.runOn(func(ctx context.Context, _ agent.Request, emit func(agent.Event)) (agent.Result, error) {
		taskID := task.ID(ctx)
		emit(agent.Event{Kind: agent.ToolStarted, Round: 2, Tool: agent.ToolRecord{
			Call: openai.ToolCall{ID: "call-9", Function: openai.FunctionCall{Name: "exec", Arguments: `{"command":"env","api_key":"sk-live","nested":{"token":"abc","keep":1}}`}},
		}})
		emit(agent.Event{Kind: agent.TextDelta, Round: 3, Text: `{"authorization":"Bearer secret","note":"hi"}`})
		insertTraceRecords(t, h.dbPath, taskID, []string{
			`{"Kind":"context_compacted","Round":4,"Checkpoint":{"Request":{"ResponsesHistory":{"call-9":["ZW5jcnlwdGVk"]}}}}`,
		})
		value, err := h.svc.toolExecutor.ExecuteToolCall(ctx, "saber_trace", map[string]any{"action": "export", "task_id": float64(taskID), "include_content": true})
		h.record(value, err)
		return agent.Result{Status: agent.Completed}, nil
	})
	submitted := h.submit(t, "@alice:test", "trace-content")
	require.Equal(t, "completed", h.wait(t, submitted.ID).Status)
	value, err := h.captured(t)
	require.NoError(t, err)
	result := value.(traceExportResult)
	lines := readTraceLines(t, filepath.Join(h.svc.executor.ArtifactDir(submitted.ID), result.Artifact))
	require.Len(t, lines, result.Records)
	byKind := map[string]map[string]any{}
	for _, line := range lines {
		byKind[line["kind"].(string)] = line
	}
	require.Contains(t, byKind["tool_started"]["args"], `"command":"env"`)
	require.Contains(t, byKind["tool_started"]["args"], `"keep":1`)
	for _, line := range lines {
		encoded, err := json.Marshal(line)
		require.NoError(t, err)
		for _, forbidden := range []string{"sk-live", `"token"`, "authorization", "ResponsesHistory", "ZW5jcnlwdGVk"} {
			require.NotContains(t, string(encoded), forbidden, "include_content 也必须剔除凭据与加密推理")
		}
	}
	require.Equal(t, `{"note":"hi"}`, byKind["text_delta"]["content"])
}

func TestTraceExport_RejectsOtherSenderAndMissingContext(t *testing.T) {
	h := newTraceHarness(t)
	owner := h.submit(t, "@alice:test", "trace-owner")
	require.Equal(t, "completed", h.wait(t, owner.ID).Status)
	// 他人任务：即使同群可见编号，也不能借自己的任务导出无关轨迹。
	h.runOn(func(ctx context.Context, _ agent.Request, _ func(agent.Event)) (agent.Result, error) {
		value, err := h.svc.toolExecutor.ExecuteToolCall(ctx, "saber_trace", map[string]any{"action": "export", "task_id": float64(owner.ID)})
		h.record(value, err)
		return agent.Result{Status: agent.Completed}, nil
	})
	intruder := h.submit(t, "@bob:test", "trace-intruder")
	require.Equal(t, "completed", h.wait(t, intruder.ID).Status)
	_, err := h.captured(t)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unrelated task")

	identity := chat.Identity{Session: h.session, SenderID: "@alice:test"}
	// 参数与上下文校验必须在真实任务上下文里跑：缺少任务编号时管理员身份也救不回来。
	h.runOn(func(ctx context.Context, _ agent.Request, _ func(agent.Event)) (agent.Result, error) {
		for _, args := range []map[string]any{
			{"action": "export", "task_id": "seven"},
			{"action": "export", "task_id": float64(0)},
			{"action": "export", "task_id": float64(owner.ID), "format": "csv"},
			{"action": "delete", "task_id": float64(owner.ID)},
			{"action": "export", "task_id": float64(999999)},
		} {
			value, err := h.svc.toolExecutor.ExecuteToolCall(ctx, "saber_trace", args)
			h.record(value, err)
		}
		value, err := h.svc.toolExecutor.ExecuteToolCall(context.Background(), "saber_trace", map[string]any{"action": "export", "task_id": float64(owner.ID)})
		h.record(value, err)
		value, err = h.svc.toolExecutor.ExecuteToolCall(chat.WithIdentity(context.Background(), identity), "saber_trace", map[string]any{"action": "export", "task_id": float64(owner.ID)})
		h.record(value, err)
		return agent.Result{Status: agent.Completed}, nil
	})
	checked := h.submit(t, "@alice:test", "trace-errors")
	require.Equal(t, "completed", h.wait(t, checked.ID).Status)
	_, errs := h.captureAll(t)
	require.Len(t, errs, 8)
	for i, want := range []string{"unrelated task", "positive integer", "positive integer", "unsupported trace format", "only export is supported", "task #999999 not found", "可信身份", "持久任务上下文"} {
		require.Error(t, errs[i], "case %d must fail", i)
		require.Contains(t, errs[i].Error(), want, "case %d", i)
	}

	disabled := &Service{}
	_, err = disabled.executeTraceTool(context.Background(), map[string]any{"action": "export", "task_id": float64(1)})
	require.ErrorContains(t, err, "任务服务未启用")
}

func TestBuildTraceLines_TruncatesOnlyOnCompleteLines(t *testing.T) {
	payload := strings.Repeat("x", 6<<20)
	records := make([]task.EventRecord, 0, 3)
	for i := range 3 {
		record, err := json.Marshal(map[string]any{
			"Kind":  "tool_finished",
			"Round": i + 1,
			"Tool": map[string]any{
				"Call":    map[string]any{"id": fmt.Sprintf("call-%d", i), "function": map[string]any{"name": "exec", "arguments": "{}"}},
				"Content": payload,
			},
		})
		require.NoError(t, err)
		records = append(records, task.EventRecord{At: time.UnixMilli(1700000000000), Record: string(record)})
	}
	data, emitted, skipped, truncated := buildTraceLines(42, "", records, true)
	require.True(t, truncated)
	require.EqualValues(t, 2, emitted)
	require.EqualValues(t, 0, skipped)
	require.LessOrEqual(t, len(data), traceMaxBytes)
	require.Equal(t, byte('\n'), data[len(data)-1], "截断后的末行必须是完整行")
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	require.Len(t, lines, 2)
	for _, line := range lines {
		var decoded traceLine
		require.NoError(t, json.Unmarshal([]byte(line), &decoded))
		require.EqualValues(t, 42, decoded.TaskID)
	}
}

func TestTraceExport_RegisteredAndReserved(t *testing.T) {
	h := newTraceHarness(t)
	ctx := chat.WithIdentity(context.Background(), chat.Identity{Session: h.session, SenderID: "@alice:test"})
	tools, ok := h.svc.toolExecutor.PrepareTools(ctx)
	require.True(t, ok)
	found := false
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == "saber_trace" {
			found = true
		}
	}
	require.True(t, found, "有可信身份时必须提供 saber_trace")
	anonymous, _ := h.svc.toolExecutor.PrepareTools(context.Background())
	for _, tool := range anonymous {
		require.NotEqual(t, "saber_trace", tool.Function.Name, "缺少可信身份时不应暴露轨迹导出")
	}
	require.True(t, builtinToolName("saber_trace"), "MCP 同名工具必须被保留名过滤")
}
