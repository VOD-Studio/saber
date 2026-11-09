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
	"rua.plus/saber/internal/config"
)

// RunFunc 使用持久化请求运行 Agent；事件回调必须在返回前串行调用。
type RunFunc func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error)

// SendFunc 只发送已保存的结果；各上传和发送步骤须独立限时并使用稳定平台幂等键。
type SendFunc func(context.Context, Task) (string, error)

const (
	// DefaultMaxConcurrent 是未配置时同时执行的任务数上限。
	DefaultMaxConcurrent = 4
	// MaxConcurrentLimit 是并发任务数的硬上限，避免误配置把工作区互斥放大成资源耗尽。
	MaxConcurrentLimit = 64
)

// normalizeMaxConcurrent 把配置值收敛到 1..MaxConcurrentLimit；零值取默认值。
func normalizeMaxConcurrent(n int) int {
	if n == 0 {
		return DefaultMaxConcurrent
	}
	return min(max(n, 1), MaxConcurrentLimit)
}

// Options 在启动工作线程前提供权限和上下文预算。
type Options struct {
	Schedule     ScheduleAuthorize
	Manage       func(chat.Identity) bool
	Context      agent.ContextPolicy
	OnProjection func()
	// Finalize 在保存终态前清理任务环境；失败时不释放工作目录。
	Finalize func(context.Context, int64) error
	// BlockedDirectories 返回因清理失败被隔离的工作区快照。
	BlockedDirectories func() []string
	// DeliveryMaxAttempts 是结果投递的最大尝试次数（含首次），用尽后任务的投递状态变为 abandoned；
	// 零值使用 DefaultDeliveryMaxAttempts。
	DeliveryMaxAttempts int
	// MaxConcurrent 是同时执行的任务数上限；零值使用 DefaultMaxConcurrent，超出范围时收敛到 1..MaxConcurrentLimit。
	// 工作目录互斥仍然生效：不同目录的任务才能并行到这个上限。
	MaxConcurrent int
	// Control 限制任务图和澄清请求；零值使用安全默认配置。
	Control config.ControlConfig
	// Authorize 在控制任务创建、恢复和派发时复核当前工作区授权。
	Authorize ScheduleAuthorize
	// AuthorizeTask 在任务编号已确定后复核绑定的派生工作区，不能由身份参数替代。
	AuthorizeTask func(context.Context, Task) error
	// PrepareSchedule 在实际执行前重新加载计划技能、模型与目的地授权。
	PrepareSchedule func(context.Context, Schedule) (agent.Request, error)
	// Handoff 只解析可信配置中的交接路由，不接受模型传入身份。
	Handoff HandoffResolver
	// TransferProcesses 在正常完成前把已授权续接的进程切换到独立目标生命周期。
	TransferProcesses func(context.Context, context.Context) error
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
	deliveryMu    sync.Mutex
	active        map[int64]context.CancelFunc
	runContexts   map[int64]context.Context
	workers       sync.WaitGroup
	closeOnce     sync.Once
	closeErr      error
	scheduleMu    sync.Mutex
	authorize     ScheduleAuthorize
	manage        func(chat.Identity) bool
	contextPolicy agent.ContextPolicy
	onProjection  func()
	finalize      func(context.Context, int64) error
	blocked       func() []string
	// deliveryMaxAttempts 是结果投递的最大尝试次数，恒为正数。
	deliveryMaxAttempts int
	// maxConcurrent 是同时执行的任务数，恒在 1..MaxConcurrentLimit。
	maxConcurrent     int
	control           config.ControlConfig
	controlAuthorize  ScheduleAuthorize
	authorizeTask     func(context.Context, Task) error
	prepareSchedule   func(context.Context, Schedule) (agent.Request, error)
	handoff           HandoffResolver
	transientRequests map[int64]agent.Request
	childFinalizers   map[int64]func() error
	transferProcesses func(context.Context, context.Context) error
	pendingContexts   map[int64]context.Context
	pendingCancels    map[int64]context.CancelFunc
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
	m := &Manager{store: s, run: run, deliveries: make(map[string]SendFunc), ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), active: make(map[int64]context.CancelFunc), transientRequests: make(map[int64]agent.Request), childFinalizers: make(map[int64]func() error), control: config.DefaultControlConfig()}
	m.runContexts = make(map[int64]context.Context)
	m.pendingContexts = make(map[int64]context.Context)
	m.pendingCancels = make(map[int64]context.CancelFunc)
	m.deliveryMaxAttempts = DefaultDeliveryMaxAttempts
	m.maxConcurrent = DefaultMaxConcurrent
	if len(options) > 0 {
		if options[0].DeliveryMaxAttempts > 0 {
			m.deliveryMaxAttempts = options[0].DeliveryMaxAttempts
		}
		m.maxConcurrent = normalizeMaxConcurrent(options[0].MaxConcurrent)
		m.authorize = options[0].Schedule
		m.manage = options[0].Manage
		m.contextPolicy = options[0].Context
		m.onProjection = options[0].OnProjection
		m.finalize = options[0].Finalize
		m.blocked = options[0].BlockedDirectories
		if options[0].Control != (config.ControlConfig{}) {
			m.control = options[0].Control
		}
		m.controlAuthorize = options[0].Authorize
		m.authorizeTask = options[0].AuthorizeTask
		if m.controlAuthorize == nil {
			m.controlAuthorize = options[0].Schedule
		}
		m.prepareSchedule = options[0].PrepareSchedule
		m.handoff = options[0].Handoff
		m.transferProcesses = options[0].TransferProcesses
	}
	if err := m.control.Validate(); err != nil {
		cancel()
		return nil, errors.Join(err, s.db.Close())
	}
	var blocked []string
	if m.blocked != nil {
		blocked = m.blocked()
	}
	if err = s.recover(ctx, blocked); err != nil {
		cancel()
		return nil, errors.Join(err, s.db.Close())
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
	m.deliveryMu.Lock()
	defer m.deliveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.Get(ctx, identity.Session, taskID)
	if err != nil {
		return Task{}, err
	}
	if !m.CanManage(identity, t.Message.SenderID) {
		return Task{}, errors.New("只有发起人或本群任务管理员可以取消任务")
	}
	var external bool
	if err := m.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_external WHERE task_id=?)`, taskID).Scan(&external); err != nil {
		return Task{}, err
	}
	if external {
		if _, err := m.store.db.ExecContext(ctx, `UPDATE tasks SET cancel_requested=1 WHERE id=? AND status='running'`, taskID); err != nil {
			return Task{}, err
		}
		return m.finishExternalLocked(ctx, t, "cancelled", "用户取消外部操作")
	}
	if m.active[taskID] == nil {
		if m.pendingContexts[taskID] != nil && m.finalize != nil {
			if cleanupErr := m.finalize(context.Background(), taskID); cleanupErr != nil {
				_, saveErr := m.store.db.ExecContext(ctx, `UPDATE tasks SET status='running',error=? WHERE id=?`, "cleanup_failed: "+cleanupErr.Error(), taskID)
				return Task{}, errors.Join(cleanupErr, saveErr)
			}
		}
		if cleanup := m.childFinalizers[taskID]; cleanup != nil {
			if cleanupErr := cleanup(); cleanupErr != nil {
				_, saveErr := m.store.db.ExecContext(ctx, `UPDATE tasks SET status='running',error=? WHERE id=?`, "cleanup_failed: "+cleanupErr.Error(), taskID)
				return Task{}, errors.Join(cleanupErr, saveErr)
			}
			delete(m.childFinalizers, taskID)
			delete(m.transientRequests, taskID)
		}
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET cancel_requested=1,status=CASE WHEN status IN ('queued','blocked','review','waiting_input') THEN 'cancelled' ELSE status END WHERE id=? AND status IN ('queued','running','blocked','review','waiting_input')`, taskID)
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
			// 排队中的任务被直接取消，不会再走 finish；批次可能在这次取消后全部终态。
			if err := finishBatchIfDone(ctx, tx, taskID); err != nil {
				return Task{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Task{}, err
	}
	if cancel := m.active[taskID]; cancel != nil {
		cancel()
	}
	if cancel := m.pendingCancels[taskID]; cancel != nil {
		cancel()
		delete(m.pendingContexts, taskID)
		delete(m.pendingCancels, taskID)
	}
	if _, err := m.store.db.ExecContext(ctx, `UPDATE task_questions SET status='cancelled',answered_at=? WHERE task_id=? AND status='pending'`, time.Now().UnixMilli(), taskID); err != nil {
		return Task{}, err
	}
	m.notify()
	if m.onProjection != nil {
		go m.onProjection()
	}
	return m.Get(ctx, identity.Session, taskID)
}

