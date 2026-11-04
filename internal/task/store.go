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
	"strings"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/db"
)

// Task 保存来源、独立请求快照、执行终态和投递进度。
type Task struct {
	// ID 是数据库生成的稳定编号。
	ID int64
	// EventKey 是持久化去重键；定时发生与真实来源消息 ID 分开保存。
	EventKey string
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
	// Delivery 为 pending、sent 或 abandoned，仅终态任务可投递；
	// abandoned 表示重试次数用尽后放弃，结果仍保存在数据库中但不再自动投递。
	Delivery string
	// DeliveryAttempts 记录结果投递尝试次数，重启后据此判断是否已达上限。
	DeliveryAttempts int
	// DeliveryError 记录最近的投递错误。
	DeliveryError string
	// DeliveryID 是已投递的平台消息 ID。
	DeliveryID string
	// Silent 表示计划选择不向聊天投递；终态仍完整落库，可用任务命令查询。
	Silent bool
	// BatchID 是所属批次编号；非批次任务为空字符串。
	BatchID string
	// BatchIndex 是批次内从 0 开始的位置；非批次任务为 -1。
	BatchIndex int
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
	db, err := sql.Open(db.DriverName, path)
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
		silent INTEGER NOT NULL DEFAULT 0,
		batch_id TEXT NOT NULL DEFAULT '', batch_index INTEGER NOT NULL DEFAULT -1,
		UNIQUE(platform, account, room, event)
	);
	CREATE UNIQUE INDEX IF NOT EXISTS tasks_running_dir ON tasks(work_dir) WHERE status = 'running';
	CREATE INDEX IF NOT EXISTS tasks_queue ON tasks(status, id);
	CREATE TABLE IF NOT EXISTS task_batches (
		batch_id TEXT PRIMARY KEY, platform TEXT NOT NULL, account TEXT NOT NULL,
		room TEXT NOT NULL, creator TEXT NOT NULL, label TEXT NOT NULL DEFAULT '',
		total INTEGER NOT NULL, created_at INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS task_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT, task_id INTEGER NOT NULL REFERENCES tasks(id),
		created_at INTEGER NOT NULL, record BLOB NOT NULL
	);
 CREATE TABLE IF NOT EXISTS task_delivery_parts (
  task_id INTEGER NOT NULL REFERENCES tasks(id), part TEXT NOT NULL,
  payload BLOB NOT NULL, message_id TEXT NOT NULL DEFAULT '', PRIMARY KEY(task_id,part)
 );
 CREATE TABLE IF NOT EXISTS task_messages (
  task_id INTEGER NOT NULL REFERENCES tasks(id), message_id TEXT NOT NULL,
  PRIMARY KEY(task_id, message_id)
 );
 CREATE TABLE IF NOT EXISTS task_contexts (
  task_id INTEGER PRIMARY KEY REFERENCES tasks(id)
 );
 CREATE TABLE IF NOT EXISTS task_links (
  task_id INTEGER PRIMARY KEY REFERENCES tasks(id), parent_id INTEGER NOT NULL REFERENCES tasks(id)
 );
 CREATE TABLE IF NOT EXISTS session_contexts (
  platform TEXT NOT NULL, account TEXT NOT NULL, room TEXT NOT NULL, thread TEXT NOT NULL,
  generation INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(platform,account,room,thread)
 );
 CREATE TABLE IF NOT EXISTS task_generations (
  task_id INTEGER PRIMARY KEY REFERENCES tasks(id), generation INTEGER NOT NULL
 );
 CREATE TABLE IF NOT EXISTS command_receipts (
  platform TEXT NOT NULL, account TEXT NOT NULL, room TEXT NOT NULL,
  event TEXT NOT NULL, command_id TEXT NOT NULL, state TEXT NOT NULL,
  reply TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(platform,account,room,event,command_id)
 );
 CREATE TABLE IF NOT EXISTS task_pending_projections (
  task_id INTEGER PRIMARY KEY REFERENCES tasks(id),
  created_at INTEGER NOT NULL
 );
 CREATE TABLE IF NOT EXISTS task_runtime_state (
  task_id INTEGER PRIMARY KEY REFERENCES tasks(id), state BLOB, checkpoint BLOB
 );
 CREATE TABLE IF NOT EXISTS task_steering (
  id INTEGER PRIMARY KEY AUTOINCREMENT, task_id INTEGER NOT NULL REFERENCES tasks(id),
  text TEXT NOT NULL, created_at INTEGER NOT NULL, delivered_at INTEGER
 );
 CREATE INDEX IF NOT EXISTS task_steering_pending ON task_steering(task_id, delivered_at);` + scheduleSchema)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err = s.migrateColumns(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return s, nil
}

// migrateColumns 为已有数据库补齐新增列；CREATE TABLE IF NOT EXISTS 不会修改旧表结构。
//
// 表名与列定义都是本包常量：表名以单引号字面量传给 pragma_table_info，列定义直接拼进 ALTER TABLE，
// 都来自固定列表，不接受外部输入。重复执行先查 pragma，已存在则跳过。
func (s *store) migrateColumns() error {
	for _, c := range []struct{ table, column, definition string }{
		{"tasks", "silent", "INTEGER NOT NULL DEFAULT 0"},
		{"tasks", "batch_id", "TEXT NOT NULL DEFAULT ''"},
		{"tasks", "batch_index", "INTEGER NOT NULL DEFAULT -1"},
		{"schedules", "requires_execution", "INTEGER NOT NULL DEFAULT 1"},
		{"schedules", "delivery", "TEXT NOT NULL DEFAULT 'origin'"},
	} {
		var exists int
		if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('`+c.table+`') WHERE name=?)`, c.column).Scan(&exists); err != nil {
			return err
		}
		if exists == 1 {
			continue
		}
		if _, err := s.db.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.column + ` ` + c.definition); err != nil {
			return err
		}
	}
	// 批次索引依赖上面补齐的列，只能在迁移之后创建；旧库已有该索引时幂等跳过。
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS tasks_batch ON tasks(batch_id, batch_index)`); err != nil {
		return err
	}
	return nil
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

func (s *store) submit(ctx context.Context, message chat.Message, dir string, req agent.Request, parent ...int64) (Task, error) {
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	inserted, err := tx.ExecContext(ctx, `INSERT INTO tasks(platform, account, room, event, sender, work_dir, message, request, created_at)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(platform,account,room,event) DO NOTHING`,
		message.Session.Platform, message.Session.Account, message.Session.Conversation, message.ID, message.SenderID, dir, m, r, time.Now().UnixMilli())
	if err != nil {
		return Task{}, err
	}
	t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE platform=? AND account=? AND room=? AND event=?`,
		message.Session.Platform, message.Session.Account, message.Session.Conversation, message.ID))
	if err != nil {
		return Task{}, err
	}
	count, err := inserted.RowsAffected()
	if err != nil {
		return Task{}, err
	}
	if count > 0 {
		var generation int64
		err = tx.QueryRowContext(ctx, `SELECT generation FROM session_contexts WHERE platform=? AND account=? AND room=? AND thread=?`,
			message.Session.Platform, message.Session.Account, message.Session.Conversation, message.Session.Thread).Scan(&generation)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Task{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_generations(task_id,generation) VALUES(?,?)`, t.ID, generation); err != nil {
			return Task{}, err
		}
	}
	if count > 0 && len(parent) > 0 && t.ID != parent[0] {
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_links(task_id,parent_id) VALUES(?,?) ON CONFLICT(task_id) DO NOTHING`, t.ID, parent[0]); err != nil {
			return Task{}, err
		}
	}
	return t, tx.Commit()
}

