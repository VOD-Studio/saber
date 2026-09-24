package task

import (
	"context"
	"errors"

	"rua.plus/saber/internal/chat"
)

// ClaimCommand 返回新执行、已完成或需核查状态。崩溃留下的执行中记录不会自动重放。
func (m *Manager) ClaimCommand(ctx context.Context, message chat.Message, commandID string) (state, reply string, err error) {
	if err = message.Validate(); err != nil {
		return "", "", err
	}
	if message.ID == "" || commandID == "" {
		return "", "", errors.New("有副作用的命令需要来源消息 ID 和命令 ID")
	}
	s := message.Session
	result, err := m.store.db.ExecContext(ctx, `INSERT INTO command_receipts(platform,account,room,event,command_id,state)
		VALUES(?,?,?,?,?,'running') ON CONFLICT DO NOTHING`, s.Platform, s.Account, s.Conversation, message.ID, commandID)
	if err != nil {
		return "", "", err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return "", "", err
	}
	if inserted > 0 {
		return "new", "", nil
	}
	err = m.store.db.QueryRowContext(ctx, `SELECT state,reply FROM command_receipts WHERE platform=? AND account=? AND room=? AND event=? AND command_id=?`,
		s.Platform, s.Account, s.Conversation, message.ID, commandID).Scan(&state, &reply)
	return state, reply, err
}

// CompleteCommand 只在副作用与回执均成功后保存可重发的回执。
func (m *Manager) CompleteCommand(ctx context.Context, message chat.Message, commandID, reply string) error {
	s := message.Session
	_, err := m.store.db.ExecContext(ctx, `UPDATE command_receipts SET state='done',reply=?
		WHERE platform=? AND account=? AND room=? AND event=? AND command_id=? AND state='running'`,
		reply, s.Platform, s.Account, s.Conversation, message.ID, commandID)
	return err
}
