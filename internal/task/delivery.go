package task

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DeliverPart 独立限时上传和发送一个交付项，持久化上传内容及消息 ID，重启后从失败步骤续传。
// prepare 必须返回可重用的完整上传元数据，send 必须使用稳定的平台事务 ID。
func (m *Manager) DeliverPart(ctx context.Context, taskID int64, part string, prepare func(context.Context) ([]byte, error), send func(context.Context, []byte) (string, error)) (string, error) {
	var payload []byte
	var messageID string
	err := m.store.db.QueryRowContext(ctx, `SELECT payload,message_id FROM task_delivery_parts WHERE task_id=? AND part=?`, taskID, part).Scan(&payload, &messageID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if errors.Is(err, sql.ErrNoRows) {
		uploadCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		payload, err = prepare(uploadCtx)
		cancel()
		if err != nil {
			return "", err
		}
		if payload == nil {
			payload = []byte{}
		}
		_, err = m.store.db.ExecContext(ctx, `INSERT INTO task_delivery_parts(task_id,part,payload) VALUES(?,?,?)`, taskID, part, payload)
		if err != nil {
			return "", err
		}
	}
	if messageID != "" {
		return messageID, nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	messageID, err = send(sendCtx, payload)
	if err != nil {
		return "", err
	}
	if messageID == "" {
		return "", errors.New("delivery returned an empty message ID")
	}
	_, err = m.store.db.ExecContext(ctx, `UPDATE task_delivery_parts SET message_id=? WHERE task_id=? AND part=?`, messageID, taskID, part)
	return messageID, err
}
