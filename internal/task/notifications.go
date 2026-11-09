package task

import (
	"context"
	"encoding/json"

	"rua.plus/saber/internal/agent"
)

func (m *Manager) contextNotifications(ctx context.Context, parent Task, throughRound int) ([]string, error) {
	rows, err := m.store.db.QueryContext(ctx, `SELECT record FROM task_events WHERE task_id=? AND json_extract(record,'$.Kind')=? AND (json_extract(record,'$.Round')>? OR ?=0) ORDER BY id`, parent.ID, string(agent.ContextNotificationsReceived), throughRound, throughRound)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var notices []string
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var event struct {
			Notifications []string `json:"notifications"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, err
		}
		notices = append(notices, event.Notifications...)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(notices) == 0 && throughRound == 0 {
		return parent.Result.Notifications, nil
	}
	return notices, nil
}
