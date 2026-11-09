package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/server"
	"rua.plus/saber/internal/task"
)

func (c *conn) promptTask(ctx context.Context, s *session, text string, attachments []chat.Attachment) (server.Turn, error) {
	emit := func(e agent.Event) error { return c.notifyAgentEvent(s.id, e) }
	watch := func(ctx context.Context, id int64) error { return c.watchTask(ctx, s, id) }
	var turn server.Turn
	var err error
	if s.pendingAnswer != nil {
		q := s.pendingAnswer.Question
		var answer any = text
		if q.MultiSelect {
			var selected []string
			if json.Unmarshal([]byte(text), &selected) != nil {
				return turn, fmt.Errorf("多选问题请以 JSON 字符串数组回答")
			}
			answer = selected
		}
		if err = c.client.control(ctx, s.id, "questions", map[string]any{"action": "answer", "question_id": q.ID, "task_id": q.TaskID, "answer": answer}, nil); err != nil {
			return turn, err
		}
		var questions []task.Question
		if err = c.client.control(ctx, s.id, "questions", map[string]any{"action": "list", "task_id": q.TaskID}, &questions); err != nil {
			return turn, err
		}
		for _, next := range questions {
			if next.Status == "pending" {
				s.pendingAnswer = &pendingAnswerError{next}
				if err := c.notifyChunk(s.id, next.Question); err != nil {
					return turn, err
				}
				return turn, s.pendingAnswer
			}
		}
		var resumed task.Task
		if err = c.client.control(ctx, s.id, "questions", map[string]any{"action": "resume", "task_id": q.TaskID}, &resumed); err != nil {
			return turn, err
		}
		s.pendingAnswer = nil
		if err = c.client.request(ctx, "GET", "/v1/runs/"+strconv.FormatInt(resumed.ID, 10)+"?session="+url.QueryEscape(s.id), nil, &turn); err != nil {
			return turn, err
		}
		turn, err = c.client.observeTask(ctx, s, turn, emit, watch)
	} else {
		turn, err = c.client.taskStream(ctx, s, text, attachments, emit, watch)
	}
	if pending, ok := err.(*pendingAnswerError); ok {
		s.pendingAnswer = pending
		return turn, err
	}
	if err != nil {
		return turn, err
	}
	for turn.Status == "review" {
		var node task.GraphNode
		if err = c.client.control(ctx, s.id, "graph", map[string]any{"action": "show", "task_id": turn.ID}, &node); err != nil {
			return turn, err
		}
		if node.Control.Phase == "review" {
			if err = c.approveTask(ctx, s, node); err != nil {
				return turn, err
			}
		}
		history, e := c.client.history(ctx, s.id)
		if e != nil {
			return turn, e
		}
		if len(history) == 0 {
			return turn, fmt.Errorf("审查后的持久任务不存在")
		}
		next := history[len(history)-1]
		if next.ID == turn.ID && next.Status == "review" {
			return next, fmt.Errorf("审查请求未得到真实处理")
		}
		turn = next
		if turn.Status == "running" || turn.Status == "queued" {
			turn, err = c.client.observeTask(ctx, s, turn, emit, watch)
			if pending, ok := err.(*pendingAnswerError); ok {
				s.pendingAnswer = pending
			}
			if err != nil {
				return turn, err
			}
		}
	}
	return turn, nil
}

func (c *conn) restorePendingQuestion(ctx context.Context, s *session) error {
	var questions []task.Question
	if err := c.client.control(ctx, s.id, "questions", map[string]any{"action": "list"}, &questions); err != nil {
		return err
	}
	for _, q := range questions {
		if q.Status == "pending" {
			s.pendingAnswer = &pendingAnswerError{q}
			break
		}
	}
	return nil
}
