package config

import (
	"errors"
	"fmt"
)

// CodeExecutionConfig 配置授权工作区中的持久 Python 代码单元与宿主工具 RPC。
type CodeExecutionConfig struct {
	Enabled           bool   `yaml:"enabled"`
	Mode              string `yaml:"mode"`
	Python            string `yaml:"python"`
	TimeoutSeconds    int    `yaml:"timeout_seconds"`
	MaxToolCalls      int    `yaml:"max_tool_calls"`
	MaxOutputBytes    int    `yaml:"max_output_bytes"`
	MaxSessionKernels int    `yaml:"max_session_kernels"`
	KernelIdleSeconds int    `yaml:"kernel_idle_seconds"`
	// Tools 是可从代码调用的精确工具名单，实际调用仍须宿主重新授权。
	Tools []string `yaml:"tools"`
}

// DefaultCodeExecutionConfig 返回默认关闭、仅允许短程文件工具链的配置。
func DefaultCodeExecutionConfig() CodeExecutionConfig {
	return CodeExecutionConfig{Mode: "project", Python: "python3", TimeoutSeconds: 300, MaxToolCalls: 50, MaxOutputBytes: 50000, MaxSessionKernels: 16, KernelIdleSeconds: 1800, Tools: []string{"read_file", "list_files", "search_files", "glob_files", "write_file", "apply_patch", "exec"}}
}

// Validate 拒绝无法实施的资源限额、执行模式和递归工具调用。
func (c CodeExecutionConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Mode != "" && c.Mode != "project" && c.Mode != "strict" {
		return errors.New("code_execution.mode must be project or strict")
	}
	for _, limit := range []struct {
		name            string
		value, min, max int
	}{{"timeout_seconds", c.TimeoutSeconds, 1, 3600}, {"max_tool_calls", c.MaxToolCalls, 1, 1000}, {"max_output_bytes", c.MaxOutputBytes, 1024, 16 << 20}, {"max_session_kernels", c.MaxSessionKernels, 1, 128}, {"kernel_idle_seconds", c.KernelIdleSeconds, 1, 86400}} {
		if limit.value != 0 && (limit.value < limit.min || limit.value > limit.max) {
			return fmt.Errorf("code_execution.%s must be %d..%d", limit.name, limit.min, limit.max)
		}
	}
	seen := map[string]bool{}
	for _, name := range c.Tools {
		if name == "" || seen[name] || name == "execute_code" || name == "process" || name == "delegate" || name == "clarify" || name == "tool_call" {
			return fmt.Errorf("code_execution.tools contains forbidden, duplicate or interactive tool %q", name)
		}
		seen[name] = true
	}
	return nil
}
