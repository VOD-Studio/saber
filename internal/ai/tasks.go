package ai

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/task"
)

// EnableTasks 在接收消息前启用持久化任务；数据库只供当前机器人进程使用。
// 工作目录固定为启动目录，与已有 stdio 工具继承的 cwd 一致。
func (s *Service) EnableTasks(path string) error {
	if s.tasks != nil {
		return errors.New("task service already enabled")
	}
	if s.matrixService == nil {
		return errors.New("task delivery requires Matrix adapter")
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	cfg := s.core.GetConfig()
	adapter := matrix.NewChatAdapter(s.matrixService, s.mediaService, cfg.Media, false, nil)
	ready := make(chan struct{})
	manager, err := task.Open(path, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		select {
		case <-ready:
		case <-ctx.Done():
			return agent.Result{}, ctx.Err()
		}
		if task.WorkDir(ctx) != s.taskDir {
			return agent.Result{}, fmt.Errorf("任务工作目录 %q 与当前启动目录 %q 不同，停止执行以免影响错误目录", task.WorkDir(ctx), s.taskDir)
		}
		if err := s.core.WaitForRateLimit(ctx); err != nil {
			return agent.Result{}, err
		}
		return s.RunAgent(ctx, req, emit)
	}, func(ctx context.Context, t task.Task) (string, error) {
		return adapter.Send(ctx, taskReply(t, "result", task.Report(t)))
	})
	if err != nil {
		return err
	}
	s.tasks, s.taskDir = manager, dir
	close(ready)
	s.matrixService.RegisterCommandWithDesc("task", "后台任务：run <内容> | list | status <ID> | cancel <ID>", &taskCommand{service: s})
	return nil
}

func taskReply(t task.Task, kind, text string) chat.Reply {
	// 使用来源身份而非仅递增编号，重建数据库也不会与旧投递事务碰撞。
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", t.Message.Session.Platform, t.Message.Session.Account, t.Message.Session.Conversation, t.Message.ID, kind)
	return chat.Reply{Session: t.Message.Session, ReplyTo: t.Message.ID, Text: taskText(text), TransactionID: fmt.Sprintf("saber-task-%x", sha256.Sum256([]byte(key)))}
}

func taskText(text string) string {
	runes := []rune(text)
	if len(runes) > 12000 {
		return string(runes[:12000]) + "\n[消息过长，完整内容保存在任务数据库中]"
	}
	return text
}

func (s *Service) submitTask(ctx context.Context, message chat.Message, req agent.Request, reply chat.Adapter) error {
	current := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: message.Text}
	if len(message.Attachments) > 0 {
		current.Content = ""
		if message.Text != "" {
			current.MultiContent = append(current.MultiContent, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: message.Text})
		}
		for _, a := range message.Attachments {
			current.MultiContent = append(current.MultiContent, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: a.URL, Detail: openai.ImageURLDetailAuto}})
		}
	}
	req.Messages = append(req.Messages, current)
	t, err := s.tasks.Submit(ctx, message, s.taskDir, req)
	if err != nil {
		return err
	}
	ackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = reply.Send(ackCtx, taskReply(t, "received", fmt.Sprintf("已接收，任务 #%d", t.ID)))
	// 接收回执失败不会撤销已持久化任务；再次送达仍返回同一编号。
	return err
}

type taskCommand struct{ service *Service }

func (c *taskCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	s := c.service
	if len(args) > 1 && args[0] == "run" {
		return s.handleAICommand(ctx, userID, roomID, s.GetModelRegistry().GetDefault(), args[1:])
	}
	adapter := matrix.NewChatAdapter(s.matrixService, nil, s.core.GetConfig().Media, false, nil)
	message := adapter.Message(ctx, userID, roomID, "!task "+strings.Join(args, " "))
	action := ""
	var taskID int64
	if len(args) == 1 && args[0] == "list" {
		action = "list"
	}
	if len(args) == 2 && (args[0] == "status" || args[0] == "cancel") {
		var err error
		taskID, err = strconv.ParseInt(strings.TrimPrefix(args[1], "#"), 10, 64)
		if err == nil && taskID > 0 {
			action = args[0]
		}
	}
	return s.replyTaskCommand(ctx, message, adapter, action, taskID)
}

