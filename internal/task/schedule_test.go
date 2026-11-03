package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/db"
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
	_, err = m.checkScheduledTask(ctx, first)
	require.Error(t, err)
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
	}, Options{Schedule: func(chat.Identity, string) error { return nil }})
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
	_, err = m.checkScheduledTask(ctx, queued)
	require.Error(t, err)
}

// 纯聊天计划创建、到点和排队执行都不需要执行授权；默认计划仍然要求授权。
func TestSchedule_PureChatSkipsAuthorization(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := openStore(filepath.Join(dir, "tasks.db"))
	require.NoError(t, err)
	m := &Manager{store: s, ctx: ctx, authorize: func(chat.Identity, string) error { return errors.New("no grant") }}
	defer func() { require.NoError(t, s.db.Close()) }()
	want, err := canonicalDir(dir)
	require.NoError(t, err)
	// 未声明纯聊天时保持旧行为：没有执行授权不能创建。
	msg := message("with-tools", "alice")
	_, err = m.CreateSchedule(ctx, msg, dir, request("goal"), ScheduleSpec{Kind: "every", Timezone: "UTC", Every: "1h"})
	require.Error(t, err)
	// 未知投递目标在创建时报错，不落库。
	msg.ID = "bad-delivery"
	_, err = m.CreateSchedule(ctx, msg, dir, request("goal"), ScheduleSpec{Kind: "every", Timezone: "UTC", Every: "1h"}, ScheduleRequiresExecution(false), WithScheduleDelivery("page"))
	require.Error(t, err)
	msg.ID = "chat"
	plan, err := m.CreateSchedule(ctx, msg, dir, request("纯聊天目标"), ScheduleSpec{Kind: "every", Timezone: "UTC", Every: "1h"}, ScheduleRequiresExecution(false), WithScheduleDelivery("silent"))
	require.NoError(t, err)
	require.False(t, plan.RequiresExecution)
	require.Equal(t, "silent", plan.Delivery)
	require.Equal(t, want, plan.WorkDir)
	// 到点不复查授权，照常建任务并带上静默标记。
	require.NoError(t, m.tickSchedules(ctx, plan.NextRun))
	plan, err = m.GetSchedule(ctx, msg.Session, plan.ID)
	require.NoError(t, err)
	require.NotZero(t, plan.LastTask)
	queued, err := m.Get(ctx, msg.Session, plan.LastTask)
	require.NoError(t, err)
	require.True(t, queued.Silent)
	require.Equal(t, want, queued.WorkDir)
	scheduled, err := m.checkScheduledTask(ctx, queued)
	require.NoError(t, err)
	require.True(t, scheduled)
	_, err = m.checkScheduledTask(ctx, queued)
	require.NoError(t, err)
}

// legacyTaskSchema 是新增 silent、requires_execution、delivery 之前的表结构。
const legacyTaskSchema = `
CREATE TABLE tasks (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 platform TEXT NOT NULL, account TEXT NOT NULL, room TEXT NOT NULL, event TEXT NOT NULL,
 sender TEXT NOT NULL, work_dir TEXT NOT NULL, message BLOB NOT NULL, request BLOB NOT NULL,
 status TEXT NOT NULL DEFAULT 'queued', result BLOB NOT NULL DEFAULT '{}', error TEXT NOT NULL DEFAULT '',
 cancel_requested INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL,
 delivery TEXT NOT NULL DEFAULT 'pending', delivery_attempts INTEGER NOT NULL DEFAULT 0,
 delivery_error TEXT NOT NULL DEFAULT '', delivery_id TEXT NOT NULL DEFAULT '', next_delivery INTEGER NOT NULL DEFAULT 0,
 UNIQUE(platform, account, room, event)
);
CREATE TABLE schedules (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 platform TEXT NOT NULL, account TEXT NOT NULL, room TEXT NOT NULL, event TEXT NOT NULL,
 message BLOB NOT NULL, work_dir TEXT NOT NULL, request BLOB NOT NULL, spec BLOB NOT NULL,
 status TEXT NOT NULL DEFAULT 'active', next_run INTEGER NOT NULL,
 last_task INTEGER NOT NULL DEFAULT 0, reason TEXT NOT NULL DEFAULT '',
 UNIQUE(platform,account,room,event)
);
CREATE TABLE schedule_runs (
 schedule_id INTEGER NOT NULL REFERENCES schedules(id), due INTEGER NOT NULL,
 handled_at INTEGER NOT NULL, outcome TEXT NOT NULL, detail TEXT NOT NULL,
 task_id INTEGER REFERENCES tasks(id), PRIMARY KEY(schedule_id,due)
);`

