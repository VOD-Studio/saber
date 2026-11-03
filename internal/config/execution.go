package config

import "fmt"

// ExecutionConfig 仅从管理员配置加载执行权限，聊天内容不能修改它。
type ExecutionConfig struct {
	// Enabled 启用已授权工作区的工具，默认关闭。
	Enabled bool `yaml:"enabled"`
	// Backend 选择执行后端：local 或 docker；空值表示未显式选择，有效默认 local。
	Backend string `yaml:"backend,omitempty"`
	// Network 控制沙箱容器是否允许联网；默认开启，设为 false 可显式关闭断网。
	Network *bool `yaml:"network,omitempty"`
	// NetworkEnabled 是 Network 的别名，便于配置命名风格保持一致。
	NetworkEnabled *bool `yaml:"network_enabled,omitempty"`
	// Image 是预先安装且可信的 Python 3 容器镜像。
	Image string `yaml:"image"`
	// LogDir 在工作区外保存命令日志与交付文件。
	LogDir string `yaml:"log_dir"`
	// TimeoutSeconds 是单次工具调用上限，默认 60 秒。
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// CommandGuard 控制危险命令绊线：block（默认）拒绝命中规则的命令，
	// ask 生成一次性的群内审批请求，off 关闭全部检查。
	CommandGuard string `yaml:"command_guard"`
	// CommandGuardDeny 是额外的字面量子串规则（大小写不敏感），最多 64 条，每条 1..256 字节。
	CommandGuardDeny []string `yaml:"command_guard_deny"`
	// ApprovalTTLSeconds 是一次性命令审批码的有效期，默认 600 秒，范围 60..3600。
	ApprovalTTLSeconds int `yaml:"approval_ttl_seconds"`
	// ApprovalMaxPending 是内存中同时保留的待审批请求上限，默认 32，范围 1..256。
	ApprovalMaxPending int `yaml:"approval_max_pending"`
	// Workspaces 将管理员命名的工作区绑定到宿主目录。
	Workspaces map[string]WorkspaceConfig `yaml:"workspaces"`
	// TaskAdmins 仅授予同群任务取消、日志读取和计划管理，不授予执行工具。
	TaskAdmins []TaskAdmin `yaml:"task_admins"`
	// Grants 按平台账号、会话、成员授权工作区；会话和成员可显式通配。
	Grants []ExecutionGrant `yaml:"grants"`
	// MCPRequirements 按 mcp:服务器:工具 声明额外能力；未声明的工具禁止调用。
	// 所有 MCP 工具都额外要求 external 能力，不信任服务端工具描述或注解。
	MCPRequirements map[string][]string `yaml:"mcp_requirements"`
}

// WorkspaceConfig 描述容器唯一可见的宿主工作目录与显式环境变量。
type WorkspaceConfig struct {
	// Path 是已存在的绝对目录，不得包含 Saber 的配置、会话或日志。
	Path string `yaml:"path"`
	// Backend 覆盖该工作区的执行后端；空值继承全局 backend。
	Backend string `yaml:"backend,omitempty"`
	// Env 只注入这些变量，不继承 Saber 环境；不得配置 Saber 自身凭据。
	Env map[string]string `yaml:"env"`
}

// ExecutionGrant 允许成员连续自主操作指定工作区，不包含逐命令确认。
type ExecutionGrant struct {
	// Platform 是接入平台。
	Platform string `yaml:"platform"`
	// Account 是机器人的账号。
	Account string `yaml:"account"`
	// Room 是授权会话 ID；"*" 表示该平台账号下的所有会话。
	Room string `yaml:"room"`
	// Users 是完整成员 ID；"*" 表示会话中的所有成员。
	Users []string `yaml:"users"`
	// Workspace 引用 Workspaces 的名称；一个成员在同群只绑定一个工作区。
	Workspace string `yaml:"workspace"`
	// Tools 精确列出工具名或 mcp:服务器:工具。
	Tools []string `yaml:"tools"`
	// Capabilities 只接受本构建真正检查的能力：MCP 工具调用额外要求 external。
	// publish、deploy、cross_directory 等未实施的名称会在启动时被拒绝
	// （capability "..." is not enforced by this build），不会被静默当成已生效的保护。
	Capabilities []string `yaml:"capabilities"`
}

// TaskAdmin 按平台、机器人账号和群精确指定任务管理员。
type TaskAdmin struct {
	// Platform 是接入平台。
	Platform string `yaml:"platform"`
	// Account 是机器人账号。
	Account string `yaml:"account"`
	// Room 是允许管理的群。
	Room string `yaml:"room"`
	// Users 是完整成员 ID，不接受通配符。
	Users []string `yaml:"users"`
}

// DefaultExecutionConfig 返回带有合理默认值的执行器配置。
//
// 沙箱容器默认开启网络权限，单次超时默认 60 秒。
func DefaultExecutionConfig() ExecutionConfig {
	defaultNetwork := true
	return ExecutionConfig{
		Enabled:            false,
		Network:            &defaultNetwork,
		TimeoutSeconds:     60,
		CommandGuard:       "block",
		ApprovalTTLSeconds: 600,
		ApprovalMaxPending: 32,
	}
}

// IsNetworkEnabled 返回沙箱容器是否开启网络权限。
//
// 默认开启（true）；只有在配置中显式将 network 或 network_enabled 设置为 false 时才关闭断网。
func (c ExecutionConfig) IsNetworkEnabled() bool {
	if c.Network != nil && !*c.Network {
		return false
	}
	if c.NetworkEnabled != nil && !*c.NetworkEnabled {
		return false
	}
	return true
}

// BackendFor 返回工作区的有效执行后端：工作区显式值 > 全局显式值 > local。
// 原始字段为空表示管理员未显式选择，有效默认仍是 local。
func (c ExecutionConfig) BackendFor(workspace string) (string, error) {
	backend := c.Backend
	if w, ok := c.Workspaces[workspace]; ok && w.Backend != "" {
		backend = w.Backend
	}
	switch backend {
	case "":
		return "local", nil
	case "local", "docker":
		return backend, nil
	default:
		return "", fmt.Errorf("unknown execution backend %q; use local or docker", backend)
	}
}
