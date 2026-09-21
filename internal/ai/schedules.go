package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/task"
)

const scheduleUsage = "用法：!schedule once <RFC3339时间> <时区> <目标> | every <周期如1h> <时区> <目标> | weekdays <HH:MM> <时区> <目标> | list | status/pause/delete <ID>"

type scheduleInput struct {
	Action string            `json:"action"`
	ID     int64             `json:"id,omitempty"`
	Spec   task.ScheduleSpec `json:"spec,omitempty"`
	Goal   string            `json:"goal,omitempty"`
}

func (s *Service) authorizeSchedule(identity chat.Identity, dir string) error {
	if !s.IsEnabled() || s.executor == nil {
		return errors.New("AI 或执行权限未启用，计划暂停")
	}
	granted, err := s.executor.Workspace(identity)
	if err != nil {
		return err
	}
	if granted != dir {
		return errors.New("计划工作目录与当前授权不同，计划暂停")
	}
	return nil
}

type scheduleCommand struct{ service *Service }

func (c *scheduleCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	s := c.service
	adapter := matrix.NewChatAdapter(s.matrixService, nil, s.core.GetConfig().Media, false, nil)
	msg := adapter.Message(ctx, userID, roomID, "!schedule "+strings.Join(args, " "))
	in := scheduleInput{}
	if len(args) == 1 && args[0] == "list" {
		in.Action = "list"
	}
	if len(args) == 2 && (args[0] == "status" || args[0] == "pause" || args[0] == "delete") {
		if n, err := strconv.ParseInt(strings.TrimPrefix(args[1], "#"), 10, 64); err == nil && n > 0 {
			in.Action, in.ID = args[0], n
		}
	}
	if len(args) >= 4 && (args[0] == "once" || args[0] == "every" || args[0] == "weekdays") {
		in.Action, in.Goal = "create", strings.Join(args[3:], " ")
		in.Spec = task.ScheduleSpec{Kind: args[0], Timezone: args[2]}
		if args[0] == "every" {
			in.Spec.Every = args[1]
		} else {
			in.Spec.At = args[1]
		}
	}
	text, err := s.scheduleOperation(ctx, msg, in)
	if err != nil {
		text = "计划操作失败：" + err.Error()
	}
	_, err = adapter.Send(ctx, chat.Reply{Session: msg.Session, ReplyTo: msg.ID, Text: taskText(text)})
	return err
}

func (s *Service) scheduleOperation(ctx context.Context, msg chat.Message, in scheduleInput) (string, error) {
	if s.tasks == nil {
		return "", errors.New("任务服务未启用")
	}
	if err := msg.Validate(); err != nil {
		return "", err
	}
	identity := chat.Identity{Session: msg.Session, SenderID: msg.SenderID}
	switch in.Action {
	case "create":
		if s.executor == nil {
			return "", errors.New("未配置执行权限")
		}
		dir, err := s.executor.Workspace(identity)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(in.Goal) == "" {
			return "", errors.New("目标不能为空")
		}
		msg.Text, msg.Attachments = strings.TrimSpace(in.Goal), nil
		req := s.taskRequest(msg, s.GetModelRegistry().GetDefault())
		req.Messages = append(req.Messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: msg.Text})
		plan, err := s.tasks.CreateSchedule(ctx, msg, dir, req, in.Spec)
		if err != nil {
			return "", err
		}
		return "计划已保存（重复消息返回原计划）：\n" + scheduleText(plan), nil
	case "list":
		plans, err := s.tasks.ListSchedules(ctx, msg.Session)
		if err != nil {
			return "", err
		}
		if len(plans) == 0 {
			return "本群暂无定时计划", nil
		}
		var lines []string
		for _, p := range plans {
			lines = append(lines, scheduleText(p))
		}
		return strings.Join(lines, "\n\n"), nil
	case "status":
		plan, err := s.tasks.GetSchedule(ctx, msg.Session, in.ID)
		if err != nil {
			return "", err
		}
		runs, err := s.tasks.ScheduleRuns(ctx, msg.Session, in.ID)
		if err != nil {
			return "", err
		}
		text := scheduleText(plan) + fmt.Sprintf("\n工作目录：%s\n来源：%s\n话题：%s\n最近决策：%s", plan.WorkDir, plan.Message.ID, plan.Message.Session.Thread, plan.Reason)
		for _, r := range runs {
			text += fmt.Sprintf("\n%s：%s，任务 #%d，%s", r.Due.Format(time.RFC3339), r.Outcome, r.TaskID, r.Detail)
		}
		return text, nil
	case "pause", "delete":
		plan, err := s.tasks.ChangeSchedule(ctx, identity, in.ID, in.Action)
		if err != nil {
			return "", err
		}
		return scheduleText(plan) + "\n已运行的任务不会自动取消；需要时使用 !task cancel <ID>。", nil
	default:
		return scheduleUsage, nil
	}
}

