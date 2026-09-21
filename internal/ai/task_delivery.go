package ai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/task"
)

func (s *Service) deliverTask(ctx context.Context, adapter chat.Adapter, t task.Task) (string, error) {
	messageID, err := s.tasks.DeliverPart(ctx, t.ID, "result", func(context.Context) ([]byte, error) { return nil, nil }, func(ctx context.Context, _ []byte) (string, error) {
		return adapter.Send(ctx, taskReply(t, "result", task.Report(t)))
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
		_, err = s.tasks.DeliverPart(ctx, t.ID, key, func(ctx context.Context) ([]byte, error) {
			data, err := os.ReadFile(artifact.Path)
			if err != nil {
				return nil, err
			}
			content, err := s.matrixService.UploadTaskFile(ctx, artifact.Name, data, id.EventID(t.Message.ID), id.EventID(t.Message.Session.Thread))
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
		if err != nil {
			return messageID, err
		}
	}
	return messageID, nil
}
