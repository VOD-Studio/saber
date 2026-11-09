package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
 requires_execution INTEGER NOT NULL DEFAULT 1, delivery TEXT NOT NULL DEFAULT 'origin',
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

// ScheduleSpec 描述明确时区的一次性、固定周期、工作日或 cron 定时计划。
type ScheduleSpec struct {
	// Kind 只能是 once、every、weekdays 或 cron。
	Kind string `json:"kind"`
	// Timezone 必须是明确的 IANA 时区，不能使用进程本地时区。
	Timezone string `json:"timezone"`
	// At 在 once 中使用带偏移的 RFC3339，在 weekdays 中使用 HH:MM。
	At string `json:"at,omitempty"`
	// Every 是 Go duration 格式的周期，范围为 1 分钟到 365 天。
	Every string `json:"every,omitempty"`
	// Cron 是 5 段表达式（分 时 日 月 周），周 0 和 7 都表示周日。
	Cron string `json:"cron,omitempty"`
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
		if err != nil || s.Every != "" || s.Cron != "" {
			return time.Time{}, errors.New("once 需要带时区偏移的 RFC3339 时间，不能指定 every 或 cron")
		}
		if !at.After(after) {
			return time.Time{}, nil
		}
		return at, nil
	case "every":
		d, err := time.ParseDuration(s.Every)
		if err != nil || d < time.Minute || d > 365*24*time.Hour || s.At != "" || s.Cron != "" {
			return time.Time{}, errors.New("every 周期必须为 1m 到 8760h，不能指定 at 或 cron")
		}
		return after.Add(d), nil
	case "weekdays":
		clock, err := time.Parse("15:04", s.At)
		if err != nil || clock.Format("15:04") != s.At || s.Every != "" || s.Cron != "" {
			return time.Time{}, errors.New("weekdays 时间必须为 HH:MM，不能指定 every 或 cron")
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
	case "cron":
		return s.nextCron(after, loc)
	default:
		return time.Time{}, errors.New("计划类型必须为 once、every、weekdays 或 cron")
	}
}

// cronField 是一个已解析的 cron 字段；values 以取值为下标，便于按值判断。
type cronField struct {
	// any 表示该字段覆盖全部合法取值，用于决定日与周的组合语义。
	any bool
	// values 标记该取值是否被表达式选中。
	values []bool
}

func (f cronField) has(value int) bool {
	return value >= 0 && value < len(f.values) && f.values[value]
}

// cronExpr 是解析后的 5 段 cron 表达式：分 时 日 月 周。
type cronExpr struct {
	minute, hour, day, month, weekday cronField
}

// parseCron 解析 5 段 cron；只支持 *、n、a-b、*/n、a-b/n 与逗号列表。
func parseCron(expr string) (cronExpr, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return cronExpr{}, errors.New("cron 必须是 5 段（分 时 日 月 周），不支持秒级或 6 段表达式")
	}
	var parsed cronExpr
	var err error
	if parsed.minute, err = parseCronField("分", fields[0], 0, 59); err != nil {
		return cronExpr{}, err
	}
	if parsed.hour, err = parseCronField("时", fields[1], 0, 23); err != nil {
		return cronExpr{}, err
	}
	if parsed.day, err = parseCronField("日", fields[2], 1, 31); err != nil {
		return cronExpr{}, err
	}
	if parsed.month, err = parseCronField("月", fields[3], 1, 12); err != nil {
		return cronExpr{}, err
	}
	// 周允许 0-7；解析后把 7 归一为 0，两个取值都表示周日。
	if parsed.weekday, err = parseCronField("周", fields[4], 0, 7); err != nil {
		return cronExpr{}, err
	}
	if parsed.weekday.values[7] {
		parsed.weekday.values[0] = true
	}
	parsed.weekday.values = parsed.weekday.values[:7]
	parsed.weekday.any = allCronValues(parsed.weekday.values, 0, 6)
	return parsed, nil
}

