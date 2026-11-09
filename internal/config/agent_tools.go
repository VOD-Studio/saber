package config

import "errors"

// AgentToolsConfig 控制延迟工具发现，桥接调用始终重用原始工具的任务和权限。
type AgentToolsConfig struct {
	ToolSearch       string   `yaml:"tool_search"`
	Deferred         []string `yaml:"deferred"`
	AlwaysVisible    []string `yaml:"always_visible"`
	ListingMaxTokens int      `yaml:"listing_max_tokens"`
	SearchLimit      int      `yaml:"search_limit"`
	ThresholdPercent int      `yaml:"threshold_percent"`
}

// DefaultAgentToolsConfig 在工具集合较大时开启渐进式披露。
func DefaultAgentToolsConfig() AgentToolsConfig {
	return AgentToolsConfig{ToolSearch: "auto", ListingMaxTokens: 2000, SearchLimit: 10, ThresholdPercent: 10, AlwaysVisible: []string{"clarify", "terminal", "read_file", "write_file", "edit_file", "tool_search", "tool_describe", "tool_call"}}
}

// Validate 拒绝未知开关和无界发现结果。
func (c AgentToolsConfig) Validate() error {
	if c.ToolSearch != "auto" && c.ToolSearch != "on" && c.ToolSearch != "off" {
		return errors.New("agent_tools.tool_search must be auto, on or off")
	}
	if c.ListingMaxTokens < 0 || c.ListingMaxTokens > 100000 || c.SearchLimit < 1 || c.SearchLimit > 100 || c.ThresholdPercent < 1 || c.ThresholdPercent > 100 {
		return errors.New("agent_tools discovery limits are out of range")
	}
	return nil
}
