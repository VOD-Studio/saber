package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"fmt"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/db"
)

// batchIdentity 返回测试用可信身份。
func batchIdentity(room, sender string) chat.Identity {
	return chat.Identity{Session: chat.Session{Platform: "matrix", Account: "bot", Conversation: room}, SenderID: sender}
}

// batchEntries 生成 N 条最小条目，最后一条按 needSilent 标记。
func batchEntries(n int, silentIndex int) []BatchEntry {
	entries := make([]BatchEntry, 0, n)
	for i := range n {
		entries = append(entries, BatchEntry{
			Text:    "批条目 " + string(rune('a'+i)),
			Silent:  i == silentIndex,
			Request: agent.Request{Model: "test", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "批条目"}}},
		})
	}
	return entries
}

// openBatchManager 装配一个立即完成的任务管理器。
func openBatchManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := Open(filepath.Join(dir, "tasks.db"), func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{Status: agent.Completed}, nil
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	return m, dir
}

// countRows 统计满足条件的行数，用于验证事务回滚。
func countRows(t *testing.T, m *Manager, query string, args ...any) int {
	t.Helper()
	var count int
	require.NoError(t, m.store.db.QueryRow(query, args...).Scan(&count))
	return count
}

func TestManager_SubmitBatchPersistsRootTasksInOneTransaction(t *testing.T) {
	ctx := context.Background()
	m, dir := openBatchManager(t)
	identity := batchIdentity("room", "alice")
	batch, ids, err := m.SubmitBatch(ctx, identity, dir, "夜间巡检", batchEntries(3, 1))
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`^b_[0-9a-f]{16}$`), batch.ID)
	require.Equal(t, 3, batch.Total)
	require.Len(t, ids, 3)
	require.Equal(t, 1, countRows(t, m, `SELECT COUNT(*) FROM task_batches WHERE batch_id=?`, batch.ID))
	for index, id := range ids {
		stored, err := m.Get(ctx, identity.Session, id)
		require.NoError(t, err)
		require.Equal(t, batch.ID, stored.BatchID)
		require.Equal(t, index, stored.BatchIndex)
		require.Equal(t, index == 1, stored.Silent, "silent 只作用于被标记的条目")
		// 条目 event 由程序生成且批次内唯一，不复用真实消息 ID。
		require.Equal(t, fmt.Sprintf("batch:%s:%d", batch.ID, index), stored.EventKey)
		require.Empty(t, stored.Message.ReplyTo, "批次条目不自动继承父消息")
	}
	// 批次条目是根任务：没有任何 task_links 行。
	require.Equal(t, 0, countRows(t, m, `SELECT COUNT(*) FROM task_links`))
	require.Eventually(t, func() bool {
		_, tasks, err := m.BatchStatus(ctx, identity, batch.ID)
		return err == nil && len(tasks) == 3 && countTerminal(tasks)
	}, 10*time.Second, 10*time.Millisecond)
	// 提交事件落在首条任务上，可从第一条任务回读整批起点。
	records, err := m.Events(ctx, identity.Session, ids[0])
	require.NoError(t, err)
	require.Equal(t, agent.BatchSubmitted, firstKind(t, records), "首条任务的第一个事件必须是批次提交")
}

func TestManager_SubmitBatchRollsBackOnInvalidItem(t *testing.T) {
	ctx := context.Background()
	m, dir := openBatchManager(t)
	entries := batchEntries(3, -1)
	entries[1].Text = ""
	_, _, err := m.SubmitBatch(ctx, batchIdentity("room", "alice"), dir, "", entries)
	require.ErrorContains(t, err, "batch item 1")
	require.Equal(t, 0, countRows(t, m, `SELECT COUNT(*) FROM task_batches`))
	require.Equal(t, 0, countRows(t, m, `SELECT COUNT(*) FROM tasks`))
}

func TestManager_BatchStatusRestrictsAccess(t *testing.T) {
	ctx := context.Background()
	m, dir := openBatchManager(t)
	owner := batchIdentity("room", "alice")
	batch, ids, err := m.SubmitBatch(ctx, owner, dir, "只读验收", batchEntries(2, -1))
	require.NoError(t, err)
	status, tasks, err := m.BatchStatus(ctx, owner, batch.ID)
	require.NoError(t, err)
	require.Equal(t, "只读验收", status.Label)
	require.Equal(t, "alice", status.Creator)
	require.Len(t, tasks, 2)
	require.Equal(t, ids, []int64{tasks[0].TaskID, tasks[1].TaskID}, "条目按 batch_index 返回")

	// 同群其他成员：能猜编号也不能读。
	_, _, err = m.BatchStatus(ctx, batchIdentity("room", "bob"), batch.ID)
	require.ErrorContains(t, err, "只有批次创建者")
	// 其他群：按 platform+account+room 过滤，直接当作不存在。
	_, _, err = m.BatchStatus(ctx, batchIdentity("other-room", "alice"), batch.ID)
	require.ErrorContains(t, err, "not found")
	_, _, err = m.BatchStatus(ctx, owner, "b_0000000000000000")
	require.ErrorContains(t, err, "not found")
}

