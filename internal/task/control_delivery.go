package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"rua.plus/saber/internal/agent"
)

type deliveryNamespaceKey struct{}

func (m *Manager) controlDelivery(ctx context.Context, t Task) (context.Context, Task, error) {
	state, err := m.controlState(ctx, t.ID)
	if err != nil {
		return nil, Task{}, err
	}
	if state.Phase != "blocked" && state.Phase != "review" && state.Phase != "waiting_input" {
		return ctx, t, nil
	}
	if state.Phase == "review" {
		t.Result.Content = "任务等待审查：" + state.Summary
	}
	if state.Phase == "blocked" {
		t.Result.Content = "任务已阻塞（" + state.Kind + "）：" + state.Reason
	}
	if state.Phase == "waiting_input" {
		questions, err := m.questions(ctx, t.ID)
		if err != nil {
			return nil, Task{}, err
		}
		data, err := json.Marshal(questions)
		if err != nil {
			return nil, Task{}, err
		}
		t.Result.Content = "任务需要用户澄清；请在问题面板或任务问题接口回答：\n" + string(data)
	}
	if _, err := m.store.db.ExecContext(ctx, `INSERT INTO task_control_delivery(task_id,revision) VALUES(?,?) ON CONFLICT(task_id) DO UPDATE SET revision=excluded.revision`, t.ID, state.Revision); err != nil {
		return nil, Task{}, err
	}
	ctx = context.WithValue(ctx, deliveryNamespaceKey{}, fmt.Sprintf("control:%d:", state.Revision))
	return ctx, t, nil
}

func controlJournal(ctx context.Context, tx *sql.Tx, id int64, value any) error {
	text, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data, err := json.Marshal(agent.Event{Kind: agent.EventKind("task_control"), Text: string(text)})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_events(task_id,created_at,record) VALUES(?,?,?)`, id, time.Now().UnixMilli(), data)
	return err
}
