package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/task"
)

func (s *Service) sendTaskLogs(ctx context.Context, identity chat.Identity, taskID int64) (string, error) {
	t, err := s.tasks.Get(ctx, identity.Session, taskID)
	if err != nil {
		return "", err
	}
	if !s.tasks.CanManage(identity, t.Message.SenderID) {
		return "", errors.New("只有发起人或本群任务管理员可以下载完整日志")
	}
	if t.Status == "queued" || t.Status == "running" {
		return "", errors.New("任务尚未结束，请结束或取消后下载完整日志")
	}
	if s.matrixService == nil || identity.Session.Platform != "matrix" {
		return "", errors.New("日志文件交付需要 Matrix")
	}
	records, err := s.tasks.Events(ctx, identity.Session, taskID)
	if err != nil {
		return "", err
	}
	events := make([]json.RawMessage, 0, len(records))
	for _, record := range records {
		events = append(events, json.RawMessage(record))
	}
	metadata, err := json.MarshalIndent(struct {
		Task   task.Task
		Events []json.RawMessage
	}{t, events}, "", "  ")
	if err != nil {
		return "", err
	}
	var logs []execution.Artifact
	if s.executor != nil {
		logs, err = s.executor.Logs(taskID)
		if err != nil {
			return "", err
		}
	}
	// 每次下载请求有独立幂等键；重复事件和中途失败复用已上传/已发送步骤。
	source := s.eventID(ctx)
	if source == "" {
		source = fmt.Sprintf("agent-%d", task.ID(ctx))
	}
	key := "logs:" + source
	if _, err = s.deliverTaskFile(ctx, t, key+":metadata", fmt.Sprintf("task-%d.json", taskID), func() ([]byte, error) { return metadata, nil }); err != nil {
		return "", err
	}
	for _, log := range logs {
		if _, err = s.deliverTaskFile(ctx, t, key+":"+log.Name, fmt.Sprintf("task-%d-%s", taskID, log.Name), func() ([]byte, error) { return os.ReadFile(log.Path) }); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("任务 #%d 完整记录及 %d 份命令日志已发送", taskID, len(logs)), nil
}

func (s *Service) deliverTaskFile(ctx context.Context, t task.Task, key, name string, read func() ([]byte, error)) (string, error) {
	return s.tasks.DeliverPart(ctx, t.ID, key, func(ctx context.Context) ([]byte, error) {
		data, err := read()
		if err != nil {
			return nil, err
		}
		content, err := s.matrixService.UploadTaskFile(ctx, name, data, id.EventID(t.Message.ID), id.EventID(t.Message.Session.Thread))
		if err != nil {
			return nil, err
		}
		return json.Marshal(content)
	}, func(ctx context.Context, data []byte) (string, error) {
		var content event.MessageEventContent
		if err := json.Unmarshal(data, &content); err != nil {
			return "", err
		}
		eventID, err := s.matrixService.SendUploadedTaskFile(ctx, id.RoomID(t.Message.Session.Conversation), &content, taskReply(t, key, "").TransactionID)
		return string(eventID), err
	})
}
