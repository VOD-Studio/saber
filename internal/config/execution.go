package config

// ExecutionConfig 仅从管理员配置加载执行权限，聊天内容不能修改它。
type ExecutionConfig struct {
	// Enabled 启用已授权工作区的工具，默认关闭。
	Enabled bool `yaml:"enabled"`
	// Image 是预先安装且可信的 Python 3 容器镜像。
	Image string `yaml:"image"`
	// LogDir 在工作区外保存命令日志与交付文件。
	LogDir string `yaml:"log_dir"`
	// TimeoutSeconds 是单次工具调用上限，默认 60 秒。
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// Workspaces 将管理员命名的工作区绑定到宿主目录。
	Workspaces map[string]WorkspaceConfig `yaml:"workspaces"`
	// TaskAdmins 仅授予同群任务取消、日志读取和计划管理，不授予执行工具。
	TaskAdmins []TaskAdmin `yaml:"task_admins"`
	// Grants 精确授权账号、群、成员与工作区。
	Grants []ExecutionGrant `yaml:"grants"`
	// MCPRequirements 按 mcp:服务器:工具 声明额外能力；未声明的工具禁止调用。
	// 所有 MCP 工具都额外要求 external 能力，不信任服务端工具描述或注解。
	MCPRequirements map[string][]string `yaml:"mcp_requirements"`
}

// WorkspaceConfig 描述容器唯一可见的宿主工作目录与显式环境变量。
type WorkspaceConfig struct {
	// Path 是已存在的绝对目录，不得包含 Saber 的配置、会话或日志。
	Path string `yaml:"path"`
	// Env 只注入这些变量，不继承 Saber 环境；不得配置 Saber 自身凭据。
	Env map[string]string `yaml:"env"`
}

// ExecutionGrant 允许成员连续自主操作指定工作区，不包含逐命令确认。
type ExecutionGrant struct {
	// Platform 是接入平台。
	Platform string `yaml:"platform"`
	// Account 是机器人的账号。
	Account string `yaml:"account"`
	// Room 是授权群 ID。
	Room string `yaml:"room"`
	// Users 是完整成员 ID，禁止通配符。
	Users []string `yaml:"users"`
	// Workspace 引用 Workspaces 的名称；一个成员在同群只绑定一个工作区。
	Workspace string `yaml:"workspace"`
	// Tools 精确列出工具名或 mcp:服务器:工具。
	Tools []string `yaml:"tools"`
	// Capabilities 单独授予 external、publish、deploy 或 cross_directory 能力。
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
