package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// RememberMessage 记录回执和交付消息，引用解析只信任数据库中的平台 ID。
func (m *Manager) RememberMessage(ctx context.Context, taskID int64, messageID string) error {
	if messageID == "" {
		return nil
	}
	_, err := m.store.db.ExecContext(ctx, `INSERT INTO task_messages(task_id,message_id) VALUES(?,?) ON CONFLICT DO NOTHING`, taskID, messageID)
	return err
}

// Continue 保存独立后续轮次，等待前序终态后恢复上下文，权限仍取当前发言人。
func (m *Manager) Continue(ctx context.Context, message chat.Message, dir string, req agent.Request) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := message.Validate(); err != nil {
		return Task{}, err
	}
	if message.ReplyTo == "" {
		return Task{}, sql.ErrNoRows
	}
	parent, err := scanTask(m.store.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE platform=? AND account=? AND room=? AND (event=? OR delivery_id=? OR id IN (SELECT task_id FROM task_messages WHERE message_id=? UNION SELECT task_id FROM task_delivery_parts WHERE message_id=?)) ORDER BY id DESC LIMIT 1`, append(scope(message.Session), message.ReplyTo, message.ReplyTo, message.ReplyTo, message.ReplyTo)...))
	if err != nil {
		return Task{}, err
	}
	// 同一引用的连续补充接到最新后续轮次，避免遗漏更早的补充要求。
	var latest int64
	err = m.store.db.QueryRowContext(ctx, `WITH RECURSIVE chain(id) AS (SELECT ? UNION ALL SELECT l.task_id FROM task_links l JOIN chain c ON l.parent_id=c.id) SELECT max(id) FROM chain`, parent.ID).Scan(&latest)
	if err != nil {
		return Task{}, err
	}
	parent, err = m.Get(ctx, message.Session, latest)
	if err != nil {
		return Task{}, err
	}
	dir, err = canonicalDir(dir)
	if err != nil {
		return Task{}, err
	}
	if dir != parent.WorkDir {
		return Task{}, errors.New("续接任务需要相同的当前授权工作目录")
	}
	t, err := m.store.submit(ctx, message, dir, req, parent.ID)
	m.notify()
	return t, err
}

func (m *Manager) continuationRequest(ctx context.Context, t Task) (agent.Request, error) {
	var ready bool
	if err := m.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_contexts WHERE task_id=?)`, t.ID).Scan(&ready); err != nil {
		return t.Request, err
	}
	if ready {
		messages, changed := sanitizeToolHistory(t.Request.Messages)
		if !changed {
			return t.Request, nil
		}
		req := t.Request
		req.Messages = messages
		return req, m.saveContinuationRequest(ctx, t.ID, req)
	}
	var parentID int64
	err := m.store.db.QueryRowContext(ctx, `SELECT parent_id FROM task_links WHERE task_id=?`, t.ID).Scan(&parentID)
	if errors.Is(err, sql.ErrNoRows) {
		return t.Request, nil
	}
	if err != nil {
		return t.Request, err
	}
	parent, err := m.Get(ctx, t.Message.Session, parentID)
	if err != nil {
		return t.Request, err
	}
	parentReq, err := m.continuationRequest(ctx, parent)
	if err != nil {
		return t.Request, err
	}
	messages := append([]openai.ChatCompletionMessage(nil), parentReq.Messages...)
	rounds := parent.Result.Rounds
	// 硬退出可能来不及保存 Result，使用已落盘的模型和工具完成事件恢复轨迹。
	if len(rounds) == 0 {
		records, err := m.Events(ctx, t.Message.Session, parent.ID)
		if err != nil {
			return t.Request, err
		}
		for _, record := range records {
			var e struct {
				Kind     agent.EventKind
				Response agent.Response
				Tool     agent.ToolRecord
			}
			if err = json.Unmarshal([]byte(record), &e); err != nil {
				return t.Request, err
			}
			if e.Kind == agent.ModelCompleted {
				rounds = append(rounds, agent.Round{Response: e.Response})
			}
			if e.Kind == agent.ToolFinished && len(rounds) > 0 {
				rounds[len(rounds)-1].Tools = append(rounds[len(rounds)-1].Tools, e.Tool)
			}
		}
	}
	for _, round := range rounds {
		resp := round.Response
		if err := agent.ValidateResponse(resp); err != nil {
			messages = append(messages, historyDiagnostic(err, resp))
			continue
		}
		if resp.Content == "" && len(resp.ToolCalls) == 0 {
			continue
		}
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: resp.Content, ToolCalls: resp.ToolCalls})
		for _, call := range resp.ToolCalls {
			content := "执行结果未知或尚未执行；先核查外部副作用，不要自动重放。"
			for _, record := range round.Tools {
				if record.Call.ID == call.ID {
					content = record.Content
					break
				}
			}
			messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleTool, ToolCallID: call.ID, Content: content})
		}
	}
	if len(rounds) == 0 && parent.Result.Content != "" {
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: parent.Result.Content})
	}
	messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: fmt.Sprintf("[任务续接] 前序任务 #%d 状态 %s。保留原目标与约束，结合以下补充继续；对未确认完成的操作先核查，不自动重放。", parent.ID, parent.Status)})
	// 新请求的系统提示不覆盖原任务约束；当前授权工具由执行入口重新筛选。
	for _, msg := range t.Request.Messages {
		if msg.Role != openai.ChatMessageRoleSystem {
			messages = append(messages, msg)
		}
	}
	req := t.Request
	req.Messages, _ = sanitizeToolHistory(messages)
	return req, m.saveContinuationRequest(ctx, t.ID, req)
}

