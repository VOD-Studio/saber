package task

import (
	"context"
	"database/sql"
	"errors"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

func taskContext(ctx context.Context, t Task) context.Context {
	ctx = chat.WithIdentity(ctx, chat.Identity{Session: t.Message.Session, SenderID: t.Message.SenderID, Direct: t.Message.Direct})
	ctx = context.WithValue(ctx, taskIDKey{}, t.ID)
	return context.WithValue(ctx, workDirKey{}, t.WorkDir)
}

// PrepareContinuation 在当前运行结束前持久预建同身份续接，为后台进程提供真实目标任务。
func (m *Manager) PrepareContinuation(ctx context.Context, msg chat.Message, dir string, req agent.Request) (Task, error) {
	identity, err := controlIdentity(ctx)
	if err != nil {
		return Task{}, err
	}
	parentID := ID(ctx)
	if parentID <= 0 {
		return Task{}, errors.New("continuation preparation requires a running source task")
	}
	if err := msg.Validate(); err != nil {
		return Task{}, err
	}
	if msg.ID == "" {
		return Task{}, errors.New("continuation requires a stable event key")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	parent, err := m.controlledTask(ctx, identity, parentID)
	if err != nil {
		return Task{}, err
	}
	if parent.Status != "running" || parent.CancelRequested {
		return Task{}, errors.New("source task is not running")
	}
	dir, err = canonicalDir(dir)
	if err != nil {
		return Task{}, err
	}
	if msg.Session != parent.Message.Session || msg.SenderID != parent.Message.SenderID || msg.Direct != parent.Message.Direct || dir != parent.WorkDir {
		return Task{}, errors.New("续接必须保持同一来源身份、线程和工作目录")
	}
	if err := m.authorizeControl(identity, dir); err != nil {
		return Task{}, err
	}
	existing, err := m.TaskForMessage(ctx, msg.Session, msg.ID)
	if err == nil {
		if err := m.verifyTaskSource(ctx, existing, msg, dir, req); err != nil {
			return Task{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Task{}, err
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var sourceGeneration, currentGeneration int64
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM task_generations WHERE task_id=?`, parentID).Scan(&sourceGeneration); err != nil {
		return Task{}, err
	}
	err = tx.QueryRowContext(ctx, `SELECT generation FROM session_contexts WHERE platform=? AND account=? AND room=? AND thread=?`, append(scope(msg.Session), msg.Session.Thread)...).Scan(&currentGeneration)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Task{}, err
	}
	if sourceGeneration != currentGeneration {
		return Task{}, errors.New("会话已重置，不移交旧任务进程")
	}
	t, err := insertTaskTx(ctx, tx, msg, dir, req, parentID)
	if err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_control(task_id) VALUES(?)`, t.ID); err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	m.transientRequests[t.ID] = req
	if err := m.normalizeInternalMessage(ctx, &t); err != nil {
		return Task{}, err
	}
	m.notify()
	return t, nil
}

// AuthorizeProcessTransfer 证明两个执行上下文确实是同一人、工作区和代号中的直接续接。
func (m *Manager) AuthorizeProcessTransfer(sourceCtx, destinationCtx context.Context) error {
	sourceIdentity, ok := chat.IdentityFromContext(sourceCtx)
	if !ok {
		return errors.New("source process owner is not trusted")
	}
	targetIdentity, ok := chat.IdentityFromContext(destinationCtx)
	if !ok || targetIdentity != sourceIdentity {
		return errors.New("process transfer cannot change identity")
	}
	sourceID, targetID := ID(sourceCtx), ID(destinationCtx)
	if sourceID <= 0 || targetID <= 0 || sourceID == targetID || WorkDir(sourceCtx) != WorkDir(destinationCtx) {
		return errors.New("invalid process ownership transition")
	}
	source, err := m.Get(sourceCtx, sourceIdentity.Session, sourceID)
	if err != nil {
		return err
	}
	target, err := m.Get(sourceCtx, sourceIdentity.Session, targetID)
	if err != nil {
		return err
	}
	if source.Message.SenderID != sourceIdentity.SenderID || target.Message.SenderID != sourceIdentity.SenderID || source.Message.Session != target.Message.Session || source.Message.Direct != target.Message.Direct || source.WorkDir != target.WorkDir || source.WorkDir != WorkDir(sourceCtx) || source.Status != "running" || source.CancelRequested || target.CancelRequested || (target.Status != "queued" && target.Status != "running") {
		return errors.New("任务身份、工作区或执行状态不允许进程移交")
	}
	var valid bool
	if err := m.store.db.QueryRowContext(sourceCtx, `SELECT EXISTS(SELECT 1 FROM task_links l JOIN task_generations a ON a.task_id=l.parent_id JOIN task_generations b ON b.task_id=l.task_id WHERE l.parent_id=? AND l.task_id=? AND a.generation=b.generation)`, sourceID, targetID).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return errors.New("process target is not a current direct continuation")
	}
	generation, err := m.ContextGeneration(sourceCtx, source.Message.Session)
	if err != nil {
		return err
	}
	var targetGeneration int64
	if err := m.store.db.QueryRowContext(sourceCtx, `SELECT generation FROM task_generations WHERE task_id=?`, targetID).Scan(&targetGeneration); err != nil {
		return err
	}
	if generation != targetGeneration {
		return errors.New("会话已重置，不移交旧进程")
	}
	return nil
}

func (m *Manager) transferContinuationProcesses(ctx context.Context, source Task) error {
	if m.transferProcesses == nil {
		return nil
	}
	targets, err := m.store.query(ctx, `id IN (SELECT task_id FROM task_links WHERE parent_id=?) AND status='queued' AND sender=? AND work_dir=? ORDER BY id LIMIT 1`, source.ID, source.Message.SenderID, source.WorkDir)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	target := targets[0]
	m.mu.Lock()
	destinationCtx := m.pendingContexts[target.ID]
	if destinationCtx == nil {
		base, cancel := context.WithCancel(m.ctx)
		destinationCtx = taskContext(base, target)
		m.pendingContexts[target.ID], m.pendingCancels[target.ID] = destinationCtx, cancel
	}
	m.mu.Unlock()
	if err := m.AuthorizeProcessTransfer(ctx, destinationCtx); err != nil {
		return err
	}
	if err := m.transferProcesses(ctx, destinationCtx); err != nil {
		return err
	}
	m.mu.Lock()
	if cleanup := m.childFinalizers[source.ID]; cleanup != nil {
		m.childFinalizers[target.ID] = cleanup
		delete(m.childFinalizers, source.ID)
	}
	m.mu.Unlock()
	return nil
}
