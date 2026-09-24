package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/command"
	"rua.plus/saber/internal/matrix"
)

func (s *appState) commandCapabilities(platform string) map[string]bool {
	return map[string]bool{"file": platform == "matrix", "image": platform == "matrix"}
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
		once := id == "ai.clear" || id == "ai.switch" || id == "task.cancel" || id == "schedule.pause" || id == "schedule.delete" || id == "meme" || strings.HasPrefix(id, "persona.") && id != "persona.list" && id != "persona.status" && id != "persona.help"
		return r.Register(command.Definition{Descriptor: command.Descriptor{ID: id, Path: path, Description: description, Arguments: args, Scope: scope}, Aliases: aliases, Capability: capability, Authorize: authorize, Once: once, Handle: handler})
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
	}
	for _, def := range defs {
		if err := add(def.id, []string{def.name}, def.description, "conversation", nil, nil, "", nil, def.handler); err != nil {
			return err
		}
	}
	if aiSvc := s.services.aiService; aiSvc != nil {
		r.SetReceipts(aiSvc.Tasks())
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
				return reply(ctx, m, a, "用法：/task run/list/status/cancel/logs")
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
				return reply(ctx, m, a, "用法：/schedule once/every/weekdays/list/status/pause/delete")
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
			message.Direct = cs.IsDirectChat(ctx, room)
			return r.Dispatch(ctx, message, adapter, s.commandCapabilities("matrix"))
		})
	}
	return nil
}