func scheduleText(s task.Schedule) string {
	rule := s.Spec.At
	switch s.Spec.Kind {
	case "every":
		rule = "每隔 " + s.Spec.Every
	case "weekdays":
		rule = "每个工作日 " + s.Spec.At
	case "once":
		rule = "一次性 " + s.Spec.At
	}
	next := "无（计划不再自动触发）"
	if s.Status == "active" && !s.NextRun.IsZero() {
		loc, err := time.LoadLocation(s.Spec.Timezone)
		if err == nil {
			next = s.NextRun.In(loc).Format(time.RFC3339)
		}
	}
	return fmt.Sprintf("计划 #%d：%s，%s（%s）\n负责人：%s\n目标：%s\n汇报到：%s\n下次执行：%s\n上次任务：#%d", s.ID, rule, s.Spec.Timezone, s.Status, s.Message.SenderID, s.Message.Text, s.Message.Session.Conversation, next, s.LastTask)
}

func (s *Service) executeScheduleTool(ctx context.Context, args map[string]any) (any, error) {
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok || s.tasks == nil || s.executor == nil {
		return nil, errors.New("计划工具需要可信身份和任务服务")
	}
	if _, err := s.executor.Workspace(identity); err != nil {
		return nil, err
	}
	// 使用当前持久化任务的真实来源，不允许工具参数伪造负责人、群或工作目录。
	source, err := s.tasks.Get(ctx, identity.Session, task.ID(ctx))
	if err != nil {
		return nil, err
	}
	if source.Message.SenderID != identity.SenderID {
		return nil, errors.New("任务身份不匹配")
	}
	data, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var in scheduleInput
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&in); err != nil {
		return nil, err
	}
	return s.scheduleOperation(ctx, source.Message, in)
}

func scheduleTool() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: "saber_schedule", Description: "仅在用户要求定时执行或管理计划时使用。创建、查看、暂停、删除本群持久化计划。身份、工作目录和汇报群由系统绑定。必须明确时区；缺少时间或时区先询问。一次请求最多创建一个计划。当前时间：" + time.Now().UTC().Format(time.RFC3339),
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"create", "list", "status", "pause", "delete"}},
			"id":     map[string]any{"type": "integer", "minimum": 1},
			"goal":   map[string]any{"type": "string"},
			"spec": map[string]any{"type": "object", "properties": map[string]any{
				"kind":     map[string]any{"type": "string", "enum": []string{"once", "every", "weekdays"}},
				"timezone": map[string]any{"type": "string", "description": "IANA 时区，如 Asia/Shanghai"},
				"at":       map[string]any{"type": "string", "description": "once: 带偏移的 RFC3339；weekdays: HH:MM"},
				"every":    map[string]any{"type": "string", "description": "every: 周期 1m 到 8760h，例如 1h"},
			}, "required": []string{"kind", "timezone"}, "additionalProperties": false},
		}, "required": []string{"action"}, "additionalProperties": false},
	}}
}
