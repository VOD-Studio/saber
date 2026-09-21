package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // 精简容器没有系统时区库时仍支持 IANA 时区。

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

const scheduleSchema = `
CREATE TABLE IF NOT EXISTS schedules (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 platform TEXT NOT NULL, account TEXT NOT NULL, room TEXT NOT NULL, event TEXT NOT NULL,
 message BLOB NOT NULL, work_dir TEXT NOT NULL, request BLOB NOT NULL, spec BLOB NOT NULL,
 status TEXT NOT NULL DEFAULT 'active', next_run INTEGER NOT NULL,
 last_task INTEGER NOT NULL DEFAULT 0, reason TEXT NOT NULL DEFAULT '',
 UNIQUE(platform,account,room,event)
);
CREATE INDEX IF NOT EXISTS schedules_due ON schedules(status,next_run);
CREATE TABLE IF NOT EXISTS schedule_runs (
 schedule_id INTEGER NOT NULL REFERENCES schedules(id), due INTEGER NOT NULL,
 handled_at INTEGER NOT NULL, outcome TEXT NOT NULL, detail TEXT NOT NULL,
 task_id INTEGER REFERENCES tasks(id), PRIMARY KEY(schedule_id,due)
);
CREATE UNIQUE INDEX IF NOT EXISTS schedule_task ON schedule_runs(task_id) WHERE task_id IS NOT NULL;
`

// ScheduleAuthorize 按当前管理员配置检查负责人和工作目录，不接受模型提供的身份。
type ScheduleAuthorize func(chat.Identity, string) error

// ScheduleSpec 描述明确时区的一次性、固定周期或工作日定时计划。
type ScheduleSpec struct {
	// Kind 只能是 once、every 或 weekdays。
	Kind string `json:"kind"`
	// Timezone 必须是明确的 IANA 时区，不能使用进程本地时区。
	Timezone string `json:"timezone"`
	// At 在 once 中使用带偏移的 RFC3339，在 weekdays 中使用 HH:MM。
	At string `json:"at,omitempty"`
	// Every 是 Go duration 格式的周期，范围为 1 分钟到 365 天。
	Every string `json:"every,omitempty"`
}

