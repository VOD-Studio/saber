package ai

import (
	"context"
	"encoding/json"
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
					"enum":        []string{"catalog", "read", "add", "replace", "remove", "hub", "search", "inspect", "install", "update", "check", "audit", "file", "write_file", "remove_file", "patch", "export", "bundle_export", "bundle_import"},
					"description": "catalog 当前可用技能；read 完整正文与 metadata；file 分页附属资源；hub/search 配置 catalog；inspect/install/update 已 pin 来源；check/audit 更新及安全扫描；patch/write_file/remove_file 版本化修改；bundle_export/import 完整包迁移",
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
				"identifier": map[string]any{"type": "string", "description": "管理员配置 catalog 的来源标识；不能传 URL"},
				"query":      map[string]any{"type": "string"},
				"path":       map[string]any{"type": "string", "description": "技能包内 references/templates/scripts/examples/assets 相对路径"},
				"offset":     map[string]any{"type": "integer", "minimum": 0},
				"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
				"old_text":   map[string]any{"type": "string", "description": "patch 必须唯一匹配的原正文"},
				"force":      map[string]any{"type": "boolean", "description": "覆盖用户修改或 high 扫描需独立 skills_force grant，critical 无法绕过"},
				"package":    map[string]any{"type": "object", "description": "bundle_export 返回的完整 metadata/resources 包"},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}}
}

// executeSkillTool 执行模型发起的技能操作，授权只取运行时可信身份。
func (s *Service) executeSkillTool(ctx context.Context, args map[string]any) (any, error) {
	if isPublicThread(ctx) {
		return nil, errors.New("公开互动回合不允许调用技能工具")
	}
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
		allowed := s.skillToolNames(ctx)
		visible := skills[:0]
		for _, sk := range skills {
			pkg, err := s.memory.GetSkillPackage(ctx, identity, sk.Name)
			if err != nil {
				return nil, err
			}
			if skillAllowed(pkg, allowed) {
				visible = append(visible, sk)
			}
		}
		skills = visible
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
		view, err := s.skillPackageView(ctx, identity, strings.TrimSpace(name))
		if err != nil {
			return nil, err
		}
		data := view.(map[string]any)
		metadata, err := json.Marshal(data["metadata"])
		if err != nil {
			return nil, err
		}
		return fmt.Sprintf("【技能：%s (v%d)】\n适用场景：%s\n\n%s\n\nmetadata: %s\n附属文件: %v\n缺少环境变量: %v\n%s", name, data["version"], data["description"], data["content"], metadata, data["files"], data["missing_environment_variables"], data["setup_notice"]), nil

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
		return s.executeSkillExtension(ctx, args)
	}
}

// skillMutationText 只描述真实结果：直接生效为已保存，否则说明仍是待确认建议。
func skillMutationText(mutation memory.SkillMutation) string {
	if mutation.Suggested {
		return fmt.Sprintf("已提交待确认技能建议 #%d；需本群管理员审批确认后才会生效，当前尚未应用。", mutation.Change.ID)
	}
	if mutation.Duplicate {
		return fmt.Sprintf("技能 %s 已存在且内容完全一致，未做重复更新。", mutation.Skill.Name)
	}
	if mutation.Skill.ID == 0 && mutation.Change.Action == memory.ActionRemove {
		return "技能已成功删除。"
	}
	return fmt.Sprintf("技能 %s 已保存生效（版本 v%d）。", mutation.Skill.Name, mutation.Skill.Version)
}
