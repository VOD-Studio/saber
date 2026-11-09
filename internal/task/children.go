package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// HasChildResultSink 校验当前 Manager 能为该真实父任务保存并读取独立子结果。
func (m *Manager) HasChildResultSink(ctx context.Context, identity chat.Identity, parentID int64) bool {
	if m.ctx.Err() != nil {
		return false
	}
	parent, err := m.controlledTask(ctx, identity, parentID)
	return err == nil && parent.Status == "running" && !parent.CancelRequested
}

// SpawnDetached 将子代理作为同一权威中的普通持久任务派发，父轮次停止不隐式取消后台任务。
func (m *Manager) SpawnDetached(ctx context.Context, identity chat.Identity, parentID int64, message chat.Message, dir string, req agent.Request) (Task, error) {
	// 绑定回调运行时已经分配持久编号，但派发仍由 Manager.mu 阻止。
	if err := m.ctx.Err(); err != nil {
		return Task{}, err
	}
	parent, err := m.controlledTask(ctx, identity, parentID)
	if err != nil {
		return Task{}, err
	}
	if parent.Status != "running" || parent.CancelRequested {
		return Task{}, errors.New("后台委派需要未取消的真实运行父任务")
	}
	if message.Session != parent.Message.Session || message.SenderID != parent.Message.SenderID || message.Direct != parent.Message.Direct {
		return Task{}, errors.New("子任务必须继承父任务的可信身份和会话")
	}
	if err := message.Validate(); err != nil {
		return Task{}, err
	}
	if message.ID == "" {
		return Task{}, errors.New("child task requires a stable source ID")
	}
	dir, err = canonicalDir(dir)
	if err != nil {
		return Task{}, err
	}
	if dir == parent.WorkDir {
		return Task{}, errors.New("后台子任务必须使用已授权的独立工作副本；共享目录请同步委派")
	}
	if req.SubagentDepth <= parent.Request.SubagentDepth {
		return Task{}, errors.New("子代理深度必须由程序递增")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	child, err := insertTaskTx(ctx, tx, message, dir, req)
	if err != nil {
		return Task{}, err
	}
	var existingParent int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT parent_id FROM task_children WHERE task_id=?),0)`, child.ID).Scan(&existingParent); err != nil {
		return Task{}, err
	}
	if existingParent > 0 {
		if existingParent != parentID {
			return Task{}, errors.New("child source ID belongs to another parent")
		}
		return child, tx.Commit()
	}
	if child.Status != "queued" {
		return Task{}, errors.New("child source ID is already in use")
	}
	limit := req.ChildMaxParallel
	if limit == 0 {
		limit = 4
	}
	if limit < 1 || limit > MaxConcurrentLimit {
		return Task{}, errors.New("invalid child task capacity")
	}
	var rootID int64
	if err := tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id) AS (SELECT ? UNION SELECT c.parent_id FROM task_children c JOIN ancestors a ON c.task_id=a.id) SELECT min(id) FROM ancestors`, parentID).Scan(&rootID); err != nil {
		return Task{}, err
	}
	var activeChildren int
	if err := tx.QueryRowContext(ctx, `WITH RECURSIVE descendants(id) AS (SELECT task_id FROM task_children WHERE parent_id=? UNION SELECT c.task_id FROM task_children c JOIN descendants d ON c.parent_id=d.id) SELECT count(*) FROM tasks JOIN task_children c ON c.task_id=tasks.id WHERE tasks.id IN (SELECT id FROM descendants) AND c.detached=1 AND tasks.status IN ('queued','running','blocked','review','waiting_input')`, rootID).Scan(&activeChildren); err != nil {
		return Task{}, err
	}
	if activeChildren >= limit {
		return Task{}, errors.New("parent task tree background child capacity exhausted")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_children(task_id,parent_id,detached,created_at) VALUES(?,?,1,?)`, child.ID, parentID, time.Now().UnixMilli()); err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	prepareErr := error(nil)
	if req.PrepareChild != nil {
		prepareErr = req.PrepareChild(ctx, child.ID)
	}
	if prepareErr == nil {
		childCtx := chat.WithIdentity(ctx, identity)
		childCtx = context.WithValue(childCtx, taskIDKey{}, child.ID)
		childCtx = context.WithValue(childCtx, workDirKey{}, dir)
		prepareErr = m.checkControlledTask(childCtx, child)
	}
	if prepareErr != nil {
		if req.ChildCleanup != nil {
			prepareErr = errors.Join(prepareErr, req.ChildCleanup(context.Background()))
		}
		_, saveErr := m.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE tasks SET status='failed',error=? WHERE id=? AND status='queued'`, prepareErr.Error(), child.ID)
		return Task{}, errors.Join(prepareErr, saveErr)
	}
	m.transientRequests[child.ID] = req
	if req.ChildCleanup != nil {
		m.childFinalizers[child.ID] = func() error { return req.ChildCleanup(context.Background()) }
	}
	if err := m.normalizeInternalMessage(ctx, &child); err != nil {
		return Task{}, err
	}
	m.notify()
	return child, nil
}

func (m *Manager) finalizeChild(taskID int64) error {
	m.mu.Lock()
	cleanup := m.childFinalizers[taskID]
	delete(m.childFinalizers, taskID)
	delete(m.transientRequests, taskID)
	m.mu.Unlock()
	if cleanup != nil {
		return cleanup()
	}
	return nil
}

