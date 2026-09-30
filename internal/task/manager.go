package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// RunFunc 使用持久化请求运行 Agent；事件回调必须在返回前串行调用。
type RunFunc func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error)

// SendFunc 只发送已保存的结果；各上传和发送步骤须独立限时并使用稳定平台幂等键。
type SendFunc func(context.Context, Task) (string, error)

// Options 在启动工作线程前提供权限和上下文预算。
type Options struct {
	Schedule     ScheduleAuthorize
	Manage       func(chat.Identity) bool
	Context      agent.ContextPolicy
	OnProjection func()
	// DeliveryMaxAttempts 是结果投递的最大尝试次数（含首次），用尽后任务的投递状态变为 abandoned；
	// 零值使用 DefaultDeliveryMaxAttempts。
	DeliveryMaxAttempts int
}

// Manager 管理单个机器人进程的队列；同一数据库只允许一个 Manager 实例。
type Manager struct {
	store         *store
	run           RunFunc
	deliveries    map[string]SendFunc
	ctx           context.Context
	cancel        context.CancelFunc
	wake          chan struct{}
	done          chan struct{}
	mu            sync.Mutex
	active        map[int64]context.CancelFunc
	workers       sync.WaitGroup
	closeOnce     sync.Once
	closeErr      error
	scheduleMu    sync.Mutex
	authorize     ScheduleAuthorize
	manage        func(chat.Identity) bool
	contextPolicy agent.ContextPolicy
	onProjection  func()
	// deliveryMaxAttempts 是结果投递的最大尝试次数，恒为正数。
	deliveryMaxAttempts int
}

// Open 打开数据库，标记中断任务，并启动队列和结果投递。
func Open(path string, run RunFunc, send SendFunc, options ...Options) (*Manager, error) {
	if run == nil {
		return nil, errors.New("task manager requires runner")
	}
	s, err := openStore(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err = s.recover(ctx); err != nil {
		cancel()
		return nil, errors.Join(err, s.db.Close())
	}
	m := &Manager{store: s, run: run, deliveries: make(map[string]SendFunc), ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), active: make(map[int64]context.CancelFunc)}
	m.deliveryMaxAttempts = DefaultDeliveryMaxAttempts
	if len(options) > 0 {
		if options[0].DeliveryMaxAttempts > 0 {
			m.deliveryMaxAttempts = options[0].DeliveryMaxAttempts
		}
		m.authorize = options[0].Schedule
		m.manage = options[0].Manage
		m.contextPolicy = options[0].Context
		m.onProjection = options[0].OnProjection
	}
	if send != nil {
		m.deliveries["*"] = send
	}
	if m.onProjection != nil {
		go m.onProjection()
	}
	go m.loop()
	return m, nil
}