// parseCronField 解析单个字段；任何非数字、反向范围或零步长都会报错。
func parseCronField(name, text string, min, max int) (cronField, error) {
	field := cronField{values: make([]bool, max+1)}
	add := func(value int) error {
		if value < min || value > max {
			return fmt.Errorf("cron %s字段超出范围 %d-%d：%d", name, min, max, value)
		}
		field.values[value] = true
		return nil
	}
	if text == "" {
		return field, fmt.Errorf("cron %s字段不能为空", name)
	}
	for _, part := range strings.Split(text, ",") {
		if part == "" {
			return field, fmt.Errorf("cron %s字段含空项", name)
		}
		if part == "*" {
			for value := min; value <= max; value++ {
				_ = add(value)
			}
			continue
		}
		if stepText, isStep := strings.CutPrefix(part, "*/"); isStep {
			step, err := strconv.Atoi(stepText)
			if err != nil || step <= 0 {
				return field, fmt.Errorf("cron %s字段步长无效：%q", name, stepText)
			}
			for value := min; value <= max; value += step {
				_ = add(value)
			}
			continue
		}
		span, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			parsedStep, err := strconv.Atoi(stepText)
			if err != nil || parsedStep <= 0 {
				return field, fmt.Errorf("cron %s字段步长无效：%q", name, stepText)
			}
			step = parsedStep
		}
		from, to, isRange := strings.Cut(span, "-")
		if !isRange {
			if hasStep {
				return field, fmt.Errorf("cron %s字段的步长必须配合 * 或范围使用", name)
			}
			value, err := strconv.Atoi(span)
			if err != nil {
				return field, fmt.Errorf("cron %s字段必须为数字：%q", name, span)
			}
			if err := add(value); err != nil {
				return field, err
			}
			continue
		}
		start, err := strconv.Atoi(from)
		if err != nil {
			return field, fmt.Errorf("cron %s字段必须为数字：%q", name, from)
		}
		end, err := strconv.Atoi(to)
		if err != nil {
			return field, fmt.Errorf("cron %s字段必须为数字：%q", name, to)
		}
		if start > end {
			return field, fmt.Errorf("cron %s字段范围反向：%d-%d", name, start, end)
		}
		for value := start; value <= end; value += step {
			if err := add(value); err != nil {
				return field, err
			}
		}
	}
	field.any = allCronValues(field.values, min, max)
	return field, nil
}

// allCronValues 判断字段是否覆盖整段合法取值；用于区分 "*/1" 与真正受限的字段。
func allCronValues(values []bool, min, max int) bool {
	for value := min; value <= max; value++ {
		if value >= len(values) || !values[value] {
			return false
		}
	}
	return true
}

// dayMatches 按标准 cron 语义判断日期：日与周同时受限时任一匹配即可。
func (e cronExpr) dayMatches(day time.Time) bool {
	dayOfMonth := e.day.has(day.Day())
	dayOfWeek := e.weekday.has(int(day.Weekday()))
	switch {
	case e.day.any && e.weekday.any:
		return true
	case e.day.any:
		return dayOfWeek
	case e.weekday.any:
		return dayOfMonth
	default:
		return dayOfMonth || dayOfWeek
	}
}

// slots 返回按时间升序排列的当天分钟数。
func (e cronExpr) slots() []int {
	var slots []int
	for slot := range 24 {
		if !e.hour.has(slot) {
			continue
		}
		for minute := range 60 {
			if e.minute.has(minute) {
				slots = append(slots, slot*60+minute)
			}
		}
	}
	return slots
}

// nextCron 计算严格晚于 after 的下一次 cron 时刻；始终在计划时区内构造墙钟时间。
func (s ScheduleSpec) nextCron(after time.Time, loc *time.Location) (time.Time, error) {
	if s.At != "" || s.Every != "" {
		return time.Time{}, errors.New("cron 不能指定 at 或 every")
	}
	expr, err := parseCron(s.Cron)
	if err != nil {
		return time.Time{}, err
	}
	slots := expr.slots()
	if len(slots) == 0 {
		return time.Time{}, errors.New("cron 没有可执行的时间点")
	}
	local := after.In(loc)
	// 以正午为锚点做日期推进，避开少数时区在午夜发生的夏令时跳变。
	base := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, loc)
	// 最多搜索五年，足以覆盖任意合法组合；不可能日期（如 2 月 31 日）直接报错。
	for offset := range 366 * 5 {
		day := time.Date(base.Year(), base.Month(), base.Day()+offset, 12, 0, 0, 0, loc)
		if !expr.month.has(int(day.Month())) || !expr.dayMatches(day) {
			continue
		}
		for _, slot := range slots {
			hour, minute := slot/60, slot%60
			at := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc)
			// 夏令时不存在的墙钟时刻会被规范化到其他时刻，跳过；重复时刻只返回一次。
			if at.Hour() != hour || at.Minute() != minute || at.Day() != day.Day() {
				continue
			}
			if at.After(after) {
				return at, nil
			}
		}
	}
	return time.Time{}, errors.New("cron 在五年内没有可执行时间")
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
	// RequiresExecution 冻结创建时是否需要执行授权；纯聊天计划为 false，使用启动目录作为工作目录。
	RequiresExecution bool
	// Delivery 是投递目标：origin 回复原消息（默认），silent 只落库不向聊天投递。
	Delivery string
}

