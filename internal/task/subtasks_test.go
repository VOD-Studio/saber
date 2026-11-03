package task

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
)

// TestSubagentsPersistLifecycle 验证子代理在真实任务运行中写入记录，事件与任务事件共用一份存储。
func TestSubagentsPersistLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var m *Manager
	var subagentID int64
	manager, err := Open(filepath.Join(dir, "tasks.db"), func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		id, err := m.RecordSubagentStart(ctx, ID(ctx), "审计接口")
		if err != nil {
			return agent.Result{}, err
		}
		subagentID = id
		emit(agent.Event{Kind: agent.SubagentStarted, Subagent: &agent.SubagentInfo{ID: id, Goal: "审计接口"}})
		usage := openai.Usage{PromptTokens: 30, CompletionTokens: 12, TotalTokens: 42}
		if err = m.RecordSubagentFinish(ctx, id, string(agent.Completed), "审计完成", usage); err != nil {
			return agent.Result{}, err
		}
		emit(agent.Event{Kind: agent.SubagentFinished, Subagent: &agent.SubagentInfo{
			ID: id, Goal: "审计接口", Status: agent.Completed, Result: "审计完成", Usage: usage,
		}})
		return agent.Result{Status: agent.Completed, Content: "任务完成"}, nil
	}, nil)
	require.NoError(t, err)
	m = manager
	defer closeManager(t, m)

	source, err := m.Submit(ctx, message("sub-agent", "alice"), dir, request("执行"))
	require.NoError(t, err)
	waitTask(t, m, source, func(task Task) bool { return task.Status == "completed" })

	records, err := m.Subagents(ctx, source.ID)
	require.NoError(t, err)
	require.Len(t, records, 1)
	record := records[0]
	require.Equal(t, subagentID, record.ID)
	require.Equal(t, source.ID, record.ParentTaskID)
	require.Equal(t, "审计接口", record.Goal)
	require.Equal(t, string(agent.Completed), record.Status)
	require.Equal(t, "审计完成", record.Result)
	require.Equal(t, 42, record.Usage.TotalTokens)
	require.False(t, record.FinishedAt.IsZero())

	// 子代理生命周期事件与任务事件共用 task_events，SSE 与 !task logs 可见。
	events, err := m.Events(ctx, source.Message.Session, source.ID)
	require.NoError(t, err)
	joined := strings.Join(events, "\n")
	require.Contains(t, joined, "subagent_started")
	require.Contains(t, joined, "subagent_finished")

	// 父任务取消后，已完成的子代理记录仍然可查。
	cancelled, err := m.RecordSubagentStart(ctx, source.ID, "被取消的子任务")
	require.NoError(t, err)
	require.NoError(t, m.RecordSubagentFinish(ctx, cancelled, string(agent.Cancelled), "", openai.Usage{}))
	all, err := m.Subagents(ctx, source.ID)
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, string(agent.Cancelled), all[1].Status)
	require.True(t, all[1].FinishedAt.After(all[1].CreatedAt) || all[1].FinishedAt.Equal(all[1].CreatedAt))
}

// TestSubagentsLazyTableAndValidation 验证懒建表、参数校验与父任务隔离。
func TestSubagentsLazyTableAndValidation(t *testing.T) {
	ctx := context.Background()
	s, err := openStore(filepath.Join(t.TempDir(), "tasks.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, s.db.Close()) }()
	m := &Manager{store: s}

	_, err = m.RecordSubagentStart(ctx, 0, "缺少父任务")
	require.Error(t, err)
	err = m.RecordSubagentFinish(ctx, 0, string(agent.Completed), "", openai.Usage{})
	require.Error(t, err)
	_, err = m.Subagents(ctx, 0)
	require.Error(t, err)

	first, err := m.RecordSubagentStart(ctx, 5, "第一个")
	require.NoError(t, err)
	second, err := m.RecordSubagentStart(ctx, 5, "第二个")
	require.NoError(t, err)
	other, err := m.RecordSubagentStart(ctx, 6, "别的任务")
	require.NoError(t, err)
	require.NotEqual(t, first, other)

	records, err := m.Subagents(ctx, 5)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, []int64{first, second}, []int64{records[0].ID, records[1].ID})
	require.Equal(t, "第一个", records[0].Goal)

	otherRecords, err := m.Subagents(ctx, 6)
	require.NoError(t, err)
	require.Len(t, otherRecords, 1)
	require.Equal(t, other, otherRecords[0].ID)
	require.Equal(t, SubagentStatusRunning, otherRecords[0].Status)
	require.True(t, otherRecords[0].FinishedAt.IsZero())
}