package ai

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/memory"
)

// MemoryCommand 执行与平台无关的长期记忆命令，身份与作用域取自可信消息。
func (s *Service) MemoryCommand(ctx context.Context, message chat.Message, adapter chat.Adapter, action, raw string) error {
	if s.memory == nil {
		return commandReply(ctx, message, adapter, "长期记忆未启用")
	}
	identity := chat.Identity{Session: message.Session, SenderID: message.SenderID, Direct: message.Direct}
	switch action {
	case "list", "status":
		if strings.TrimSpace(raw) != "" {
			return commandReply(ctx, message, adapter, "用法：/memory "+action)
		}
		text, err := s.memoryStatusText(ctx, identity, action == "status")
		if err != nil {
			return commandReply(ctx, message, adapter, "读取记忆失败："+err.Error())
		}
		return commandReply(ctx, message, adapter, text)
	case "add":
		content := strings.TrimSpace(raw)
		if content == "" {
			return commandReply(ctx, message, adapter, "用法：/memory add <内容>")
		}
		mutation, err := s.memory.Add(ctx, identity, content, memory.Source{}, true)
		if err != nil {
			return commandReply(ctx, message, adapter, "保存失败："+err.Error())
		}
		return commandReply(ctx, message, adapter, memoryMutationText(mutation))
	case "edit", "forget":
		idField, content := commandField(raw)
		entryID, err := parseEntryID(idField)
		if err != nil || action == "edit" && strings.TrimSpace(content) == "" {
			usage := "/memory " + action + " <ID>"
			if action == "edit" {
				usage += " <内容>"
			}
			return commandReply(ctx, message, adapter, "用法："+usage)
		}
		entry, err := s.memoryEntry(ctx, identity, entryID)
		if err != nil {
			return commandReply(ctx, message, adapter, memoryFailureText(action, err))
		}
		var mutation memory.Mutation
		if action == "edit" {
			mutation, err = s.memory.Replace(ctx, identity, entryID, entry.Version, content, memory.Source{})
		} else {
			mutation, err = s.memory.Remove(ctx, identity, entryID, entry.Version, memory.Source{})
		}
		if err != nil {
			return commandReply(ctx, message, adapter, memoryFailureText(action, err))
		}
		return commandReply(ctx, message, adapter, memoryMutationText(mutation))
	case "pending":
		if strings.TrimSpace(raw) != "" {
			return commandReply(ctx, message, adapter, "用法：/memory pending")
		}
		changes, err := s.memory.Pending(ctx, identity)
		if err != nil {
			return commandReply(ctx, message, adapter, "读取待确认建议失败："+err.Error())
		}
		if len(changes) == 0 {
			return commandReply(ctx, message, adapter, "当前空间没有待确认建议")
		}
		var b strings.Builder
		b.WriteString("待确认建议：")
		for _, change := range changes {
			fmt.Fprintf(&b, "\n- #%d %s（由 %s 提出）：%s", change.ID, memoryActionLabel(change.Action), change.Proposer, memoryChangeSummary(change))
		}
		return commandReply(ctx, message, adapter, b.String())
	case "approve", "reject":
		changeID, err := parseEntryID(strings.TrimSpace(raw))
		if err != nil {
			return commandReply(ctx, message, adapter, "用法：/memory "+action+" <ID>")
		}
		if action == "approve" {
			mutation, err := s.memory.Approve(ctx, identity, changeID)
			if err != nil {
				return commandReply(ctx, message, adapter, "确认失败："+err.Error())
			}
			return commandReply(ctx, message, adapter, "已确认建议："+memoryMutationText(mutation))
		}
		change, err := s.memory.Reject(ctx, identity, changeID)
		if err != nil {
			return commandReply(ctx, message, adapter, "拒绝失败："+err.Error())
		}
		return commandReply(ctx, message, adapter, fmt.Sprintf("已拒绝建议 #%d。", change.ID))
	default:
		return commandReply(ctx, message, adapter, "用法：/memory list/status/add/edit/forget/pending/approve/reject")
	}
}

// memoryStatusText 汇总当前空间的条目与容量；detailed 为 true 时附带空间名称。
func (s *Service) memoryStatusText(ctx context.Context, identity chat.Identity, detailed bool) (string, error) {
	entries, err := s.memory.List(ctx, identity)
	if err != nil {
		return "", err
	}
	usage, err := s.memory.Usage(ctx, identity)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return fmt.Sprintf("%s暂无记忆条目（0/%d 字符）", usage.Scope, usage.MaxChars), nil
	}
	var b strings.Builder
	if detailed {
		fmt.Fprintf(&b, "%s：%d/%d 字符，%d 条", usage.Scope, usage.Chars, usage.MaxChars, usage.Count)
	} else {
		fmt.Fprintf(&b, "记忆条目（%d 条）：", usage.Count)
	}
	for _, entry := range entries {
		marker := ""
		if entry.Explicit {
			marker = "，用户明确保存"
		}
		fmt.Fprintf(&b, "\n- #%d v%d%s：%s", entry.ID, entry.Version, marker, entry.Content)
	}
	return b.String(), nil
}

// memoryEntry 在当前空间内按编号查找条目，越权访问返回 ErrNotFound。
func (s *Service) memoryEntry(ctx context.Context, identity chat.Identity, id int64) (memory.Entry, error) {
	entries, err := s.memory.List(ctx, identity)
	if err != nil {
		return memory.Entry{}, err
	}
	for _, entry := range entries {
		if entry.ID == id {
			return entry, nil
		}
	}
	return memory.Entry{}, memory.ErrNotFound
}

func memoryFailureText(action string, err error) string {
	verb := map[string]string{"edit": "修改", "forget": "删除"}[action]
	return verb + "失败：" + err.Error()
}

func memoryActionLabel(action string) string {
	switch action {
	case memory.ActionAdd:
		return "新增"
	case memory.ActionReplace:
		return "修改"
	case memory.ActionRemove:
		return "删除"
	default:
		return action
	}
}

func memoryChangeSummary(change memory.Change) string {
	switch change.Action {
	case memory.ActionAdd:
		return change.Content
	case memory.ActionReplace:
		return fmt.Sprintf("#%d → %s", change.EntryID, change.Content)
	case memory.ActionRemove:
		return fmt.Sprintf("#%d", change.EntryID)
	default:
		return change.Content
	}
}

// parseEntryID 解析命令中的条目编号，允许带 # 前缀。
func parseEntryID(field string) (int64, error) {
	field = strings.TrimSpace(field)
	if field == "" {
		return 0, errors.New("需要条目编号")
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(field, "#"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("条目编号必须是正整数")
	}
	return id, nil
}