const taskColumns = `id,message,work_dir,request,status,result,error,cancel_requested,delivery,delivery_attempts,delivery_error,delivery_id,created_at,event,silent,batch_id,batch_index`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var m, req, result []byte
	var created int64
	var silent int
	err := row.Scan(&t.ID, &m, &t.WorkDir, &req, &t.Status, &result, &t.Error, &t.CancelRequested, &t.Delivery, &t.DeliveryAttempts, &t.DeliveryError, &t.DeliveryID, &created, &t.EventKey, &silent, &t.BatchID, &t.BatchIndex)
	if err != nil {
		return t, err
	}
	t.Silent = silent != 0
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

// recover 标记中断任务；被隔离目录的运行行保持原状以继续锁定工作区。
func (s *store) recover(ctx context.Context, blocked []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	query := `UPDATE tasks SET status='interrupted',error='进程停止，执行结果可能包含外部副作用；请核查执行记录，不自动重放' WHERE status='running'`
	args := []any{}
	if len(blocked) > 0 {
		query += ` AND work_dir NOT IN (` + placeholders(len(blocked)) + `)`
		for _, dir := range blocked {
			args = append(args, dir)
		}
	}
	if _, err = tx.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	projection := `INSERT INTO task_pending_projections(task_id,created_at)
		SELECT id, ? FROM tasks WHERE status='interrupted'`
	projectionArgs := []any{time.Now().UnixMilli()}
	if len(blocked) > 0 {
		projection += ` AND work_dir NOT IN (` + placeholders(len(blocked)) + `)`
		for _, dir := range blocked {
			projectionArgs = append(projectionArgs, dir)
		}
	}
	projection += ` ON CONFLICT(task_id) DO NOTHING`
	if _, err = tx.ExecContext(ctx, projection, projectionArgs...); err != nil {
		return err
	}
	return tx.Commit()
}

