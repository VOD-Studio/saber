package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"rua.plus/saber/internal/chat"
)

const (
	// MaxSteeringTextBytes 是单条补充要求的上限，避免把过长的转向压进模型输入。
	MaxSteeringTextBytes = 4096
	// MaxPendingSteering 是单个任务尚未生效的补充要求条数上限。
	MaxPendingSteering = 8
)

// ErrTaskFinished 表示目标任务已进入终态，不能再排队补充要求。
var ErrTaskFinished = errors.New("任务已结束，请直接回复结果续接或新建任务")

// Redirect 把补充要求排队到目标任务：运行中在下一轮模型请求前生效，排队中在任务开始时生效。
// 权限与 Cancel 相同；不取消也不重开任务，已派发的命令不受影响。
func (m *Manager) Redirect(ctx context.Context, identity chat.Identity, taskID int64, text string) (Task, error) {
	text = strings.TrimSpace(text)
	if taskID <= 0 {
		return Task{}, errors.New("任务编号无效")
	}
	if text == "" {
		return Task{}, errors.New("补充要求不能为空")
	}
	if len(text) > MaxSteeringTextBytes {
		return Task{}, fmt.Errorf("补充要求超过 %d 字节", MaxSteeringTextBytes)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return Task{}, err
	}
	t, err := m.Get(ctx, identity.Session, taskID)
	if err != nil {
		return Task{}, err
	}
	if !m.CanManage(identity, t.Message.SenderID) {
		return Task{}, errors.New("只有发起人或本群任务管理员可以补充任务要求")
	}
	if err = m.store.redirect(ctx, taskID, text); err != nil {
		return Task{}, err
	}
	return m.Get(ctx, identity.Session, taskID)
}

// ConsumeSteering 读取并标记未生效的补充要求，标记与读取在同一事务内完成，
// 重启后不会重复注入。limit 收敛到 1..MaxPendingSteering。
func (m *Manager) ConsumeSteering(ctx context.Context, taskID int64, limit int) (texts []string, err error) {
	if taskID <= 0 {
		return nil, nil
	}
	if limit <= 0 || limit > MaxPendingSteering {
		limit = MaxPendingSteering
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id,text FROM task_steering WHERE task_id=? AND delivered_at IS NULL ORDER BY id LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		var text string
		if err = rows.Scan(&id, &text); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		texts = append(texts, text)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, time.Now().UnixMilli())
	for _, id := range ids {
		args = append(args, id)
	}
	// 只标记确实读到的行；并发消费同一任务时不会重复计入 delivered_at。
	if _, err = tx.ExecContext(ctx, `UPDATE task_steering SET delivered_at=? WHERE delivered_at IS NULL AND id IN (`+placeholders(len(ids))+`)`, args...); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return texts, nil
}

// PendingSteering 返回任务尚未生效的补充要求条数；任务必须属于当前会话。
func (m *Manager) PendingSteering(ctx context.Context, session chat.Session, taskID int64) (int, error) {
	if _, err := m.Get(ctx, session, taskID); err != nil {
		return 0, err
	}
	return m.store.pendingSteering(ctx, taskID)
}

// redirect 在单个事务内确认状态与上限后插入待投递行。
// 终态任务在读取状态时就拒绝，避免把上限错误当成主因返回。
func (s *store) redirect(ctx context.Context, taskID int64, text string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, taskID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTaskFinished
		}
		return err
	}
	if status != "queued" && status != "running" && status != "blocked" && status != "review" && status != "waiting_input" {
		return ErrTaskFinished
	}
	pending, err := storePendingSteering(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if pending >= MaxPendingSteering {
		return fmt.Errorf("任务 #%d 待生效补充要求已达 %d 条，请等待下一轮生效后再补充", taskID, MaxPendingSteering)
	}
	// 条件插入与状态检查在同一事务内，任务在此期间结束时不会留下无效行。
	res, err := tx.ExecContext(ctx, `INSERT INTO task_steering(task_id,text,created_at)
		SELECT ?,?,? WHERE EXISTS(SELECT 1 FROM tasks WHERE id=? AND status IN ('queued','running','blocked','review','waiting_input'))`,
		taskID, text, time.Now().UnixMilli(), taskID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrTaskFinished
	}
	return tx.Commit()
}

func (s *store) pendingSteering(ctx context.Context, taskID int64) (int, error) {
	return storePendingSteering(ctx, s.db, taskID)
}

// storePendingSteering 统计未生效补充要求；连接与事务共用同一查询。
func storePendingSteering(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, taskID int64) (int, error) {
	var count int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_steering WHERE task_id=? AND delivered_at IS NULL`, taskID).Scan(&count)
	return count, err
}
