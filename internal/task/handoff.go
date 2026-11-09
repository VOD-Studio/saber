package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"rua.plus/saber/internal/chat"
)

// HandoffResolver 只从可信配置解析同一人的显式跨平台目的地，不接受模型声明用户身份。
type HandoffResolver func(context.Context, chat.Identity, string) (chat.Identity, error)

// SetHandoffResolver 设置显式交接路由；每次投递仍重新验证当前映射。
func (m *Manager) SetHandoffResolver(resolver HandoffResolver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handoff = resolver
}

// Handoff 仅改变任务结果目的地，不复制记忆、历史或再次执行任务。
func (m *Manager) Handoff(ctx context.Context, source chat.Identity, taskID int64, route string) (Task, error) {
	m.deliveryMu.Lock()
	defer m.deliveryMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.controlledTask(ctx, source, taskID)
	if err != nil {
		return Task{}, err
	}
	if m.handoff == nil {
		return Task{}, errors.New("没有配置可信跨平台交接路由")
	}
	if t.Status == "running" || t.Delivery != "pending" || t.DeliveryAttempts > 0 {
		return Task{}, errors.New("运行中或已经投递的任务不能交接")
	}
	trustedSource := chat.Identity{Session: t.Message.Session, SenderID: t.Message.SenderID, Direct: t.Message.Direct}
	target, err := m.handoff(ctx, trustedSource, route)
	if err != nil {
		return Task{}, err
	}
	if target.SenderID == "" || target.Session.Validate() != nil {
		return Task{}, errors.New("交接路由没有完整目标身份")
	}
	if !m.control.AllowCrossUserHandoff && target.SenderID != trustedSource.SenderID {
		return Task{}, errors.New("跨用户标识交接需要显式control.allow_cross_user_handoff")
	}
	var parts int
	if err := m.store.db.QueryRowContext(ctx, `SELECT count(*) FROM task_delivery_parts WHERE task_id=?`, taskID).Scan(&parts); err != nil {
		return Task{}, err
	}
	if parts != 0 {
		return Task{}, errors.New("已有上传或发送断点，不能更换目标后重复交付")
	}
	sourceJSON, err := json.Marshal(trustedSource)
	if err != nil {
		return Task{}, err
	}
	targetJSON, err := json.Marshal(target)
	if err != nil {
		return Task{}, err
	}
	_, err = m.store.db.ExecContext(ctx, `INSERT INTO task_handoffs(task_id,route,source,target,created_at) VALUES(?,?,?,?,?) ON CONFLICT(task_id) DO UPDATE SET route=excluded.route,source=excluded.source,target=excluded.target,created_at=excluded.created_at`, taskID, route, sourceJSON, targetJSON, time.Now().UnixMilli())
	m.notify()
	return t, err
}

// DeliveryTask 为发送器返回经当前可信路由复核的结果目标，原任务归属始终保持不变。
func (m *Manager) DeliveryTask(ctx context.Context, t Task) (Task, error) {
	var route string
	var sourceJSON, targetJSON []byte
	err := m.store.db.QueryRowContext(ctx, `SELECT route,source,target FROM task_handoffs WHERE task_id=?`, t.ID).Scan(&route, &sourceJSON, &targetJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return t, nil
	}
	if err != nil {
		return Task{}, err
	}
	var source, expected chat.Identity
	if err = errors.Join(json.Unmarshal(sourceJSON, &source), json.Unmarshal(targetJSON, &expected)); err != nil {
		return Task{}, err
	}
	m.mu.Lock()
	resolver := m.handoff
	allowCrossUser := m.control.AllowCrossUserHandoff
	m.mu.Unlock()
	if resolver == nil {
		return Task{}, errors.New("持久交接映射当前不可用，停止投递")
	}
	target, err := resolver(ctx, source, route)
	if err != nil {
		return Task{}, err
	}
	if target != expected || (!allowCrossUser && target.SenderID != source.SenderID) {
		return Task{}, errors.New("交接映射已更改或撤销，拒绝投递到未经确认的新身份")
	}
	t.Message.Session, t.Message.SenderID, t.Message.Direct = target.Session, target.SenderID, target.Direct
	t.Message.ID, t.Message.ReplyTo = "", ""
	return t, nil
}