var naturalTaskID = regexp.MustCompile(`^(查看|查询|取消)任务\s*#?([0-9]+)\s*(?:的状态)?[。！!？?]?$`)

func naturalTaskCommand(text string) (string, int64, bool) {
	text = strings.TrimSpace(text)
	if text == "列出任务" || text == "查看任务列表" || text == "任务列表" {
		return "list", 0, true
	}
	match := naturalTaskID.FindStringSubmatch(text)
	if match == nil {
		return "", 0, false
	}
	taskID, err := strconv.ParseInt(match[2], 10, 64)
	if err != nil || taskID <= 0 {
		return "", 0, false
	}
	action := "status"
	if match[1] == "取消" {
		action = "cancel"
	}
	return action, taskID, true
}

func (s *Service) replyTaskCommand(ctx context.Context, message chat.Message, reply chat.Adapter, action string, taskID int64) error {
	text, err := s.taskOperation(ctx, chat.Identity{Session: message.Session, SenderID: message.SenderID}, action, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			text = "本群不存在该任务"
		} else {
			text = "任务操作失败：" + err.Error()
		}
	}
	_, sendErr := reply.Send(ctx, chat.Reply{Session: message.Session, ReplyTo: message.ID, Text: taskText(text)})
	return sendErr
}

func (s *Service) taskOperation(ctx context.Context, identity chat.Identity, action string, taskID int64) (string, error) {
	if s.tasks == nil {
		return "", errors.New("任务服务未启用")
	}
	switch action {
	case "list":
		tasks, err := s.tasks.List(ctx, identity.Session)
		if err != nil {
			return "", err
		}
		if len(tasks) == 0 {
			return "本群暂无任务", nil
		}
		var lines []string
		for _, t := range tasks {
			lines = append(lines, fmt.Sprintf("#%d %s · %s · 投递 %s", t.ID, t.Status, t.Message.SenderID, t.Delivery))
		}
		return strings.Join(lines, "\n"), nil
	case "status":
		t, err := s.tasks.Get(ctx, identity.Session, taskID)
		if err != nil {
			return "", err
		}
		events, err := s.tasks.Events(ctx, identity.Session, taskID)
		if err != nil {
			return "", err
		}
		text := fmt.Sprintf("任务 #%d：%s\n发起人：%s\n工作目录：%s\n来源消息：%s\n话题：%s\n取消请求：%t\n执行记录：%d 条\n结果投递：%s（尝试 %d 次）", t.ID, t.Status, t.Message.SenderID, t.WorkDir, t.Message.ID, t.Message.Session.Thread, t.CancelRequested, len(events), t.Delivery, t.DeliveryAttempts)
		if t.Message.Session.Platform == "matrix" {
			text += "\nhttps://matrix.to/#/" + t.Message.Session.Conversation + "/" + t.Message.ID
		}
		if t.Error != "" {
			text += "\n终止原因：" + t.Error
		}
		if t.DeliveryError != "" {
			text += "\n投递错误：" + t.DeliveryError
		}
		if t.Result.Content != "" {
			text += "\n结果：\n" + t.Result.Content
		}
		return text, nil
	case "cancel":
		t, err := s.tasks.Cancel(ctx, identity, taskID)
		if err != nil {
			return "", err
		}
		if t.Status == "running" {
			return fmt.Sprintf("已请求取消任务 #%d，等待当前步骤退出；已发生的外部副作用不会撤销", t.ID), nil
		}
		return task.Report(t), nil
	default:
		return "用法：!task run <内容> | !task list | !task status <ID> | !task cancel <ID>", nil
	}
}

func taskTool() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: "saber_task", Description: "查询本群任务列表或状态，或者取消当前用户自己发起的指定任务。身份与群由系统提供，不得代其他用户操作。创建任务由聊天入口自动完成。",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"list", "status", "cancel"}},
			"id":     map[string]any{"type": "integer", "minimum": 1},
		}, "required": []string{"action"}, "additionalProperties": false},
	}}
}
