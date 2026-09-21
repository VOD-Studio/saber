package task

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

func TestScheduleSpec_Next(t *testing.T) {
	for _, tc := range []struct {
		name        string
		spec        ScheduleSpec
		after, want string
		invalid     bool
	}{
		{"weekend", ScheduleSpec{Kind: "weekdays", Timezone: "Asia/Shanghai", At: "09:00"}, "2026-09-18T09:00:00+08:00", "2026-09-21T09:00:00+08:00", false},
		{"today", ScheduleSpec{Kind: "weekdays", Timezone: "Asia/Shanghai", At: "09:00"}, "2026-09-21T08:00:00+08:00", "2026-09-21T09:00:00+08:00", false},
		{"dst", ScheduleSpec{Kind: "weekdays", Timezone: "America/New_York", At: "09:00"}, "2026-03-06T09:00:00-05:00", "2026-03-09T09:00:00-04:00", false},
		{"interval", ScheduleSpec{Kind: "every", Timezone: "UTC", Every: "2h"}, "2026-01-01T00:00:00Z", "2026-01-01T02:00:00Z", false},
		{"once", ScheduleSpec{Kind: "once", Timezone: "Asia/Shanghai", At: "2026-09-21T09:00:00+08:00"}, "2026-01-01T00:00:00Z", "2026-09-21T09:00:00+08:00", false},
		{"once spent", ScheduleSpec{Kind: "once", Timezone: "UTC", At: "2025-01-01T00:00:00Z"}, "2026-01-01T00:00:00Z", "", false},
		{"missing zone", ScheduleSpec{Kind: "every", Every: "1h"}, "", "", true},
		{"local zone", ScheduleSpec{Kind: "every", Timezone: "Local", Every: "1h"}, "", "", true},
		{"bad zone", ScheduleSpec{Kind: "every", Timezone: "Bad/Zone", Every: "1h"}, "", "", true},
		{"short", ScheduleSpec{Kind: "every", Timezone: "UTC", Every: "1s"}, "", "", true},
		{"bad clock", ScheduleSpec{Kind: "weekdays", Timezone: "UTC", At: "25:00"}, "", "", true},
		{"no offset", ScheduleSpec{Kind: "once", Timezone: "UTC", At: "2027-01-01T00:00"}, "", "", true},
		{"unknown", ScheduleSpec{Kind: "cron", Timezone: "UTC"}, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after, _ := time.Parse(time.RFC3339, tc.after)
			got, err := tc.spec.next(after)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			want, _ := time.Parse(time.RFC3339, tc.want)
			require.True(t, want.Equal(got), "want %v got %v", want, got)
		})
	}
}

func TestSchedule_AtomicCatchupOverlapRestartAndPermissions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.db")
	s, err := openStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.db.Close()) }()
	var revoked atomic.Bool
	auth := func(identity chat.Identity, workdir string) error {
		if revoked.Load() || identity.SenderID != "alice" || workdir == "" {
			return errors.New("owner revoked")
		}
		return nil
	}
	m := &Manager{store: s, ctx: ctx, authorize: auth}
	msg := message("source", "alice")
	plan, err := m.CreateSchedule(ctx, msg, dir, request("goal"), ScheduleSpec{Kind: "every", Timezone: "Asia/Shanghai", Every: "1h"})
	require.NoError(t, err)
	duplicate, err := m.CreateSchedule(ctx, msg, dir, request("changed"), plan.Spec)
	require.NoError(t, err)
	require.Equal(t, plan.ID, duplicate.ID)
	other := msg.Session
	other.Conversation = "other"
	_, err = m.GetSchedule(ctx, other, plan.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = m.ChangeSchedule(ctx, chat.Identity{Session: msg.Session, SenderID: "bob"}, plan.ID, "delete")
	require.Error(t, err)
	// 在推进计划时模拟写盘失败：新任务和触发记录必须一起回滚。
	_, err = s.db.Exec(`CREATE TRIGGER fail_schedule BEFORE UPDATE ON schedules BEGIN SELECT RAISE(FAIL,'disk failure'); END`)
	require.NoError(t, err)
	require.Error(t, m.tickSchedules(ctx, plan.NextRun))
	items, err := m.List(ctx, msg.Session)
	require.NoError(t, err)
	require.Empty(t, items)
	_, err = s.db.Exec(`DROP TRIGGER fail_schedule`)
	require.NoError(t, err)
	now := plan.NextRun.Add(72*time.Hour + 30*time.Minute)
	require.NoError(t, m.tickSchedules(ctx, now))
	require.NoError(t, m.tickSchedules(ctx, now))
	plan, err = m.GetSchedule(ctx, msg.Session, plan.ID)
	require.NoError(t, err)
	require.True(t, plan.NextRun.Equal(now.Add(30*time.Minute)))
	items, err = m.List(ctx, msg.Session)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, msg.ID, items[0].Message.ID)
	require.Equal(t, "goal", items[0].Request.Messages[0].Content)
	first := items[0]
	require.NoError(t, m.tickSchedules(ctx, plan.NextRun))
	runs, err := m.ScheduleRuns(ctx, msg.Session, plan.ID)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	require.Equal(t, "skipped", runs[0].Outcome)
	// 真正关闭并重开同一 SQLite 文件，已提交发生不重复创建。
	require.NoError(t, s.db.Close())
	s, err = openStore(path)
	require.NoError(t, err)
	m.store = s
	require.NoError(t, m.tickSchedules(ctx, plan.NextRun))
	items, err = m.List(ctx, msg.Session)
	require.NoError(t, err)
	require.Len(t, items, 1)
	// 即使触发时授权通过，排队后撤权也不能进入 runner。
	revoked.Store(true)
	require.Error(t, m.checkScheduledTask(ctx, first))
	plan, err = m.GetSchedule(ctx, msg.Session, plan.ID)
	require.NoError(t, err)
	require.Equal(t, "paused", plan.Status)
	require.Contains(t, plan.Reason, "revoked")
	_, err = m.ChangeSchedule(ctx, chat.Identity{Session: msg.Session, SenderID: "alice"}, plan.ID, "delete")
	require.NoError(t, err)
	duplicate, err = m.CreateSchedule(ctx, msg, dir, request("changed"), plan.Spec)
	require.NoError(t, err)
	require.Equal(t, "deleted", duplicate.Status)
	plans, err := m.ListSchedules(ctx, msg.Session)
	require.NoError(t, err)
	require.Empty(t, plans)
}