func TestManager_CancelBatchSkipsTerminalTasks(t *testing.T) {
	ctx := context.Background()
	m, dir := openBatchManager(t)
	identity := batchIdentity("room", "alice")
	batch, ids, err := m.SubmitBatch(ctx, identity, dir, "", batchEntries(2, -1))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, tasks, err := m.BatchStatus(ctx, identity, batch.ID)
		return err == nil && countTerminal(tasks)
	}, 10*time.Second, 10*time.Millisecond)
	// 已完成的任务不能取消，但也不算失败。
	_, cancelled, skipped, err := m.CancelBatch(ctx, identity, batch.ID)
	require.NoError(t, err)
	require.Empty(t, cancelled)
	require.ElementsMatch(t, ids, skipped)
	// 他人不能取消别人的批次。
	_, _, _, err = m.CancelBatch(ctx, batchIdentity("room", "bob"), batch.ID)
	require.ErrorContains(t, err, "只有批次创建者")
}

func TestManager_CancelBatchCancelsQueuedAndRunning(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	release := make(chan struct{})
	defer close(release)
	m, err := Open(filepath.Join(dir, "tasks.db"), func(ctx context.Context, _ agent.Request, _ func(agent.Event)) (agent.Result, error) {
		select {
		case <-ctx.Done():
			return agent.Result{Status: agent.Cancelled}, ctx.Err()
		case <-release:
			return agent.Result{Status: agent.Completed}, nil
		}
	}, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, m.Close()) }()
	identity := batchIdentity("room", "alice")
	batch, ids, err := m.SubmitBatch(ctx, identity, dir, "", batchEntries(2, -1))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var running int
		if err := m.store.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE batch_id=? AND status='running'`, batch.ID).Scan(&running); err != nil {
			return false
		}
		return running == 1
	}, 10*time.Second, 10*time.Millisecond)
	_, cancelled, skipped, err := m.CancelBatch(ctx, identity, batch.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, ids, cancelled)
	require.Empty(t, skipped)
	require.Eventually(t, func() bool {
		_, tasks, err := m.BatchStatus(ctx, identity, batch.ID)
		return err == nil && countTerminal(tasks)
	}, 10*time.Second, 10*time.Millisecond)
	_, tasks, err := m.BatchStatus(ctx, identity, batch.ID)
	require.NoError(t, err)
	for _, item := range tasks {
		require.Equal(t, "cancelled", item.Status)
	}
}

func TestManager_BatchFinishedEventIsWrittenOnce(t *testing.T) {
	ctx := context.Background()
	m, dir := openBatchManager(t)
	identity := batchIdentity("room", "alice")
	batch, _, err := m.SubmitBatch(ctx, identity, dir, "", batchEntries(3, 0))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, tasks, err := m.BatchStatus(ctx, identity, batch.ID)
		return err == nil && countTerminal(tasks)
	}, 10*time.Second, 10*time.Millisecond)
	rows, err := m.store.db.Query(`SELECT record FROM task_events e JOIN tasks t ON t.id=e.task_id WHERE t.batch_id=?`, batch.ID)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	finished := 0
	var info agent.BatchInfo
	for rows.Next() {
		var record string
		require.NoError(t, rows.Scan(&record))
		var event agent.Event
		require.NoError(t, json.Unmarshal([]byte(record), &event))
		if event.Kind == agent.BatchFinished {
			finished++
			require.NotNil(t, event.Batch)
			info = *event.Batch
		}
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 1, finished, "批次结束只写一条事件")
	require.Equal(t, batch.ID, info.ID)
	require.Equal(t, 3, info.Total)
	require.Equal(t, 3, info.Counts["completed"])
}

func TestBatch_LegacyDatabaseMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.db")
	raw, err := sql.Open(db.DriverName, path)
	require.NoError(t, err)
	_, err = raw.Exec(legacyTaskSchema)
	require.NoError(t, err)
	msg := message("legacy-batch", "alice")
	msgData, err := json.Marshal(msg)
	require.NoError(t, err)
	reqData, err := json.Marshal(request("legacy"))
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO tasks(platform,account,room,event,sender,work_dir,message,request,status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		msg.Session.Platform, msg.Session.Account, msg.Session.Conversation, msg.ID, msg.SenderID, dir, msgData, reqData, "completed", time.Now().UnixMilli())
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	s, err := openStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.db.Close()) }()
	for _, column := range []string{"batch_id", "batch_index"} {
		var exists int
		require.NoError(t, s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('tasks') WHERE name=?)`, column).Scan(&exists))
		require.Equal(t, 1, exists, "tasks.%s 未补齐", column)
	}
	var indexed int
	require.NoError(t, s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='index' AND name='tasks_batch')`).Scan(&indexed))
	require.Equal(t, 1, indexed)
	legacy, err := scanTask(s.db.QueryRow(`SELECT ` + taskColumns + ` FROM tasks WHERE id=1`))
	require.NoError(t, err)
	require.Equal(t, "", legacy.BatchID)
	require.Equal(t, -1, legacy.BatchIndex)
	// 迁移后的库可以继续接受批次。
	m := &Manager{store: s, ctx: context.Background()}
	batch, ids, err := m.SubmitBatch(context.Background(), batchIdentity("room", "alice"), dir, "", batchEntries(1, -1))
	require.NoError(t, err)
	require.NotEmpty(t, batch.ID)
	require.Len(t, ids, 1)
}

// firstKind 返回首条事件的 kind，用于确认事件顺序。
func firstKind(t *testing.T, records []string) agent.EventKind {
	t.Helper()
	require.NotEmpty(t, records)
	var event agent.Event
	require.NoError(t, json.Unmarshal([]byte(records[0]), &event))
	return event.Kind
}

// countTerminal 判断批次内是否已没有排队或运行中的任务。
func countTerminal(tasks []BatchTask) bool {
	if len(tasks) == 0 {
		return false
	}
	for _, item := range tasks {
		if item.Status == "queued" || item.Status == "running" {
			return false
		}
	}
	return true
}
