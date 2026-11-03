package task

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
)

func TestManager_PersistsStateCheckpointAndContinuation(t *testing.T) {
	ctx := context.Background()
	path, dir := filepath.Join(t.TempDir(), "tasks.db"), t.TempDir()
	var captured agent.Request
	final := agent.TaskState{
		Plan:       []agent.PlanStep{{Text: "修复 CLI", Status: "done"}},
		Findings:   []string{"定位到参数校验"},
		Artifacts:  []string{"internal/cli/flags.go"},
		OpenIssues: []string{},
	}
	m, err := Open(path, func(_ context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		captured = req
		state := agent.CloneTaskState(req.State)
		state.Plan = final.Plan
		state.Findings = final.Findings
		state.Artifacts = final.Artifacts
		state.Revision = 3
		emit(agent.Event{Kind: agent.TaskStateUpdated, State: agent.CloneTaskState(state)})
		checkpoint := &agent.Checkpoint{Request: agent.Request{
			Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "压缩后的历史"}},
			State:    agent.CloneTaskState(state),
		}, ThroughRound: 0}
		emit(agent.Event{Kind: agent.ContextCompacted, Checkpoint: checkpoint, State: agent.CloneTaskState(state)})
		return agent.Result{Status: agent.Completed, Content: "完成", State: state, Checkpoint: checkpoint}, nil
	}, nil, Options{Context: agent.ContextPolicy{Enabled: true, MaxMessages: 50, MaxInputTokens: 32768}})
	require.NoError(t, err)
	defer closeManager(t, m)
	rootMessage := message("state-root", "alice")
	rootMessage.Text = "只修改 internal/cli"
	first, err := m.Submit(ctx, rootMessage, dir, request("只修改 internal/cli"))
	require.NoError(t, err)
	first = waitTask(t, m, first, func(t Task) bool { return t.Status == "completed" })
	require.NotNil(t, captured.State, "持久化任务必须初始化程序状态")
	require.Equal(t, "只修改 internal/cli", captured.State.Instructions[0].Text)

	state, err := m.RuntimeState(ctx, first.Message.Session, first.ID)
	require.NoError(t, err)
	require.Equal(t, int64(3), state.Revision)
	require.Equal(t, "修复 CLI", state.Plan[0].Text)
	require.Equal(t, "只修改 internal/cli", state.Instructions[0].Text)

	storedState, checkpoint, err := m.store.runtimeState(ctx, first.ID)
	require.NoError(t, err)
	require.NotNil(t, storedState)
	require.NotNil(t, checkpoint)
	require.Equal(t, "压缩后的历史", checkpoint.Request.Messages[0].Content)

	// 续接从检查点恢复压缩请求，而不是重放被覆盖的旧轨迹。
	follow := message("state-follow", "alice")
	follow.ReplyTo = first.Message.ID
	child, err := m.Continue(ctx, follow, dir, request("继续修复"))
	require.NoError(t, err)
	req, err := m.continuationRequest(ctx, child)
	require.NoError(t, err)
	require.Equal(t, "压缩后的历史", req.Messages[0].Content)
	require.Equal(t, "继续修复", req.Messages[len(req.Messages)-1].Content)
	require.NotNil(t, req.State)
	require.Equal(t, "修复 CLI", req.State.Plan[0].Text)
	require.NotNil(t, req.State.Instructions)
}

func TestManager_RuntimeStateFallbackForLegacyTask(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m, err := Open(filepath.Join(t.TempDir(), "tasks.db"), func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Result, error) {
		return agent.Result{Status: agent.Completed, Content: "old"}, nil
	}, nil, Options{Context: agent.ContextPolicy{Enabled: true, MaxMessages: 50, MaxInputTokens: 32768}})
	require.NoError(t, err)
	defer closeManager(t, m)
	legacyMessage := message("legacy", "alice")
	legacyMessage.Text = "旧任务指令"
	legacy, err := m.Submit(ctx, legacyMessage, dir, request("旧任务指令"))
	require.NoError(t, err)
	legacy = waitTask(t, m, legacy, func(t Task) bool { return t.Status == "completed" })
	require.NoError(t, legacy.Message.Validate())
	// 模拟旧版本：终态结果与运行时状态表都没有状态。
	_, err = m.store.db.Exec(`DELETE FROM task_runtime_state WHERE task_id=?`, legacy.ID)
	require.NoError(t, err)
	_, err = m.store.db.Exec(`UPDATE tasks SET result=? WHERE id=?`, []byte(`{"Status":"completed","Content":"old"}`), legacy.ID)
	require.NoError(t, err)
	state, err := m.RuntimeState(ctx, legacy.Message.Session, legacy.ID)
	require.NoError(t, err)
	require.Equal(t, "旧任务指令", state.Instructions[0].Text)
	require.Zero(t, state.Revision)
}