// claim 只领取未被隔离目录的任务，避免先领取再运行无工具任务。
func (s *store) claim(ctx context.Context, blocked []string) (Task, error) {
	query := `UPDATE tasks SET status='running' WHERE id=(
		SELECT id FROM tasks q WHERE status='queued' AND NOT EXISTS (
   SELECT 1 FROM task_links l JOIN tasks p ON p.id=l.parent_id WHERE l.task_id=q.id AND p.status IN ('queued','running')
  ) AND NOT EXISTS (
			SELECT 1 FROM tasks r WHERE r.status='running' AND r.work_dir=q.work_dir
		)`
	args := []any{}
	if len(blocked) > 0 {
		query += ` AND q.work_dir NOT IN (` + placeholders(len(blocked)) + `)`
		for _, dir := range blocked {
			args = append(args, dir)
		}
	}
	query += ` ORDER BY id LIMIT 1
	) RETURNING ` + taskColumns
	return scanTask(s.db.QueryRowContext(ctx, query, args...))
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// cleanupFailed 保留 running 状态锁住目录，并记录可公开的清理失败原因。
func (s *store) cleanupFailed(ctx context.Context, taskID int64, cause error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	message := "cleanup_failed: " + cause.Error()
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET error=? WHERE id=? AND status='running'`, message, taskID); err != nil {
		return err
	}
	event, err := json.Marshal(agent.Event{Kind: agent.EnvironmentCleanupFailed, Error: errors.New(message)})
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_events(task_id,created_at,record) VALUES(?,?,?)`, taskID, time.Now().UnixMilli(), event); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *store) record(ctx context.Context, taskID int64, event agent.Event) error {
	// 增量与执行记录共用持久化游标，订阅断开不影响执行，也不会丢失文本。
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_events(task_id,created_at,record) VALUES(?,?,?)`, taskID, time.Now().UnixMilli(), data); err != nil {
		return err
	}
	if event.State != nil || event.Checkpoint != nil {
		var stateJSON, checkpointJSON []byte
		if event.State != nil {
			if stateJSON, err = json.Marshal(event.State); err != nil {
				return err
			}
		}
		if event.Checkpoint != nil {
			if checkpointJSON, err = json.Marshal(event.Checkpoint); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_runtime_state(task_id,state,checkpoint) VALUES(?,?,?)
			ON CONFLICT(task_id) DO UPDATE SET state=COALESCE(excluded.state,task_runtime_state.state),
			checkpoint=COALESCE(excluded.checkpoint,task_runtime_state.checkpoint)`, taskID, stateJSON, checkpointJSON); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// runtimeState 返回已持久化的状态与检查点；没有记录时返回 nil。
