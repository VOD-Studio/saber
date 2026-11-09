package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// PluginSkillReader 只读取插件 manifest 声明且宿主每次重新授权的资源。
type PluginSkillReader interface {
	ReadSkill(context.Context, string, string, string) ([]byte, error)
	SkillCatalog(context.Context) map[string][]string
}

func (s *Service) pluginSkills() PluginSkillReader {
	s.providers.mu.Lock()
	defer s.providers.mu.Unlock()
	reader, _ := s.providers.caller.(PluginSkillReader)
	return reader
}
func pluginSkillParts(reference string) (string, string, error) {
	name, ok := strings.CutPrefix(reference, "plugin:")
	if !ok {
		return "", "", errors.New("插件技能需要 plugin:<name>/<skill> 标识")
	}
	plugin, skill, ok := strings.Cut(name, "/")
	if !ok || plugin == "" || validateSkillName(skill) != nil {
		return "", "", errors.New("插件技能标识无效")
	}
	return plugin, skill, nil
}

// PluginSkillCatalog 返回实际运行的插件 manifest 技能，授权拒绝的插件不会出现在目录。
func (s *Service) PluginSkillCatalog(ctx context.Context) ([]SkillPackage, error) {
	reader := s.pluginSkills()
	if reader == nil {
		return nil, nil
	}
	catalog := reader.SkillCatalog(ctx)
	var out []SkillPackage
	plugins := make([]string, 0, len(catalog))
	for plugin := range catalog {
		plugins = append(plugins, plugin)
	}
	sort.Strings(plugins)
	for _, plugin := range plugins {
		skills := append([]string(nil), catalog[plugin]...)
		sort.Strings(skills)
		for _, skill := range skills {
			pkg, err := s.ReadPluginSkill(ctx, "plugin:"+plugin+"/"+skill)
			if err != nil {
				return nil, err
			}
			out = append(out, pkg)
		}
	}
	return out, nil
}

// ReadPluginSkill 从真实插件包读取 SKILL.md，模型不能提供插件目录、凭据或身份。
func (s *Service) ReadPluginSkill(ctx context.Context, reference string) (SkillPackage, error) {
	reader := s.pluginSkills()
	if reader == nil {
		return SkillPackage{}, errors.New("插件技能服务未配置")
	}
	plugin, skill, err := pluginSkillParts(reference)
	if err != nil {
		return SkillPackage{}, err
	}
	data, err := reader.ReadSkill(ctx, plugin, skill, SkillFileName)
	if err != nil {
		return SkillPackage{}, err
	}
	file, err := ParseSkillFile(data, SkillFileName)
	if err != nil {
		return SkillPackage{}, err
	}
	if file.Name != skill {
		return SkillPackage{}, errors.New("插件技能名称与 manifest 不一致")
	}
	return SkillPackage{Name: reference, Description: file.Description, Content: file.Content, Metadata: file.Metadata, Provenance: SkillProvenance{Source: "plugin:" + plugin}}, nil
}

// ReadPluginSkillResource 读取已授权插件技能的指定附属文件，不执行其中脚本。
func (s *Service) ReadPluginSkillResource(ctx context.Context, reference, resource string, offset, limit int) (map[string]any, error) {
	if err := safeSkillResource(resource); err != nil {
		return nil, err
	}
	if offset < 0 || limit < 0 {
		return nil, errors.New("offset/limit 不得为负")
	}
	reader := s.pluginSkills()
	if reader == nil {
		return nil, errors.New("插件技能服务未配置")
	}
	plugin, skill, err := pluginSkillParts(reference)
	if err != nil {
		return nil, err
	}
	data, err := reader.ReadSkill(ctx, plugin, skill, resource)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSkillFileBytes || !utf8.Valid(data) {
		return nil, errors.New("插件附属资源超限或不是 UTF-8 文本")
	}
	if limit == 0 || limit > 200 {
		limit = 200
	}
	lines := strings.Split(string(data), "\n")
	if offset > len(lines) {
		offset = len(lines)
	}
	end := min(len(lines), offset+limit)
	return map[string]any{"name": reference, "path": resource, "content": strings.Join(lines[offset:end], "\n"), "offset": offset, "next_offset": end, "has_more": end < len(lines), "source": fmt.Sprintf("plugin:%s", plugin)}, nil
}

// SkillSettings 返回非秘密技能设置；声明默认值在未显式配置时生效，敏感字段不会注入模型。
func (s *Service) SkillSettings(pkg SkillPackage) map[string]any {
	out := make(map[string]any)
	meta, _ := pkg.Metadata["metadata"].(map[string]any)
	hermes, _ := meta["hermes"].(map[string]any)
	items, _ := hermes["config"].([]any)
	for _, item := range items {
		field, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key, _ := field["key"].(string)
		if key == "" {
			continue
		}
		lower := strings.ToLower(key)
		if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "api_key") {
			out[key] = "[REDACTED]"
			continue
		}
		if value, ok := s.skillConfig[key]; ok {
			out[key] = value
		} else if value, ok := field["default"]; ok {
			out[key] = value
		}
	}
	return out
}
