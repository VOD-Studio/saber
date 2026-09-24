package ai

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

func commandReply(ctx context.Context, message chat.Message, adapter chat.Adapter, text string) error {
	_, err := adapter.Send(ctx, chat.Reply{Session: message.Session, ReplyTo: message.ID, Text: taskText(text)})
	return err
}

// ChatCommand 将命令后的正文原样交给同一条聊天任务链路。
func (s *Service) ChatCommand(ctx context.Context, message chat.Message, adapter chat.Adapter, modelName, raw string) error {
	if strings.HasPrefix(raw, "-- ") {
		raw = raw[3:]
	} else if raw == "--" {
		raw = ""
	}
	if strings.TrimSpace(raw) == "" {
		return commandReply(ctx, message, adapter, "用法：/ai <问题>；问题以命令名开头时使用 /ai -- <问题>")
	}
	message.Text, message.ControlText = raw, &raw
	_, err := s.handleChat(ctx, message, adapter, modelName, false)
	return err
}

// AICommand 执行与平台无关的 AI 管理命令。
func (s *Service) AICommand(ctx context.Context, message chat.Message, adapter chat.Adapter, action, raw string) error {
	registry := s.GetModelRegistry()
	if action != "switch" && strings.TrimSpace(raw) != "" {
		return commandReply(ctx, message, adapter, "用法：/ai "+action)
	}
	switch action {
	case "clear":
		if s.chatProcessor != nil {
			if err := s.chatProcessor.Clear(ctx, message.Session); err != nil {
				return err
			}
		}
		if s.tasks != nil {
			generation, err := s.tasks.ClearContext(ctx, message.Session)
			if err != nil {
				return err
			}
			return commandReply(ctx, message, adapter, fmt.Sprintf("对话上下文已清除；当前续接代号 %d。旧任务仍可查询。", generation))
		}
		return commandReply(ctx, message, adapter, "对话上下文已清除")
	case "context":
		var lines []string
		if s.tasks != nil {
			generation, err := s.tasks.ContextGeneration(ctx, message.Session)
			if err != nil {
				return err
			}
			lines = append(lines, fmt.Sprintf("持久化任务续接代号：%d（只续接同代号、同话题的任务）", generation))
		}
		if s.contextManager != nil {
			count, tokens := s.contextManager.history.GetContextSize(message.Session.Key())
			lines = append(lines, fmt.Sprintf("即时历史：%d 条消息，约 %d 个令牌", count, tokens))
		}
		if len(lines) == 0 {
			lines = append(lines, "当前没有保存的对话上下文")
		}
		return commandReply(ctx, message, adapter, strings.Join(lines, "\n"))
	case "models":
		models := registry.ListModels()
		if len(models) == 0 {
			return commandReply(ctx, message, adapter, "没有配置任何模型")
		}
		var lines []string
		for _, model := range models {
			marker := ""
			if model.ID == registry.GetDefault() {
				marker = "（当前默认）"
			}
			lines = append(lines, fmt.Sprintf("- `%s` → `%s`%s", model.ID, model.Model, marker))
		}
		return commandReply(ctx, message, adapter, "可用模型：\n"+strings.Join(lines, "\n"))
	case "current":
		return commandReply(ctx, message, adapter, fmt.Sprintf("当前全局默认模型：`%s`；配置默认：`%s`", registry.GetDefault(), registry.GetConfigDefault()))
	case "switch":
		args := strings.Fields(raw)
		if len(args) != 1 {
			return commandReply(ctx, message, adapter, "用法：/ai switch <model-id>（全局）")
		}
		old := registry.GetDefault()
		if err := registry.SetDefault(args[0]); err != nil {
			return commandReply(ctx, message, adapter, "切换模型失败："+err.Error())
		}
		return commandReply(ctx, message, adapter, fmt.Sprintf("全局默认模型已从 `%s` 切换到 `%s`；重启后恢复配置默认。", old, registry.GetDefault()))
	default:
		return commandReply(ctx, message, adapter, "未知 AI 命令")
	}
}

// TaskCommand 执行任务控制；取消和查询在新聊天任务创建之前完成。
func (s *Service) TaskCommand(ctx context.Context, message chat.Message, adapter chat.Adapter, action, raw string) error {
	if action == "run" {
		return s.ChatCommand(ctx, message, adapter, s.GetModelRegistry().GetDefault(), raw)
	}
	var id int64
	if action != "list" && action != "" {
		fields := strings.Fields(raw)
		if len(fields) == 1 {
			id, _ = strconv.ParseInt(strings.TrimPrefix(fields[0], "#"), 10, 64)
		}
		if id <= 0 {
			return commandReply(ctx, message, adapter, "用法：/task "+action+" <id>")
		}
	} else if strings.TrimSpace(raw) != "" {
		return commandReply(ctx, message, adapter, "用法：/task list")
	}
	text, err := s.taskOperation(ctx, chat.Identity{Session: message.Session, SenderID: message.SenderID}, action, id, message.ID)
	if errors.Is(err, sql.ErrNoRows) {
		text = "本会话不存在该任务"
	} else if err != nil {
		text = "任务操作失败：" + err.Error()
	}
	return commandReply(ctx, message, adapter, text)
}

// ScheduleCommand 使用可信消息身份创建或管理持久化计划。
func (s *Service) ScheduleCommand(ctx context.Context, message chat.Message, adapter chat.Adapter, action, raw string) error {
	in := scheduleInput{Action: action}
	switch action {
	case "list":
		if strings.TrimSpace(raw) != "" {
			return commandReply(ctx, message, adapter, "用法：/schedule list")
		}
	case "status", "pause", "delete":
		fields := strings.Fields(raw)
		if len(fields) != 1 {
			return commandReply(ctx, message, adapter, "用法：/schedule "+action+" <id>")
		}
		in.ID, _ = strconv.ParseInt(strings.TrimPrefix(fields[0], "#"), 10, 64)
		if in.ID <= 0 {
			return commandReply(ctx, message, adapter, "用法：/schedule "+action+" <id>")
		}
	case "once", "every", "weekdays":
		fields := strings.Fields(raw)
		if len(fields) < 3 {
			return commandReply(ctx, message, adapter, "用法：/schedule "+action+" <时间或周期> <IANA 时区> <目标>")
		}
		in.Action, in.Goal = "create", strings.Join(fields[2:], " ")
		in.Spec = task.ScheduleSpec{Kind: action, Timezone: fields[1]}
		if action == "every" {
			in.Spec.Every = fields[0]
		} else {
			in.Spec.At = fields[0]
		}
	default:
		return commandReply(ctx, message, adapter, "用法：/schedule once/every/weekdays/list/status/pause/delete")
	}
	text, err := s.scheduleOperation(ctx, message, in)
	if err != nil {
		text = "计划操作失败：" + err.Error()
	}
	return commandReply(ctx, message, adapter, text)
}
