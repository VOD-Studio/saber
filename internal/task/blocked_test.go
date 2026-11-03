package task

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
)

func TestStore_BlockedDirectoriesStayLockedAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.db")
	s, err := openStore(path)
	require.NoError(t, err)
	blocked, open := t.TempDir(), t.TempDir()
	blocked, err = canonicalDir(blocked)
	require.NoError(t, err)
	open, err = canonicalDir(open)
	require.NoError(t, err)
	running, err := s.submit(ctx, message("blocked-running", "alice"), blocked, request("blocked"))
	require.NoError(t, err)
	_, err = s.claim(ctx, nil)
	require.NoError(t, err)
	queued, err := s.submit(ctx, message("blocked-queued", "alice"), blocked, request("also blocked"))
	require.NoError(t, err)
	available, err := s.submit(ctx, message("available", "bob"), open, request("free"))
	require.NoError(t, err)

	require.NoError(t, s.recover(ctx, []string{blocked}))
	stillRunning, err := s.get(ctx, running.Message.Session, running.ID)
	require.NoError(t, err)
	require.Equal(t, "running", stillRunning.Status, "被隔离目录的运行行必须保持锁定")
	next, err := s.claim(ctx, []string{blocked})
	require.NoError(t, err)
	require.Equal(t, available.ID, next.ID)
	_, err = s.claim(ctx, []string{blocked})
	require.Error(t, err, "被隔离目录的排队任务不能被领取")
	queuedTask, err := s.get(ctx, queued.Message.Session, queued.ID)
	require.NoError(t, err)
	require.Equal(t, "queued", queuedTask.Status)
	require.NoError(t, s.db.Close())

	// 重启后同一阻断快照继续生效。
	s2, err := openStore(path)
	require.NoError(t, err)
	defer func() { _ = s2.db.Close() }()
	require.NoError(t, s2.recover(ctx, []string{blocked}))
	stillRunning, err = s2.get(ctx, running.Message.Session, running.ID)
	require.NoError(t, err)
	require.Equal(t, "running", stillRunning.Status)
}

func TestManager_FinalizeFailureKeepsTaskRunningAndEmitsEvent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.db")
	dir := t.TempDir()
	dir, err := canonicalDir(dir)
	require.NoError(t, err)
	var finalized atomic.Int32
	options := Options{
		Context: agent.ContextPolicy{Enabled: true, MaxMessages: 50, MaxInputTokens: 32768},
		Finalize: func(context.Context, int64) error {
			finalized.Add(1)
			return errors.New("container removal failed")
		},
	}
	m, err := Open(path, func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{Status: agent.Completed, Content: "done"}, nil
	}, nil, options)
	require.NoError(t, err)
	task, err := m.Submit(ctx, message("finalize-failure", "alice"), dir, request("run"))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		current, err := m.Get(ctx, task.Message.Session, task.ID)
		return err == nil && strings.Contains(current.Error, "cleanup_failed") && current.Status == "running"
	}, 5*time.Second, 20*time.Millisecond)
	records, err := m.Events(ctx, task.Message.Session, task.ID)
	require.NoError(t, err)
	require.Contains(t, strings.Join(records, "\n"), "environment_cleanup_failed")
	require.Equal(t, int32(1), finalized.Load())
	require.NoError(t, m.Close())

	// 重启后仍被排除在领取之外，不发无可执行工具的任务。
	restarted, err := Open(path, func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{Status: agent.Completed}, nil
	}, nil, Options{
		Context:            agent.ContextPolicy{Enabled: true, MaxMessages: 50, MaxInputTokens: 32768},
		BlockedDirectories: func() []string { return []string{dir} },
	})
	require.NoError(t, err)
	defer closeManager(t, restarted)
	again, err := restarted.Submit(ctx, message("after-restart", "alice"), dir, request("must stay queued"))
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond)
	current, err := restarted.Get(ctx, again.Message.Session, again.ID)
	require.NoError(t, err)
	require.Equal(t, "queued", current.Status)
}
