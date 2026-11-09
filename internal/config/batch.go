package config

import "fmt"

// BatchConfig 配置真实任务轨迹生成和评估，不接受数据集内的凭据或服务 URL。
type BatchConfig struct {
	Enabled                  bool                          `yaml:"enabled" json:"enabled"`
	MaxSamples               int                           `yaml:"max_samples" json:"max_samples"`
	BatchSize                int                           `yaml:"batch_size" json:"batch_size"`
	MaxConcurrent            int                           `yaml:"max_concurrent" json:"max_concurrent"`
	MaxTurns                 int                           `yaml:"max_turns" json:"max_turns"`
	IncludeMemory            bool                          `yaml:"include_memory" json:"include_memory"`
	IsolateWorkspace         bool                          `yaml:"isolate_workspace" json:"isolate_workspace"`
	MaxInputBytes            int64                         `yaml:"max_input_bytes" json:"max_input_bytes"`
	RequireReasoning         bool                          `yaml:"require_reasoning" json:"require_reasoning"`
	EvaluationModel          string                        `yaml:"evaluation_model" json:"evaluation_model"`
	EvaluationTimeoutSeconds int                           `yaml:"evaluation_timeout_seconds" json:"evaluation_timeout_seconds"`
	Distributions            map[string]map[string]float64 `yaml:"distributions" json:"distributions"`
}

// DefaultBatchConfig 默认关闭数据生成，启用后使用有界输入和任务并发。
func DefaultBatchConfig() BatchConfig {
	return BatchConfig{MaxSamples: 10000, BatchSize: 10, MaxConcurrent: 4, MaxTurns: 10, IsolateWorkspace: true, RequireReasoning: true, MaxInputBytes: 64 << 20, EvaluationTimeoutSeconds: 60, Distributions: map[string]map[string]float64{"default": {}}}
}

// Validate 检查样本、文件和独立工具概率的界限。
func (c BatchConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.MaxSamples < 1 || c.BatchSize < 1 || c.BatchSize > 1000 || c.MaxConcurrent < 1 || c.MaxConcurrent > 64 || c.MaxTurns < 1 || c.MaxTurns > 1000 || c.MaxInputBytes < 1 || c.EvaluationTimeoutSeconds < 1 || c.EvaluationTimeoutSeconds > 600 {
		return fmt.Errorf("批量样本、并发、轮数、大小或评估超时无效")
	}
	for name, weights := range c.Distributions {
		if name == "" {
			return fmt.Errorf("批量工具分布名称为空")
		}
		for tool, p := range weights {
			if tool == "" || p < 0 || p > 1 || p != p {
				return fmt.Errorf("分布 %s 工具 %q 概率无效", name, tool)
			}
		}
	}
	return nil
}
