package task

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// steeringHarness 用可控闸门让任务停在运行中，便于验证转向的排队与生效时机。
type steeringHarness struct {
	mu       sync.Mutex
	gates    map[int64]chan struct{}
	consumed map[int64][]string
	started  chan int64
}

func newSteeringHarness() *steeringHarness {
	return &steeringHarness{gates: map[int64]chan struct{}{}, consumed: map[int64][]string{}, started: make(chan int64, 16)}
}

// gate 返回指定任务的放行通道；同一任务只创建一个。
func (h *steeringHarness) gate(id int64) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	gate := h.gates[id]
	if gate == nil {
		gate = make(chan struct{})
		h.gates[id] = gate
	}
	return gate
}

func (h *steeringHarness) record(id int64, texts []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.consumed[id] = texts
}

func (h *steeringHarness) texts(id int64) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.consumed[id]...)
}

// runner 返回测试用 Runner：登记开始、等待闸门、消费补充要求并发出 Steered 事件。
func (h *steeringHarness) runner(m **Manager, ready <-chan struct{}) RunFunc {
	return func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		<-ready
		id := ID(ctx)
		h.started <- id
		select {
		case <-h.gate(id):
		case <-ctx.Done():
			return agent.Result{}, ctx.Err()
		}
		texts, err := (*m).ConsumeSteering(ctx, id, MaxPendingSteering)
		if err != nil {
			return agent.Result{}, err
		}
		for _, text := range texts {
			emit(agent.Event{Kind: agent.Steered, Round: 1, Text: text})
		}
		h.record(id, texts)
		return agent.Result{Status: agent.Completed, Content: "done"}, nil
	}
}

func steeringMessage(event, sender, conversation string) chat.Message {
	return chat.Message{ID: event, SenderID: sender, Text: sender + " input", Session: chat.Session{Platform: "matrix", Account: "bot", Conversation: conversation, Thread: "topic"}}
}

func TestManager_RedirectRunningPermissionsAndLimits(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	h := newSteeringHarness()
	ready := make(chan struct{})
	var m *Manager
	manager, err := Open(filepath.Join(dir, "tasks.db"), h.runner(&m, ready), nil)
	require.NoError(t, err)
	m = manager
	close(ready)
	defer closeManager(t, m)

	owner := chat.Identity{Session: steeringMessage("$a", "alice", "room").Session, SenderID: "alice"}
	stranger := owner
	stranger.SenderID = "mallory"
	source, err := m.Submit(ctx, steeringMessage("$a", "alice", "room"), dir, request("aim"))
	require.NoError(t, err)
	require.Equal(t, source.ID, <-h.started)
	require.Equal(t, "running", waitTask(t, m, source, func(Task) bool { return true }).Status)

	// 发起人可以排队补充要求，任务不取消也不重开。
	got, err := m.Redirect(ctx, owner, source.ID, "  优先检查配置  ")
	require.NoError(t, err)
	require.Equal(t, source.ID, got.ID)
	require.Equal(t, "running", got.Status)
	pending, err := m.PendingSteering(ctx, owner.Session, source.ID)
	require.NoError(t, err)
	require.Equal(t, 1, pending)

	// 权限与 Cancel 相同：陌生人被拒绝；空文本与超长文本在写库前拒绝。
	_, err = m.Redirect(ctx, stranger, source.ID, "越权")
	require.ErrorContains(t, err, "只有发起人或本群任务管理员")
	_, err = m.Redirect(ctx, owner, source.ID, "   ")
	require.Error(t, err)
	_, err = m.Redirect(ctx, owner, source.ID, strings.Repeat("中", MaxSteeringTextBytes))
	require.ErrorContains(t, err, "补充要求超过")
	// 其他会话看不到该任务。
	other := owner
	other.Session.Conversation = "other-room"
	_, err = m.Redirect(ctx, other, source.ID, "别处")
	require.ErrorIs(t, err, sql.ErrNoRows)

	// 运行中到达下一轮前生效：放行后消费一次，待生效归零。
	close(h.gate(source.ID))
	finished := waitTask(t, m, source, func(t Task) bool { return t.Status == "completed" })
	require.Equal(t, finished.ID, source.ID)
	require.Equal(t, []string{"优先检查配置"}, h.texts(source.ID))
	pending, err = m.PendingSteering(ctx, owner.Session, source.ID)
	require.NoError(t, err)
	require.Zero(t, pending)

	// 终态任务明确拒绝，不回退为取消或重开。
	_, err = m.Redirect(ctx, owner, source.ID, "再来一条")
	require.ErrorIs(t, err, ErrTaskFinished)
	require.ErrorContains(t, err, "任务已结束，请直接回复结果续接或新建任务")

	// Steered 事件已落盘，SSE 读取可见轮次与原文。
	records, err := m.ReadEvents(ctx, owner.Session, source.ID, 0)
	require.NoError(t, err)
	var steered []Record
	for _, record := range records {
		if record.Kind == agent.Steered {
			steered = append(steered, record)
		}
	}
	require.Len(t, steered, 1)
	require.Equal(t, 1, steered[0].Round)
	require.Equal(t, "优先检查配置", steered[0].Text)
}

