package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// RegisterDelivery 为指定入口注册可重试的终态投递；未注册入口仍可读取结果和事件。
func (m *Manager) RegisterDelivery(platform string, send SendFunc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return err
	}
	if platform == "" || send == nil {
		return errors.New("delivery requires platform and sender")
	}
	if m.deliveries[platform] != nil {
		return errors.New("delivery already registered")
	}
	m.deliveries[platform] = send
	return nil
}

// Record 是可公开给聊天入口的持久化增量，不暴露原始模型响应及加密推理。
type Record struct {
	ID     int64              `json:"id"`
	Kind   agent.EventKind    `json:"kind"`
	Round  int                `json:"round"`
	Text   string             `json:"text,omitempty"`
	Tool   agent.ToolRecord   `json:"tool,omitempty"`
	State  *agent.TaskState   `json:"state,omitempty"`
	Budget *agent.BudgetStats `json:"budget,omitempty"`
	Batch  *agent.BatchInfo   `json:"batch,omitempty"`
}

// ReadEvents 按游标读取至多 256 条事件；慢速订阅者可以断开后继续读取。
func (m *Manager) ReadEvents(ctx context.Context, session chat.Session, taskID, after int64) (records []Record, err error) {
	if _, err = m.Get(ctx, session, taskID); err != nil {
		return nil, err
	}
	rows, err := m.store.db.QueryContext(ctx, `SELECT id,record FROM task_events WHERE task_id=? AND id>? ORDER BY id LIMIT 256`, taskID, after)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var record Record
		var data []byte
		if err = rows.Scan(&record.ID, &data); err != nil {
			return nil, err
		}
		var event struct {
			Kind     agent.EventKind
			Round    int
			Text     string
			Tool     agent.ToolRecord
			State    *agent.TaskState
			Budget   *agent.BudgetStats
			Batch    *agent.BatchInfo
			Response struct{ Content string }
		}
		if err = json.Unmarshal(data, &event); err != nil {
			return nil, err
		}
		record.Kind, record.Round, record.Text, record.Tool = event.Kind, event.Round, event.Text, event.Tool
		record.State, record.Budget, record.Batch = event.State, event.Budget, event.Batch
		if event.Kind == agent.ModelCompleted {
			record.Text = event.Response.Content
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (m *Manager) deliverLoop() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
		}
		m.mu.Lock()
		registered := make([]string, 0, len(m.deliveries))
		wildcard := m.deliveries["*"] != nil
		for platform := range m.deliveries {
			registered = append(registered, platform)
		}
		m.mu.Unlock()
		if len(registered) == 0 {
			continue
		}
		// 只查询已注册投递器的平台，避免未订阅平台的积压任务占满每轮窗口。
		where := `(status IN ('completed','failed','cancelled','interrupted','blocked','review','waiting_input') OR (status='running' AND id IN (SELECT task_id FROM task_control WHERE phase='waiting_input'))) AND delivery='pending' AND next_delivery<=?`
		args := []any{time.Now().UnixMilli()}
		if !wildcard {
			sort.Strings(registered)
			where += ` AND platform IN (?` + strings.Repeat(",?", len(registered)-1) + `)`
			for _, platform := range registered {
				args = append(args, platform)
			}
		}
		tasks, err := m.store.query(m.ctx, where+` ORDER BY next_delivery,id LIMIT 20`, args...)
		if err != nil {
			if m.ctx.Err() == nil {
				slog.Error("读取待投递任务失败", "error", err)
			}
			continue
		}
		for _, t := range tasks {
			if m.ctx.Err() != nil {
				return
			}
			m.deliveryMu.Lock()
			sendCtx, noticeTask, noticeErr := m.controlDelivery(m.ctx, t)
			deliveryTask, resolveErr := m.DeliveryTask(m.ctx, noticeTask)
			resolveErr = errors.Join(noticeErr, resolveErr)
			m.mu.Lock()
			send := m.deliveries[deliveryTask.Message.Session.Platform]
			if send == nil {
				send = m.deliveries["*"]
			}
			m.mu.Unlock()
			if send == nil && resolveErr == nil {
				m.deliveryMu.Unlock()
				continue
			}
			// 服务停止时 m.ctx 被取消，正在进行的这次发送会以 context canceled 失败，
			// 并像普通失败一样多消耗一次尝试次数（delivered 用独立的 Background 上下文落盘）。
			// 每次停机最多影响当前这一个任务，对默认 20 次上限影响很小，因此不特殊处理。
			messageID, sendErr := "", resolveErr
			if sendErr == nil {
				messageID, sendErr = safeSend(sendCtx, send, deliveryTask)
			}
			status, err := m.store.delivered(context.Background(), t.ID, messageID, sendErr, t.DeliveryAttempts, m.deliveryMaxAttempts)
			m.deliveryMu.Unlock()
			if err != nil {
				slog.Error("保存任务投递状态失败", "error", taskError(t.ID, err))
				continue
			}
			if status == "abandoned" {
				// 结果仍完整保存在数据库中，可用 !task status / !task logs 查看；这里不再自动重试。
				slog.Error("任务结果投递重试次数用尽，已放弃", "task", t.ID, "platform", t.Message.Session.Platform, "attempts", t.DeliveryAttempts+1, "max_attempts", m.deliveryMaxAttempts, "error", sendErr)
			}
		}
	}
}

// safeSend 把投递函数的 panic 转成错误，使任务按退避重试处理而不是拖垮整个进程。
func safeSend(ctx context.Context, send SendFunc, t Task) (messageID string, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("任务投递 panic", "task", t.ID, "platform", t.Message.Session.Platform, "panic", r, "stack", string(debug.Stack()))
			messageID, err = "", fmt.Errorf("任务投递 panic: %v", r)
		}
	}()
	return send(ctx, t)
}
