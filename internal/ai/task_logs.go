package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/task"
)

func (s *Service) sendTaskLogs(ctx context.Context, identity chat.Identity, taskID int64, source string) (string, error) {
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
	files := s.taskFileAdapter(identity.Session.Platform, nil)
	if files == nil {
		return "", errors.New("当前平台不支持日志文件交付")
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
	if source == "" {
		source = fmt.Sprintf("agent-%d", task.ID(ctx))
	}
	key := "logs:" + source
	if _, err = s.deliverTaskFile(ctx, files, t, key+":metadata", fmt.Sprintf("task-%d.json", taskID), func() ([]byte, error) { return metadata, nil }); err != nil {
		return "", err
	}
	for _, log := range logs {
		if _, err = s.deliverTaskFile(ctx, files, t, key+":"+log.Name, fmt.Sprintf("task-%d-%s", taskID, log.Name), func() ([]byte, error) { return os.ReadFile(log.Path) }); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("任务 #%d 完整记录及 %d 份命令日志已发送", taskID, len(logs)), nil
}

// taskFileAdapter 返回能为该平台发送文件的 adapter：优先使用调用方正在使用的投递 adapter，
// 其次使用平台注册任务投递时登记的 adapter；平台没有文件发送能力时返回 nil。
func (s *Service) taskFileAdapter(platform string, delivery chat.Adapter) chat.FileAdapter {
	if files, ok := delivery.(chat.FileAdapter); ok && files != nil {
		return files
	}
	if value, ok := s.taskFiles.Load(platform); ok {
		return value.(chat.FileAdapter)
	}
	return nil
}

// deliverTaskFile 通过平台的文件发送能力幂等交付一个文件；files 为 nil 时返回错误而不是 panic。
func (s *Service) deliverTaskFile(ctx context.Context, files chat.FileAdapter, t task.Task, key, name string, read func() ([]byte, error)) (string, error) {
	if files == nil {
		return "", errors.New("当前平台不支持文件交付")
	}
	return s.tasks.DeliverPart(ctx, t.ID, key, func(ctx context.Context) ([]byte, error) {
		data, err := read()
		if err != nil {
			return nil, err
		}
		return files.UploadFile(ctx, chat.FileUpload{Session: t.Message.Session, ReplyTo: t.Message.ID, Name: name, Data: data})
	}, func(ctx context.Context, payload []byte) (string, error) {
		return files.SendUploadedFile(ctx, taskReply(t, key, ""), payload)
	})
}
