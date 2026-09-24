package persona

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"rua.plus/saber/internal/chat"
)

// ChatCommand 使用完整会话键执行人格命令，平台只负责提供可信身份与回执。
func (s *Service) ChatCommand(ctx context.Context, message chat.Message, adapter chat.Adapter, action, raw string) error {
	reply := func(text string) error {
		_, err := adapter.Send(ctx, chat.Reply{Session: message.Session, ReplyTo: message.ID, Text: text})
		return err
	}
	args, err := quotedArgs(raw)
	if err != nil {
		return reply("参数错误：" + err.Error())
	}
	if (action == "list" || action == "status") && len(args) != 0 {
		return reply("用法：/persona " + action)
	}
	switch action {
	case "list":
		personas := s.List()
		slices.SortFunc(personas, func(a, b *Persona) int { return strings.Compare(a.ID, b.ID) })
		lines := make([]string, 0, len(personas))
		for _, p := range personas {
			lines = append(lines, fmt.Sprintf("- `%s` %s：%s", p.ID, p.Name, p.Description))
		}
		return reply("可用人格：\n" + strings.Join(lines, "\n"))
	case "status":
		if p := s.GetSessionPersona(message.Session); p != nil {
			return reply(fmt.Sprintf("当前会话人格：`%s` %s：%s", p.ID, p.Name, p.Description))
		}
		return reply("当前会话未设置人格")
	case "set":
		if len(args) != 1 {
			return reply("用法：/persona set <id>")
		}
		if err := s.SetSessionPersona(ctx, message.Session, strings.ToLower(args[0])); err != nil {
			return reply("设置人格失败：" + err.Error())
		}
		return reply("当前会话人格已设置为 `" + strings.ToLower(args[0]) + "`")
	case "clear":
		if len(args) != 0 {
			return reply("用法：/persona clear")
		}
		if err := s.SetSessionPersona(ctx, message.Session, ""); err != nil {
			return err
		}
		return reply("当前会话人格已清除")
	case "new":
		if len(args) != 4 {
			return reply(`用法：/persona new <id> "<name>" "<prompt>" "<description>"`)
		}
		if err := s.Create(strings.ToLower(args[0]), args[1], args[2], args[3]); err != nil {
			return reply("创建人格失败：" + err.Error())
		}
		return reply("共享人格 `" + strings.ToLower(args[0]) + "` 已创建")
	case "del":
		if len(args) != 1 {
			return reply("用法：/persona del <id>")
		}
		if err := s.Delete(strings.ToLower(args[0])); err != nil {
			return reply("删除人格失败：" + err.Error())
		}
		return reply("共享人格 `" + strings.ToLower(args[0]) + "` 已删除")
	default:
		return reply("用法：/persona list/set/clear/status/new/del")
	}
}

func quotedArgs(raw string) ([]string, error) {
	var args []string
	var current strings.Builder
	quoted, started, escaped := false, false, false
	for _, r := range raw {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\' && quoted:
			escaped = true
		case r == '"':
			quoted, started = !quoted, true
		case unicode.IsSpace(r) && !quoted:
			if started {
				args = append(args, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(r)
			started = true
		}
	}
	if quoted || escaped {
		return nil, errors.New("引号未闭合")
	}
	if started {
		args = append(args, current.String())
	}
	return args, nil
}
