package task

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// taskSourceFingerprint 只覆盖调用方可控的输入：系统提示与工具集合会随时间和授权变化，
// 把它们算进幂等指纹会让同一条消息的重复提交被误判为不同请求。
func taskSourceFingerprint(message chat.Message, dir string, req agent.Request) ([]byte, error) {
	data, err := json.Marshal(struct {
		Message   chat.Message
		Directory string
		Model     string
	}{message, dir, req.Model})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return sum[:], nil
}

func (m *Manager) verifyTaskSource(ctx context.Context, t Task, message chat.Message, dir string, req agent.Request) error {
	dir, err := canonicalDir(dir)
	if err != nil {
		return err
	}
	expected, err := taskSourceFingerprint(message, dir, req)
	if err != nil {
		return err
	}
	var stored []byte
	err = m.store.db.QueryRowContext(ctx, `SELECT fingerprint FROM task_sources WHERE task_id=?`, t.ID).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		// 旧库无法证明原始请求未被续接修改，不能把不确定的副作用输入当成重试。
		return errors.New("旧任务没有不可变来源指纹，请使用新的幂等键")
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(expected, stored) {
		return errors.New("幂等键已用于不同消息、模型或工作目录")
	}
	return nil
}

// SubmitSilent 为批量处理保存不自动投递的真实任务，来源去重与 silent 标志在同一事务完成。
func (m *Manager) SubmitSilent(ctx context.Context, message chat.Message, dir string, req agent.Request) (Task, error) {
	if err := message.Validate(); err != nil {
		return Task{}, err
	}
	if message.ID == "" {
		return Task{}, errors.New("silent task requires a stable source ID")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return Task{}, err
	}
	existing, err := m.TaskForMessage(ctx, message.Session, message.ID)
	if err == nil {
		if err := m.verifyTaskSource(ctx, existing, message, dir, req); err != nil {
			return Task{}, err
		}
		if !existing.Silent {
			return Task{}, errors.New("source ID belongs to a non-silent task")
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
	identity := chat.Identity{Session: message.Session, SenderID: message.SenderID, Direct: message.Direct}
	if err = m.authorizeControl(identity, dir); err != nil {
		return Task{}, err
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	t, err := insertTaskTx(ctx, tx, message, dir, req)
	if err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET silent=1 WHERE id=?`, t.ID); err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_control(task_id) VALUES(?)`, t.ID); err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	t.Silent = true
	m.transientRequests[t.ID] = req
	if err := m.normalizeInternalMessage(ctx, &t); err != nil {
		return Task{}, err
	}
	m.notify()
	return t, nil
}