func TestManager_RedirectQueuedLimitAndStart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	h := newSteeringHarness()
	ready := make(chan struct{})
	var m *Manager
	manager, err := Open(filepath.Join(dir, "tasks.db"), h.runner(&m, ready), nil)
	require.NoError(t, err)
	m = manager
	close(ready)
	defer closeManager(t, m)

	owner := chat.Identity{Session: steeringMessage("$a", "alice", "room").Session, SenderID: "alice"}
	first, err := m.Submit(ctx, steeringMessage("$first", "alice", "room"), dir, request("first"))
	require.NoError(t, err)
	require.Equal(t, first.ID, <-h.started)
	// 同目录的第二个任务保持排队。
	second, err := m.Submit(ctx, steeringMessage("$second", "alice", "room"), dir, request("second"))
	require.NoError(t, err)
	require.Equal(t, "queued", second.Status)

	// 排队中最多 8 条待投递。
	for i := range MaxPendingSteering {
		_, err = m.Redirect(ctx, owner, second.ID, "补充 "+string(rune('A'+i)))
		require.NoError(t, err)
	}
	_, err = m.Redirect(ctx, owner, second.ID, "第九条")
	require.ErrorContains(t, err, "待生效补充要求已达")
	pending, err := m.PendingSteering(ctx, owner.Session, second.ID)
	require.NoError(t, err)
	require.Equal(t, MaxPendingSteering, pending)

	// 前序任务结束后，排队任务开始时一次性消费全部补充要求。
	close(h.gate(first.ID))
	require.Equal(t, second.ID, <-h.started)
	close(h.gate(second.ID))
	waitTask(t, m, second, func(t Task) bool { return t.Status == "completed" })
	require.Len(t, h.texts(second.ID), MaxPendingSteering)
	require.Equal(t, second.ID, waitTask(t, m, second, func(t Task) bool { return t.Status == "completed" }).ID)
	pending, err = m.PendingSteering(ctx, owner.Session, second.ID)
	require.NoError(t, err)
	require.Zero(t, pending)
}

// TestManager_RedirectSurvivesRestart 验证未消费的补充要求留在 SQLite，重启后的下一次运行生效。
func TestManager_RedirectSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.db")
	h := newSteeringHarness()
	ready := make(chan struct{})
	var first *Manager
	manager, err := Open(path, h.runner(&first, ready), nil)
	require.NoError(t, err)
	first = manager
	close(ready)
	owner := chat.Identity{Session: steeringMessage("$a", "alice", "room").Session, SenderID: "alice"}
	running, err := first.Submit(ctx, steeringMessage("$first", "alice", "room"), dir, request("first"))
	require.NoError(t, err)
	require.Equal(t, running.ID, <-h.started)
	queued, err := first.Submit(ctx, steeringMessage("$second", "alice", "room"), dir, request("second"))
	require.NoError(t, err)
	_, err = first.Redirect(ctx, owner, queued.ID, "重启后仍要生效")
	require.NoError(t, err)
	require.NoError(t, first.Close())

	// 重启后排队任务继续执行，运行开始时消费到重启前写入的要求。
	h2 := newSteeringHarness()
	ready2 := make(chan struct{})
	var second *Manager
	manager2, err := Open(path, h2.runner(&second, ready2), nil)
	require.NoError(t, err)
	second = manager2
	close(ready2)
	defer closeManager(t, second)
	require.Equal(t, queued.ID, <-h2.started)
	close(h2.gate(queued.ID))
	done := waitTask(t, second, queued, func(t Task) bool { return t.Status == "completed" })
	require.Equal(t, queued.ID, done.ID)
	require.Equal(t, []string{"重启后仍要生效"}, h2.texts(queued.ID))
	pending, err := second.PendingSteering(ctx, owner.Session, queued.ID)
	require.NoError(t, err)
	require.Zero(t, pending)
}

