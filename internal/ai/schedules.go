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
	"unicode"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/task"
)

const scheduleUsage = "用法：!schedule once <RFC3339时间> <时区> [model=模型] <目标> | every <周期如1h> <时区> [model=模型] <目标> | weekdays <HH:MM> <时区> [model=模型] <目标> | cron \"<分 时 日 月 周>\" <时区> [model=模型] <目标> | edit <ID> <once|every|weekdays|cron> <时间或周期> <时区> [model=模型] <目标> | list | status/run/resume/pause/delete <ID>"

type scheduleInput struct {
	Action string            `json:"action"`
	ID     int64             `json:"id,omitempty"`
	Spec   task.ScheduleSpec `json:"spec,omitempty"`
	Goal   string            `json:"goal,omitempty"`
	// Model 是可选模型 ID；创建时冻结到请求快照，编辑时省略表示沿用原模型。
	Model string `json:"model,omitempty"`
	// Delivery 是投递目标：origin（默认）或 silent；命令入口用目标末尾的 --silent 表达。
	Delivery string `json:"delivery,omitempty"`
}

// chatToolNames 是不依赖执行后端的内置聊天工具；纯聊天计划只保留这些能力。
// delegate 只是把当前已授权的工具子集交给子代理，本身不产生外部副作用；
// saber_tweet 是平台写端口，执行时按 commands.admins 重新授权，不申请工作区。
var chatToolNames = map[string]bool{
	"saber_task": true, "saber_schedule": true, "saber_memory": true,
	"saber_history": true, "saber_skill": true, "saber_task_state": true,
	"saber_trace":      true,
	"saber_batch":      true,
	tweetToolName:      true,
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

// scheduleGoal 解析目标与投递选项：目标末尾的 --silent 表示静默投递，显式 delivery 未给时保持空值。
func scheduleGoal(raw, delivery string) (string, string, error) {
	goal := strings.TrimSpace(raw)
	if strings.HasSuffix(goal, "--silent") {
		goal = strings.TrimSpace(strings.TrimSuffix(goal, "--silent"))
		delivery = "silent"
	}
	switch delivery {
	case "", "origin", "silent":
	default:
		return "", "", fmt.Errorf("未知投递目标 %q，只支持 origin 或 silent", delivery)
	}
	if goal == "" {
		return "", "", errors.New("目标不能为空")
	}
	return goal, delivery, nil
}

// scheduleModel 解析计划模型；显式名称必须是已配置模型，空值沿用旧模型或全局默认。
func (s *Service) scheduleModel(name, fallback string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		// 旧快照与全局默认已在创建时通过 taskRequest 的提供商校验，这里不再重复否决。
		if fallback = strings.TrimSpace(fallback); fallback != "" {
			return fallback, nil
		}
		return s.GetModelRegistry().GetDefault(), nil
	}
	if _, ok := s.core.GetConfig().GetModelConfig(name); !ok {
		return "", fmt.Errorf("模型 %q 未配置，可用 /ai models 查看", name)
	}
	return name, nil
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
		model, err := s.scheduleModel(in.Model, "")
		if err != nil {
			return "", err
		}
		req, err := s.taskRequest(msg, model)
		if err != nil {
			return "", err
		}
		req.Messages = append(req.Messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: msg.Text})
		plan, err := s.tasks.CreateSchedule(ctx, msg, dir, req, in.Spec, task.ScheduleRequiresExecution(requires), task.WithScheduleDelivery(delivery))
		if err != nil {
			return "", err
		}
		return "计划已保存（重复消息返回原计划）：\n" + scheduleText(plan), nil
	case "edit":
		goal, delivery, err := scheduleGoal(in.Goal, in.Delivery)
		if err != nil {
			return "", err
		}
		// 读取原计划只为沿用模型与投递设置；最终权限由任务管理层按负责人/管理员校验。
		plan, err := s.tasks.GetSchedule(ctx, msg.Session, in.ID)
		if err != nil {
			return "", err
		}
		model, err := s.scheduleModel(in.Model, plan.Request.Model)
		if err != nil {
			return "", err
		}
		msg.Text, msg.Attachments = goal, nil
		req, err := s.taskRequest(msg, model)
		if err != nil {
			return "", err
		}
		req.Messages = append(req.Messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: msg.Text})
		var opts []task.ScheduleOption
		if delivery != "" {
			opts = append(opts, task.WithScheduleDelivery(delivery))
		}
		updated, err := s.tasks.EditSchedule(ctx, identity, in.ID, req, in.Spec, opts...)
		if err != nil {
			return "", err
		}
		return "计划已更新：\n" + scheduleText(updated), nil
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
	case "run":
		run, err := s.tasks.RunSchedule(ctx, identity, in.ID)
		if err != nil {
			return "", err
		}
		switch run.Outcome {
		case "created":
			return fmt.Sprintf("计划 #%d 已立即生成任务 #%d；下次自动执行时间不变。", in.ID, run.TaskID), nil
		case "skipped":
			return fmt.Sprintf("计划 #%d 未执行：%s", in.ID, run.Detail), nil
		default:
			return fmt.Sprintf("计划 #%d 已暂停：%s", in.ID, run.Detail), nil
		}
	case "resume":
		plan, err := s.tasks.ResumeSchedule(ctx, identity, in.ID)
		if err != nil {
			return "", err
		}
		return "计划已恢复：\n" + scheduleText(plan), nil
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
	// 目标以请求快照为准：编辑后消息快照仍保留原事件，只有请求承载新的目标。
	goal := s.Message.Text
	for i := len(s.Request.Messages) - 1; i >= 0; i-- {
		if s.Request.Messages[i].Role == openai.ChatMessageRoleUser {
			goal = s.Request.Messages[i].Content
			break
		}
	}
	switch s.Spec.Kind {
	case "every":
		rule = "每隔 " + s.Spec.Every
	case "weekdays":
		rule = "每个工作日 " + s.Spec.At
	case "once":
		rule = "一次性 " + s.Spec.At
	case "cron":
		rule = "cron " + s.Spec.Cron
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
	return fmt.Sprintf("计划 #%d：%s，%s（%s）\n负责人：%s\n目标：%s\n模型：%s\n汇报到：%s\n投递：%s\n执行：%s\n下次执行：%s\n上次任务：#%d", s.ID, rule, s.Spec.Timezone, s.Status, s.Message.SenderID, goal, s.Request.Model, s.Message.Session.Conversation, delivery, executionMode, next, s.LastTask)
}

