package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/memory"
)

// SkillCommand 执行与平台无关的程序性经验（技能）命令，身份与作用域取自可信消息。
func (s *Service) SkillCommand(ctx context.Context, message chat.Message, adapter chat.Adapter, action, raw string) error {
	if s.memory == nil {
		return commandReply(ctx, message, adapter, "长期记忆与技能未启用")
	}
	identity := chat.Identity{Session: message.Session, SenderID: message.SenderID, Direct: message.Direct}

	switch action {
	case "list":
		if strings.TrimSpace(raw) != "" {
			return commandReply(ctx, message, adapter, "用法：/skill list")
		}
		skills, err := s.memory.ListSkills(ctx, identity)
		if err != nil {
			return commandReply(ctx, message, adapter, "读取技能列表失败："+err.Error())
		}
		if len(skills) == 0 {
			return commandReply(ctx, message, adapter, "当前空间暂无可用技能。可通过 /skill add <名称> <描述> <内容> 创建。")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "可用技能列表（共 %d 个）：", len(skills))
		for _, sk := range skills {
			fmt.Fprintf(&b, "\n- %s (v%d)：%s", sk.Name, sk.Version, sk.Description)
		}
		return commandReply(ctx, message, adapter, b.String())

	case "show":
		name := strings.TrimSpace(raw)
		if name == "" {
			return commandReply(ctx, message, adapter, "用法：/skill show <技能名称>")
		}
		entry, err := s.memory.GetSkill(ctx, identity, name)
		if err != nil {
			return commandReply(ctx, message, adapter, "查看技能失败："+err.Error())
		}
		var b strings.Builder
		fmt.Fprintf(&b, "【技能：%s (v%d)】\n适用场景：%s\n更新时间：%s\n\n%s",
			entry.Name, entry.Version, entry.Description, entry.UpdatedAt.Format("2006-01-02 15:04:05"), entry.Content)
		return commandReply(ctx, message, adapter, b.String())

	case "add":
		name, desc, content, err := parseSkillAddArgs(raw)
		if err != nil {
			return commandReply(ctx, message, adapter, "用法：/skill add <名称> <描述> <内容...>")
		}
		mut, err := s.memory.AddSkill(ctx, identity, name, desc, content, memory.Source{})
		if err != nil {
			return commandReply(ctx, message, adapter, "保存技能失败："+err.Error())
		}
		return commandReply(ctx, message, adapter, skillMutationText(mut))

	case "edit":
		name, newContent := commandField(raw)
		name = strings.TrimSpace(name)
		newContent = strings.TrimSpace(newContent)
		if name == "" || newContent == "" {
			return commandReply(ctx, message, adapter, "用法：/skill edit <名称> <新内容...>")
		}
		current, err := s.memory.GetSkill(ctx, identity, name)
		if err != nil {
			return commandReply(ctx, message, adapter, "修改技能失败："+err.Error())
		}
		mut, err := s.memory.ReplaceSkill(ctx, identity, name, current.Version, current.Description, newContent, memory.Source{})
		if err != nil {
			return commandReply(ctx, message, adapter, "修改技能失败："+err.Error())
		}
		return commandReply(ctx, message, adapter, skillMutationText(mut))

	case "delete":
		name := strings.TrimSpace(raw)
		if name == "" {
			return commandReply(ctx, message, adapter, "用法：/skill delete <名称>")
		}
		current, err := s.memory.GetSkill(ctx, identity, name)
		if err != nil {
			return commandReply(ctx, message, adapter, "删除技能失败："+err.Error())
		}
		mut, err := s.memory.RemoveSkill(ctx, identity, name, current.Version, memory.Source{})
		if err != nil {
			return commandReply(ctx, message, adapter, "删除技能失败："+err.Error())
		}
		if mut.Suggested {
			return commandReply(ctx, message, adapter, skillMutationText(mut))
		}
		return commandReply(ctx, message, adapter, fmt.Sprintf("技能 %s 已成功删除。", name))

	case "pending":
		if strings.TrimSpace(raw) != "" {
			return commandReply(ctx, message, adapter, "用法：/skill pending")
		}
		changes, err := s.memory.PendingSkillChanges(ctx, identity)
		if err != nil {
			return commandReply(ctx, message, adapter, "读取待确认技能建议失败："+err.Error())
		}
		if len(changes) == 0 {
			return commandReply(ctx, message, adapter, "当前空间没有待确认的技能建议")
		}
		var b strings.Builder
		b.WriteString("待确认技能建议：")
		for _, c := range changes {
			fmt.Fprintf(&b, "\n- #%d %s %s（由 %s 提出）：%s", c.ID, memoryActionLabel(c.Action), c.Name, c.Proposer, c.Description)
		}
		return commandReply(ctx, message, adapter, b.String())

	case "approve", "reject":
		changeID, err := parseEntryID(strings.TrimSpace(raw))
		if err != nil {
			return commandReply(ctx, message, adapter, "用法：/skill "+action+" <ID>")
		}
		if action == "approve" {
			skill, err := s.memory.ApproveSkillChange(ctx, identity, changeID)
			if err != nil {
				return commandReply(ctx, message, adapter, "确认失败："+err.Error())
			}
			return commandReply(ctx, message, adapter, fmt.Sprintf("已确认技能建议，技能 %s 已生效（版本 v%d）。", skill.Name, skill.Version))
		}
		change, err := s.memory.RejectSkillChange(ctx, identity, changeID)
		if err != nil {
			return commandReply(ctx, message, adapter, "拒绝失败："+err.Error())
		}
		return commandReply(ctx, message, adapter, fmt.Sprintf("已拒绝技能建议 #%d（%s）。", change.ID, change.Name))

	default:
		return commandReply(ctx, message, adapter, "用法：/skill list/show/add/edit/delete/pending/approve/reject")
	}
}

func parseSkillAddArgs(raw string) (name, desc, content string, err error) {
	name, rest := commandField(raw)
	if name == "" {
		return "", "", "", errors.New("缺少技能名称")
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", "", errors.New("缺少技能描述与内容")
	}
	if idx := strings.Index(rest, "\n"); idx != -1 {
		desc = strings.TrimSpace(rest[:idx])
		content = strings.TrimSpace(rest[idx+1:])
	} else {
		desc, content = commandField(rest)
		content = strings.TrimSpace(content)
		if content == "" {
			content = desc
		}
	}
	if desc == "" {
		return "", "", "", errors.New("缺少技能描述")
	}
	if content == "" {
		return "", "", "", errors.New("缺少技能正文")
	}
	return name, desc, content, nil
}