// AuthorizeOutput 校验输出属于当前任务的续接祖先或真实委派树，并要求调用者为源任务发起人或本群任务管理员。
// 不接受仅凭相同房间读取其他成员任务日志。
func (m *Manager) AuthorizeOutput(ctx context.Context, identity chat.Identity, currentID, sourceID int64) error {
	if currentID <= 0 || sourceID <= 0 {
		return errors.New("output access requires a persistent task")
	}
	if _, err := m.Get(ctx, identity.Session, currentID); err != nil {
		return err
	}
	var ancestor bool
	err := m.store.db.QueryRowContext(ctx, `WITH RECURSIVE edges(task_id,parent_id) AS (SELECT task_id,parent_id FROM task_links UNION ALL SELECT task_id,parent_id FROM task_children), ancestors(id) AS (SELECT ? UNION SELECT e.parent_id FROM edges e JOIN ancestors a ON e.task_id=a.id), descendants(id) AS (SELECT ? UNION SELECT c.task_id FROM task_children c JOIN descendants d ON c.parent_id=d.id) SELECT EXISTS(SELECT 1 FROM ancestors WHERE id=? UNION SELECT 1 FROM descendants WHERE id=?)`, currentID, currentID, sourceID, sourceID).Scan(&ancestor)
	if err != nil {
		return err
	}
	if !ancestor {
		return errors.New("output belongs to an unrelated task")
	}
	source, err := m.Get(ctx, identity.Session, sourceID)
	if err != nil {
		return err
	}
	if !m.CanManage(identity, source.Message.SenderID) {
		return errors.New("只有源任务发起人或本群任务管理员可以读取该输出")
	}
	return nil
}

