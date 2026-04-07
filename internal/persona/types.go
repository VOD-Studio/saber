// Package persona 提供机器人人格管理功能。
// 人格（Persona）允许机器人在不同房间展现不同的对话风格和角色设定。
package persona

import "time"

// Persona 表示一个人格配置。
// 每个人格包含唯一的标识符、显示名称、系统提示词和描述信息。
type Persona struct {
	// ID 是人格的唯一标识符，用于命令行引用（如 "catgirl", "pirate"）
	ID string `json:"id"`

	// Name 是人格的显示名称，用于展示给用户（如 "猫娘", "海盗"）
	Name string `json:"name"`

	// Prompt 是人格的系统提示词，会被合并到 AI 的 system prompt 中
	Prompt string `json:"prompt"`

	// Description 是人格的简短描述，用于 !persona list 命令展示
	Description string `json:"description"`

	// IsBuiltin 标记是否为内置人格，内置人格不可被删除
	IsBuiltin bool `json:"is_builtin"`

	// CreatedAt 是人格的创建时间
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt 是人格的最后更新时间
	UpdatedAt time.Time `json:"updated_at"`
}

// IsEmpty 检查人格是否为空（ID 为空）。
func (p *Persona) IsEmpty() bool {
	return p == nil || p.ID == ""
}

// DisplayName 返回人格的显示名称，如果人格为空则返回空字符串。
func (p *Persona) DisplayName() string {
	if p.IsEmpty() {
		return ""
	}
	return p.Name
}

// FullPrompt 返回人格的完整提示词。
// 如果提供了基础提示词，会将人格提示词追加到后面。
func (p *Persona) FullPrompt(basePrompt string) string {
	if p.IsEmpty() || p.Prompt == "" {
		return basePrompt
	}

	if basePrompt == "" {
		return p.Prompt
	}

	// 将基础提示词和人格提示词合并，用分隔线隔开
	return basePrompt + "\n\n---\n\n" + p.Prompt
}