// 旧数据库升级：新增列必须幂等补齐，旧行回退到“需要执行授权 + 回复原消息 + 不静默”。
func TestSchedule_LegacyDatabaseMigration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.db")
	raw, err := sql.Open(db.DriverName, path)
	require.NoError(t, err)
	_, err = raw.Exec(legacyTaskSchema)
	require.NoError(t, err)
	msg := message("legacy", "alice")
	msgData, err := json.Marshal(msg)
	require.NoError(t, err)
	reqData, err := json.Marshal(request("legacy goal"))
	require.NoError(t, err)
	specData, err := json.Marshal(ScheduleSpec{Kind: "every", Timezone: "UTC", Every: "1h"})
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO schedules(platform,account,room,event,message,work_dir,request,spec,next_run) VALUES(?,?,?,?,?,?,?,?,?)`,
		msg.Session.Platform, msg.Session.Account, msg.Session.Conversation, msg.ID, msgData, dir, reqData, specData, time.Now().Add(time.Hour).UnixMilli())
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO tasks(platform,account,room,event,sender,work_dir,message,request,status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		msg.Session.Platform, msg.Session.Account, msg.Session.Conversation, "legacy-task", msg.SenderID, dir, msgData, reqData, "queued", time.Now().UnixMilli())
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	s, err := openStore(path)
	require.NoError(t, err)
	for _, c := range []struct{ table, column string }{
		{"tasks", "silent"}, {"schedules", "requires_execution"}, {"schedules", "delivery"},
	} {
		var exists int
		require.NoError(t, s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('`+c.table+`') WHERE name=?)`, c.column).Scan(&exists))
		require.Equal(t, 1, exists, "%s.%s 未补齐", c.table, c.column)
	}
	m := &Manager{store: s, ctx: ctx, authorize: func(chat.Identity, string) error { return nil }}
	plans, err := m.ListSchedules(ctx, msg.Session)
	require.NoError(t, err)
	require.Len(t, plans, 1)
	require.True(t, plans[0].RequiresExecution, "旧计划必须继续要求执行授权")
	require.Equal(t, "origin", plans[0].Delivery)
	legacy, err := m.Get(ctx, msg.Session, 1)
	require.NoError(t, err)
	require.False(t, legacy.Silent)
	// 迁移后的库继续接受新语义，并在关闭重开后保持。
	chatMsg := message("pure-chat", "alice")
	plan, err := m.CreateSchedule(ctx, chatMsg, dir, request("纯聊天"), ScheduleSpec{Kind: "every", Timezone: "UTC", Every: "1h"}, ScheduleRequiresExecution(false), WithScheduleDelivery("silent"))
	require.NoError(t, err)
	require.NoError(t, m.tickSchedules(ctx, plan.NextRun))
	plan, err = m.GetSchedule(ctx, msg.Session, plan.ID)
	require.NoError(t, err)
	require.NotZero(t, plan.LastTask)
	require.NoError(t, s.db.Close())
	s, err = openStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.db.Close()) }()
	m.store = s
	plan, err = m.GetSchedule(ctx, msg.Session, plan.ID)
	require.NoError(t, err)
	require.False(t, plan.RequiresExecution)
	require.Equal(t, "silent", plan.Delivery)
	taskRow, err := m.Get(ctx, msg.Session, plan.LastTask)
	require.NoError(t, err)
	require.True(t, taskRow.Silent)
	plans, err = m.ListSchedules(ctx, msg.Session)
	require.NoError(t, err)
	require.Len(t, plans, 2)
}
