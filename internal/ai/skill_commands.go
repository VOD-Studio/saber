package ai

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
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

	case "files":
		if strings.TrimSpace(raw) != "" {
			return commandReply(ctx, message, adapter, "用法：/skill files")
		}
		dirs := s.memory.SkillDirs()
		if len(dirs) == 0 {
			return commandReply(ctx, message, adapter, "未配置外部技能目录：请在 memory.skills.dirs 中添加绝对路径后重启。")
		}
		listings, errs := memory.ListSkillFiles(dirs)
		var b strings.Builder
		b.WriteString("外部技能目录（只读，未写入当前空间）：")
		for i, dir := range dirs {
			fmt.Fprintf(&b, "\n%d. %s", i+1, dir)
		}
		if len(listings) == 0 {
			b.WriteString("\n（没有发现可导入的 <name>/SKILL.md）")
		}
		for _, item := range listings {
			fmt.Fprintf(&b, "\n- %s：%s（%s）", item.Name, item.Description, item.RelPath)
		}
		for _, err := range errs {
			fmt.Fprintf(&b, "\n错误：%s", err)
		}
		return commandReply(ctx, message, adapter, b.String())

	case "import":
		dirArg, rest, err := splitSkillDirArg(raw)
		if err != nil || rest != "" {
			return commandReply(ctx, message, adapter, "用法：/skill import [--dir <目录索引或绝对路径>]")
		}
		configured, single, err := s.skillImportTarget(dirArg)
		if err != nil {
			return commandReply(ctx, message, adapter, err.Error())
		}
		if single == "" && len(configured) == 0 {
			return commandReply(ctx, message, adapter, "未配置外部技能目录：请在 memory.skills.dirs 中添加绝对路径，或用 --dir 指定目录。")
		}
		var result memory.SkillImportResult
		if single != "" {
			result, err = s.memory.ImportSkills(ctx, identity, single)
		} else {
			result, err = s.memory.SyncExternalSkills(ctx, identity, configured)
		}
		if err != nil {
			return commandReply(ctx, message, adapter, "导入技能失败："+err.Error())
		}
		return commandReply(ctx, message, adapter, skillImportSummary(result))

	case "export":
		dirArg, rest, err := splitSkillDirArg(raw)
		if err != nil {
			return commandReply(ctx, message, adapter, "用法：/skill export <名称> [--dir <绝对路径>]")
		}
		name := strings.TrimSpace(rest)
		if name == "" || strings.ContainsAny(name, " \t\r\n") {
			return commandReply(ctx, message, adapter, "用法：/skill export <名称> [--dir <绝对路径>]")
		}
		target := dirArg
		if target == "" {
			target = s.memory.SkillExportDir()
		}
		if target == "" {
			return commandReply(ctx, message, adapter, "未配置导出目录：请设置 memory.skills.export_dir，或用 --dir 指定绝对路径。")
		}
		path, err := s.memory.ExportSkill(ctx, identity, name, target)
		if err != nil {
			return commandReply(ctx, message, adapter, "导出技能失败："+err.Error())
		}
		return commandReply(ctx, message, adapter, fmt.Sprintf("技能 %s 已导出到 %s", name, path))

	default:
		return commandReply(ctx, message, adapter, "用法：/skill list/show/add/edit/delete/pending/approve/reject/import/export/files")
	}
}

// splitSkillDirArg 提取 `--dir <值>` 或 `--dir=<值>` 选项，返回剩余参数。
func splitSkillDirArg(raw string) (dir, rest string, err error) {
	fields := strings.Fields(raw)
	var others []string
	for i := 0; i < len(fields); i++ {
		switch {
		case fields[i] == "--dir":
			if i+1 >= len(fields) {
				return "", "", errors.New("--dir 需要目录参数")
			}
			dir = fields[i+1]
			i++
		case strings.HasPrefix(fields[i], "--dir="):
			dir = strings.TrimPrefix(fields[i], "--dir=")
		default:
			others = append(others, fields[i])
		}
	}
	dir = strings.TrimSpace(dir)
	if dir == "" && strings.Contains(raw, "--dir") {
		return "", "", errors.New("--dir 需要目录参数")
	}
	return dir, strings.Join(others, " "), nil
}

// skillImportTarget 解析导入目标：空值表示全部已配置目录，数字为目录索引（从 1 开始），
// 其他值必须是绝对路径目录。
func (s *Service) skillImportTarget(dirArg string) (configured []string, single string, err error) {
	configured = s.memory.SkillDirs()
	if dirArg == "" {
		return configured, "", nil
	}
	if idx, err := strconv.Atoi(dirArg); err == nil {
		if idx < 1 || idx > len(configured) {
			return nil, "", fmt.Errorf("目录索引 %d 超出范围（已配置 %d 个目录）", idx, len(configured))
		}
		return nil, configured[idx-1], nil
	}
	if !filepath.IsAbs(dirArg) {
		return nil, "", errors.New("--dir 需要使用已配置目录的索引或绝对路径")
	}
	return nil, filepath.Clean(dirArg), nil
}

// skillImportSummary 汇总导入结果；只展示相对路径与技能名，不泄露目录正文。
func skillImportSummary(result memory.SkillImportResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "技能导入完成：新增 %d，更新 %d，跳过 %d。", result.Imported, result.Updated, result.Skipped)
	if result.Suggested > 0 {
		fmt.Fprintf(&b, "另有 %d 项转为待确认建议（群空间无写权限），需管理员审批后才生效。", result.Suggested)
	}
	if len(result.Errors) > 0 {
		fmt.Fprintf(&b, "\n失败 %d 项：", len(result.Errors))
		for i, err := range result.Errors {
			if i >= 10 {
				b.WriteString("\n- …")
				break
			}
			fmt.Fprintf(&b, "\n- %s", err)
		}
	}
	return b.String()
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
