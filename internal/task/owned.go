package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"rua.plus/saber/internal/chat"
)

// ResolveOwnedTask 从已认证账号和用户解析持久响应编号，不要求调用方猜测原会话。
func (m *Manager) ResolveOwnedTask(ctx context.Context, identity chat.Identity, id int64) (Task, error) {
	if identity.Session.Platform == "" || identity.Session.Account == "" || identity.SenderID == "" || id <= 0 {
		return Task{}, errors.New("task lookup requires authenticated platform/account/user and positive ID")
	}
	t, err := scanTask(m.store.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE platform=? AND account=? AND id=?`, identity.Session.Platform, identity.Session.Account, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m.handoffTask(ctx, identity, id)
	}
	if err != nil {
		return Task{}, err
	}
	actor := identity
	actor.Session = t.Message.Session
	if !m.CanManage(actor, t.Message.SenderID) {
		return Task{}, errors.New("task response belongs to another user")
	}
	return t, nil
}

func (m *Manager) authorizedHandoff(ctx context.Context, identity chat.Identity, t Task) error {
	var route string
	var sourceData, targetData []byte
	if err := m.store.db.QueryRowContext(ctx, `SELECT route,source,target FROM task_handoffs WHERE task_id=?`, t.ID).Scan(&route, &sourceData, &targetData); err != nil {
		return err
	}
	var source, expected chat.Identity
	if err := errors.Join(json.Unmarshal(sourceData, &source), json.Unmarshal(targetData, &expected)); err != nil {
		return err
	}
	if expected != identity || source.Session != t.Message.Session || source.SenderID != t.Message.SenderID || source.Direct != t.Message.Direct {
		return errors.New("handoff does not authorize this identity")
	}
	m.mu.Lock()
	resolver := m.handoff
	m.mu.Unlock()
	if resolver == nil {
		return errors.New("handoff resolver is unavailable")
	}
	actual, err := resolver(ctx, source, route)
	if err != nil {
		return err
	}
	if actual != expected || (!m.control.AllowCrossUserHandoff && actual.SenderID != source.SenderID) {
		return errors.New("handoff route was changed or revoked")
	}
	return nil
}

func (m *Manager) handoffTask(ctx context.Context, identity chat.Identity, id int64) (Task, error) {
	t, err := scanTask(m.store.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if err != nil {
		return Task{}, err
	}
	if err := m.authorizedHandoff(ctx, identity, t); err != nil {
		return Task{}, sql.ErrNoRows
	}
	return t, nil
}