// parseScheduleInput 把命令参数解析为计划操作；创建与编辑共用同一套规则解析。
func parseScheduleInput(action, raw string) (scheduleInput, error) {
	in := scheduleInput{Action: action}
	switch action {
	case "list":
		if strings.TrimSpace(raw) != "" {
			return in, errors.New("用法：/schedule list")
		}
	case "status", "pause", "delete", "run", "resume":
		id, ok := parseScheduleID(raw)
		if !ok {
			return in, fmt.Errorf("用法：/schedule %s <id>", action)
		}
		in.ID = id
	case "once", "every", "weekdays", "cron":
		spec, goal, model, err := parseScheduleRule(action, raw)
		if err != nil {
			return in, err
		}
		in.Action, in.Goal, in.Model, in.Spec = "create", goal, model, spec
	case "edit":
		idText, rest := commandField(raw)
		id, err := strconv.ParseInt(strings.TrimPrefix(idText, "#"), 10, 64)
		if err != nil || id <= 0 {
			return in, errors.New("用法：/schedule edit <id> <once|every|weekdays|cron> <时间或周期> <IANA 时区> [model=模型] <目标>")
		}
		kind, rest := commandField(rest)
		spec, goal, model, err := parseScheduleRule(kind, rest)
		if err != nil {
			return in, err
		}
		in.ID, in.Goal, in.Model, in.Spec = id, goal, model, spec
	default:
		return in, errors.New(scheduleUsage)
	}
	return in, nil
}

// parseScheduleID 读取单个计划编号，允许 # 前缀。
func parseScheduleID(raw string) (int64, bool) {
	fields := strings.Fields(raw)
	if len(fields) != 1 {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(fields[0], "#"), 10, 64)
	return id, err == nil && id > 0
}

