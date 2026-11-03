package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/task"
)

const scheduleUsage = "用法：!schedule once <RFC3339时间> <时区> <目标> | every <周期如1h> <时区> <目标> | weekdays <HH:MM> <时区> <目标> | list | status/pause/delete <ID>"

type scheduleInput struct {
	Action string            `json:"action"`
	ID     int64             `json:"id,omitempty"`
	Spec   task.ScheduleSpec `json:"spec,omitempty"`
	Goal   string            `json:"goal,omitempty"`
	// Delivery 是投递目标：origin（默认）或 silent；命令入口用目标末尾的 --silent 表达。
	Delivery string `json:"delivery,omitempty"`
}

// chatToolNames 是不依赖执行后端的内置聊天工具；纯聊天计划只保留这些能力。
// delegate 只是把当前已授权的工具子集交给子代理，本身不产生外部副作用。
var chatToolNames = map[string]bool{
	"saber_task": true, "saber_schedule": true, "saber_memory": true,
	"saber_history": true, "saber_skill": true, "saber_task_state": true,
	agent.DelegateTool: true,
}

// scheduleNeedsExecution 判断当前授权下的工具集合是否包含执行或 MCP 工具。
//
// 本地执行工具直接视为需要授权；内置聊天工具不需要；其余名称一律视为 MCP 工具，需要授权。
// 判断结果在创建时冻结，避免以后授权变化让纯聊天计划意外获得执行能力。
func scheduleNeedsExecution(tools []openai.Tool) bool {
	for _, tool := range tools {
		if tool.Function == nil {
			continue
		}
		if execution.LocalTool(tool.Function.Name) {
			// 本地执行工具本身就需要工作区授权。
			return true
		}
		if chatToolNames[tool.Function.Name] {
			continue
		}
		// 其余工具一律视为 MCP 工具，都需要执行授权。
		return true
	}
	return false
}

// filterChatTools 在运行时再次裁剪工具集合：纯聊天计划不会因创建后新获得的授权拿到执行能力。
func filterChatTools(tools []openai.Tool) []openai.Tool {
	var kept []openai.Tool
	for _, tool := range tools {
		if tool.Function != nil && chatToolNames[tool.Function.Name] {
			kept = append(kept, tool)
		}
	}
	return kept
}

// scheduleGoal 解析创建参数：目标末尾的 --silent 与 delivery 字段都表示静默投递，冲突时静默优先。
func scheduleGoal(raw, delivery string) (string, string, error) {
	goal := strings.TrimSpace(raw)
	if strings.HasSuffix(goal, "--silent") {
		goal = strings.TrimSpace(strings.TrimSuffix(goal, "--silent"))
		delivery = "silent"
	}
	switch delivery {
	case "":
		delivery = "origin"
	case "origin", "silent":
	default:
		return "", "", fmt.Errorf("未知投递目标 %q，只支持 origin 或 silent", delivery)
	}
	if goal == "" {
		return "", "", errors.New("目标不能为空")
	}
	return goal, delivery, nil
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

func (s *Service) scheduleOperation(ctx context.Context, msg chat.Message, in scheduleInput) (string, error) {
	if s.tasks == nil {
		return "", errors.New("任务服务未启用")
	}
	if err := msg.Validate(); err != nil {
		return "", err
	}
	identity := chat.Identity{Session: msg.Session, SenderID: msg.SenderID, Direct: msg.Direct}
	switch in.Action {
	case "create":
		goal, delivery, err := scheduleGoal(in.Goal, in.Delivery)
		if err != nil {
			return "", err
		}
		// 按当前授权筛选工具，据此冻结计划是否需要执行授权；纯聊天计划不申请工作区。
		toolCtx := chat.WithIdentity(ctx, identity)
		granted := ""
		if s.executor != nil {
			if dir, grantErr := s.executor.Workspace(identity); grantErr == nil {
				granted = dir
				toolCtx = execution.WithTask(toolCtx, 0, dir)
			}
		}
		tools, _ := s.toolExecutor.PrepareTools(toolCtx)
		requires := scheduleNeedsExecution(tools)
		dir := s.taskDir
		if requires {
			if granted == "" {
				return "", errors.New("计划包含需要执行授权的工具，但当前身份没有可用工作区")
			}
			dir = granted
		}
		msg.Text, msg.Attachments = goal, nil
		req, err := s.taskRequest(msg, s.GetModelRegistry().GetDefault())
		if err != nil {
			return "", err
		}
		req.Messages = append(req.Messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: msg.Text})
		plan, err := s.tasks.CreateSchedule(ctx, msg, dir, req, in.Spec, task.ScheduleRequiresExecution(requires), task.WithScheduleDelivery(delivery))
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
	delivery := "回复原消息"
	if s.Delivery == "silent" {
		delivery = "静默（不发送，结果落库可用 !task status/logs 查询）"
	}
	executionMode := "需要执行授权"
	if !s.RequiresExecution {
		executionMode = "纯聊天（不申请执行授权，运行时只保留聊天工具）"
	}
	return fmt.Sprintf("计划 #%d：%s，%s（%s）\n负责人：%s\n目标：%s\n汇报到：%s\n投递：%s\n执行：%s\n下次执行：%s\n上次任务：#%d", s.ID, rule, s.Spec.Timezone, s.Status, s.Message.SenderID, s.Message.Text, s.Message.Session.Conversation, delivery, executionMode, next, s.LastTask)
}

func (s *Service) executeScheduleTool(ctx context.Context, args map[string]any) (any, error) {
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok || s.tasks == nil {
		return nil, errors.New("计划工具需要可信身份和任务服务")
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
		Name: "saber_schedule", Description: "仅在用户要求定时执行或管理计划时使用。创建、查看、暂停、删除本群持久化计划。身份、工作目录和汇报群由系统绑定；纯聊天计划不需要执行授权。必须明确时区；缺少时间或时区先询问。一次请求最多创建一个计划。delivery 默认 origin（回复原消息），silent 表示不向聊天发送、结果只落库。当前时间：" + time.Now().UTC().Format(time.RFC3339),
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"action":   map[string]any{"type": "string", "enum": []string{"create", "list", "status", "pause", "delete"}},
			"id":       map[string]any{"type": "integer", "minimum": 1},
			"goal":     map[string]any{"type": "string"},
			"delivery": map[string]any{"type": "string", "enum": []string{"origin", "silent"}, "description": "origin 回复原消息（默认）；silent 不向聊天投递，结果可用任务命令查询"},
			"spec": map[string]any{"type": "object", "properties": map[string]any{
				"kind":     map[string]any{"type": "string", "enum": []string{"once", "every", "weekdays"}},
				"timezone": map[string]any{"type": "string", "description": "IANA 时区，如 Asia/Shanghai"},
				"at":       map[string]any{"type": "string", "description": "once: 带偏移的 RFC3339；weekdays: HH:MM"},
				"every":    map[string]any{"type": "string", "description": "every: 周期 1m 到 8760h，例如 1h"},
			}, "required": []string{"kind", "timezone"}, "additionalProperties": false},
		}, "required": []string{"action"}, "additionalProperties": false},
	}}
}
