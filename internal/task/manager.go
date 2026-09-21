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

// SendFunc 只发送已保存的结果；重试必须使用同一个 Task.ID 作为平台幂等键。
type SendFunc func(context.Context, Task) (string, error)

// Manager 管理单个机器人进程的队列；同一数据库只允许一个 Manager 实例。
type Manager struct {
	store     *store
	run       RunFunc
	send      SendFunc
	ctx       context.Context
	cancel    context.CancelFunc
	wake      chan struct{}
	done      chan struct{}
	mu        sync.Mutex
	active    map[int64]context.CancelFunc
	workers   sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// Open 打开数据库，标记中断任务，并启动队列和结果投递。
func Open(path string, run RunFunc, send SendFunc) (*Manager, error) {
	if run == nil || send == nil {
		return nil, errors.New("task manager requires runner and sender")
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
	m := &Manager{store: s, run: run, send: send, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), active: make(map[int64]context.CancelFunc)}
	go m.loop()
	return m, nil
}

// Submit 原子去重并持久化请求，返回后即可回复接收编号。
func (m *Manager) Submit(ctx context.Context, message chat.Message, dir string, req agent.Request) (Task, error) {
	if err := m.ctx.Err(); err != nil {
		return Task{}, err
	}
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

// Cancel 只允许发起人取消；运行中任务退出前不会释放工作目录。
func (m *Manager) Cancel(ctx context.Context, identity chat.Identity, taskID int64) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.Get(ctx, identity.Session, taskID)
	if err != nil {
		return Task{}, err
	}
	if t.Message.SenderID != identity.SenderID {
		return Task{}, errors.New("只能取消自己发起的任务")
	}
	_, err = m.store.db.ExecContext(ctx, `UPDATE tasks SET cancel_requested=1,status=CASE WHEN status='queued' THEN 'cancelled' ELSE status END WHERE id=? AND status IN ('queued','running')`, taskID)
	if err != nil {
		return Task{}, err
	}
	if cancel := m.active[taskID]; cancel != nil {
		cancel()
	}
	m.notify()
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
	ctx = chat.WithIdentity(ctx, chat.Identity{Session: t.Message.Session, SenderID: t.Message.SenderID})
	ctx = context.WithValue(ctx, workDirKey{}, t.WorkDir)
	var journalErr error
	result, runErr := m.run(ctx, t.Request, func(event agent.Event) {
		if journalErr != nil {
			return
		}
		journalErr = m.store.record(context.WithoutCancel(ctx), t.ID, event)
		// ToolStarted 持久化失败时取消，Runtime 在派发工具前再次检查 ctx。
		if journalErr != nil {
			cancel()
		}
	})
	if journalErr != nil {
		runErr = errors.Join(runErr, journalErr)
	}
	if err := m.store.finish(context.Background(), t, result, runErr, m.ctx.Err() != nil); err != nil {
		// 保持 running 以锁住目录；重启后转 interrupted，绝不重跑。
		slog.Error("保存任务终态失败，需要人工核查", "error", taskError(t.ID, err))
	}
}

func (m *Manager) deliverLoop() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
		}
		tasks, err := m.store.query(m.ctx, `status NOT IN ('queued','running') AND delivery='pending' AND next_delivery<=? ORDER BY next_delivery,id LIMIT 20`, time.Now().UnixMilli())
		if err != nil {
			if m.ctx.Err() == nil {
				slog.Error("读取待投递任务失败", "error", err)
			}
			continue
		}
		for _, t := range tasks {
			if m.ctx.Err() != nil {
				return
			}
			ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
			messageID, sendErr := m.send(ctx, t)
			cancel()
			if err := m.store.delivered(context.Background(), t.ID, messageID, sendErr, t.DeliveryAttempts); err != nil {
				slog.Error("保存任务投递状态失败", "error", taskError(t.ID, err))
			}
		}
	}
}

// Close 停止派发并等待运行退出；排队任务留待下次启动。
func (m *Manager) Close() error {
	m.closeOnce.Do(func() { m.cancel(); <-m.done; m.closeErr = m.store.db.Close() })
	return m.closeErr
}

type workDirKey struct{}

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
