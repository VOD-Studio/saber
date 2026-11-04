package ai

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/task"
)

// batchHarness 装配真实任务管理器，批次工具走与生产相同的调用路径。
type batchHarness struct {
	svc     *Service
	mgr     *task.Manager
	session chat.Session
}

func newBatchHarness(t *testing.T, run func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error)) *batchHarness {
	t.Helper()
	svc, err := NewService(createTestMultiProviderAIConfig())
	require.NoError(t, err)
	require.NoError(t, svc.ConfigureExecution(config.ExecutionConfig{LogDir: t.TempDir()}, nil, nil))
	if run == nil {
		run = func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
			return agent.Result{Status: agent.Completed}, nil
		}
	}
	mgr, err := task.Open(t.TempDir()+"/tasks.db", run, nil)
	require.NoError(t, err)
	svc.tasks, svc.taskDir = mgr, t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, mgr.Close())
		svc.Stop()
	})
	return &batchHarness{svc: svc, mgr: mgr, session: chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test"}}
}

func (h *batchHarness) identity(sender string) context.Context {
	return chat.WithIdentity(context.Background(), chat.Identity{Session: h.session, SenderID: sender})
}

// batchArgs 生成带 N 条文本的提交参数。
func batchArgs(texts ...string) map[string]any {
	items := make([]any, 0, len(texts))
	for _, text := range texts {
		items = append(items, map[string]any{"text": text})
	}
	return map[string]any{"action": "submit", "items": items}
}

func TestBatchTool_SubmitThenStatus(t *testing.T) {
	h := newBatchHarness(t, nil)
	ctx := h.identity("@alice:test")
	value, err := h.svc.toolExecutor.ExecuteToolCall(ctx, "saber_batch", map[string]any{
		"action": "submit",
		"label":  "夜间巡检",
		"items": []any{
			map[string]any{"text": "检查磁盘"},
			map[string]any{"text": "检查内存", "silent": true},
			map[string]any{"text": "检查证书"},
		},
	})
	require.NoError(t, err)
	submitted, ok := value.(batchSubmitResult)
	require.True(t, ok, "提交必须返回 batch_id 与任务编号")
	require.NotEmpty(t, submitted.BatchID)
	require.Len(t, submitted.TaskIDs, 3)
	// silent 只影响投递，任务本身照常落库并执行。
	silent, err := h.mgr.Get(ctx, h.session, submitted.TaskIDs[1])
	require.NoError(t, err)
	require.True(t, silent.Silent)
	require.Equal(t, submitted.BatchID, silent.BatchID)
	require.Equal(t, 1, silent.BatchIndex)

	var status batchStatusResult
	require.Eventually(t, func() bool {
		value, err := h.svc.toolExecutor.ExecuteToolCall(ctx, "saber_batch", map[string]any{"action": "status", "batch_id": submitted.BatchID})
		if err != nil {
			return false
		}
		status = value.(batchStatusResult)
		return status.Counts["completed"] == 3
	}, 10*time.Second, 10*time.Millisecond)
	require.Equal(t, 3, status.Total)
	require.Len(t, status.Items, 3)
	for _, key := range []string{"queued", "running", "completed", "failed", "cancelled", "interrupted"} {
		require.Contains(t, status.Counts, key, "counts 必须包含全部状态")
	}
	for index, item := range status.Items {
		require.Equal(t, submitted.TaskIDs[index], item.TaskID)
		_, err := time.Parse(time.RFC3339Nano, item.CreatedAt)
		require.NoError(t, err, "created_at 必须是 RFC3339 时间")
	}
}