func (s *store) runtimeState(ctx context.Context, taskID int64) (*agent.TaskState, *agent.Checkpoint, error) {
	var stateJSON, checkpointJSON []byte
	err := s.db.QueryRowContext(ctx, `SELECT state,checkpoint FROM task_runtime_state WHERE task_id=?`, taskID).Scan(&stateJSON, &checkpointJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var state *agent.TaskState
	var checkpoint *agent.Checkpoint
	if len(stateJSON) > 0 {
		state = &agent.TaskState{}
		if err = json.Unmarshal(stateJSON, state); err != nil {
			return nil, nil, err
		}
	}
	if len(checkpointJSON) > 0 {
		checkpoint = &agent.Checkpoint{}
		if err = json.Unmarshal(checkpointJSON, checkpoint); err != nil {
			return nil, nil, err
		}
	}
	return state, checkpoint, nil
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET status=CASE WHEN cancel_requested=1 THEN 'cancelled' ELSE ? END,result=?,error=? WHERE id=? AND status='running'`, status, data, message, t.ID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO task_pending_projections(task_id,created_at) VALUES(?,?) ON CONFLICT(task_id) DO NOTHING`, t.ID, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		if result.State != nil {
			stateJSON, marshalErr := json.Marshal(result.State)
			if marshalErr != nil {
				return marshalErr
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO task_runtime_state(task_id,state) VALUES(?,?)
				ON CONFLICT(task_id) DO UPDATE SET state=excluded.state`, t.ID, stateJSON); err != nil {
				return err
			}
		}
		if err = finishBatchIfDone(ctx, tx, t.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const qualifiedTaskColumns = `tasks.id,tasks.message,tasks.work_dir,tasks.request,tasks.status,tasks.result,tasks.error,tasks.cancel_requested,tasks.delivery,tasks.delivery_attempts,tasks.delivery_error,tasks.delivery_id,tasks.created_at,tasks.event,tasks.silent,tasks.batch_id,tasks.batch_index`

func (s *store) pendingProjections(ctx context.Context, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+qualifiedTaskColumns+` FROM tasks JOIN task_pending_projections p ON p.task_id=tasks.id ORDER BY p.task_id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *store) acknowledgeProjection(ctx context.Context, taskID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM task_pending_projections WHERE task_id=?`, taskID)
	return err
}

func (s *store) backfillTasks(ctx context.Context, limit int, afterID int64) ([]Task, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id > ? AND status IN ('completed','failed','cancelled','interrupted') ORDER BY id ASC LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// DefaultDeliveryMaxAttempts 是结果投递的默认最大尝试次数（含首次）。
//
// 退避为 1、2、4…秒，封顶 256 秒（见 deliveryDelay）：前 9 次失败累计等待 511 秒，
// 此后每次 256 秒。20 次（19 次等待）约为 511 + 10×256 = 3071 秒（约 51 分钟）的持续故障容忍，
// 足够覆盖常见的服务端重启或短时网络中断，又不会让无法投递的任务永远占用投递循环。
const DefaultDeliveryMaxAttempts = 20

// deliveryDelay 返回第 attempts 次失败（从 0 计）之后的等待时间：2^attempts 秒，封顶 256 秒。
func deliveryDelay(attempts int) time.Duration {
	return time.Second * time.Duration(1<<min(attempts, 8))
}

// delivered 记录一次投递尝试并返回任务新的投递状态。
// 失败且累计尝试次数已达 maxAttempts 时标记为 abandoned，不再进入投递队列；
// 已持久化的分片（task_delivery_parts）不受影响。
func (s *store) delivered(ctx context.Context, taskID int64, messageID string, sendErr error, attempts, maxAttempts int) (string, error) {
	status, detail := "sent", ""
	if sendErr != nil {
		status, detail = "pending", sendErr.Error()
		if attempts+1 >= maxAttempts {
			status = "abandoned"
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET delivery=?,delivery_attempts=delivery_attempts+1,delivery_error=?,delivery_id=?,next_delivery=? WHERE id=?`, status, detail, messageID, time.Now().Add(deliveryDelay(attempts)).UnixMilli(), taskID)
	return status, err
}

func scope(session chat.Session) []any {
	return []any{session.Platform, session.Account, session.Conversation}
}

func (s *store) get(ctx context.Context, session chat.Session, taskID int64) (Task, error) {
	return scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE platform=? AND account=? AND room=? AND id=?`, append(scope(session), taskID)...))
}

func taskError(taskID int64, err error) error { return fmt.Errorf("task #%d: %w", taskID, err) }
