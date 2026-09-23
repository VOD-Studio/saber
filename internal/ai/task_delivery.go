package ai

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/task"
)

func (s *Service) deliverTask(ctx context.Context, adapter chat.Adapter, t task.Task) (string, error) {
	messageID, err := s.tasks.DeliverPart(ctx, t.ID, "result", func(context.Context) ([]byte, error) { return nil, nil }, func(ctx context.Context, _ []byte) (string, error) {
		text := task.Report(t)
		if t.Status == "completed" {
			text = t.Result.Content
			if strings.TrimSpace(text) == "" {
				text = "任务已完成"
			}
		}
		reply := taskReply(t, "result", text)
		messageID, err := adapter.Send(ctx, reply)
		if err != nil || !adapter.Capabilities().Edit {
			return messageID, err
		}
		// 流式发送可能已经创建同一条消息；幂等发送取回 ID 后原地写入终态。
		if err := adapter.Edit(ctx, messageID, reply); err != nil {
			return "", err
		}
		return messageID, nil
	})
	if err != nil || s.executor == nil || t.Status != "completed" {
		return messageID, err
	}
	artifacts, err := s.executor.Artifacts(t.ID)
	if err != nil {
		return messageID, err
	}
	for _, artifact := range artifacts {
		identity := chat.Identity{Session: t.Message.Session, SenderID: t.Message.SenderID}
		artifactCtx := execution.WithTask(chat.WithIdentity(ctx, identity), t.ID, t.WorkDir)
		if err = s.executor.Check(artifactCtx, "read_file"); err != nil {
			return messageID, err
		}
		key := "artifact:" + filepath.Base(artifact.Path)
		_, err = s.deliverTaskFile(ctx, t, key, artifact.Name, func() ([]byte, error) { return os.ReadFile(artifact.Path) })
		if err != nil {
			return messageID, err
		}
	}
	return messageID, nil
}