// Submit 原子去重并持久化请求，返回后即可回复接收编号。
func (m *Manager) Submit(ctx context.Context, message chat.Message, dir string, req agent.Request) (Task, error) {
	if err := m.ctx.Err(); err != nil {
		return Task{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.store.submit(ctx, message, dir, req)
	m.notify()
	return t, err
}

// List 返回当前群最近的 20 条任务，线程不限制查询范围。
func (m *Manager) List(ctx context.Context, session chat.Session) ([]Task, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	return m.store.query(ctx, `platform=? AND account=? AND room=? ORDER BY id DESC LIMIT 20`, scope(session)...)
}

// Get 仅允许从任务所属账号和群查询，避免猜编号读取其他群的输入或结果。
func (m *Manager) Get(ctx context.Context, session chat.Session, taskID int64) (Task, error) {
	if err := session.Validate(); err != nil {
		return Task{}, err
	}
	return m.store.get(ctx, session, taskID)
}

// Cancel 只允许发起人或同群授权管理员取消；运行中任务退出前不会释放工作目录。
func (m *Manager) Cancel(ctx context.Context, identity chat.Identity, taskID int64) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.Get(ctx, identity.Session, taskID)
	if err != nil {
		return Task{}, err
	}
	if !m.CanManage(identity, t.Message.SenderID) {
		return Task{}, errors.New("只有发起人或本群任务管理员可以取消任务")
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET cancel_requested=1,status=CASE WHEN status='queued' THEN 'cancelled' ELSE status END WHERE id=? AND status IN ('queued','running')`, taskID)
	if err != nil {
		return Task{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return Task{}, err
	}
	if affected > 0 {
		var finalStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, taskID).Scan(&finalStatus); err == nil && finalStatus == "cancelled" {
			_, _ = tx.ExecContext(ctx, `INSERT INTO task_pending_projections(task_id,created_at) VALUES(?,?) ON CONFLICT(task_id) DO NOTHING`, taskID, time.Now().UnixMilli())
		}
	}
	if err := tx.Commit(); err != nil {
		return Task{}, err
	}
	if cancel := m.active[taskID]; cancel != nil {
		cancel()
	}
	m.notify()
	if m.onProjection != nil {
		go m.onProjection()
	}
	return m.Get(ctx, identity.Session, taskID)
}

// Events 返回同群可见的持久化执行记录，包括崩溃前已派发的工具。
func (m *Manager) Events(ctx context.Context, session chat.Session, taskID int64) (records []string, err error) {
	if _, err := m.Get(ctx, session, taskID); err != nil {
		return nil, err
	}
	rows, err := m.store.db.QueryContext(ctx, `SELECT record FROM task_events WHERE task_id=? ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var record string
		if err := rows.Scan(&record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (m *Manager) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) loop() {
	defer close(m.done)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	// 发送与运行分离，网络阻塞不会占用执行工作槽。
	deliveryDone := make(chan struct{})
	go func() { defer close(deliveryDone); m.deliverLoop() }()
	defer func() { m.workers.Wait(); <-deliveryDone }()
	for {
		if m.ctx.Err() != nil {
			return
		}
		if err := m.tickSchedules(m.ctx, time.Now()); err != nil && m.ctx.Err() == nil {
			slog.Error("调度定时任务失败", "error", err)
		}
		m.dispatch()
		select {
		case <-m.ctx.Done():
			return
		case <-m.wake:
		case <-ticker.C:
		}
	}
}

func (m *Manager) dispatch() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.active) < 4 && m.ctx.Err() == nil {
		t, err := m.store.claim(m.ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		if err != nil {
			slog.Error("领取任务失败", "error", err)
			return
		}
		ctx, cancel := context.WithCancel(m.ctx)
		m.active[t.ID] = cancel
		m.workers.Add(1)
		go m.execute(ctx, cancel, t)
	}
}

func (m *Manager) execute(ctx context.Context, cancel context.CancelFunc, t Task) {
	defer m.workers.Done()
	defer cancel()
	defer func() { m.mu.Lock(); delete(m.active, t.ID); m.mu.Unlock(); m.notify() }()
	ctx = chat.WithIdentity(ctx, chat.Identity{Session: t.Message.Session, SenderID: t.Message.SenderID, Direct: t.Message.Direct})
	ctx = context.WithValue(ctx, workDirKey{}, t.WorkDir)
	ctx = context.WithValue(ctx, taskIDKey{}, t.ID)
	scheduled, err := m.checkScheduledTask(ctx, t)
	if err != nil {
		if finishErr := m.store.finish(context.Background(), t, agent.Result{}, err, m.ctx.Err() != nil); finishErr != nil {
			slog.Error("保存计划任务拒绝状态失败", "error", finishErr)
		}
		return
	}
	if scheduled {
		ctx = withScheduled(ctx)
	}
	var journalErr error
	req, resumeErr := m.continuationRequest(ctx, t)
	result, runErr := agent.Result{}, resumeErr
	if resumeErr == nil {
		result, runErr = m.run(ctx, req, func(event agent.Event) {
			if journalErr != nil {
				return
			}
			journalErr = m.store.record(context.WithoutCancel(ctx), t.ID, event)
			// ToolStarted 持久化失败时取消，Runtime 在派发工具前再次检查 ctx。
			if journalErr != nil {
				cancel()
			}
		})
	}
	if journalErr != nil {
		runErr = errors.Join(runErr, journalErr)
	}
	if err := m.store.finish(context.Background(), t, result, runErr, m.ctx.Err() != nil); err != nil {
		// 保持 running 以锁住目录；重启后转 interrupted，绝不重跑。
		slog.Error("保存任务终态失败，需要人工核查", "error", taskError(t.ID, err))
	} else if m.onProjection != nil {
		go m.onProjection()
	}
}

// Close 停止派发并等待运行退出；排队任务留待下次启动。
func (m *Manager) Close() error {
	m.closeOnce.Do(func() { m.cancel(); <-m.done; m.closeErr = m.store.db.Close() })
	return m.closeErr
}

type workDirKey struct{}
type taskIDKey struct{}
type scheduledKey struct{}

// withScheduled 标记本次运行由定时计划触发，读取个人记忆前必须重新验证接收场景。
func withScheduled(ctx context.Context) context.Context {
	return context.WithValue(ctx, scheduledKey{}, true)
}

// Scheduled 表示当前任务由定时计划触发，而不是当前的实时聊天轮次。
func Scheduled(ctx context.Context) bool {
	ok, _ := ctx.Value(scheduledKey{}).(bool)
	return ok
}

// ID 返回当前持久化任务编号，供执行器关联日志和文件归档。
func ID(ctx context.Context) int64 { id, _ := ctx.Value(taskIDKey{}).(int64); return id }

// WorkDir 从执行上下文读取工作目录；工具不得通过修改进程 cwd 切换目录。
func WorkDir(ctx context.Context) string { dir, _ := ctx.Value(workDirKey{}).(string); return dir }

// Report 返回可重复发送的固定结果正文，始终标明原任务编号。
func Report(t Task) string {
	text := fmt.Sprintf("任务 #%d：%s", t.ID, t.Status)
	if t.Status == "completed" {
		return text + "\n" + t.Result.Content
	}
	return text + "\n" + t.Error
}

// CanManage 检查发起人或受信配置中的管理员，不授予工作区执行权限。
func (m *Manager) CanManage(identity chat.Identity, owner string) bool {
	return identity.SenderID != "" && (identity.SenderID == owner || (m.manage != nil && m.manage(identity)))
}

// PendingProjections 返回待处理历史投影的任务列表。
func (m *Manager) PendingProjections(ctx context.Context, limit int) ([]Task, error) {
	return m.store.pendingProjections(ctx, limit)
}

// AcknowledgeProjection 确认指定任务的历史投影已完成同步。
func (m *Manager) AcknowledgeProjection(ctx context.Context, taskID int64) error {
	return m.store.acknowledgeProjection(ctx, taskID)
}

// BackfillTasks 分批读取已达终态的历史任务用于回填索引。
func (m *Manager) BackfillTasks(ctx context.Context, limit int, afterID int64) ([]Task, error) {
	return m.store.backfillTasks(ctx, limit, afterID)
}