func TestBatchTool_ValidationAndAccess(t *testing.T) {
	h := newBatchHarness(t, nil)
	ctx := h.identity("@alice:test")
	tooMany := make([]string, 21)
	for i := range tooMany {
		tooMany[i] = "任务"
	}
	cases := []struct {
		name string
		ctx  context.Context
		args map[string]any
		want string
	}{
		{"no identity", context.Background(), batchArgs("a"), "可信身份"},
		{"no items", ctx, map[string]any{"action": "submit"}, "must contain 1..20"},
		{"too many", ctx, batchArgs(tooMany...), "at most 20 items"},
		{"empty text", ctx, batchArgs(""), "text is required"},
		{"blank text", ctx, batchArgs("   "), "text is required"},
		{"too long", ctx, batchArgs(strings.Repeat("长", 8001)), "exceeds 8000 characters"},
		{"bad label", ctx, map[string]any{"action": "submit", "label": strings.Repeat("标", 257), "items": []any{map[string]any{"text": "a"}}}, "label exceeds"},
		{"bad batch id", ctx, map[string]any{"action": "status", "batch_id": "../etc"}, "b_<16 hex"},
		{"unknown batch", ctx, map[string]any{"action": "status", "batch_id": "b_0000000000000000"}, "not found"},
		{"unknown action", ctx, map[string]any{"action": "merge"}, "invalid batch action"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.svc.toolExecutor.ExecuteToolCall(tc.ctx, "saber_batch", tc.args)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
	disabled := &Service{}
	_, err := disabled.executeBatchTool(context.Background(), batchArgs("a"))
	require.ErrorContains(t, err, "任务服务未启用")
}

func TestBatchTool_RejectsOtherSender(t *testing.T) {
	h := newBatchHarness(t, nil)
	submitted, err := h.svc.toolExecutor.ExecuteToolCall(h.identity("@alice:test"), "saber_batch", batchArgs("只读", "验收"))
	require.NoError(t, err)
	batchID := submitted.(batchSubmitResult).BatchID
	for _, action := range []string{"status", "cancel"} {
		_, err := h.svc.toolExecutor.ExecuteToolCall(h.identity("@bob:test"), "saber_batch", map[string]any{"action": action, "batch_id": batchID})
		require.ErrorContains(t, err, "只有批次创建者", "%s 必须校验批次归属", action)
	}
}

func TestBatchTool_CancelSkipsFinishedTasks(t *testing.T) {
	h := newBatchHarness(t, nil)
	ctx := h.identity("@alice:test")
	submitted, err := h.svc.toolExecutor.ExecuteToolCall(ctx, "saber_batch", batchArgs("一", "二"))
	require.NoError(t, err)
	result := submitted.(batchSubmitResult)
	require.Eventually(t, func() bool {
		value, err := h.svc.toolExecutor.ExecuteToolCall(ctx, "saber_batch", map[string]any{"action": "status", "batch_id": result.BatchID})
		return err == nil && value.(batchStatusResult).Counts["completed"] == 2
	}, 10*time.Second, 10*time.Millisecond)
	value, err := h.svc.toolExecutor.ExecuteToolCall(ctx, "saber_batch", map[string]any{"action": "cancel", "batch_id": result.BatchID})
	require.NoError(t, err)
	cancelled := value.(batchCancelResult)
	require.Empty(t, cancelled.Cancelled)
	require.ElementsMatch(t, result.TaskIDs, cancelled.Skipped, "已终态任务进 skipped 而不是报错")
}

func TestBatchTool_RegisteredAndReserved(t *testing.T) {
	h := newBatchHarness(t, nil)
	tools, ok := h.svc.toolExecutor.PrepareTools(h.identity("@alice:test"))
	require.True(t, ok)
	found := false
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == "saber_batch" {
			found = true
		}
	}
	require.True(t, found, "有可信身份时必须提供 saber_batch")
	anonymous, _ := h.svc.toolExecutor.PrepareTools(context.Background())
	for _, tool := range anonymous {
		require.NotEqual(t, "saber_batch", tool.Function.Name, "缺少可信身份时不应暴露批次工具")
	}
	require.True(t, builtinToolName("saber_batch"), "MCP 同名工具必须被保留名过滤")
	require.True(t, chatToolNames["saber_batch"], "批次属于聊天工具，纯聊天计划保留可用")
}
