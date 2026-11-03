package ai

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/memory"
)

// skillInvocation 是一次 `/name [args]` 技能调用的解析结果。
type skillInvocation struct {
	entry memory.SkillEntry
	args  string
}

// parseSkillInvocation 解析首词为 `/name` 的技能调用。
//
// 只识别单个斜杠开头的首词；`//name` 是用户显式转义，保持原样交给模型。
// 名称规则与记忆服务一致，非法名称不视为技能调用。
func parseSkillInvocation(text string) (name, args string, ok bool) {
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	if !strings.HasPrefix(text, "/") || strings.HasPrefix(text, "//") {
		return "", "", false
	}
	rest := text[1:]
	if idx := strings.IndexFunc(rest, unicode.IsSpace); idx >= 0 {
		name, args = rest[:idx], strings.TrimSpace(rest[idx:])
	} else {
		name = rest
	}
	name = strings.ToLower(name)
	if memory.ValidateSkillName(name) != nil {
		return "", "", false
	}
	return name, args, true
}

// skillMessageText 返回识别技能调用时应使用的正文。
//
// 接入端把 `//name` 去掉一个斜杠后会同时写入 Text 与 ControlText，表示这段正文
// 已被用户转义；此类消息只作为普通内容交给模型，不能再被识别成技能调用。
// 其余情况优先使用 ControlText：引用回复时它只包含用户本轮真正输入的部分。
func skillMessageText(message chat.Message) (string, bool) {
	if message.ControlText != nil && *message.ControlText == message.Text {
		return "", false
	}
	return message.CommandText(), true
}

// lookupSkillInvocation 在当前可信空间查找 `/name` 对应的技能。
//
// 未命中、名称非法或无法证明可信作用域时返回 false，调用方继续走普通聊天路径。
func (s *Service) lookupSkillInvocation(ctx context.Context, message chat.Message) (skillInvocation, bool) {
	if s.memory == nil {
		return skillInvocation{}, false
	}
	text, ok := skillMessageText(message)
	if !ok {
		return skillInvocation{}, false
	}
	name, args, ok := parseSkillInvocation(text)
	if !ok {
		return skillInvocation{}, false
	}
	identity := chat.Identity{Session: message.Session, SenderID: message.SenderID, Direct: message.Direct}
	entry, err := s.memory.GetSkill(ctx, identity, name)
	if err != nil {
		if !errors.Is(err, memory.ErrSkillNotFound) && !errors.Is(err, memory.ErrScope) && !errors.Is(err, memory.ErrInvalidSkillName) {
			slog.Warn("解析技能调用失败", "skill", name, "error", err)
		}
		return skillInvocation{}, false
	}
	return skillInvocation{entry: entry, args: args}, true
}

// userText 返回技能调用的用户消息：优先使用剩余参数，缺省使用技能描述。
func (inv skillInvocation) userText() string {
	if strings.TrimSpace(inv.args) != "" {
		return inv.args
	}
	return inv.entry.Description
}

// runtimeInstruction 把技能正文装配为本次运行的运行时指令，并按预算截断。
//
// 正文只进入 RuntimeContext，不写回持久化请求或摘要输入。
func (inv skillInvocation) runtimeInstruction(budget int) string {
	var b strings.Builder
	b.WriteString("【技能：")
	b.WriteString(inv.entry.Name)
	b.WriteString(" (v")
	b.WriteString(strconv.FormatInt(inv.entry.Version, 10))
	b.WriteString(")】\n适用场景：")
	b.WriteString(inv.entry.Description)
	b.WriteString("\n\n")
	if budget <= 0 {
		b.WriteString(inv.entry.Content)
		return b.String()
	}
	remaining := budget - b.Len()
	switch {
	case remaining <= 0:
		// 头部已占满预算时不再注入正文，避免超出调用方上限。
	case len(inv.entry.Content) > remaining:
		const suffix = "\n…（技能正文超出预算，已截断）"
		if remaining <= len(suffix) {
			b.WriteString(truncateSkillBody(inv.entry.Content, remaining))
			break
		}
		b.WriteString(truncateSkillBody(inv.entry.Content, remaining-len(suffix)))
		b.WriteString(suffix)
	default:
		b.WriteString(inv.entry.Content)
	}
	return b.String()
}

// truncateSkillBody 按 UTF-8 字符边界截断技能正文。
func truncateSkillBody(text string, limit int) string {
	if limit >= len(text) {
		return text
	}
	if limit <= 0 {
		return ""
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// skillInvokeBudget 返回单次技能正文注入的字节上限：默认 8 KiB，且不超过输入预算的八分之一。
func (s *Service) skillInvokeBudget() int {
	budget := 8 << 10
	if s.config != nil {
		if limit := s.config.Agent.Context.MaxTokens; limit > 0 && limit/8 < budget {
			budget = limit / 8
		}
	}
	return budget
}
