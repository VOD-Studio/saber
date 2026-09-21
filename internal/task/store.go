// Package task 提供聊天任务的持久化、后台执行和独立结果投递。
package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	_ "rua.plus/saber/internal/db"
)

// Task 保存来源、独立请求快照、执行终态和投递进度。
type Task struct {
	// ID 是数据库生成的稳定编号。
	ID int64
	// Message 保留发起人、群、原消息、引用及话题。
	Message chat.Message
	// WorkDir 是规范化的工作目录，也是互斥执行键。
	WorkDir string
	// Request 是接收时冻结的模型请求，不读取共享聊天历史。
	Request agent.Request
	// Status 为 queued、running、completed、failed、cancelled 或 interrupted。
	Status string
	// Result 保留完整运行轨迹，失败时也保存部分结果。
	Result agent.Result
	// Error 记录终止原因。
	Error string
	// CancelRequested 表示用户已请求取消，执行退出前仍持有目录锁。
	CancelRequested bool
	// Delivery 为 pending 或 sent，仅终态任务可投递。
	Delivery string
	// DeliveryAttempts 记录结果投递尝试次数。
	DeliveryAttempts int
	// DeliveryError 记录最近的投递错误。
	DeliveryError string
	// DeliveryID 是已投递的平台消息 ID。
	DeliveryID string
	// CreatedAt 是接收时间。
	CreatedAt time.Time
}

type store struct{ db *sql.DB }

func openStore(path string) (*store, error) {
	// 先以私有权限创建文件，SQLite 的 WAL/SHM 会继承数据库权限。
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3-fk-wal", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &store{db: db}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		platform TEXT NOT NULL, account TEXT NOT NULL, room TEXT NOT NULL, event TEXT NOT NULL,
		sender TEXT NOT NULL, work_dir TEXT NOT NULL, message BLOB NOT NULL, request BLOB NOT NULL,
		status TEXT NOT NULL DEFAULT 'queued', result BLOB NOT NULL DEFAULT '{}', error TEXT NOT NULL DEFAULT '',
		cancel_requested INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL,
		delivery TEXT NOT NULL DEFAULT 'pending', delivery_attempts INTEGER NOT NULL DEFAULT 0,
		delivery_error TEXT NOT NULL DEFAULT '', delivery_id TEXT NOT NULL DEFAULT '', next_delivery INTEGER NOT NULL DEFAULT 0,
		UNIQUE(platform, account, room, event)
	);
	CREATE UNIQUE INDEX IF NOT EXISTS tasks_running_dir ON tasks(work_dir) WHERE status = 'running';
	CREATE INDEX IF NOT EXISTS tasks_queue ON tasks(status, id);
	CREATE TABLE IF NOT EXISTS task_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT, task_id INTEGER NOT NULL REFERENCES tasks(id),
		created_at INTEGER NOT NULL, record BLOB NOT NULL
	);`)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return s, nil
}

func canonicalDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("task work directory is not a directory")
	}
	return abs, nil
}

func (s *store) submit(ctx context.Context, message chat.Message, dir string, req agent.Request) (Task, error) {
	if err := message.Validate(); err != nil {
		return Task{}, err
	}
	if message.ID == "" {
		return Task{}, errors.New("task requires a source message ID")
	}
	dir, err := canonicalDir(dir)
	if err != nil {
		return Task{}, err
	}
	m, err := json.Marshal(message)
	if err != nil {
		return Task{}, err
	}
	r, err := json.Marshal(req)
	if err != nil {
		return Task{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO tasks(platform, account, room, event, sender, work_dir, message, request, created_at)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(platform,account,room,event) DO NOTHING`,
		message.Session.Platform, message.Session.Account, message.Session.Conversation, message.ID, message.SenderID, dir, m, r, time.Now().UnixMilli())
	if err != nil {
		return Task{}, err
	}
	return scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE platform=? AND account=? AND room=? AND event=?`,
		message.Session.Platform, message.Session.Account, message.Session.Conversation, message.ID))
}

const taskColumns = `id,message,work_dir,request,status,result,error,cancel_requested,delivery,delivery_attempts,delivery_error,delivery_id,created_at`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var m, req, result []byte
	var created int64
	err := row.Scan(&t.ID, &m, &t.WorkDir, &req, &t.Status, &result, &t.Error, &t.CancelRequested, &t.Delivery, &t.DeliveryAttempts, &t.DeliveryError, &t.DeliveryID, &created)
	if err != nil {
		return t, err
	}
	t.CreatedAt = time.UnixMilli(created)
	return t, errors.Join(json.Unmarshal(m, &t.Message), json.Unmarshal(req, &t.Request), json.Unmarshal(result, &t.Result))
}

func (s *store) query(ctx context.Context, where string, args ...any) (tasks []Task, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *store) recover(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status='interrupted',error='进程停止，执行结果可能包含外部副作用；请核查执行记录，不自动重放' WHERE status='running'`)
	return err
}

func (s *store) claim(ctx context.Context) (Task, error) {
	return scanTask(s.db.QueryRowContext(ctx, `UPDATE tasks SET status='running' WHERE id=(
		SELECT id FROM tasks q WHERE status='queued' AND NOT EXISTS (
			SELECT 1 FROM tasks r WHERE r.status='running' AND r.work_dir=q.work_dir
		) ORDER BY id LIMIT 1
	) RETURNING `+taskColumns))
}

func (s *store) record(ctx context.Context, taskID int64, event agent.Event) error {
	// 文本增量不单独落盘；完整模型响应及工具开始/结束事件足以核查副作用。
	if event.Kind == agent.TextDelta {
		return nil
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO task_events(task_id,created_at,record) VALUES(?,?,?)`, taskID, time.Now().UnixMilli(), data)
	return err
}

func (s *store) finish(ctx context.Context, t Task, result agent.Result, runErr error, interrupted bool) error {
	status := "failed"
	if runErr == nil && result.Status == agent.Completed {
		status = "completed"
	}
	if result.Status == agent.Cancelled {
		status = "cancelled"
	}
	if interrupted {
		status = "interrupted"
	}
	message := ""
	if runErr != nil {
		message = runErr.Error()
	}
	if interrupted {
		message = "进程停止，未完成的执行不会自动重放；请核查外部副作用"
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE tasks SET status=CASE WHEN cancel_requested=1 THEN 'cancelled' ELSE ? END,result=?,error=? WHERE id=? AND status='running'`, status, data, message, t.ID)
	return err
}

func (s *store) delivered(ctx context.Context, taskID int64, messageID string, sendErr error, attempts int) error {
	status, detail := "sent", ""
	if sendErr != nil {
		status, detail = "pending", sendErr.Error()
	}
	delay := time.Second * time.Duration(1<<min(attempts, 8))
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET delivery=?,delivery_attempts=delivery_attempts+1,delivery_error=?,delivery_id=?,next_delivery=? WHERE id=?`, status, detail, messageID, time.Now().Add(delay).UnixMilli(), taskID)
	return err
}

func scope(session chat.Session) []any {
	return []any{session.Platform, session.Account, session.Conversation}
}

func (s *store) get(ctx context.Context, session chat.Session, taskID int64) (Task, error) {
	return scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE platform=? AND account=? AND room=? AND id=?`, append(scope(session), taskID)...))
}

func taskError(taskID int64, err error) error { return fmt.Errorf("task #%d: %w", taskID, err) }