// EventRecord 是一条持久化执行记录及其写入时间。
//
// 时间取 task_events.created_at，与记录本身分开保存，导出时才能保留事件先后顺序。
type EventRecord struct {
	// At 是事件写入时间，精度到毫秒。
	At time.Time
	// Record 是原始事件 JSON，格式由 agent.Event 决定，未知追加类型原样保留。
	Record string
}

// Events 返回同群可见的持久化执行记录，包括崩溃前已派发的工具。
func (m *Manager) Events(ctx context.Context, session chat.Session, taskID int64) ([]string, error) {
	records, err := m.TraceRecords(ctx, session, taskID)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(records))
	for _, record := range records {
		result = append(result, record.Record)
	}
	return result, nil
}

// TraceRecords 返回任务全部执行记录及写入时间，供轨迹导出保留时序。
//
// 可见性规则与 Events 相同：只按任务所属账号与会话查询，不接受跨会话编号。
func (m *Manager) TraceRecords(ctx context.Context, session chat.Session, taskID int64) (records []EventRecord, err error) {
	if _, err := m.Get(ctx, session, taskID); err != nil {
		return nil, err
	}
	rows, err := m.store.db.QueryContext(ctx, `SELECT created_at, record FROM task_events WHERE task_id=? ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var createdAt int64
		var record string
		if err := rows.Scan(&createdAt, &record); err != nil {
			return nil, err
		}
		records = append(records, EventRecord{At: time.UnixMilli(createdAt), Record: record})
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
		if err := m.tickControl(m.ctx); err != nil && m.ctx.Err() == nil {
			slog.Error("推进任务控制状态失败", "error", err)
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
	var blocked []string
	if m.blocked != nil {
		blocked = m.blocked()
	}
	for len(m.active) < m.maxConcurrent && m.ctx.Err() == nil {
		t, err := m.store.claim(m.ctx, blocked)
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		if err != nil {
			slog.Error("领取任务失败", "error", err)
			return
		}
		ctx, cancel := m.pendingContexts[t.ID], m.pendingCancels[t.ID]
		if ctx == nil {
			ctx, cancel = context.WithCancel(m.ctx)
		}
		delete(m.pendingContexts, t.ID)
		delete(m.pendingCancels, t.ID)
		m.active[t.ID] = cancel
		m.runContexts[t.ID] = ctx
		m.workers.Add(1)
		if transient, ok := m.transientRequests[t.ID]; ok {
			t.Request = mergeTransientRequest(t.Request, transient)
		}
		go m.execute(ctx, cancel, t)
	}
}

func (m *Manager) execute(ctx context.Context, cancel context.CancelFunc, t Task) {
	defer m.workers.Done()
	defer cancel()
	defer func() { m.mu.Lock(); delete(m.active, t.ID); delete(m.runContexts, t.ID); m.mu.Unlock(); m.notify() }()
	ctx = chat.WithIdentity(ctx, chat.Identity{Session: t.Message.Session, SenderID: t.Message.SenderID, Direct: t.Message.Direct})
	ctx = context.WithValue(ctx, workDirKey{}, t.WorkDir)
	ctx = context.WithValue(ctx, taskIDKey{}, t.ID)
	preflightErr := m.checkControlledTask(ctx, t)
	scheduled := false
	if preflightErr == nil {
		scheduled, preflightErr = m.checkScheduledTask(ctx, t)
	}
	if scheduled {
		ctx = withScheduled(ctx)
		if preflightErr == nil && m.prepareSchedule != nil {
			t, preflightErr = m.Get(ctx, t.Message.Session, t.ID)
		}
	}
	if preflightErr == nil {
		m.mu.Lock()
		if transient, ok := m.transientRequests[t.ID]; ok {
			t.Request = mergeTransientRequest(t.Request, transient)
		}
		m.mu.Unlock()
	}
	var journalErr error
	req, resumeErr := t.Request, preflightErr
	if resumeErr == nil {
		req, resumeErr = m.continuationRequest(ctx, t)
	}
	if resumeErr == nil {
		state, stateErr := m.initialTaskState(ctx, t)
		if stateErr != nil {
			resumeErr = stateErr
		} else {
			req.State = &state
			if err := m.markStateInitialized(context.WithoutCancel(ctx), t.ID, state); err != nil {
				resumeErr = err
			}
		}
	}
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
	if runErr == nil && result.Status == agent.Completed && m.ctx.Err() == nil {
		if transferErr := m.transferContinuationProcesses(ctx, t); transferErr != nil {
			if recordErr := m.store.cleanupFailed(context.Background(), t.ID, transferErr); recordErr != nil {
				slog.Error("保存进程移交失败状态失败", "error", recordErr)
			}
			return
		}
	}
	if cleanupErr := m.finalizeChild(t.ID); cleanupErr != nil {
		if recordErr := m.store.cleanupFailed(context.Background(), t.ID, cleanupErr); recordErr != nil {
			slog.Error("保存子任务清理失败状态失败", "error", recordErr)
		}
		return
	}
	if m.finalize != nil {
		if cleanupErr := m.finalize(context.Background(), t.ID); cleanupErr != nil {
			// 保持 running 锁住目录；不宣布 cancelled/completed，等待人工核查。
			if recordErr := m.store.cleanupFailed(context.Background(), t.ID, cleanupErr); recordErr != nil {
				slog.Error("保存环境清理失败状态失败，需要人工核查", "error", taskError(t.ID, recordErr))
			}
			return
		}
	}
	m.deliveryMu.Lock()
	finishErr := m.store.finish(context.Background(), t, result, runErr, m.ctx.Err() != nil)
	m.deliveryMu.Unlock()
	if finishErr != nil {
		// 保持 running 以锁住目录；重启后转 interrupted，绝不重跑。
		slog.Error("保存任务终态失败，需要人工核查", "error", taskError(t.ID, finishErr))
	} else if m.onProjection != nil {
		go m.onProjection()
	}
}

// Close 停止派发并等待运行退出；排队任务留待下次启动。
func (m *Manager) Close() error {
	m.closeOnce.Do(func() { m.mu.Lock(); m.cancel(); m.mu.Unlock(); <-m.done; m.closeErr = m.store.db.Close() })
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
