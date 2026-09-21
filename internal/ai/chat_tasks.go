package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

// Tasks 提供入口所需的持久化查询与订阅能力；生命周期仍由 Service 管理。
func (s *Service) Tasks() *task.Manager { return s.tasks }

// ChatModel 只公开模型名称和默认思考等级，不携带提供商密钥。
type ChatModel struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ReasoningEffort string `json:"reasoning_effort"`
	Default         bool   `json:"default"`
}

// ChatModels 返回终端可选择的模型和实际继承的思考等级。
func (s *Service) ChatModels() []ChatModel {
	registry := s.GetModelRegistry()
	models := make([]ChatModel, 0)
	for _, info := range registry.ListModels() {
		cfg, _ := s.core.GetConfig().GetModelConfig(info.ID)
		models = append(models, ChatModel{ID: info.ID, Name: info.Model, ReasoningEffort: cfg.ReasoningEffort, Default: info.ID == registry.GetDefault()})
	}
	return models
}

// SubmitChatTask 使用已认证入口提供的身份提交独立轮次，思考等级仅覆盖本轮。
func (s *Service) SubmitChatTask(ctx context.Context, message chat.Message, model, effort string) (task.Task, error) {
	if s.tasks == nil {
		return task.Task{}, errors.New("任务服务未启用")
	}
	if err := message.Validate(); err != nil {
		return task.Task{}, err
	}
	if model == "" {
		model = s.GetModelRegistry().GetDefault()
	}
	if _, ok := s.GetModelRegistry().GetModelInfo(model); !ok {
		return task.Task{}, fmt.Errorf("模型 %q 不在可用列表中", model)
	}
	if len(effort) > 64 || strings.ContainsAny(effort, "\r\n\x00") {
		return task.Task{}, errors.New("无效思考等级")
	}
	req := s.taskRequest(message, model)
	req.Stream = true
	req.ReasoningEffort = effort
	req.Messages = append(req.Messages, taskInput(message))
	dir := s.taskDir
	if s.executor != nil {
		if granted, err := s.executor.Workspace(chat.Identity{Session: message.Session, SenderID: message.SenderID}); err == nil {
			dir = granted
		}
	}
	return s.tasks.SubmitTurn(ctx, message, dir, req)
}
