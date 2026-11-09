package task

import (
	"context"
	"encoding/json"

	"rua.plus/saber/internal/agent"
)

func mergeTransientRequest(persisted, transient agent.Request) agent.Request {
	persisted.RuntimeContext = transient.RuntimeContext
	persisted.ParentAttemptGate = transient.ParentAttemptGate
	persisted.PrepareChild = transient.PrepareChild
	persisted.ChildCleanup = transient.ChildCleanup
	persisted.Purpose = transient.Purpose
	return persisted
}

// normalizeInternalMessage 保留事件去重键，但不把内部编号当作平台可引用的消息 ID。
func (m *Manager) normalizeInternalMessage(ctx context.Context, t *Task) error {
	t.Message.ID, t.Message.ReplyTo = "", ""
	data, err := json.Marshal(t.Message)
	if err != nil {
		return err
	}
	_, err = m.store.db.ExecContext(ctx, `UPDATE tasks SET message=? WHERE id=?`, data, t.ID)
	return err
}
