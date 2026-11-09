package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// StartExternal 为音频或直接工具创建真实持久生命周期，不派发任何伪模型请求。
func (m *Manager) StartExternal(ctx context.Context, message chat.Message, dir, kind string) (Task, error) {
	identity, err := controlIdentity(ctx)
	if err != nil {
		return Task{}, err
	}
	if err := message.Validate(); err != nil {
		return Task{}, err
	}
	if message.ID == "" || strings.TrimSpace(kind) == "" || len(kind) > 64 {
		return Task{}, errors.New("external task requires source ID and explicit kind")
	}
	if message.Session != identity.Session || message.SenderID != identity.SenderID || message.Direct != identity.Direct {
		return Task{}, errors.New("external task cannot override trusted identity")
	}
	dir, err = canonicalDir(dir)
	if err != nil {
		return Task{}, err
	}
	if err := m.authorizeControl(identity, dir); err != nil {
		return Task{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return Task{}, err
	}
	existing, err := m.TaskForMessage(ctx, message.Session, message.ID)
	if err == nil {
		if err := m.verifyTaskSource(ctx, existing, message, dir, agent.Request{}); err != nil {
			return Task{}, err
		}
		var existingKind string
		if err := m.store.db.QueryRowContext(ctx, `SELECT kind FROM task_external WHERE task_id=?`, existing.ID).Scan(&existingKind); err != nil {
			return Task{}, err
		}
		if existingKind != kind {
			return Task{}, errors.New("external source key belongs to a different operation")
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Task{}, err
	}
	if len(m.active) >= m.maxConcurrent {
		return Task{}, ErrBusy
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE status='running' AND work_dir=?)`, dir).Scan(&locked); err != nil {
		return Task{}, err
	}
	if locked {
		return Task{}, fmt.Errorf("%w: 工作目录正在使用；实时TTS请绑定当前模型任务", ErrBusy)
	}
	t, err := insertTaskTx(ctx, tx, message, dir, agent.Request{})
	if err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_external(task_id,kind) VALUES(?,?)`, t.ID, kind); err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_control(task_id) VALUES(?)`, t.ID); err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='running',silent=1 WHERE id=?`, t.ID); err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	t.Status, t.Silent = "running", true
	base, cancel := context.WithCancel(m.ctx)
	runCtx := taskContext(base, t)
	m.active[t.ID], m.runContexts[t.ID] = cancel, runCtx
	if err := m.store.record(ctx, t.ID, agent.Event{Kind: agent.RunStarted, Text: "external:" + kind}); err != nil {
		cancel()
		delete(m.active, t.ID)
		delete(m.runContexts, t.ID)
		_, saveErr := m.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE tasks SET status='failed',error=? WHERE id=?`, err.Error(), t.ID)
		return Task{}, errors.Join(err, saveErr)
	}
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		<-runCtx.Done()
		status := "cancelled"
		if m.ctx.Err() != nil {
			status = "interrupted"
		}
		if _, err := m.FinishExternal(context.Background(), identity, t.ID, status, "外部操作生命周期停止，已派发副作用不会自动重放"); err != nil {
			slog.Error("保存外部任务停止状态失败", "task", t.ID, "error", err)
		}
	}()
	return t, nil
}

// FinishExternal 保存真实外部操作终态并释放所有权；重复相同终态不会再次执行或发送。
func (m *Manager) FinishExternal(ctx context.Context, identity chat.Identity, taskID int64, status, result string) (Task, error) {
	m.deliveryMu.Lock()
	defer m.deliveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.controlledTask(ctx, identity, taskID)
	if err != nil {
		return Task{}, err
	}
	return m.finishExternalLocked(ctx, t, status, result)
}

func (m *Manager) finishExternalLocked(ctx context.Context, t Task, status, content string) (Task, error) {
	var external bool
	if err := m.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_external WHERE task_id=?)`, t.ID).Scan(&external); err != nil {
		return Task{}, err
	}
	if !external {
		return Task{}, errors.New("task is not an external operation")
	}
	if t.Status != "running" {
		return t, nil
	}
	if status != "completed" && status != "failed" && status != "cancelled" && status != "interrupted" {
		return Task{}, errors.New("invalid external terminal status")
	}
	if m.finalize != nil {
		if cleanupErr := m.finalize(context.Background(), t.ID); cleanupErr != nil {
			return Task{}, errors.Join(cleanupErr, m.store.cleanupFailed(context.Background(), t.ID, cleanupErr))
		}
	}
	result := agent.Result{Status: agent.Status(status), Content: content}
	var runErr error
	if status == "failed" {
		result.Content = ""
		runErr = errors.New(content)
	}
	if err := m.store.record(context.WithoutCancel(ctx), t.ID, agent.Event{Kind: agent.RunFinished, Status: result.Status, Text: content, Error: runErr}); err != nil {
		return Task{}, err
	}
	if err := m.store.finish(context.WithoutCancel(ctx), t, result, runErr, status == "interrupted"); err != nil {
		return Task{}, err
	}
	if cancel := m.active[t.ID]; cancel != nil {
		cancel()
	}
	delete(m.active, t.ID)
	delete(m.runContexts, t.ID)
	m.notify()
	return m.Get(context.WithoutCancel(ctx), t.Message.Session, t.ID)
}
