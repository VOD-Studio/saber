package task

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// Batch 是一次提交的批次元数据；条目本身是彼此独立的根任务。
type Batch struct {
	// ID 是程序生成的批次编号，形状为 b_<16hex>。
	ID string
	// Creator 是提交者，查询与取消都按发起人或本群任务管理员判定。
	Creator string
	// Label 是提交者给出的可选说明，不参与执行。
	Label string
	// Total 是批次条目数，提交后不再变化。
	Total int
	// CreatedAt 是提交时间。
	CreatedAt time.Time
}

// BatchTask 是批次内一条任务的状态摘要。
type BatchTask struct {
	// TaskID 是普通任务编号，行为与手动提交的任务一致。
	TaskID int64
	// Status 是该任务当前状态。
	Status string
	// Silent 表示该条目结果只落库、不向聊天投递。
	Silent bool
	// CreatedAt 是任务接收时间。
	CreatedAt time.Time
}

// BatchEntry 是一条待提交的任务：Text 是这条任务自己的指令，Request 是冻结的模型请求。
type BatchEntry struct {
	// Text 是这条任务唯一的用户输入；批次条目互不共享历史。
	Text string
	// Silent 表示结果只落库、不向聊天投递。
	Silent bool
	// Request 由调用方按当前授权组装，存储层只做快照。
	Request agent.Request
}

// newBatchID 生成 b_<16hex> 批次编号；编号只由程序生成，不接受外部输入。
func newBatchID() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "b_" + hex.EncodeToString(random[:]), nil
}

