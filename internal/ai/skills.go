package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/memory"
	"rua.plus/saber/internal/task"
)

// skillTool 让模型管理与按需读取当前会话所属空间的程序性经验（技能/工作流/排错指南）。
func skillTool() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: "saber_skill",
		Description: "查阅、管理与检索当前会话所属空间的程序性经验（技能/工作流/排错指南）。" +
			"遵循按需渐进加载：系统提示中仅展示可用技能目录；当需要执行具体复杂流程或进行故障排错时，调用 read 操作读取完整步骤。" +
			"当遇到并成功解决复杂排错或多步流程时，可通过 add/replace 沉淀为可复用技能。" +
			"私聊技能为个人专属，群聊技能由全群共享；群聊中普通成员或自动复盘的修改转为待确认建议，需管理员审批。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"catalog", "read", "add", "replace", "remove"},
					"description": "操作类型：catalog 查看可用技能清单；read 按需读取指定技能完整正文；add 新增技能；replace 更新已有技能；remove 删除技能",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "技能唯一标识名（如 k8s-pod-troubleshoot, deploy-service），read/add/replace/remove 时必填",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "技能适用场景与触发时机描述，用于轻量目录展示与模型路由，add/replace 时必填",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "技能的完整 Markdown 操作说明，包含标准步骤、排错清单与注意事项，add/replace 时必填",
				},
				"expected_version": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "replace 与 remove 时必须传入当前技能版本号，防止并发覆盖他人更新",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}}
}

// executeSkillTool 执行模型发起的技能操作，授权只取运行时可信身份。
func (s *Service) executeSkillTool(ctx context.Context, args map[string]any) (any, error) {
	if s.memory == nil {
		return nil, errors.New("技能与记忆服务未启用")
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return nil, errors.New("技能工具需要可信身份")
	}

	action, _ := args["action"].(string)
	name, _ := args["name"].(string)
	desc, _ := args["description"].(string)
	content, _ := args["content"].(string)

	switch action {
	case "catalog":
		skills, err := s.memory.ListSkills(ctx, identity)
		if err != nil {
			return nil, err
		}
		if len(skills) == 0 {
			return "当前空间暂无可用技能。可使用 add 操作沉淀复杂工作流与排错经验。", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "当前可用技能列表（共 %d 个）：", len(skills))
		for _, sk := range skills {
			fmt.Fprintf(&b, "\n- %s (v%d)：%s", sk.Name, sk.Version, sk.Description)
		}
		return b.String(), nil

	case "read":
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("read 操作必须指定技能名称 name")
		}
		entry, err := s.memory.GetSkill(ctx, identity, strings.TrimSpace(name))
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "【技能：%s (v%d)】\n适用场景：%s\n更新时间：%s\n\n%s",
			entry.Name, entry.Version, entry.Description, entry.UpdatedAt.Format("2006-01-02 15:04:05"), entry.Content)
		return b.String(), nil

	case "add":
		source := memory.Source{Task: task.ID(ctx)}
		var mut memory.SkillMutation
		var err error
		if isReviewContext(ctx) {
			mut, err = s.memory.AutoAddSkill(ctx, identity, name, desc, content, source)
		} else {
			mut, err = s.memory.AddSkill(ctx, identity, name, desc, content, source)
		}
		if err != nil {
			return nil, err
		}
		return skillMutationText(mut), nil

	case "replace", "remove":
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("%s 操作必须指定技能名称 name", action)
		}
		isReview := isReviewContext(ctx)
		version, err := memoryIntArg(args, "expected_version", !isReview)
		if err != nil {
			return nil, err
		}
		source := memory.Source{Task: task.ID(ctx)}
		var mut memory.SkillMutation
		if isReview {
			if action == "replace" {
				mut, err = s.memory.AutoReplaceSkill(ctx, identity, name, version, desc, content, source)
			} else {
				mut, err = s.memory.AutoRemoveSkill(ctx, identity, name, version, source)
			}
		} else {
			if action == "replace" {
				mut, err = s.memory.ReplaceSkill(ctx, identity, name, version, desc, content, source)
			} else {
				mut, err = s.memory.RemoveSkill(ctx, identity, name, version, source)
			}
		}
		if err != nil {
			return nil, err
		}
		if action == "remove" && !mut.Suggested {
			return fmt.Sprintf("技能 %s 已成功删除。", name), nil
		}
		return skillMutationText(mut), nil

	default:
		return nil, fmt.Errorf("未知技能操作 %q", action)
	}
}

// skillMutationText 只描述真实结果：直接生效为已保存，否则说明仍是待确认建议。
func skillMutationText(mutation memory.SkillMutation) string {
	if mutation.Suggested {
		return fmt.Sprintf("已提交技能变更建议 #%d；需本群管理员审批确认后才会生效，当前尚未应用。", mutation.Change.ID)
	}
	if mutation.Duplicate {
		return fmt.Sprintf("技能 %s 已存在且内容完全一致，未做重复更新。", mutation.Skill.Name)
	}
	if mutation.Skill.ID == 0 && mutation.Change.Action == memory.ActionRemove {
		return "技能已成功删除。"
	}
	return fmt.Sprintf("技能 %s 已保存生效（版本 v%d）。", mutation.Skill.Name, mutation.Skill.Version)
}