func (m *Manager) saveContinuationRequest(ctx context.Context, taskID int64, req agent.Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET request=? WHERE id=?`, data, taskID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_contexts(task_id) VALUES(?) ON CONFLICT DO NOTHING`, taskID); err != nil {
		return err
	}
	return tx.Commit()
}

func historyDiagnostic(err error, record any) openai.ChatCompletionMessage {
	return openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: fmt.Sprintf("[历史响应校验失败：%v；仅作文字诊断，不作为工具调用回放]\n%+v", err, record)}
}

// sanitizeToolHistory 同时修复旧版本已持久化的异常调用及孤立结果，不改写合法的工具失败记录。
func sanitizeToolHistory(messages []openai.ChatCompletionMessage) ([]openai.ChatCompletionMessage, bool) {
	clean := make([]openai.ChatCompletionMessage, 0, len(messages))
	changed := false
	for i := 0; i < len(messages); {
		msg := messages[i]
		if msg.Role == openai.ChatMessageRoleAssistant && len(msg.ToolCalls) > 0 {
			end := i + 1
			for end < len(messages) && messages[end].Role == openai.ChatMessageRoleTool {
				end++
			}
			err := agent.ValidateResponse(agent.Response{ToolCalls: msg.ToolCalls, FinishReason: "tool_calls"})
			if err == nil {
				pending := make(map[string]bool, len(msg.ToolCalls))
				for _, call := range msg.ToolCalls {
					pending[call.ID] = true
				}
				for _, result := range messages[i+1 : end] {
					if !pending[result.ToolCallID] {
						err = errors.New("tool result has no matching call")
						break
					}
					delete(pending, result.ToolCallID)
				}
				if err == nil && len(pending) > 0 {
					err = errors.New("tool calls have missing results")
				}
			}
			if err != nil {
				clean = append(clean, historyDiagnostic(err, messages[i:end]))
				changed = true
			} else {
				clean = append(clean, messages[i:end]...)
			}
			i = end
			continue
		}
		if msg.Role == openai.ChatMessageRoleTool {
			clean = append(clean, historyDiagnostic(errors.New("orphan tool result"), msg))
			changed = true
		} else {
			clean = append(clean, msg)
		}
		i++
	}
	return clean, changed
}