// SubmitBatch 在同一事务内写入 1 条批次与 N 条根任务。
//
// 任一条插入失败整体回滚，不会留下「N 条任务但没有批次行」的半批。
// 条目之间不建立 task_links，也不继承提交者的任务链：批次不是续接。
//
// Errors:
//   - 身份或会话非法、工作目录无法规范化时拒绝提交
//   - 编号生成失败或任一插入失败时整个批次回滚
func (m *Manager) SubmitBatch(ctx context.Context, identity chat.Identity, dir, label string, entries []BatchEntry) (Batch, []int64, error) {
	if err := m.ctx.Err(); err != nil {
		return Batch{}, nil, err
	}
	if len(entries) == 0 {
		return Batch{}, nil, errors.New("batch requires at least one item")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	batch, ids, err := m.store.submitBatch(ctx, identity, dir, label, entries)
	m.notify()
	return batch, ids, err
}

// BatchStatus 返回批次元数据与每条任务的状态；只允许创建者或本群任务管理员查看。
func (m *Manager) BatchStatus(ctx context.Context, identity chat.Identity, batchID string) (Batch, []BatchTask, error) {
	batch, err := m.batch(ctx, identity, batchID)
	if err != nil {
		return Batch{}, nil, err
	}
	tasks, err := m.store.batchTasks(ctx, batchID)
	if err != nil {
		return Batch{}, nil, err
	}
	return batch, tasks, nil
}

// CancelBatch 逐条取消批次内的非终态任务。
//
// 已终态的任务与取消失败的任务都进入 skipped，不把部分成功伪装成全成功。
func (m *Manager) CancelBatch(ctx context.Context, identity chat.Identity, batchID string) (Batch, []int64, []int64, error) {
	batch, err := m.batch(ctx, identity, batchID)
	if err != nil {
		return Batch{}, nil, nil, err
	}
	tasks, err := m.store.batchTasks(ctx, batchID)
	if err != nil {
		return Batch{}, nil, nil, err
	}
	cancelled, skipped := []int64{}, []int64{}
	for _, item := range tasks {
		if item.Status != "queued" && item.Status != "running" {
			skipped = append(skipped, item.TaskID)
			continue
		}
		if _, err := m.Cancel(ctx, identity, item.TaskID); err != nil {
			skipped = append(skipped, item.TaskID)
			continue
		}
		cancelled = append(cancelled, item.TaskID)
	}
	return batch, cancelled, skipped, nil
}

// batch 读取批次并按所属账号、群与创建者校验访问权。
//
// 先按 platform+account+room 过滤，再要求 CanManage，因此凭 batch_id 猜不到其他群或他人的批次。
func (m *Manager) batch(ctx context.Context, identity chat.Identity, batchID string) (Batch, error) {
	if err := identity.Session.Validate(); err != nil {
		return Batch{}, err
	}
	var batch Batch
	var created int64
	err := m.store.db.QueryRowContext(ctx, `SELECT batch_id,creator,label,total,created_at FROM task_batches
		WHERE batch_id=? AND platform=? AND account=? AND room=?`,
		batchID, identity.Session.Platform, identity.Session.Account, identity.Session.Conversation).
		Scan(&batch.ID, &batch.Creator, &batch.Label, &batch.Total, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Batch{}, fmt.Errorf("batch %s not found", batchID)
	}
	if err != nil {
		return Batch{}, err
	}
	if !m.CanManage(identity, batch.Creator) {
		return Batch{}, errors.New("只有批次创建者或本群任务管理员可以查看该批次")
	}
	batch.CreatedAt = time.UnixMilli(created)
	return batch, nil
}

// submitBatch 在调用方锁内写入批次与全部条目。
func (s *store) submitBatch(ctx context.Context, identity chat.Identity, dir, label string, entries []BatchEntry) (Batch, []int64, error) {
	if err := identity.Session.Validate(); err != nil {
		return Batch{}, nil, err
	}
	if identity.SenderID == "" {
		return Batch{}, nil, errors.New("batch requires a trusted sender")
	}
	dir, err := canonicalDir(dir)
	if err != nil {
		return Batch{}, nil, err
	}
	batchID, err := newBatchID()
	if err != nil {
		return Batch{}, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Batch{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now()
	// event 列必须满足 UNIQUE(platform,account,room,event)：批次条目使用
	// batch:<batch_id>:<index>，既不复用真实消息 ID，也不伪造用户消息。
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_batches(batch_id,platform,account,room,creator,label,total,created_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		batchID, identity.Session.Platform, identity.Session.Account, identity.Session.Conversation,
		identity.SenderID, label, len(entries), now.UnixMilli()); err != nil {
		return Batch{}, nil, err
	}
	ids := make([]int64, 0, len(entries))
	for index, entry := range entries {
		message := chat.Message{
			ID: fmt.Sprintf("batch:%s:%d", batchID, index), SenderID: identity.SenderID,
			Session: identity.Session, Direct: identity.Direct, Text: entry.Text,
		}
		if err := message.Validate(); err != nil {
			return Batch{}, nil, fmt.Errorf("batch item %d: %w", index, err)
		}
		m, err := json.Marshal(message)
		if err != nil {
			return Batch{}, nil, err
		}
		r, err := json.Marshal(entry.Request)
		if err != nil {
			return Batch{}, nil, err
		}
		inserted, err := tx.ExecContext(ctx, `INSERT INTO tasks(platform,account,room,event,sender,work_dir,message,request,created_at,silent,batch_id,batch_index)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			identity.Session.Platform, identity.Session.Account, identity.Session.Conversation, message.ID,
			identity.SenderID, dir, m, r, now.UnixMilli(), entry.Silent, batchID, index)
		if err != nil {
			return Batch{}, nil, err
		}
		id, err := inserted.LastInsertId()
		if err != nil {
			return Batch{}, nil, err
		}
		ids = append(ids, id)
	}
	// 提交事件写在首条任务上：批次没有独立的事件通道，从第一条任务即可回读整批起点。
	record, err := json.Marshal(agent.Event{Kind: agent.BatchSubmitted, Batch: &agent.BatchInfo{ID: batchID, Total: len(entries)}})
	if err != nil {
		return Batch{}, nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_events(task_id,created_at,record) VALUES(?,?,?)`, ids[0], now.UnixMilli(), record); err != nil {
		return Batch{}, nil, err
	}
	if err = tx.Commit(); err != nil {
		return Batch{}, nil, err
	}
	return Batch{ID: batchID, Creator: identity.SenderID, Label: label, Total: len(entries), CreatedAt: now}, ids, nil
}

// batchTasks 按批次内位置返回任务摘要。
func (s *store) batchTasks(ctx context.Context, batchID string) (tasks []BatchTask, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,status,silent,created_at FROM tasks WHERE batch_id=? ORDER BY batch_index, id`, batchID)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var item BatchTask
		var silent int
		var created int64
		if err := rows.Scan(&item.TaskID, &item.Status, &silent, &created); err != nil {
			return nil, err
		}
		item.Silent = silent != 0
		item.CreatedAt = time.UnixMilli(created)
		tasks = append(tasks, item)
	}
	return tasks, rows.Err()
}

// finishBatchIfDone 在批次最后一条任务终态时补写 batch_finished 事件。
//
// 必须与终态更新处于同一事务：连接串行（MaxOpenConns=1）且终态不可逆，
// 因此只有一个事务会观察到「没有剩余 queued/running」，不会重复写入。
func finishBatchIfDone(ctx context.Context, tx *sql.Tx, taskID int64) error {
	var batchID string
	if err := tx.QueryRowContext(ctx, `SELECT batch_id FROM tasks WHERE id=?`, taskID).Scan(&batchID); err != nil {
		return err
	}
	if batchID == "" {
		return nil
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE batch_id=? AND status IN ('queued','running')`, batchID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return nil
	}
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT total FROM task_batches WHERE batch_id=?`, batchID).Scan(&total); err != nil {
		return err
	}
	counts, err := batchCounts(ctx, tx, batchID)
	if err != nil {
		return err
	}
	record, err := json.Marshal(agent.Event{Kind: agent.BatchFinished, Batch: &agent.BatchInfo{ID: batchID, Total: total, Counts: counts}})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_events(task_id,created_at,record) VALUES(?,?,?)`, taskID, time.Now().UnixMilli(), record)
	return err
}

// batchCounts 按状态统计批次条目，用于结束事件。
func batchCounts(ctx context.Context, tx *sql.Tx, batchID string) (map[string]int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT status,COUNT(*) FROM tasks WHERE batch_id=? GROUP BY status`, batchID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}
