package ai

import (
	"context"
	"slices"
	"strings"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/memory"
)

// Capability 描述一个功能组的真实状态；sandboxed 始终显式输出。
type Capability struct {
	State     string   `json:"state"`
	Reason    string   `json:"reason,omitempty"`
	Backend   string   `json:"backend,omitempty"`
	Tools     []string `json:"tools"`
	Sandboxed bool     `json:"sandboxed"`
}

const (
	capabilityAvailable    = "available"
	capabilityDisabled     = "disabled"
	capabilityUnauthorized = "unauthorized"
	capabilityUnavailable  = "unavailable"
)

// DisabledCapabilities 在 AI 服务未初始化时仍返回完整的能力关闭状态。
func DisabledCapabilities(reason string) map[string]Capability {
	result := make(map[string]Capability, 10)
	for _, key := range []string{"tasks", "trace_export", "execution", "file_read", "file_search", "processes", "memory", "skills", "mcp", "context_compaction", "adaptive_budget"} {
		result[key] = Capability{State: capabilityDisabled, Reason: reason, Tools: []string{}}
	}
	return result
}

// Capabilities 根据程序开关、当前身份授权、隔离状态与健康结果返回真实能力。
func (s *Service) Capabilities(ctx context.Context, identity chat.Identity) map[string]Capability {
	if err := ctx.Err(); err != nil {
		return map[string]Capability{"tasks": {State: capabilityUnavailable, Reason: err.Error(), Tools: []string{}}}
	}
	result := map[string]Capability{}
	put := func(key, state, reason string, tools []string) {
		if tools == nil {
			tools = []string{}
		}
		result[key] = Capability{State: state, Reason: reason, Tools: tools}
	}
	if s.tasks != nil {
		put("tasks", capabilityAvailable, "", nil)
		// 轨迹导出只读写 tasks.db 与任务私有产物目录，与执行后端无关。
		put("trace_export", capabilityAvailable, "", []string{"saber_trace"})
	} else {
		put("tasks", capabilityDisabled, "任务服务未启用", nil)
		put("trace_export", capabilityDisabled, "任务服务未启用", nil)
	}
	// 执行相关能力先看总开关，再看身份授权与隔离状态。
	var allowed []string
	backend := ""
	sandboxed := false
	if s.executor == nil {
		for _, key := range []string{"execution", "file_read", "file_search", "processes"} {
			put(key, capabilityDisabled, "execution.enabled=false", nil)
		}
	} else if s.executor.WorkspaceBlocked(identity) {
		for _, key := range []string{"execution", "file_read", "file_search", "processes"} {
			put(key, capabilityUnavailable, "工作区已锁定等待人工核查环境清理", nil)
		}
	} else {
		allowed = s.executor.AllowedTools(identity)
		identityBackend, identitySandboxed, err := s.executor.IdentityBackend(identity)
		if err != nil {
			for _, key := range []string{"execution", "file_read", "file_search", "processes"} {
				put(key, capabilityUnauthorized, "当前身份未获工作区授权", nil)
			}
		} else {
			backend, sandboxed = identityBackend, identitySandboxed
			group := func(key string, names ...string) {
				var tools []string
				for _, name := range names {
					if slices.Contains(allowed, name) {
						tools = append(tools, name)
					}
				}
				capability := Capability{Tools: tools, Backend: backend, Sandboxed: sandboxed}
				if len(tools) == 0 {
					capability.State = capabilityUnauthorized
					capability.Reason = "当前身份未获此组工具授权"
				} else {
					capability.State = capabilityAvailable
				}
				result[key] = capability
			}
			group("execution", "exec")
			group("file_read", "read_file", "list_files")
			group("file_search", "search_files", "glob_files")
			group("processes", "process")
		}
	}
	var memoryTools []string
	if s.memory != nil {
		if _, ok := memory.Space(identity); ok {
			put("memory", capabilityAvailable, "", nil)
			put("skills", capabilityAvailable, "", nil)
		} else {
			put("memory", capabilityUnauthorized, "当前身份没有记忆作用域", nil)
			put("skills", capabilityUnauthorized, "当前身份没有记忆作用域", nil)
		}
	} else {
		put("memory", capabilityDisabled, "memory.enabled=false", nil)
		put("skills", capabilityDisabled, "memory.enabled=false", nil)
	}
	if s.mcpManager == nil || !s.mcpManager.IsEnabled() {
		put("mcp", capabilityDisabled, "mcp.enabled=false", nil)
	} else {
		for _, tool := range allowed {
			if strings.HasPrefix(tool, "mcp:") {
				memoryTools = append(memoryTools, tool)
			}
		}
		if len(memoryTools) == 0 {
			put("mcp", capabilityUnauthorized, "当前身份未获 MCP 工具授权", nil)
		} else {
			put("mcp", capabilityAvailable, "", memoryTools)
		}
	}
	if s.config.Agent.Context.Compaction.Enabled {
		put("context_compaction", capabilityAvailable, "", nil)
	} else {
		put("context_compaction", capabilityDisabled, "agent.context.compaction.enabled=false", nil)
	}
	if s.config.Agent.Budget.Enabled {
		put("adaptive_budget", capabilityAvailable, "", nil)
	} else {
		put("adaptive_budget", capabilityDisabled, "agent.budget.enabled=false", nil)
	}
	return result
}