// TestManager_ConsumeSteeringIsIdempotent 验证同一事务读标记后不会重复返回。
func TestManager_ConsumeSteeringIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	h := newSteeringHarness()
	ready := make(chan struct{})
	var m *Manager
	manager, err := Open(filepath.Join(dir, "tasks.db"), h.runner(&m, ready), nil)
	require.NoError(t, err)
	m = manager
	close(ready)
	defer closeManager(t, m)
	owner := chat.Identity{Session: steeringMessage("$a", "alice", "room").Session, SenderID: "alice"}
	source, err := m.Submit(ctx, steeringMessage("$a", "alice", "room"), dir, request("aim"))
	require.NoError(t, err)
	require.Equal(t, source.ID, <-h.started)
	_, err = m.Redirect(ctx, owner, source.ID, "只投递一次")
	require.NoError(t, err)
	texts, err := m.ConsumeSteering(ctx, source.ID, MaxPendingSteering)
	require.NoError(t, err)
	require.Equal(t, []string{"只投递一次"}, texts)
	again, err := m.ConsumeSteering(ctx, source.ID, MaxPendingSteering)
	require.NoError(t, err)
	require.Empty(t, again)
	// 没有持久任务时消费是空操作。
	texts, err = m.ConsumeSteering(ctx, 0, MaxPendingSteering)
	require.NoError(t, err)
	require.Empty(t, texts)
	close(h.gate(source.ID))
	waitTask(t, m, source, func(t Task) bool { return t.Status == "completed" })
}

// TestManager_RedirectPendingKeptWhenRunEnds 验证任务在消费前结束，待投递行仍保留并可查询。
func TestManager_RedirectPendingKeptWhenRunEnds(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.db")
	started := make(chan int64, 1)
	gate := make(chan struct{})
	ready := make(chan struct{})
	m, err := Open(path, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		<-ready
		started <- ID(ctx)
		select {
		case <-gate:
		case <-ctx.Done():
			return agent.Result{}, ctx.Err()
		}
		// 故意不消费补充要求：任务在生效前结束。
		return agent.Result{Status: agent.Completed, Content: "done"}, nil
	}, nil)
	require.NoError(t, err)
	close(ready)
	owner := chat.Identity{Session: steeringMessage("$a", "alice", "room").Session, SenderID: "alice"}
	source, err := m.Submit(ctx, steeringMessage("$a", "alice", "room"), dir, request("aim"))
	require.NoError(t, err)
	require.Equal(t, source.ID, <-started)
	_, err = m.Redirect(ctx, owner, source.ID, "结束时才补充")
	require.NoError(t, err)
	close(gate)
	waitTask(t, m, source, func(t Task) bool { return t.Status == "completed" })
	pending, err := m.PendingSteering(ctx, owner.Session, source.ID)
	require.NoError(t, err)
	require.Equal(t, 1, pending)
	require.NoError(t, m.Close())

	// 关闭重开后仍是未生效状态，不会被静默清理或自动重放。
	reopened, err := Open(path, func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{Status: agent.Completed}, nil
	}, nil)
	require.NoError(t, err)
	defer closeManager(t, reopened)
	pending, err = reopened.PendingSteering(ctx, owner.Session, source.ID)
	require.NoError(t, err)
	require.Equal(t, 1, pending)
}

// TestManager_RedirectCancelledTask 验证已取消任务同样拒绝排队。
func TestManager_RedirectCancelledTask(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	h := newSteeringHarness()
	ready := make(chan struct{})
	var m *Manager
	manager, err := Open(filepath.Join(dir, "tasks.db"), h.runner(&m, ready), nil)
	require.NoError(t, err)
	m = manager
	close(ready)
	defer closeManager(t, m)
	owner := chat.Identity{Session: steeringMessage("$a", "alice", "room").Session, SenderID: "alice"}
	running, err := m.Submit(ctx, steeringMessage("$a", "alice", "room"), dir, request("aim"))
	require.NoError(t, err)
	require.Equal(t, running.ID, <-h.started)
	queued, err := m.Submit(ctx, steeringMessage("$b", "alice", "room"), dir, request("queued"))
	require.NoError(t, err)
	cancelled, err := m.Cancel(ctx, owner, queued.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", cancelled.Status)
	_, err = m.Redirect(ctx, owner, cancelled.ID, "结束时补充")
	require.ErrorIs(t, err, ErrTaskFinished)
	close(h.gate(running.ID))
	waitTask(t, m, running, func(t Task) bool { return t.Status == "completed" })
}