func (s ScheduleSpec) next(after time.Time) (time.Time, error) {
	if s.Timezone == "" || s.Timezone == "Local" {
		return time.Time{}, errors.New("必须指定 IANA 时区，例如 Asia/Shanghai")
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	switch s.Kind {
	case "once":
		at, err := time.Parse(time.RFC3339, s.At)
		if err != nil || s.Every != "" {
			return time.Time{}, errors.New("once 需要带时区偏移的 RFC3339 时间，不能指定 every")
		}
		if !at.After(after) {
			return time.Time{}, nil
		}
		return at, nil
	case "every":
		d, err := time.ParseDuration(s.Every)
		if err != nil || d < time.Minute || d > 365*24*time.Hour || s.At != "" {
			return time.Time{}, errors.New("every 周期必须为 1m 到 8760h，不能指定 at")
		}
		return after.Add(d), nil
	case "weekdays":
		clock, err := time.Parse("15:04", s.At)
		if err != nil || clock.Format("15:04") != s.At || s.Every != "" {
			return time.Time{}, errors.New("weekdays 时间必须为 HH:MM，不能指定 every")
		}
		local := after.In(loc)
		for day := 0; day < 15; day++ {
			date := time.Date(local.Year(), local.Month(), local.Day()+day, 12, 0, 0, 0, loc)
			at := time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
			// 夏令时跳过不存在的墙钟时刻；重复时刻固定选一次，不追补第二次。
			if at.Weekday() != time.Saturday && at.Weekday() != time.Sunday && at.Hour() == clock.Hour() && at.Minute() == clock.Minute() && at.Day() == date.Day() && at.After(after) {
				return at, nil
			}
		}
		return time.Time{}, errors.New("找不到下次工作日时间")
	default:
		return time.Time{}, errors.New("计划类型必须为 once、every 或 weekdays")
	}
}

// Schedule 保存负责人、原消息/汇报目的地、工作目录和独立请求快照。
type Schedule struct {
	// ID 是计划的稳定编号，与普通任务编号独立。
	ID int64
	// Message 保留真实负责人、目标文本与汇报目的地。
	Message chat.Message
	// WorkDir 是创建时固定的规范化授权目录。
	WorkDir string
	// Request 是每次运行使用的独立模型请求快照。
	Request agent.Request
	// Spec 保存原始时间规则和时区。
	Spec ScheduleSpec
	// Status 为 active、paused、deleted 或 completed。
	Status string
	// NextRun 是下一次发生的绝对时刻；一次性完成后为空。
	NextRun time.Time
	// LastTask 指向最近生成的普通任务，用于避免执行重叠。
	LastTask int64
	// Reason 保留最近跳过、暂停或合并周期的原因。
	Reason string
}

// ScheduleRun 是一次到点决策；skipped 表示上次未结束，paused 表示权限失效。
type ScheduleRun struct {
	// Due 是被处理的计划时刻，也是发生去重键。
	Due time.Time
	// HandledAt 是实际处理时间，与 Due 的差值体现离线延迟。
	HandledAt time.Time
	// Outcome 为 created、skipped 或 paused。
	Outcome string
	// Detail 保存决策原因。
	Detail string
	// TaskID 非零时关联可查询执行和投递状态的普通任务。
	TaskID int64
}

const scheduleColumns = `id,message,work_dir,request,spec,status,next_run,last_task,reason`

func scanSchedule(row interface{ Scan(...any) error }) (Schedule, error) {
	var s Schedule
	var msg, req, spec []byte
	var next int64
	err := row.Scan(&s.ID, &msg, &s.WorkDir, &req, &spec, &s.Status, &next, &s.LastTask, &s.Reason)
	if err != nil {
		return s, err
	}
	if next != 0 {
		s.NextRun = time.UnixMilli(next)
	}
	return s, errors.Join(json.Unmarshal(msg, &s.Message), json.Unmarshal(req, &s.Request), json.Unmarshal(spec, &s.Spec))
}

func (m *Manager) authorizeSchedule(s Schedule) error {
	if m.authorize == nil {
		return errors.New("未配置定时任务负责人授权")
	}
	return m.authorize(chat.Identity{Session: s.Message.Session, SenderID: s.Message.SenderID}, s.WorkDir)
}

// CreateSchedule 使用真实来源去重；同一消息最多创建一个计划，重试不会复活已删除计划。
func (m *Manager) CreateSchedule(ctx context.Context, msg chat.Message, dir string, req agent.Request, spec ScheduleSpec) (Schedule, error) {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	if err := msg.Validate(); err != nil {
		return Schedule{}, err
	}
	if msg.ID == "" || strings.TrimSpace(msg.Text) == "" {
		return Schedule{}, errors.New("计划必须有原消息 ID 和目标")
	}
	old, err := scanSchedule(m.store.db.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE platform=? AND account=? AND room=? AND event=?`, msg.Session.Platform, msg.Session.Account, msg.Session.Conversation, msg.ID))
	if err == nil {
		if old.Message.SenderID != msg.SenderID {
			return Schedule{}, errors.New("计划来源身份不匹配")
		}
		return old, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, err
	}
	dir, err = canonicalDir(dir)
	if err != nil {
		return Schedule{}, err
	}
	s := Schedule{Message: msg, WorkDir: dir, Request: req, Spec: spec}
	if err := m.authorizeSchedule(s); err != nil {
		return Schedule{}, err
	}
	next, err := spec.next(time.Now())
	if err != nil {
		return Schedule{}, err
	}
	if next.IsZero() {
		return Schedule{}, errors.New("一次性计划时间必须在未来")
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return Schedule{}, err
	}
	requestData, err := json.Marshal(req)
	if err != nil {
		return Schedule{}, err
	}
	specData, err := json.Marshal(spec)
	if err != nil {
		return Schedule{}, err
	}
	result, err := m.store.db.ExecContext(ctx, `INSERT INTO schedules(platform,account,room,event,message,work_dir,request,spec,next_run) VALUES(?,?,?,?,?,?,?,?,?)`, msg.Session.Platform, msg.Session.Account, msg.Session.Conversation, msg.ID, data, dir, requestData, specData, next.UnixMilli())
	if err != nil {
		return Schedule{}, err
	}
	s.ID, err = result.LastInsertId()
	if err != nil {
		return Schedule{}, err
	}
	m.notify()
	return m.GetSchedule(ctx, msg.Session, s.ID)
}

// GetSchedule 仅允许在计划所属的账号和群内查看。
func (m *Manager) GetSchedule(ctx context.Context, session chat.Session, id int64) (Schedule, error) {
	if err := session.Validate(); err != nil {
		return Schedule{}, err
	}
	return scanSchedule(m.store.db.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE platform=? AND account=? AND room=? AND id=?`, append(scope(session), id)...))
}

func (m *Manager) schedules(ctx context.Context, where string, args ...any) (items []Schedule, err error) {
	rows, err := m.store.db.QueryContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	return items, rows.Err()
}

// ListSchedules 返回本群最近的 50 个未删除计划。
func (m *Manager) ListSchedules(ctx context.Context, session chat.Session) ([]Schedule, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	return m.schedules(ctx, `platform=? AND account=? AND room=? AND status!='deleted' ORDER BY id DESC LIMIT 50`, scope(session)...)
}