// parseScheduleRule 解析“<时间或周期> <IANA 时区> [model=模型] <目标>”片段。
func parseScheduleRule(kind, raw string) (task.ScheduleSpec, string, string, error) {
	switch kind {
	case "once", "every", "weekdays", "cron":
	default:
		return task.ScheduleSpec{}, "", "", fmt.Errorf("未知计划规则 %q，只支持 once/every/weekdays/cron", kind)
	}
	when, rest := scheduleField(raw)
	zone, rest := scheduleField(rest)
	if when == "" || zone == "" || strings.TrimSpace(rest) == "" {
		return task.ScheduleSpec{}, "", "", fmt.Errorf("用法：/schedule %s <时间或周期> <IANA 时区> [model=模型] <目标>", kind)
	}
	spec := task.ScheduleSpec{Kind: kind, Timezone: zone}
	switch kind {
	case "every":
		spec.Every = when
	case "cron":
		// cron 表达式含空格，必须整段用引号包裹；段数在此提前拒绝，避免把后半段当成时区。
		if len(strings.Fields(when)) != 5 {
			return task.ScheduleSpec{}, "", "", errors.New(`cron 表达式必须用引号包裹 5 段，例如 !schedule cron "*/15 9-18 * * 1-5" Asia/Shanghai 目标`)
		}
		spec.Cron = when
	default:
		spec.At = when
	}
	goal, model, err := splitScheduleModel(rest)
	if err != nil {
		return task.ScheduleSpec{}, "", "", err
	}
	return spec, goal, model, nil
}

// scheduleField 读取一个参数；cron 表达式含空格，允许用单引号或双引号包裹。
func scheduleField(text string) (field, rest string) {
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	if text == "" {
		return "", ""
	}
	if quote := text[0]; quote == '"' || quote == '\'' {
		if end := strings.IndexByte(text[1:], quote); end >= 0 {
			return text[1 : end+1], strings.TrimLeftFunc(text[end+2:], unicode.IsSpace)
		}
	}
	return commandField(text)
}

// splitScheduleModel 提取目标前的 model= 选项；模型名不能为空，重复出现必须一致。
func splitScheduleModel(raw string) (goal, model string, err error) {
	for {
		field, rest := commandField(raw)
		if !strings.HasPrefix(field, "model=") {
			if goal = strings.TrimSpace(raw); goal == "" {
				return "", "", errors.New("目标不能为空")
			}
			return goal, model, nil
		}
		value := strings.TrimSpace(strings.TrimPrefix(field, "model="))
		if value == "" {
			return "", "", errors.New("model 选项不能为空，例如 model=openai.gpt-4o-mini")
		}
		if model != "" && model != value {
			return "", "", errors.New("只能指定一个 model 选项")
		}
		model = value
		raw = rest
	}
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
		Name: "saber_schedule", Description: "仅在用户要求定时执行或管理计划时使用。创建、编辑、立即执行、恢复、查看、暂停、删除本群持久化计划。身份、工作目录和汇报群由系统绑定；纯聊天计划不需要执行授权。必须明确时区；缺少时间或时区先询问。一次请求最多创建一个计划。可用 model 指定每个计划使用的模型，缺省使用全局默认。delivery 默认 origin（回复原消息），silent 表示不向聊天发送、结果只落库。当前时间：" + time.Now().UTC().Format(time.RFC3339),
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"action":   map[string]any{"type": "string", "enum": []string{"create", "edit", "list", "status", "run", "resume", "pause", "delete"}},
			"id":       map[string]any{"type": "integer", "minimum": 1},
			"goal":     map[string]any{"type": "string"},
			"model":    map[string]any{"type": "string", "description": "可选模型 ID，如 openai.gpt-4o-mini；创建时冻结，编辑时省略表示沿用原模型"},
			"delivery": map[string]any{"type": "string", "enum": []string{"origin", "silent"}, "description": "origin 回复原消息（默认）；silent 不向聊天投递，结果可用任务命令查询"},
			"spec": map[string]any{"type": "object", "properties": map[string]any{
				"kind":     map[string]any{"type": "string", "enum": []string{"once", "every", "weekdays", "cron"}},
				"timezone": map[string]any{"type": "string", "description": "IANA 时区，如 Asia/Shanghai"},
				"at":       map[string]any{"type": "string", "description": "once: 带偏移的 RFC3339；weekdays: HH:MM"},
				"every":    map[string]any{"type": "string", "description": "every: 周期 1m 到 8760h，例如 1h"},
				"cron":     map[string]any{"type": "string", "description": "cron: 5 段表达式（分 时 日 月 周），周 0 和 7 都表示周日，例如 \"*/15 9-18 * * 1-5\""},
			}, "required": []string{"kind", "timezone"}, "additionalProperties": false},
		}, "required": []string{"action"}, "additionalProperties": false},
	}}
}