// DrainChildCompletions 原子领取真实子终态并保存收件副本，父轮次取消不会丢失已领取结果。
func (m *Manager) DrainChildCompletions(ctx context.Context, identity chat.Identity, parentID int64) ([]agent.ChildCompletion, error) {
	if _, err := m.controlledTask(ctx, identity, parentID); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT `+qualifiedTaskColumns+` FROM task_children c JOIN tasks ON tasks.id=c.task_id WHERE c.parent_id=? AND c.consumed_at IS NULL AND tasks.status IN ('completed','failed','cancelled','interrupted') ORDER BY tasks.id`, parentID)
	if err != nil {
		return nil, err
	}
	var children []Task
	for rows.Next() {
		child, err := scanTask(rows)
		if err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		children = append(children, child)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	results := make([]agent.ChildCompletion, 0, len(children))
	for _, child := range children {
		completion := childCompletion(child)
		data, err := json.Marshal(completion)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_child_received(parent_id,child_id,payload,created_at) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, parentID, child.ID, data, time.Now().UnixMilli()); err != nil {
			return nil, err
		}
		results = append(results, completion)
		if _, err = tx.ExecContext(ctx, `UPDATE task_children SET consumed_at=? WHERE task_id=? AND consumed_at IS NULL`, time.Now().UnixMilli(), child.ID); err != nil {
			return nil, err
		}
	}
	return results, tx.Commit()
}

// DrainChildResults 为文字消费者包装同一次终态领取，不维护第二个消费游标。
func (m *Manager) DrainChildResults(ctx context.Context, identity chat.Identity, parentID int64) ([]string, error) {
	completions, err := m.DrainChildCompletions(ctx, identity, parentID)
	if err != nil {
		return nil, err
	}
	results := make([]string, 0, len(completions))
	for _, completion := range completions {
		data, err := json.Marshal(completion)
		if err != nil {
			return nil, err
		}
		results = append(results, string(data))
	}
	return results, nil
}

func childCompletion(child Task) agent.ChildCompletion {
	completion := agent.ChildCompletion{TaskID: child.ID, Goal: child.Message.Text, Status: child.Status, Content: child.Result.Content}
	if child.Error != "" {
		completion.Content += "\n执行错误：" + child.Error
	}
	for _, round := range child.Result.Rounds {
		for _, record := range round.Tools {
			completion.Images = append(completion.Images, record.Images...)
			completion.Files = append(completion.Files, record.Files...)
		}
	}
	for _, nested := range child.Result.ChildCompletions {
		completion.Images = append(completion.Images, nested.Images...)
		completion.Files = append(completion.Files, nested.Files...)
	}
	return completion
}

func (m *Manager) receivedChildCompletions(ctx context.Context, parentID int64, through ...int) ([]agent.ChildCompletion, error) {
	throughRound := 0
	if len(through) > 0 {
		throughRound = through[0]
	}
	rows, err := m.store.db.QueryContext(ctx, `SELECT payload FROM task_child_received WHERE parent_id=? AND (delivered_round=0 OR delivered_round>?) ORDER BY child_id`, parentID, throughRound)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	completions := []agent.ChildCompletion{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var completion agent.ChildCompletion
		if err := json.Unmarshal(data, &completion); err != nil {
			return nil, err
		}
		completions = append(completions, completion)
	}
	return completions, rows.Err()
}

// StopChildren 显式停止任务树下的后台子任务，不改变已完成子结果。
func (m *Manager) StopChildren(ctx context.Context, identity chat.Identity, parentID int64) ([]Task, error) {
	if _, err := m.controlledTask(ctx, identity, parentID); err != nil {
		return nil, err
	}
	children, err := m.store.query(ctx, `id IN (WITH RECURSIVE descendants(id) AS (SELECT task_id FROM task_children WHERE parent_id=? UNION SELECT c.task_id FROM task_children c JOIN descendants d ON c.parent_id=d.id) SELECT id FROM descendants) AND status IN ('queued','running','blocked','review','waiting_input') ORDER BY id`, parentID)
	if err != nil {
		return nil, err
	}
	stopped := make([]Task, 0, len(children))
	for _, child := range children {
		t, err := m.Cancel(ctx, identity, child.ID)
		if err != nil {
			return stopped, fmt.Errorf("stop child #%d: %w", child.ID, err)
		}
		stopped = append(stopped, t)
	}
	return stopped, nil
}

// RebindQueuedWorkDir 在派发前把仍在排队的子任务绑定到新授权的独立工作副本。
// PrepareChild 已在持有 Manager 锁时调用，这里只做一条受控更新，避免再次取锁。
func (m *Manager) RebindQueuedWorkDir(ctx context.Context, identity chat.Identity, id int64, dir string) error {
	if id <= 0 {
		return errors.New("work directory rebind requires task ID")
	}
	dir, err := canonicalDir(dir)
	if err != nil {
		return err
	}
	result, err := m.store.db.ExecContext(ctx, `UPDATE tasks SET work_dir=? WHERE id=? AND status='queued' AND platform=? AND account=? AND room=? AND sender=?`,
		dir, id, identity.Session.Platform, identity.Session.Account, identity.Session.Conversation, identity.SenderID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("queued task does not belong to this identity")
	}
	return nil
}