// ScheduleOption 在创建计划时追加执行授权与投递目标选项；未提供时保持旧行为。
type ScheduleOption func(*Schedule)

// ScheduleRequiresExecution 显式声明计划是否需要执行授权；默认需要，避免旧调用方意外放宽。
func ScheduleRequiresExecution(required bool) ScheduleOption {
	return func(s *Schedule) { s.RequiresExecution = required }
}

// WithScheduleDelivery 设置投递目标；取值在创建时校验，未知值直接报错。
func WithScheduleDelivery(target string) ScheduleOption {
	return func(s *Schedule) { s.Delivery = target }
}

// normalizeScheduleDelivery 把空值落为默认投递目标，并拒绝未知值。
func normalizeScheduleDelivery(target string) (string, error) {
	switch target {
	case "":
		return "origin", nil
	case "origin", "silent":
		return target, nil
	default:
		return "", fmt.Errorf("未知投递目标 %q，只支持 origin 或 silent", target)
	}
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

const scheduleColumns = `id,message,work_dir,request,spec,status,next_run,last_task,reason,requires_execution,delivery`

func scanSchedule(row interface{ Scan(...any) error }) (Schedule, error) {
	var s Schedule
	var msg, req, spec []byte
	var next int64
	var requires int
	err := row.Scan(&s.ID, &msg, &s.WorkDir, &req, &spec, &s.Status, &next, &s.LastTask, &s.Reason, &requires, &s.Delivery)
	if err != nil {
		return s, err
	}
	if next != 0 {
		s.NextRun = time.UnixMilli(next)
	}
	s.RequiresExecution = requires != 0
	return s, errors.Join(json.Unmarshal(msg, &s.Message), json.Unmarshal(req, &s.Request), json.Unmarshal(spec, &s.Spec))
}

func (m *Manager) authorizeSchedule(s Schedule) error {
	if m.authorize == nil {
		return errors.New("未配置定时任务负责人授权")
	}
	return m.authorize(chat.Identity{Session: s.Message.Session, SenderID: s.Message.SenderID}, s.WorkDir)
}

// CreateSchedule 使用真实来源去重；同一消息最多创建一个计划，重试不会复活已删除计划。
// 未提供选项时保持旧行为：要求执行授权并按原消息回复。
func (m *Manager) CreateSchedule(ctx context.Context, msg chat.Message, dir string, req agent.Request, spec ScheduleSpec, opts ...ScheduleOption) (Schedule, error) {
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
	s := Schedule{Message: msg, WorkDir: dir, Request: req, Spec: spec, RequiresExecution: true, Delivery: "origin"}
	for _, opt := range opts {
		if opt != nil {
			opt(&s)
		}
	}
	if s.Delivery, err = normalizeScheduleDelivery(s.Delivery); err != nil {
		return Schedule{}, err
	}
	if s.WorkDir, err = canonicalDir(s.WorkDir); err != nil {
		return Schedule{}, err
	}
	// 纯聊天计划不申请执行授权；能跳过授权的前提是创建时已经冻结了工具集合结论。
	if s.RequiresExecution {
		if err := m.authorizeSchedule(s); err != nil {
			return Schedule{}, err
		}
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
	result, err := m.store.db.ExecContext(ctx, `INSERT INTO schedules(platform,account,room,event,message,work_dir,request,spec,next_run,requires_execution,delivery) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, msg.Session.Platform, msg.Session.Account, msg.Session.Conversation, msg.ID, data, s.WorkDir, requestData, specData, next.UnixMilli(), s.RequiresExecution, s.Delivery)
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
	if !m.CanManage(identity, s.Message.SenderID) {
		return Schedule{}, errors.New("只有负责人或本群任务管理员可以修改计划")
	}
	status := map[string]string{"pause": "paused", "delete": "deleted"}[action]
	if status == "" {
		return Schedule{}, errors.New("只支持 pause 或 delete")
	}
	_, err = m.store.db.ExecContext(ctx, `UPDATE schedules SET status=?,reason=? WHERE id=? AND status!='deleted'`, status, "成员 "+identity.SenderID+" 操作："+action, id)
	if err != nil {
		return Schedule{}, err
	}
	return m.GetSchedule(ctx, identity.Session, id)
}

// EditSchedule 更新计划的规则、请求快照和投递目标；已删除的计划不可编辑。
//
// 权限与暂停一致：负责人或本群任务管理员。编辑保留原消息、负责人和房间，
// 把计划恢复为 active 并重新计算下一次时间；一次性计划的新时间必须在未来。
func (m *Manager) EditSchedule(ctx context.Context, identity chat.Identity, id int64, req agent.Request, spec ScheduleSpec, opts ...ScheduleOption) (Schedule, error) {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	s, err := m.GetSchedule(ctx, identity.Session, id)
	if err != nil {
		return Schedule{}, err
	}
	if s.Status == "deleted" {
		return Schedule{}, errors.New("计划已删除，不能编辑")
	}
	if !m.CanManage(identity, s.Message.SenderID) {
		return Schedule{}, errors.New("只有负责人或本群任务管理员可以修改计划")
	}
	if req.Model == "" {
		req.Model = s.Request.Model
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&s)
		}
	}
	if s.Delivery, err = normalizeScheduleDelivery(s.Delivery); err != nil {
		return Schedule{}, err
	}
	if s.RequiresExecution {
		// 编辑后立刻生效，必须先确认负责人当前仍有同一工作目录的授权。
		if err := m.authorizeSchedule(s); err != nil {
			return Schedule{}, err
		}
	}
	next, err := spec.next(time.Now())
	if err != nil {
		return Schedule{}, err
	}
	if next.IsZero() {
		return Schedule{}, errors.New("一次性计划时间必须在未来")
	}
	requestData, err := json.Marshal(req)
	if err != nil {
		return Schedule{}, err
	}
	specData, err := json.Marshal(spec)
	if err != nil {
		return Schedule{}, err
	}
	result, err := m.store.db.ExecContext(ctx, `UPDATE schedules SET request=?,spec=?,next_run=?,status='active',reason=?,requires_execution=?,delivery=? WHERE id=? AND status!='deleted'`,
		requestData, specData, next.UnixMilli(), "成员 "+identity.SenderID+" 编辑计划", s.RequiresExecution, s.Delivery, id)
	if err != nil {
		return Schedule{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Schedule{}, err
	} else if affected == 0 {
		return Schedule{}, errors.New("计划已删除，不能编辑")
	}
	m.notify()
	return m.GetSchedule(ctx, identity.Session, id)
}

// ResumeSchedule 把已暂停的计划恢复为 active 并重新计算下一次时间；已删除或一次性过期计划不可恢复。
func (m *Manager) ResumeSchedule(ctx context.Context, identity chat.Identity, id int64) (Schedule, error) {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	s, err := m.GetSchedule(ctx, identity.Session, id)
	if err != nil {
		return Schedule{}, err
	}
	switch s.Status {
	case "deleted":
		return Schedule{}, errors.New("计划已删除，不能恢复")
	case "paused":
	default:
		return Schedule{}, fmt.Errorf("只有已暂停的计划可以恢复，当前状态 %s", s.Status)
	}
	if !m.CanManage(identity, s.Message.SenderID) {
		return Schedule{}, errors.New("只有负责人或本群任务管理员可以恢复计划")
	}
	if s.RequiresExecution {
		if err := m.authorizeSchedule(s); err != nil {
			return Schedule{}, err
		}
	}
	next, err := s.Spec.next(time.Now())
	if err != nil {
		return Schedule{}, err
	}
	if next.IsZero() {
		return Schedule{}, errors.New("一次性计划时间已过，请用 edit 修改时间")
	}
	_, err = m.store.db.ExecContext(ctx, `UPDATE schedules SET status='active',next_run=?,reason=? WHERE id=? AND status='paused'`,
		next.UnixMilli(), "成员 "+identity.SenderID+" 恢复计划", id)
	if err != nil {
		return Schedule{}, err
	}
	m.notify()
	return m.GetSchedule(ctx, identity.Session, id)
}

// RunSchedule 立即生成一次普通任务，不改变计划的下次执行时间。
//
// 同一计划同一毫秒的重复调用返回同一条决策记录，不会重复建任务；
// 需要执行授权的计划在创建任务前按当前授权复查，撤销时把计划置为 paused。
func (m *Manager) RunSchedule(ctx context.Context, identity chat.Identity, id int64) (ScheduleRun, error) {
	return m.runSchedule(ctx, identity, id, time.Now())
}

func (m *Manager) runSchedule(ctx context.Context, identity chat.Identity, id int64, now time.Time) (record ScheduleRun, err error) {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	s, err := m.GetSchedule(ctx, identity.Session, id)
	if err != nil {
		return ScheduleRun{}, err
	}
	if s.Status == "deleted" {
		return ScheduleRun{}, errors.New("计划已删除，不能立即执行")
	}
	if !m.CanManage(identity, s.Message.SenderID) {
		return ScheduleRun{}, errors.New("只有负责人或本群任务管理员可以立即执行计划")
	}
	// 以毫秒为去重键：重复调用与到点发生共用同一张决策表。
	due := now.Truncate(time.Millisecond)
	if due.Equal(s.NextRun) {
		// 错开正在到点的发生，避免占用它的决策键导致自动触发永远推进不了。
		due = due.Add(-time.Millisecond)
	}
	if existing, found, err := m.scheduleRun(ctx, s.ID, due); err != nil {
		return ScheduleRun{}, err
	} else if found {
		return existing, nil
	}
	record = ScheduleRun{Due: due, HandledAt: now, Outcome: "created", Detail: "成员 " + identity.SenderID + " 立即执行"}
	status := s.Status
	if s.RequiresExecution {
		if err := m.authorizeSchedule(s); err != nil {
			status, record.Outcome, record.Detail = "paused", "paused", err.Error()
		}
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return ScheduleRun{}, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	if record.Outcome == "created" && s.LastTask != 0 {
		var state string
		if err = tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, s.LastTask).Scan(&state); err != nil {
			return ScheduleRun{}, err
		}
		if state == "queued" || state == "running" {
			record.Outcome, record.Detail = "skipped", "上次任务尚未结束，未重复执行"
		}
	}
	var taskID any
	last := s.LastTask
	if record.Outcome == "created" {
		if last, err = insertScheduledTask(ctx, tx, s, fmt.Sprintf("saber-schedule:%d:run:%d", s.ID, due.UnixMilli()), now); err != nil {
			return ScheduleRun{}, err
		}
		taskID, record.TaskID = last, last
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO schedule_runs(schedule_id,due,handled_at,outcome,detail,task_id) VALUES(?,?,?,?,?,?)`,
		s.ID, due.UnixMilli(), now.UnixMilli(), record.Outcome, record.Detail, taskID); err != nil {
		// 并发或到点占用了同一毫秒：回滚本次新任务，返回已有决策，保证幂等。
		insertErr := err
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return ScheduleRun{}, errors.Join(insertErr, rollbackErr)
		}
		if existing, found, lookupErr := m.scheduleRun(ctx, s.ID, due); lookupErr == nil && found {
			return existing, nil
		}
		return ScheduleRun{}, insertErr
	}
	if _, err = tx.ExecContext(ctx, `UPDATE schedules SET status=?,last_task=?,reason=? WHERE id=?`, status, last, record.Detail, s.ID); err != nil {
		return ScheduleRun{}, err
	}
	if err = tx.Commit(); err != nil {
		return ScheduleRun{}, err
	}
	m.notify()
	return record, nil
}

// scheduleRun 读取某次发生已有的决策记录，用于重复调用去重。
func (m *Manager) scheduleRun(ctx context.Context, scheduleID int64, due time.Time) (ScheduleRun, bool, error) {
	var record ScheduleRun
	var handled int64
	err := m.store.db.QueryRowContext(ctx, `SELECT handled_at,outcome,detail,COALESCE(task_id,0) FROM schedule_runs WHERE schedule_id=? AND due=?`, scheduleID, due.UnixMilli()).Scan(&handled, &record.Outcome, &record.Detail, &record.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	record.Due, record.HandledAt = due, time.UnixMilli(handled)
	return record, true, nil
}

// insertScheduledTask 在调用方事务内为一次计划发生创建普通任务，返回任务编号。
func insertScheduledTask(ctx context.Context, tx *sql.Tx, s Schedule, event string, now time.Time) (int64, error) {
	msg, err := json.Marshal(s.Message)
	if err != nil {
		return 0, err
	}
	req, err := json.Marshal(s.Request)
	if err != nil {
		return 0, err
	}
	// SQL 去重键区分每次发生，消息快照仍保留真实原事件供引用及文件投递。
	result, err := tx.ExecContext(ctx, `INSERT INTO tasks(platform,account,room,event,sender,work_dir,message,request,created_at,silent) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		s.Message.Session.Platform, s.Message.Session.Account, s.Message.Session.Conversation, event, s.Message.SenderID, s.WorkDir, msg, req, now.UnixMilli(), s.Delivery == "silent")
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
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
	// 纯聊天计划创建时已冻结为不需要执行授权，到点也不复查授权。
	if s.RequiresExecution {
		if authErr := m.authorizeSchedule(s); authErr != nil {
			status, outcome, detail = "paused", "paused", authErr.Error()
		}
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
		// SQL 去重键区分每次发生，消息快照仍保留真实原事件供引用及文件投递。
		if last, err = insertScheduledTask(ctx, tx, s, fmt.Sprintf("saber-schedule:%d:%d", s.ID, s.NextRun.UnixMilli()), now); err != nil {
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

// ScheduleForTask 返回由该任务到点生成的计划；非计划任务返回 ok=false。
//
// 只读取持久化记录，不复查授权：调用方按计划的 RequiresExecution 决定是否要求执行授权。
func (m *Manager) ScheduleForTask(ctx context.Context, taskID int64) (Schedule, bool, error) {
	s, err := scanSchedule(m.store.db.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id=(SELECT schedule_id FROM schedule_runs WHERE task_id=?)`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, false, nil
	}
	if err != nil {
		return Schedule{}, false, err
	}
	return s, true, nil
}

func (m *Manager) checkScheduledTask(ctx context.Context, t Task) (bool, error) {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	s, scheduled, err := m.ScheduleForTask(ctx, t.ID)
	if err != nil || !scheduled {
		return false, err
	}
	if s.Status == "paused" || s.Status == "deleted" {
		return true, fmt.Errorf("计划 #%d 已%s，停止排队执行", s.ID, s.Status)
	}
	// 纯聊天计划不持有执行授权，排队进入执行前同样不复查；运行时另行裁剪工具集合。
	if s.RequiresExecution {
		if err = m.authorizeSchedule(s); err != nil {
			_, saveErr := m.store.db.ExecContext(ctx, `UPDATE schedules SET status='paused',reason=? WHERE id=?`, err.Error(), s.ID)
			return true, errors.Join(err, saveErr)
		}
	}
	if m.prepareSchedule != nil {
		req, prepareErr := m.prepareSchedule(ctx, s)
		if prepareErr != nil {
			_, saveErr := m.store.db.ExecContext(ctx, `UPDATE schedules SET status='paused',reason=? WHERE id=?`, prepareErr.Error(), s.ID)
			return true, errors.Join(prepareErr, saveErr)
		}
		data, marshalErr := json.Marshal(req)
		if marshalErr != nil {
			return true, marshalErr
		}
		if _, err = m.store.db.ExecContext(ctx, `UPDATE tasks SET request=? WHERE id=?`, data, t.ID); err != nil {
			return true, err
		}
		m.mu.Lock()
		m.transientRequests[t.ID] = req
		m.mu.Unlock()
	}
	return true, nil
}
