package task

import (
	"context"
	"database/sql"
	"errors"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// TaskForMessage 查找已持久接收的来源事件，供自动化在崩溃后核对而不盲目重放。
func (m *Manager) TaskForMessage(ctx context.Context, session chat.Session, event string) (Task, error) {
	if err := session.Validate(); err != nil {
		return Task{}, err
	}
	return scanTask(m.store.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE platform=? AND account=? AND room=? AND event=?`, append(scope(session), event)...))
}

// SessionIdle 检查会话是否没有排队、运行或等待用户的任务；后台子任务同样计入。
func (m *Manager) SessionIdle(ctx context.Context, session chat.Session) (bool, error) {
	if err := session.Validate(); err != nil {
		return false, err
	}
	var busy bool
	err := m.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE platform=? AND account=? AND room=? AND status IN ('queued','running','blocked','review','waiting_input'))`, scope(session)...).Scan(&busy)
	return !busy, err
}

// SubmitAutomation 原子复核当前授权与会话空闲后接收自动任务。
func (m *Manager) SubmitAutomation(ctx context.Context, msg chat.Message, dir string, req agent.Request, continuation bool) (Task, error) {
	generation, err := m.ContextGeneration(ctx, msg.Session)
	if err != nil {
		return Task{}, err
	}
	return m.SubmitAutomationAtGeneration(ctx, msg, dir, req, continuation, generation)
}

// SubmitAutomationAtGeneration 将发生时的会话代号与任务接收放入同一事务，阻止重置后复活旧发生。
func (m *Manager) SubmitAutomationAtGeneration(ctx context.Context, msg chat.Message, dir string, req agent.Request, continuation bool, generation int64) (Task, error) {
	if err := msg.Validate(); err != nil {
		return Task{}, err
	}
	if msg.ID == "" {
		return Task{}, errors.New("automation requires a stable event ID")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return Task{}, err
	}
	existing, err := m.TaskForMessage(ctx, msg.Session, msg.ID)
	if err == nil {
		if existing.Message.SenderID != msg.SenderID || existing.Message.Session.Thread != msg.Session.Thread {
			return Task{}, errors.New("automation event belongs to another identity")
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Task{}, err
	}
	dir, err = canonicalDir(dir)
	if err != nil {
		return Task{}, err
	}
	identity := chat.Identity{Session: msg.Session, SenderID: msg.SenderID, Direct: msg.Direct}
	if err := m.authorizeControl(identity, dir); err != nil {
		return Task{}, err
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT generation FROM session_contexts WHERE platform=? AND account=? AND room=? AND thread=?`, append(scope(msg.Session), msg.Session.Thread)...).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Task{}, err
	}
	if current != generation {
		return Task{}, errors.New("会话已经重置，拒绝恢复旧自动化发生")
	}
	var busy bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE platform=? AND account=? AND room=? AND status IN ('queued','running','blocked','review','waiting_input'))`, scope(msg.Session)...).Scan(&busy); err != nil {
		return Task{}, err
	}
	if busy {
		return Task{}, ErrBusy
	}
	var parent []int64
	if continuation {
		previous, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE platform=? AND account=? AND room=? ORDER BY id DESC LIMIT 1`, scope(msg.Session)...))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Task{}, err
		}
		if err == nil {
			if previous.Message.Session.Thread != msg.Session.Thread || previous.Message.SenderID != msg.SenderID || previous.WorkDir != dir {
				return Task{}, errors.New("自动续接的当前任务身份、线程或工作目录不同")
			}
			var previousGeneration int64
			if err := tx.QueryRowContext(ctx, `SELECT generation FROM task_generations WHERE task_id=?`, previous.ID).Scan(&previousGeneration); err != nil {
				return Task{}, err
			}
			if previousGeneration != generation {
				return Task{}, errors.New("自动续接不能跨会话重置")
			}
			parent = append(parent, previous.ID)
		}
	}
	t, err := insertTaskTx(ctx, tx, msg, dir, req, parent...)
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
