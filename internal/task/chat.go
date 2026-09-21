package task

import (
	"context"
	"database/sql"
	"errors"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// ErrBusy 表示同一会话已有未结束的轮次，调用方可以在终态后再次发送。
var ErrBusy = errors.New("当前会话正在回答，请等待完成或先停止")

// SubmitTurn 原子去重并续接当前会话的上一轮，避免并发客户端分叉历史。
func (m *Manager) SubmitTurn(ctx context.Context, message chat.Message, dir string, req agent.Request) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return Task{}, err
	}
	if err := message.Validate(); err != nil {
		return Task{}, err
	}
	existing, err := m.store.query(ctx, `platform=? AND account=? AND room=? AND event=?`, append(scope(message.Session), message.ID)...)
	if err != nil {
		return Task{}, err
	}
	if len(existing) > 0 {
		return existing[0], nil
	}
	latest, err := m.store.query(ctx, `platform=? AND account=? AND room=? ORDER BY id DESC LIMIT 1`, scope(message.Session)...)
	if err != nil {
		return Task{}, err
	}
	var parent []int64
	if len(latest) > 0 {
		previous := latest[0]
		if previous.Status == "queued" || previous.Status == "running" {
			return Task{}, ErrBusy
		}
		if previous.Message.SenderID != message.SenderID {
			return Task{}, errors.New("会话不属于当前用户")
		}
		canonical, err := canonicalDir(dir)
		if err != nil {
			return Task{}, err
		}
		if canonical != previous.WorkDir {
			return Task{}, errors.New("会话工作目录已改变，请新建会话")
		}
		parent = append(parent, previous.ID)
	}
	result, err := m.store.submit(ctx, message, dir, req, parent...)
	m.notify()
	return result, err
}

// History 返回会话最近的 100 轮，按时间顺序展示；完整续接历史仍保留在数据库中。
func (m *Manager) History(ctx context.Context, session chat.Session) ([]Task, error) {
	if err := session.Validate(); err != nil {
		return nil, err
	}
	return m.store.query(ctx, `id IN (SELECT id FROM tasks WHERE platform=? AND account=? AND room=? ORDER BY id DESC LIMIT 100) ORDER BY id`, scope(session)...)
}

// Conversations 返回该入口账号最近的 50 个会话的最新任务，不跨入口读取。
func (m *Manager) Conversations(ctx context.Context, platform, account string) ([]Task, error) {
	if platform == "" || account == "" {
		return nil, sql.ErrNoRows
	}
	return m.store.query(ctx, `id IN (SELECT max(id) FROM tasks WHERE platform=? AND account=? GROUP BY room) ORDER BY id DESC LIMIT 50`, platform, account)
}
