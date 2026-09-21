package task

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

func TestManager_ExecutionWithoutDeliveryAndReplay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.db")
	var runs atomic.Int32
	runner := func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		runs.Add(1)
		return agent.Result{Status: agent.Completed, Content: "完成"}, nil
	}
	m, err := Open(path, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		emit(agent.Event{Kind: agent.TextDelta, Text: "你好"})
		emit(agent.Event{Kind: agent.ToolStarted})
		emit(agent.Event{Kind: agent.TextDelta, Text: "完成"})
		return runner(ctx, req, emit)
	}, nil)
	require.NoError(t, err)
	msg := message("tui-message", "local")
	msg.Session.Platform = "terminal"
	first, err := m.Submit(ctx, msg, dir, request("hello"))
	require.NoError(t, err)
	completed := waitTask(t, m, first, func(t Task) bool { return t.Status == "completed" })
	require.Equal(t, "pending", completed.Delivery)
	events, err := m.ReadEvents(ctx, msg.Session, first.ID, 0)
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Equal(t, "你好", events[0].Text)
	tail, err := m.ReadEvents(ctx, msg.Session, first.ID, events[0].ID)
	require.NoError(t, err)
	require.Equal(t, events[1:], tail)
	other := msg.Session
	other.Conversation = "other"
	_, err = m.ReadEvents(ctx, other, first.ID, 0)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, m.Close())
	m, err = Open(path, runner, nil)
	require.NoError(t, err)
	defer closeManager(t, m)
	replay, err := m.ReadEvents(ctx, msg.Session, first.ID, 0)
	require.NoError(t, err)
	require.Equal(t, events, replay)
	var sends atomic.Int32
	require.NoError(t, m.RegisterDelivery("terminal", func(context.Context, Task) (string, error) { sends.Add(1); return "reply", nil }))
	require.Error(t, m.RegisterDelivery("terminal", func(context.Context, Task) (string, error) { return "", nil }))
	waitTask(t, m, first, func(t Task) bool { return t.Delivery == "sent" })
	require.EqualValues(t, 1, runs.Load())
	require.EqualValues(t, 1, sends.Load())
}

func TestManager_UnsubscribedResultsDoNotStarveDelivery(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m, err := Open(filepath.Join(dir, "tasks.db"), func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{Status: agent.Completed}, nil
	}, nil)
	require.NoError(t, err)
	defer closeManager(t, m)
	for i := range 22 {
		msg := message(string(rune('a'+i)), "local")
		msg.Session.Platform = "terminal"
		item, err := m.Submit(ctx, msg, dir, request("input"))
		require.NoError(t, err)
		waitTask(t, m, item, func(t Task) bool { return t.Status == "completed" })
	}
	require.NoError(t, m.RegisterDelivery("matrix", func(context.Context, Task) (string, error) { return "matrix-reply", nil }))
	item, err := m.Submit(ctx, message("matrix", "user"), dir, request("input"))
	require.NoError(t, err)
	waitTask(t, m, item, func(t Task) bool { return t.DeliveryID == "matrix-reply" })
}

func TestManager_ChatTurnDedupBusyAndCancel(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	started := make(chan struct{})
	m, err := Open(filepath.Join(dir, "tasks.db"), func(ctx context.Context, _ agent.Request, _ func(agent.Event)) (agent.Result, error) {
		close(started)
		<-ctx.Done()
		return agent.Result{Status: agent.Cancelled}, ctx.Err()
	}, nil)
	require.NoError(t, err)
	defer closeManager(t, m)
	msg := message("first", "local")
	item, err := m.SubmitTurn(ctx, msg, dir, request("one"))
	require.NoError(t, err)
	<-started
	duplicate, err := m.SubmitTurn(ctx, msg, dir, request("one"))
	require.NoError(t, err)
	require.Equal(t, item.ID, duplicate.ID)
	msg.ID = "second"
	_, err = m.SubmitTurn(ctx, msg, dir, request("two"))
	require.ErrorIs(t, err, ErrBusy)
	_, err = m.Cancel(ctx, chat.Identity{Session: msg.Session, SenderID: msg.SenderID}, item.ID)
	require.NoError(t, err)
	waitTask(t, m, item, func(t Task) bool { return t.Status == "cancelled" })
	sessions, err := m.Conversations(ctx, msg.Session.Platform, msg.Session.Account)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	history, err := m.History(ctx, msg.Session)
	require.NoError(t, err)
	require.Len(t, history, 1)
}