func TestSchedule_BackgroundOnceAndDeliveryRetry(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	var executions, deliveries atomic.Int32
	m, err := Open(filepath.Join(dir, "tasks.db"), func(ctx context.Context, req agent.Request, _ func(agent.Event)) (agent.Result, error) {
		executions.Add(1)
		identity, ok := chat.IdentityFromContext(ctx)
		if !ok || identity.SenderID != "alice" || req.Messages[0].Content != "scheduled goal" {
			return agent.Result{}, errors.New("lost context")
		}
		return agent.Result{Status: agent.Completed, Content: "offline result"}, nil
	}, func(_ context.Context, task Task) (string, error) {
		if deliveries.Add(1) == 1 {
			return "", errors.New("offline")
		}
		if task.Message.ID != "original" || task.Message.Session.Thread != "topic" {
			return "", errors.New("lost source")
		}
		return "reply", nil
	}, func(chat.Identity, string) error { return nil })
	require.NoError(t, err)
	defer closeManager(t, m)
	msg := message("original", "alice")
	plan, err := m.CreateSchedule(ctx, msg, dir, request("scheduled goal"), ScheduleSpec{Kind: "once", Timezone: "UTC", At: time.Now().Add(2 * time.Second).Format(time.RFC3339)})
	require.NoError(t, err)
	var last Task
	require.Eventually(t, func() bool {
		p, e := m.GetSchedule(ctx, msg.Session, plan.ID)
		if e != nil || p.LastTask == 0 {
			return false
		}
		last, e = m.Get(ctx, msg.Session, p.LastTask)
		return e == nil && last.Delivery == "sent"
	}, 6*time.Second, 20*time.Millisecond)
	require.Equal(t, "completed", last.Status)
	require.EqualValues(t, 1, executions.Load())
	require.EqualValues(t, 2, deliveries.Load())
	plan, err = m.GetSchedule(ctx, msg.Session, plan.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", plan.Status)
	require.True(t, plan.NextRun.IsZero())
}

func TestSchedule_RevokedAtDueAndPausedBeforeExecution(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := openStore(filepath.Join(dir, "tasks.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, s.db.Close()) }()
	m := &Manager{store: s, ctx: ctx, authorize: func(chat.Identity, string) error { return nil }}
	msg := message("a", "alice")
	plan, err := m.CreateSchedule(ctx, msg, dir, request("goal"), ScheduleSpec{Kind: "every", Timezone: "UTC", Every: "1m"})
	require.NoError(t, err)
	m.authorize = nil
	require.NoError(t, m.tickSchedules(ctx, plan.NextRun))
	plan, err = m.GetSchedule(ctx, msg.Session, plan.ID)
	require.NoError(t, err)
	require.Equal(t, "paused", plan.Status)
	require.Zero(t, plan.LastTask)
	runs, err := m.ScheduleRuns(ctx, msg.Session, plan.ID)
	require.NoError(t, err)
	require.Equal(t, "paused", runs[0].Outcome)
	m.authorize = func(chat.Identity, string) error { return nil }
	msg.ID = "b"
	plan, err = m.CreateSchedule(ctx, msg, dir, request("goal"), plan.Spec)
	require.NoError(t, err)
	require.NoError(t, m.tickSchedules(ctx, plan.NextRun))
	plan, err = m.ChangeSchedule(ctx, chat.Identity{Session: msg.Session, SenderID: "alice"}, plan.ID, "pause")
	require.NoError(t, err)
	queued, err := m.Get(ctx, msg.Session, plan.LastTask)
	require.NoError(t, err)
	require.Error(t, m.checkScheduledTask(ctx, queued))
}