// ChangeSchedule 只允许负责人暂停或软删除；已运行任务需另行取消。
func (m *Manager) ChangeSchedule(ctx context.Context, identity chat.Identity, id int64, action string) (Schedule, error) {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	s, err := m.GetSchedule(ctx, identity.Session, id)
	if err != nil {
		return Schedule{}, err
	}
	if s.Message.SenderID != identity.SenderID {
		return Schedule{}, errors.New("只能修改自己创建的计划")
	}
	status := map[string]string{"pause": "paused", "delete": "deleted"}[action]
	if status == "" {
		return Schedule{}, errors.New("只支持 pause 或 delete")
	}
	_, err = m.store.db.ExecContext(ctx, `UPDATE schedules SET status=?,reason=? WHERE id=? AND status!='deleted'`, status, "负责人操作："+action, id)
	if err != nil {
		return Schedule{}, err
	}
	return m.GetSchedule(ctx, identity.Session, id)
}

// ScheduleRuns 返回同群可见的最近 20 条到点决策记录。
func (m *Manager) ScheduleRuns(ctx context.Context, session chat.Session, id int64) (records []ScheduleRun, err error) {
	if _, err = m.GetSchedule(ctx, session, id); err != nil {
		return nil, err
	}
	rows, err := m.store.db.QueryContext(ctx, `SELECT due,handled_at,outcome,detail,COALESCE(task_id,0) FROM schedule_runs WHERE schedule_id=? ORDER BY due DESC LIMIT 20`, id)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var r ScheduleRun
		var due, handled int64
		if err := rows.Scan(&due, &handled, &r.Outcome, &r.Detail, &r.TaskID); err != nil {
			return nil, err
		}
		r.Due, r.HandledAt = time.UnixMilli(due), time.UnixMilli(handled)
		records = append(records, r)
	}
	return records, rows.Err()
}

func (m *Manager) tickSchedules(ctx context.Context, now time.Time) error {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	items, err := m.schedules(ctx, `status='active' AND next_run<=? ORDER BY next_run LIMIT 50`, now.UnixMilli())
	if err != nil {
		return err
	}
	for _, s := range items {
		if err := m.fireSchedule(ctx, s, now); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) fireSchedule(ctx context.Context, s Schedule, now time.Time) (err error) {
	next, err := s.Spec.next(now)
	if err != nil {
		return err
	}
	if s.Spec.Kind == "every" {
		d, _ := time.ParseDuration(s.Spec.Every) // next 已完成校验。
		next = now.Add(d - now.Sub(s.NextRun)%d) // 保留原始周期相位，不逐条补跑。
	}
	status, outcome, detail := "active", "created", "错过的周期合并为本次，下一次推进到未来"
	var nextMillis int64
	if next.IsZero() {
		status = "completed"
	} else {
		nextMillis = next.UnixMilli()
	}
	if authErr := m.authorizeSchedule(s); authErr != nil {
		status, outcome, detail = "paused", "paused", authErr.Error()
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	if outcome == "created" && s.LastTask != 0 {
		var state string
		if err = tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, s.LastTask).Scan(&state); err != nil {
			return err
		}
		if state == "queued" || state == "running" {
			outcome, detail = "skipped", "上次任务尚未结束"
		}
	}
	var taskID any
	last := s.LastTask
	if outcome == "created" {
		msg, marshalErr := json.Marshal(s.Message)
		if marshalErr != nil {
			return marshalErr
		}
		req, marshalErr := json.Marshal(s.Request)
		if marshalErr != nil {
			return marshalErr
		}
		// SQL 去重键区分每次发生，消息快照仍保留真实原事件供引用及文件投递。
		event := fmt.Sprintf("saber-schedule:%d:%d", s.ID, s.NextRun.UnixMilli())
		result, insertErr := tx.ExecContext(ctx, `INSERT INTO tasks(platform,account,room,event,sender,work_dir,message,request,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, s.Message.Session.Platform, s.Message.Session.Account, s.Message.Session.Conversation, event, s.Message.SenderID, s.WorkDir, msg, req, now.UnixMilli())
		if insertErr != nil {
			return insertErr
		}
		last, err = result.LastInsertId()
		if err != nil {
			return err
		}
		taskID = last
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO schedule_runs(schedule_id,due,handled_at,outcome,detail,task_id) VALUES(?,?,?,?,?,?)`, s.ID, s.NextRun.UnixMilli(), now.UnixMilli(), outcome, detail, taskID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE schedules SET status=?,next_run=?,last_task=?,reason=? WHERE id=?`, status, nextMillis, last, detail, s.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) checkScheduledTask(ctx context.Context, t Task) error {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	s, err := scanSchedule(m.store.db.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=(SELECT schedule_id FROM schedule_runs WHERE task_id=?)`, t.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.Status == "paused" || s.Status == "deleted" {
		return fmt.Errorf("计划 #%d 已%s，停止排队执行", s.ID, s.Status)
	}
	if err = m.authorizeSchedule(s); err != nil {
		_, saveErr := m.store.db.ExecContext(ctx, `UPDATE schedules SET status='paused',reason=? WHERE id=?`, err.Error(), s.ID)
		return errors.Join(err, saveErr)
	}
	return nil
}
