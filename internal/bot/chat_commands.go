package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/command"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/platform"
)

func (s *appState) commandCapabilities(platformName string) map[string]bool {
	p, ok := s.services.platforms.Lookup(platformName)
	if !ok {
		return nil
	}
	delivery, ok := p.(platform.TaskDelivery)
	if !ok {
		return nil
	}
	adapter := delivery.DeliveryAdapter()
	_, file := adapter.(chat.FileAdapter)
	_, image := adapter.(chat.ImageAdapter)
	return map[string]bool{"file": file, "image": image}
}

func (s *appState) handleChat(ctx context.Context, message chat.Message, adapter chat.Adapter) (agent.Result, error) {
	if s.services.commandRegistry != nil {
		if handled, err := s.services.commandRegistry.Dispatch(ctx, message, adapter, s.commandCapabilities(message.Session.Platform)); handled || err != nil {
			return agent.Result{}, err
		}
	}
	if strings.HasPrefix(message.Text, "//") {
		message.Text = message.Text[1:]
		message.ControlText = &message.Text
	}
	if s.services.aiService == nil {
		return agent.Result{}, nil
	}
	return s.services.aiService.HandleChat(ctx, message, adapter)
}

func (s *appState) buildCommands() error {
	r := command.New()
	add := func(id string, path []string, description, scope string, args []command.Argument, aliases [][]string, capability string, authorize func(chat.Identity) bool, handler command.Handler) error {
		return r.Register(command.Definition{Descriptor: command.Descriptor{ID: id, Path: path, Description: description, Arguments: args, Scope: scope}, Aliases: aliases, Capability: capability, Authorize: authorize, Once: onceCommand(id), Handle: handler})
	}
	reply := func(ctx context.Context, message chat.Message, adapter chat.Adapter, text string) error {
		_, err := adapter.Send(ctx, chat.Reply{Session: message.Session, ReplyTo: message.ID, Text: text})
		return err
	}
	defs := []struct {
		id, name, description string
		handler               command.Handler
	}{
		{"ping", "ping", "检查机器人是否在线", func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
			if strings.TrimSpace(raw) != "" {
				return reply(ctx, m, a, "用法：/ping")
			}
			return reply(ctx, m, a, "🏓 Pong!")
		}},
		{"help", "help", "列出可用命令", func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
			if strings.TrimSpace(raw) != "" {
				return reply(ctx, m, a, "用法：/help")
			}
			prefix := "/"
			if m.Session.Platform == "matrix" {
				prefix = "!"
			}
			var lines []string
			for _, def := range r.Describe(s.commandCapabilities(m.Session.Platform)) {
				lines = append(lines, fmt.Sprintf("%s%s - %s", prefix, strings.Join(def.Path, " "), def.Description))
			}
			return reply(ctx, m, a, "可用命令：\n"+strings.Join(lines, "\n"))
		}},
		{"version", "version", "显示版本信息", func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
			if strings.TrimSpace(raw) != "" {
				return reply(ctx, m, a, "用法：/version")
			}
			return reply(ctx, m, a, fmt.Sprintf("版本: %s\n提交: %s\n分支: %s\n构建时间: %s", s.info.Version, s.info.GitCommit, s.info.GitBranch, s.info.BuildTime))
		}},
		{"status", "status", "查看真实能力、后端与安全提示", func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
			if strings.TrimSpace(raw) != "" {
				return reply(ctx, m, a, "用法：/status")
			}
			return reply(ctx, m, a, s.statusReport(m))
		}},
	}
	for _, def := range defs {
		if err := add(def.id, []string{def.name}, def.description, "conversation", nil, nil, "", nil, def.handler); err != nil {
			return err
		}
	}
	if s.services.memoryService == nil {
		disabled := func(feature string) command.Handler {
			return func(ctx context.Context, m chat.Message, a chat.Adapter, _ string) error {
				return reply(ctx, m, a, "需启用 memory.enabled 后使用"+feature+"。")
			}
		}
		for _, feature := range []struct{ id, name string }{{"memory.disabled", "memory"}, {"skill.disabled", "skill"}} {
			if err := r.Register(command.Definition{Descriptor: command.Descriptor{ID: feature.id, Path: []string{feature.name}, Description: "需启用 memory.enabled", Scope: "conversation"}, Hidden: true, Handle: disabled(feature.name)}); err != nil {
				return err
			}
		}
	}
	if aiSvc := s.services.aiService; aiSvc != nil {
		if tasks := aiSvc.Tasks(); tasks != nil {
			r.SetReceipts(tasks)
		}
		// 未命中任何命令路径的 `/词` 只在该词对应当前可信空间的技能时交给 AI；
		// 其余正文仍由注册表回复「未知命令」，不会被静默吞掉。
		r.SetFallback(func(ctx context.Context, message chat.Message, reply chat.Adapter) (bool, error) {
			if !aiSvc.CanInvokeSkill(ctx, message) {
				return false, nil
			}
			return true, aiSvc.InvokeSkillCommand(ctx, message, reply)
		})
		// 审批命令与 Matrix、terminal/violet 共用同一注册表；身份只取自消息入口。
		for _, op := range []struct {
			id, name, description string
			approve               bool
		}{
			{"exec.approve", "approve", "批准一次危险命令审批码", true},
			{"exec.deny", "deny", "拒绝一次危险命令审批码", false},
		} {
			if err := add(op.id, []string{op.name}, op.description, "conversation",
				[]command.Argument{{Name: "code", Type: "string", Required: true}},
				[][]string{{"exec-" + op.name}}, "", nil,
				func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
					text, err := aiSvc.ApproveCommand(ctx, chat.Identity{Session: m.Session, SenderID: m.SenderID, Direct: m.Direct}, raw, op.approve)
					if err != nil {
						// 错误回执不回显命令正文。
						return reply(ctx, m, a, "审批未生效："+err.Error())
					}
					return reply(ctx, m, a, text)
				}); err != nil {
				return err
			}
		}
		admin := s.cfg.Commands.IsAdmin
		writer := func(identity chat.Identity) bool { return identity.Direct || s.cfg.Commands.CanWriteSession(identity) }
		if err := add("ai.chat", []string{"ai"}, "向 Saber 提问", "conversation", []command.Argument{{Name: "question", Type: "string", Required: true}}, nil, "", nil,
			func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
				return aiSvc.ChatCommand(ctx, m, a, aiSvc.GetModelRegistry().GetDefault(), raw)
			}); err != nil {
			return err
		}
		for _, sub := range []struct {
			name, description string
			authorize         func(chat.Identity) bool
			args              []command.Argument
		}{
			{"clear", "清除当前会话上下文", writer, nil},
			{"context", "查看当前有效上下文", nil, nil},
			{"models", "查看可用模型", nil, nil},
			{"current", "查看全局默认模型", nil, nil},
			{"switch", "切换全局默认模型（全局）", admin, []command.Argument{{Name: "model-id", Type: "string", Required: true}}},
		} {
			action := sub.name
			scope := "conversation"
			if action == "switch" {
				scope = "global"
			}
			if err := add("ai."+action, []string{"ai", action}, sub.description, scope, sub.args, [][]string{{"ai-" + action}}, "", sub.authorize,
				func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
					return aiSvc.AICommand(ctx, m, a, action, raw)
				}); err != nil {
				return err
			}
		}
		for model := range s.cfg.AI.Models {
			modelName := model
			if err := add("ai.model."+modelName, []string{"ai-" + modelName}, "使用指定模型提问", "conversation", []command.Argument{{Name: "question", Type: "string", Required: true}}, nil, "", nil,
				func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
					return aiSvc.ChatCommand(ctx, m, a, modelName, raw)
				}); err != nil {
				slog.Warn("模型快捷命令不可注册，仍可通过 /ai 发送普通问题", "model", modelName, "error", err)
			}
		}
		for _, sub := range []struct {
			name, description, capability string
			args                          []command.Argument
		}{
			{"run", "运行后台任务", "", []command.Argument{{Name: "goal", Type: "string", Required: true}}},
			{"list", "列出当前会话任务", "", nil},
			{"status", "查看任务状态", "", []command.Argument{{Name: "id", Type: "integer", Required: true}}},
			{"cancel", "取消自己或获授权的任务", "", []command.Argument{{Name: "id", Type: "integer", Required: true}}},
			{"redirect", "补充运行中任务的要求，下一轮生效", "", []command.Argument{{Name: "id", Type: "integer", Required: true}, {Name: "text", Type: "string", Required: true}}},
			{"logs", "下载任务完整日志", "file", []command.Argument{{Name: "id", Type: "integer", Required: true}}},
		} {
			action := sub.name
			if err := add("task."+action, []string{"task", action}, sub.description, "conversation", sub.args, nil, sub.capability, nil,
				func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
					return aiSvc.TaskCommand(ctx, m, a, action, raw)
				}); err != nil {
				return err
			}
		}
		if err := add("task.help", []string{"task"}, "任务命令用法", "conversation", nil, nil, "", nil,
			func(ctx context.Context, m chat.Message, a chat.Adapter, _ string) error {
				return reply(ctx, m, a, "用法：/task run/list/status/cancel/redirect/logs")
			}); err != nil {
			return err
		}
		for _, sub := range []struct {
			name, description string
			args              []command.Argument
		}{
			{"once", "创建一次性计划", []command.Argument{{Name: "time", Type: "string", Required: true}, {Name: "timezone", Type: "string", Required: true}, {Name: "goal", Type: "string", Required: true}}},
			{"every", "创建周期计划", []command.Argument{{Name: "interval", Type: "string", Required: true}, {Name: "timezone", Type: "string", Required: true}, {Name: "goal", Type: "string", Required: true}}},
			{"weekdays", "创建工作日计划", []command.Argument{{Name: "time", Type: "string", Required: true}, {Name: "timezone", Type: "string", Required: true}, {Name: "goal", Type: "string", Required: true}}},
			{"cron", "创建 cron 计划（5 段表达式，可用引号包裹）", []command.Argument{{Name: "expression", Type: "string", Required: true}, {Name: "timezone", Type: "string", Required: true}, {Name: "goal", Type: "string", Required: true}}},
			{"edit", "编辑计划规则、目标或模型", []command.Argument{{Name: "id", Type: "integer", Required: true}, {Name: "rule", Type: "string", Required: true}, {Name: "time", Type: "string", Required: true}, {Name: "timezone", Type: "string", Required: true}, {Name: "goal", Type: "string", Required: true}}},
			{"run", "立即执行一次计划，不改下次时间", []command.Argument{{Name: "id", Type: "integer", Required: true}}},
			{"resume", "恢复已暂停的计划并重算下次时间", []command.Argument{{Name: "id", Type: "integer", Required: true}}},
			{"list", "列出当前会话计划", nil},
			{"status", "查看计划状态", []command.Argument{{Name: "id", Type: "integer", Required: true}}},
			{"pause", "暂停自己或获授权的计划", []command.Argument{{Name: "id", Type: "integer", Required: true}}},
			{"delete", "删除自己或获授权的计划", []command.Argument{{Name: "id", Type: "integer", Required: true}}},
		} {
			action := sub.name
			if err := add("schedule."+action, []string{"schedule", action}, sub.description, "conversation", sub.args, nil, "", nil,
				func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
					return aiSvc.ScheduleCommand(ctx, m, a, action, raw)
				}); err != nil {
				return err
			}
		}
		if err := add("schedule.help", []string{"schedule"}, "计划命令用法", "conversation", nil, nil, "", nil,
			func(ctx context.Context, m chat.Message, a chat.Adapter, _ string) error {
				return reply(ctx, m, a, "用法：/schedule once/every/weekdays/cron/edit/run/resume/list/status/pause/delete")
			}); err != nil {
			return err
		}
		if s.services.mcpManager != nil && s.services.mcpManager.IsEnabled() {
			if err := add("mcp.list", []string{"mcp", "list"}, "列出 MCP 服务器", "conversation", nil, nil, "", nil,
				func(ctx context.Context, m chat.Message, a chat.Adapter, _ string) error {
					var lines []string
					for _, srv := range s.services.mcpManager.ListServers() {
						lines = append(lines, fmt.Sprintf("- %s (%s)", srv.Name, srv.Type))
					}
					return reply(ctx, m, a, "MCP 服务器：\n"+strings.Join(lines, "\n"))
				}); err != nil {
				return err
			}
		}
		if personaSvc := s.services.personaService; personaSvc != nil {
			for _, sub := range []struct {
				name, description, scope string
				aliases                  [][]string
				args                     []command.Argument
				authorize                func(chat.Identity) bool
			}{
				{"list", "列出可用人格", "conversation", [][]string{{"persona", "ls"}}, nil, nil},
				{"status", "查看当前会话人格", "conversation", [][]string{{"persona", "show"}}, nil, nil},
				{"set", "设置当前会话人格", "conversation", nil, []command.Argument{{Name: "id", Type: "string", Required: true}}, writer},
				{"clear", "清除当前会话人格", "conversation", [][]string{{"persona", "reset"}}, nil, writer},
				{"new", "创建共享人格（全局）", "global", [][]string{{"persona", "create"}}, []command.Argument{{Name: "id", Type: "string", Required: true}, {Name: "name", Type: "string", Required: true}, {Name: "prompt", Type: "string", Required: true}, {Name: "description", Type: "string", Required: true}}, admin},
				{"del", "删除共享人格（全局）", "global", [][]string{{"persona", "delete"}, {"persona", "rm"}}, []command.Argument{{Name: "id", Type: "string", Required: true}}, admin},
			} {
				action := sub.name
				if err := add("persona."+action, []string{"persona", action}, sub.description, sub.scope, sub.args, sub.aliases, "", sub.authorize,
					func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
						return personaSvc.ChatCommand(ctx, m, a, action, raw)
					}); err != nil {
					return err
				}
			}
			if err := add("persona.help", []string{"persona"}, "人格命令用法", "conversation", nil, [][]string{{"persona", "help"}}, "", nil,
				func(ctx context.Context, m chat.Message, a chat.Adapter, _ string) error {
					return reply(ctx, m, a, "用法：/persona list/set/clear/status/new/del")
				}); err != nil {
				return err
			}
		}
		if memeSvc := s.services.memeService; memeSvc != nil {
			if err := add("meme", []string{"meme"}, "搜索并发送梗图", "conversation", []command.Argument{{Name: "query", Type: "string", Required: true}}, nil, "image", nil,
				func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
					return memeSvc.ChatCommand(ctx, m, a, raw)
				}); err != nil {
				return err
			}
		}
		if s.services.memoryService != nil {
			for _, sub := range []struct {
				name, description string
				args              []command.Argument
				authorize         func(chat.Identity) bool
			}{
				{"list", "查看当前空间记忆条目", nil, nil},
				{"status", "查看当前空间记忆容量与状态", nil, nil},
				{"add", "保存当前空间记忆；群内无写权限时形成建议", []command.Argument{{Name: "content", Type: "string", Required: true}}, nil},
				{"edit", "修改当前空间记忆", []command.Argument{{Name: "id", Type: "integer", Required: true}, {Name: "content", Type: "string", Required: true}}, writer},
				{"forget", "删除当前空间记忆", []command.Argument{{Name: "id", Type: "integer", Required: true}}, writer},
				{"pending", "查看当前空间待确认建议", nil, nil},
				{"approve", "确认当前空间待确认建议", []command.Argument{{Name: "id", Type: "integer", Required: true}}, writer},
				{"reject", "拒绝当前空间待确认建议", []command.Argument{{Name: "id", Type: "integer", Required: true}}, writer},
				{"pause", "暂停当前空间记忆自动学习", nil, writer},
				{"resume", "恢复当前空间记忆自动学习", nil, writer},
				{"profile", "查看当前空间画像；profile refresh 立即重建", nil, nil},
			} {
				action := sub.name
				if err := add("memory."+action, []string{"memory", action}, sub.description, "conversation", sub.args, nil, "", sub.authorize,
					func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
						return aiSvc.MemoryCommand(ctx, m, a, action, raw)
					}); err != nil {
					return err
				}
			}
			if err := add("memory.help", []string{"memory"}, "记忆命令用法", "conversation", nil, nil, "", nil,
				func(ctx context.Context, m chat.Message, a chat.Adapter, _ string) error {
					return reply(ctx, m, a, "用法：/memory list/status/profile/add/edit/forget/pending/approve/reject/pause/resume")
				}); err != nil {
				return err
			}
			for _, sub := range []struct {
				name, description string
				args              []command.Argument
				authorize         func(chat.Identity) bool
			}{
				{"list", "查看当前空间可用技能列表", nil, nil},
				{"show", "查看指定技能完整正文", []command.Argument{{Name: "name", Type: "string", Required: true}}, nil},
				{"add", "新增技能；群内无写权限时形成建议", []command.Argument{{Name: "name", Type: "string", Required: true}, {Name: "description", Type: "string", Required: true}, {Name: "content", Type: "string", Required: true}}, nil},
				{"edit", "修改当前空间技能正文", []command.Argument{{Name: "name", Type: "string", Required: true}, {Name: "content", Type: "string", Required: true}}, writer},
				{"delete", "删除当前空间技能", []command.Argument{{Name: "name", Type: "string", Required: true}}, writer},
				{"pending", "查看当前空间待确认技能建议", nil, nil},
				{"approve", "确认当前空间待确认技能建议", []command.Argument{{Name: "id", Type: "integer", Required: true}}, writer},
				{"reject", "拒绝当前空间待确认技能建议", []command.Argument{{Name: "id", Type: "integer", Required: true}}, writer},
				{"import", "从 SKILL.md 目录导入技能；群内无写权限时形成建议", []command.Argument{{Name: "dir", Type: "string", Required: false}}, nil},
				{"export", "导出技能为 SKILL.md", []command.Argument{{Name: "name", Type: "string", Required: true}, {Name: "dir", Type: "string", Required: false}}, writer},
				{"files", "列出已配置外部目录中的技能（不写库）", nil, nil},
			} {
				action := sub.name
				if err := add("skill."+action, []string{"skill", action}, sub.description, "conversation", sub.args, nil, "", sub.authorize,
					func(ctx context.Context, m chat.Message, a chat.Adapter, raw string) error {
						return aiSvc.SkillCommand(ctx, m, a, action, raw)
					}); err != nil {
					return err
				}
			}
			if err := add("skill.help", []string{"skill"}, "技能命令用法", "conversation", nil, nil, "", nil,
				func(ctx context.Context, m chat.Message, a chat.Adapter, _ string) error {
					return reply(ctx, m, a, "用法：/skill list/show/add/edit/delete/pending/approve/reject/import/export/files")
				}); err != nil {
				return err
			}
		}
	}
	s.services.commandRegistry = r
	for _, platform := range s.services.platforms.Enabled(s.cfg) {
		if catalog, ok := platform.(interface{ SetCommands([]command.Descriptor) }); ok {
			catalog.SetCommands(r.Describe(s.commandCapabilities(platform.Name())))
		}
	}
	if cs := s.services.commandService; cs != nil {
		cs.SetCommandDispatcher(func(ctx context.Context, sender id.UserID, room id.RoomID, body string) (bool, error) {
			adapter := matrix.NewChatAdapter(cs, s.services.mediaService, s.cfg.Matrix.Media, false, nil)
			message := adapter.Message(ctx, sender, room, body)
			return r.Dispatch(ctx, message, adapter, s.commandCapabilities("matrix"))
		})
	}
	return nil
}

