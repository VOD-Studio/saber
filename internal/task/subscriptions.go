package task

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
	ID    int64            `json:"id"`
	Kind  agent.EventKind  `json:"kind"`
	Round int              `json:"round"`
	Text  string           `json:"text,omitempty"`
	Tool  agent.ToolRecord `json:"tool,omitempty"`
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
			Response struct{ Content string }
		}
		if err = json.Unmarshal(data, &event); err != nil {
			return nil, err
		}
		record.Kind, record.Round, record.Text, record.Tool = event.Kind, event.Round, event.Text, event.Tool
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
		platforms := make([]any, 0, len(m.deliveries))
		placeholders := make([]string, 0, len(m.deliveries))
		for platform := range m.deliveries {
			platforms = append(platforms, platform)
			placeholders = append(placeholders, "?")
		}
		_, all := m.deliveries["*"]
		m.mu.Unlock()
		if len(platforms) == 0 {
			continue
		}
		filter := ""
		args := []any{time.Now().UnixMilli()}
		if !all {
			filter = " AND platform IN (" + strings.Join(placeholders, ",") + ")"
			args = append(args, platforms...)
		}
		tasks, err := m.store.query(m.ctx, `status NOT IN ('queued','running') AND delivery='pending' AND next_delivery<=?`+filter+` ORDER BY next_delivery,id LIMIT 20`, args...)
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
			m.mu.Lock()
			send := m.deliveries[t.Message.Session.Platform]
			if send == nil {
				send = m.deliveries["*"]
			}
			m.mu.Unlock()
			if send == nil {
				continue
			}
			messageID, sendErr := send(m.ctx, t)
			if err := m.store.delivered(context.Background(), t.ID, messageID, sendErr, t.DeliveryAttempts); err != nil {
				slog.Error("保存任务投递状态失败", "error", taskError(t.ID, err))
			}
		}
	}
}
