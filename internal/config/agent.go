package config

import "fmt"

// 子代理 delegate 的默认值与上限；agent 包的同名策略常量必须保持一致。
const (
	// DefaultSubagentMaxRounds 是子代理的默认轮数上限。
	DefaultSubagentMaxRounds = 12
	// SubagentMaxRoundsLimit 是子代理轮数上限的配置天花板。
	SubagentMaxRoundsLimit = 40
	// DefaultSubagentMaxParallel 是同一轮并发子代理数的默认值。
	DefaultSubagentMaxParallel = 4
	// SubagentMaxParallelLimit 是同一轮并发子代理数的配置天花板。
	SubagentMaxParallelLimit = 16
)

// SubagentsConfig 控制内置 delegate 工具与子代理运行上限。
// Enabled 使用指针以保持「零值等于默认值」：省略或 null 表示开启，显式 false 才关闭。
type SubagentsConfig struct {
	Enabled        *bool `yaml:"enabled"`          // 是否向顶层任务暴露 delegate；默认开启
	MaxRounds      int   `yaml:"max_rounds"`       // 单个子代理的轮数上限，默认 12，范围 1..40
	MaxTotalTokens int   `yaml:"max_total_tokens"` // 子代理独立 token 上限，0 表示只受父账本约束
	MaxParallel    int   `yaml:"max_parallel"`     // 同一轮并发子代理数，默认 4，范围 1..16
}

// DefaultSubagentsConfig 返回子代理默认策略：开启、12 轮、4 路并行、无独立 token 上限。
func DefaultSubagentsConfig() SubagentsConfig {
	enabled := true
	return SubagentsConfig{
		Enabled:        &enabled,
		MaxRounds:      DefaultSubagentMaxRounds,
		MaxTotalTokens: 0,
		MaxParallel:    DefaultSubagentMaxParallel,
	}
}

// IsEnabled 报告是否启用子代理；未配置时按默认开启处理。
func (c SubagentsConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// ApplyDefaults 填充零值，使零值配置与默认配置等价。
func (c *SubagentsConfig) ApplyDefaults() {
	if c.Enabled == nil {
		enabled := true
		c.Enabled = &enabled
	}
	if c.MaxRounds == 0 {
		c.MaxRounds = DefaultSubagentMaxRounds
	}
	if c.MaxParallel == 0 {
		c.MaxParallel = DefaultSubagentMaxParallel
	}
}

// Validate 校验并归一化子代理配置；范围越界时返回带字段名的错误。
func (c *SubagentsConfig) Validate() error {
	c.ApplyDefaults()
	if c.MaxRounds < 1 || c.MaxRounds > SubagentMaxRoundsLimit {
		return fmt.Errorf("agent.subagents.max_rounds 必须在 1 到 %d 之间", SubagentMaxRoundsLimit)
	}
	if c.MaxParallel < 1 || c.MaxParallel > SubagentMaxParallelLimit {
		return fmt.Errorf("agent.subagents.max_parallel 必须在 1 到 %d 之间", SubagentMaxParallelLimit)
	}
	if c.MaxTotalTokens < 0 {
		return fmt.Errorf("agent.subagents.max_total_tokens 必须非负，0 表示只受父账本约束")
	}
	return nil
}