// statusReport 输出服务端真实能力与 local 后端风险，不读取其他用户会话。
func (s *appState) statusReport(message chat.Message) string {
	identity := chat.Identity{Session: message.Session, SenderID: message.SenderID, Direct: message.Direct}
	capabilities := map[string]ai.Capability{}
	if s.services.aiService != nil {
		capabilities = s.services.aiService.Capabilities(context.Background(), identity)
	} else {
		for _, key := range []string{"tasks", "trace_export", "batches", "execution", "file_read", "file_search", "processes", "memory", "skills", "mcp", "context_compaction", "adaptive_budget"} {
			capabilities[key] = ai.Capability{State: "disabled", Reason: "AI 服务未启用", Tools: []string{}}
		}
	}
	lines := []string{fmt.Sprintf("版本 %s｜提交 %s｜分支 %s", s.info.Version, s.info.GitCommit, s.info.GitBranch)}
	local := false
	for _, key := range []string{"tasks", "trace_export", "batches", "execution", "file_read", "file_search", "processes", "memory", "skills", "mcp", "context_compaction", "adaptive_budget"} {
		capability, ok := capabilities[key]
		if !ok {
			continue
		}
		line := key + ": " + capability.State
		if capability.Backend != "" {
			line += " (backend=" + capability.Backend + ", sandboxed=" + fmt.Sprint(capability.Sandboxed) + ")"
			local = local || capability.Backend == "local" && !capability.Sandboxed
		}
		if capability.Reason != "" {
			line += " — " + capability.Reason
		}
		if len(capability.Tools) > 0 {
			line += "｜工具: " + strings.Join(capability.Tools, ", ")
		}
		lines = append(lines, line)
	}
	if local {
		lines = append(lines, "风险：local 后端可读取 Saber 用户可访问的宿主文件与配置密钥；不要授予不可信群成员。")
	}
	return strings.Join(lines, "\n")
}

// onceCommand 标记有副作用的命令，重复投递只重试回执，不重做写入。
func onceCommand(id string) bool {
	switch id {
	case "ai.clear", "ai.switch", "task.cancel", "task.redirect", "schedule.pause", "schedule.delete", "schedule.edit", "schedule.run", "schedule.resume", "meme",
		"exec.approve", "exec.deny",
		"memory.add", "memory.edit", "memory.forget", "memory.approve", "memory.reject", "memory.pause", "memory.resume",
		"skill.add", "skill.edit", "skill.delete", "skill.approve", "skill.reject":
		return true
	}
	return strings.HasPrefix(id, "persona.") && id != "persona.list" && id != "persona.status" && id != "persona.help"
}
