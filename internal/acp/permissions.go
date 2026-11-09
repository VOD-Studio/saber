package acp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"rua.plus/saber/internal/task"
)

type pendingAnswerError struct{ Question task.Question }

func (e *pendingAnswerError) Error() string { return "等待用户下一条消息回答澄清" }
func (c *chatClient) control(ctx context.Context, session, kind string, args map[string]any, out any) error {
	return c.request(ctx, "POST", "/v1/control/"+kind, map[string]any{"session": session, "arguments": args}, out)
}
func (c *conn) answerQuestion(ctx context.Context, s *session, q task.Question) error {
	options := []map[string]string{}
	if len(q.Choices) > 0 && !q.MultiSelect {
		for index, choice := range q.Choices {
			options = append(options, map[string]string{"optionId": "choice_" + strconv.Itoa(index), "name": choice, "kind": "allow_once"})
		}
	} else {
		options = append(options, map[string]string{"optionId": "answer_next", "name": "在下一条消息中填写答案", "kind": "allow_once"})
	}
	options = append(options, map[string]string{"optionId": "cancel", "name": "取消此问题", "kind": "reject_once"})
	choice, err := c.requestPermission(ctx, s.id, q.Question, fmt.Sprintf("question_%d", q.ID), options)
	if err != nil {
		return err
	}
	if choice == "answer_next" {
		hint := q.Question
		if q.MultiSelect {
			hint += "\n请用 JSON 字符串数组回答多个选项：" + strings.Join(q.Choices, "、")
		}
		if err := c.notifyChunk(s.id, hint); err != nil {
			return err
		}
		return &pendingAnswerError{q}
	}
	args := map[string]any{"task_id": q.TaskID, "question_id": q.ID, "action": "answer"}
	if choice == "cancel" || choice == "cancelled" {
		args["action"] = "cancel"
	} else {
		index, err := strconv.Atoi(strings.TrimPrefix(choice, "choice_"))
		if err != nil || index < 0 || index >= len(q.Choices) {
			return fmt.Errorf("invalid question choice")
		}
		args["answer"] = q.Choices[index]
	}
	return c.client.control(ctx, s.id, "questions", args, nil)
}
func (c *conn) approveTask(ctx context.Context, s *session, node task.GraphNode) error {
	summary := node.Control.Summary
	if summary == "" {
		summary = node.Control.Reason
	}
	if summary == "" {
		summary = "审核当前任务执行结果"
	}
	choice, err := c.requestPermission(ctx, s.id, summary, fmt.Sprintf("review_%d", node.Task.ID), []map[string]string{{"optionId": "approve", "name": "批准本次请求", "kind": "allow_once"}, {"optionId": "deny", "name": "拒绝本次请求", "kind": "reject_once"}})
	if err != nil {
		return err
	}
	if choice == "cancelled" {
		return c.client.stopTask(s.id, node.Task.ID)
	}
	action := "approve"
	reason := "用户通过 ACP 批准本次请求"
	if choice == "deny" {
		action = "request_changes"
		reason = "用户通过 ACP 拒绝本次请求；不得执行被拒操作，调整方案"
	}
	return c.client.control(ctx, s.id, "graph", map[string]any{"action": action, "task_id": node.Task.ID, "reason": reason}, nil)
}
func (c *conn) watchTask(ctx context.Context, s *session, id int64) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	reviewed := int64(-1)
	for {
		var questions []task.Question
		if err := c.client.control(ctx, s.id, "questions", map[string]any{"action": "list", "task_id": id}, &questions); err != nil {
			return err
		}
		for _, q := range questions {
			if q.Status == "pending" {
				if err := c.answerQuestion(ctx, s, q); err != nil {
					return err
				}
			}
		}
		var node task.GraphNode
		if err := c.client.control(ctx, s.id, "graph", map[string]any{"action": "show", "task_id": id}, &node); err != nil {
			return err
		}
		if node.Control.Phase == "review" && node.Control.Revision != reviewed {
			if err := c.approveTask(ctx, s, node); err != nil {
				return err
			}
			reviewed = node.Control.Revision
		}
		if node.Task.Status != "running" && node.Task.Status != "queued" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
