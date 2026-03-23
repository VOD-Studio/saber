// Package config 提供 YAML 配置文件的加载、验证和默认值管理。
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 存储从 YAML 配置文件加载的应用程序配置
type Config struct {
	Matrix   MatrixConfig   `yaml:"matrix"`
	AI       AIConfig       `yaml:"ai"`
	MCP      MCPConfig      `yaml:"mcp"`
	Shutdown ShutdownConfig `yaml:"shutdown"`
}

// ShutdownConfig 存储关闭配置
type ShutdownConfig struct {
	TimeoutSeconds int `yaml:"timeout_seconds"` // 关闭超时时间（秒）
}

// MatrixConfig 存储 Matrix 连接配置
type MatrixConfig struct {
	Homeserver          string   `yaml:"homeserver"`
	UserID              string   `yaml:"user_id"`               // 完整的 Matrix ID，如 @user:matrix.org
	DeviceID            string   `yaml:"device_id"`             // 设备标识符
	DeviceName          string   `yaml:"device_name"`           // 设备显示名称
	Password            string   `yaml:"password"`              // 密码登录（可选）
	AccessToken         string   `yaml:"access_token"`          // Token 登录（可选，优先级高于密码）
	AutoJoinRooms       []string `yaml:"auto_join_rooms"`       // 启动时自动加入的房间列表
	EnableE2EE          bool     `yaml:"enable_e2ee"`           // 启用端到端加密（可选）
	E2EESessionPath     string   `yaml:"e2ee_session_path"`     // 端到端加密会话文件路径（可选）
	PickleKeyPath       string   `yaml:"pickle_key_path"`       // E2EE pickle 密钥文件路径（可选，默认为 e2ee_session_path + ".key")
	MaxConcurrentEvents int      `yaml:"max_concurrent_events"` // 最大并发事件处理数（默认 10）
}

// AIConfig 存储 AI 服务配置
type AIConfig struct {
	Enabled               bool                   `yaml:"enabled"`                  // 是否启用 AI 功能
	Provider              string                 `yaml:"provider"`                 // AI 提供商名称（如 openai, anthropic）
	BaseURL               string                 `yaml:"base_url"`                 // API 基础 URL
	APIKey                string                 `yaml:"api_key"`                  // API 密钥
	DefaultModel          string                 `yaml:"default_model"`            // 默认使用的模型
	MaxTokens             int                    `yaml:"max_tokens"`               // 最大生成 token 数
	Temperature           float64                `yaml:"temperature"`              // 生成温度（0-2）
	SystemPrompt          string                 `yaml:"system_prompt"`            // 系统提示词
	RateLimitPerMinute    int                    `yaml:"rate_limit_per_minute"`    // 每分钟请求限制（0 表示无限制）
	Context               ContextConfig          `yaml:"context"`                  // 上下文管理配置
	StreamEnabled         bool                   `yaml:"stream_enabled"`           // 是否启用流式响应
	StreamEdit            StreamEditConfig       `yaml:"stream_edit"`              // 流式编辑配置
	Retry                 RetryConfig            `yaml:"retry"`                    // 重试配置
	ToolCalling           ToolCallingConfig      `yaml:"tool_calling"`             // 工具调用配置
	Models                map[string]ModelConfig `yaml:"models"`                   // 模型特定配置
	TimeoutSeconds        int                    `yaml:"timeout_seconds"`          // 请求超时时间（秒）
	DirectChatAutoReply   bool                   `yaml:"direct_chat_auto_reply"`   // 在私聊中自动回复（无需 !ai 前缀）
	GroupChatMentionReply bool                   `yaml:"group_chat_mention_reply"` // 在群聊中 @mention 时自动回复（无需 !ai 前缀）
	ReplyToBotReply       bool                   `yaml:"reply_to_bot_reply"`       // 回复机器人自己的回复（用于连续对话）
	Proactive             ProactiveConfig        `yaml:"proactive"`                // 主动聊天配置
	Media                 MediaConfig            `yaml:"media"`                    // 媒体文件处理配置
}

// ContextConfig 存储上下文管理配置
type ContextConfig struct {
	Enabled           bool `yaml:"enabled"`             // 是否启用上下文管理
	MaxMessages       int  `yaml:"max_messages"`        // 最大保留消息数
	MaxTokens         int  `yaml:"max_tokens"`          // 最大 token 数
	ExpiryMinutes     int  `yaml:"expiry_minutes"`      // 上下文过期时间（分钟）
	InactiveRoomHours int  `yaml:"inactive_room_hours"` // 不活跃房间清理阈值（小时）
}

// StreamEditConfig 存储流式编辑配置
type StreamEditConfig struct {
	Enabled         bool `yaml:"enabled"`           // 是否启用流式编辑
	CharThreshold   int  `yaml:"char_threshold"`    // 触发编辑的字符阈值
	TimeThresholdMs int  `yaml:"time_threshold_ms"` // 触发编辑的时间阈值（毫秒）
	EditIntervalMs  int  `yaml:"edit_interval_ms"`  // 编辑间隔（毫秒）
	MaxEdits        int  `yaml:"max_edits"`         // 最大编辑次数
}

// RetryConfig 存储重试配置
type RetryConfig struct {
	Enabled         bool     `yaml:"enabled"`          // 是否启用重试
	MaxRetries      int      `yaml:"max_retries"`      // 最大重试次数
	InitialDelayMs  int      `yaml:"initial_delay_ms"` // 初始延迟（毫秒）
	MaxDelayMs      int      `yaml:"max_delay_ms"`     // 最大延迟（毫秒）
	BackoffFactor   float64  `yaml:"backoff_factor"`   // 退避因子
	FallbackEnabled bool     `yaml:"fallback_enabled"` // 是否启用降级
	FallbackModels  []string `yaml:"fallback_models"`  // 降级模型列表
}

// ToolCallingConfig 存储工具调用配置
type ToolCallingConfig struct {
	MaxIterations int `yaml:"max_iterations"` // 最大工具调用迭代次数（默认 5）
}

// MCPConfig 存储 MCP (Model Context Protocol) 集成配置
type MCPConfig struct {
	Enabled bool                    `yaml:"enabled"` // 是否启用 MCP 功能
	Servers map[string]ServerConfig `yaml:"servers"` // MCP 服务器配置
	Builtin BuiltinConfig           `yaml:"builtin"` // 内置工具配置
}

// BuiltinConfig 存储内置 MCP 工具配置
type BuiltinConfig struct {
	WebSearch WebSearchConfig `yaml:"web_search"` // web_search 工具配置
	JSSandbox JSSandboxConfig `yaml:"js_sandbox"` // js_sandbox 工具配置
}

// WebSearchConfig 存储 web_search 工具配置
type WebSearchConfig struct {
	Instances      []string `yaml:"instances"`       // SearXNG 实例列表
	MaxResults     int      `yaml:"max_results"`     // 最大返回结果数
	TimeoutSeconds int      `yaml:"timeout_seconds"` // 请求超时时间（秒）
}

// JSSandboxConfig 存储 js_sandbox 工具配置
type JSSandboxConfig struct {
	Enabled         bool `yaml:"enabled"`           // 是否启用 JS 沙箱
	TimeoutMs       int  `yaml:"timeout_ms"`        // 执行超时时间（毫秒）
	MaxMemoryMB     int  `yaml:"max_memory_mb"`     // 最大内存限制（MB）
	MaxOutputLength int  `yaml:"max_output_length"` // 最大输出长度（字符）
}

// ServerConfig 存储单个 MCP 服务器配置
type ServerConfig struct {
	Type            string            `yaml:"type"`              // 服务器类型：builtin, stdio, http
	Enabled         bool              `yaml:"enabled"`           // 是否启用
	Command         string            `yaml:"command,omitempty"` // stdio: 可执行文件路径
	Args            []string          `yaml:"args,omitempty"`    // stdio: 命令参数
	Env             map[string]string `yaml:"env,omitempty"`     // stdio: 环境变量
	URL             string            `yaml:"url,omitempty"`     // http: 服务器地址
	Token           string            `yaml:"token,omitempty"`   // http: Bearer 认证令牌
	Timeout         int               `yaml:"timeout_seconds"`   // 调用超时（秒）
	AllowedCommands []string          `yaml:"allowed_commands"`  // stdio: 命令白名单（默认禁止所有）
}

// ModelConfig 存储特定模型配置
type ModelConfig struct {
	Model       string  `yaml:"model"`       // 模型标识符
	Provider    string  `yaml:"provider"`    // 提供商（覆盖全局）
	BaseURL     string  `yaml:"base_url"`    // API URL（覆盖全局）
	APIKey      string  `yaml:"api_key"`     // API 密钥（覆盖全局）
	MaxTokens   int     `yaml:"max_tokens"`  // 最大 token 数（覆盖全局）
	Temperature float64 `yaml:"temperature"` // 温度（覆盖全局）
}

// ProactiveConfig 存储 AI 主动聊天配置
type ProactiveConfig struct {
	Enabled            bool            `yaml:"enabled"`              // 是否启用主动聊天
	MaxMessagesPerDay  int             `yaml:"max_messages_per_day"` // 每天最大主动消息数
	MinIntervalMinutes int             `yaml:"min_interval_minutes"` // 最小间隔时间（分钟）
	Silence            SilenceConfig   `yaml:"silence"`              // 静默检测配置
	Schedule           ScheduleConfig  `yaml:"schedule"`             // 定时聊天配置
	NewMember          NewMemberConfig `yaml:"new_member"`           // 新成员欢迎配置
	Decision           DecisionConfig  `yaml:"decision"`             // 决策模型配置
}

// SilenceConfig 存储静默检测配置
type SilenceConfig struct {
	Enabled              bool `yaml:"enabled"`                // 是否启用静默检测
	ThresholdMinutes     int  `yaml:"threshold_minutes"`      // 静默阈值（分钟）
	CheckIntervalMinutes int  `yaml:"check_interval_minutes"` // 检查间隔（分钟）
}

// ScheduleConfig 存储定时聊天配置
type ScheduleConfig struct {
	Enabled bool     `yaml:"enabled"` // 是否启用定时聊天
	Times   []string `yaml:"times"`   // 定时时间点（格式："HH:MM"）
}

// NewMemberConfig 存储新成员欢迎配置
type NewMemberConfig struct {
	Enabled       bool   `yaml:"enabled"`        // 是否启用新成员欢迎
	WelcomePrompt string `yaml:"welcome_prompt"` // 欢迎提示词
}

// DecisionConfig 存储决策模型配置
type DecisionConfig struct {
	Model          string  `yaml:"model"`           // 用于决策的模型
	Temperature    float64 `yaml:"temperature"`     // 决策温度（0-2）
	PromptTemplate string  `yaml:"prompt_template"` // 决策提示词模板
	StreamEnabled  bool    `yaml:"stream_enabled"`  // 是否启用流式请求（默认 true）
}

// MediaConfig 存储媒体文件处理配置
type MediaConfig struct {
	Enabled    bool   `yaml:"enabled"`     // 是否启用媒体文件处理
	MaxSizeMB  int    `yaml:"max_size_mb"` // 最大文件大小（MB）
	TimeoutSec int    `yaml:"timeout_sec"` // 处理超时时间（秒）
	Model      string `yaml:"model"`       // 图片识别专用模型（留空则使用默认模型）
}

// UseTokenAuth 检查是否使用 Token 认证
func (m *MatrixConfig) UseTokenAuth() bool {
	return m.AccessToken != ""
}

// UsePasswordAuth 检查是否使用密码认证
func (m *MatrixConfig) UsePasswordAuth() bool {
	return m.Password != "" && m.AccessToken == ""
}

// DefaultAIConfig 返回带有合理默认值的 AI 配置
func DefaultAIConfig() AIConfig {
	return AIConfig{
		Enabled:               false,
		Provider:              "",
		BaseURL:               "",
		APIKey:                "",
		DefaultModel:          "",
		MaxTokens:             256000,
		Temperature:           0.7,
		SystemPrompt:          "",
		RateLimitPerMinute:    0,
		Context:               DefaultContextConfig(),
		StreamEnabled:         true,
		StreamEdit:            DefaultStreamEditConfig(),
		Retry:                 DefaultRetryConfig(),
		ToolCalling:           DefaultToolCallingConfig(),
		Models:                make(map[string]ModelConfig),
		TimeoutSeconds:        30,
		DirectChatAutoReply:   true,
		GroupChatMentionReply: true,
		ReplyToBotReply:       true,
		Proactive:             DefaultProactiveConfig(),
		Media:                 DefaultMediaConfig(),
	}
}

// DefaultContextConfig 返回带有合理默认值的上下文配置
func DefaultContextConfig() ContextConfig {
	return ContextConfig{
		Enabled:           true,
		MaxMessages:       50,
		MaxTokens:         8000,
		ExpiryMinutes:     60,
		InactiveRoomHours: 24,
	}
}

// DefaultStreamEditConfig 返回带有合理默认值的流式编辑配置
func DefaultStreamEditConfig() StreamEditConfig {
	return StreamEditConfig{
		Enabled:         true,
		CharThreshold:   300,
		TimeThresholdMs: 3000,
		EditIntervalMs:  500,
		MaxEdits:        5,
	}
}

// DefaultRetryConfig 返回带有合理默认值的重试配置
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		Enabled:         true,
		MaxRetries:      3,
		InitialDelayMs:  1000,
		MaxDelayMs:      30000,
		BackoffFactor:   2.0,
		FallbackEnabled: true,
		FallbackModels:  []string{},
	}
}

// DefaultToolCallingConfig 返回带有合理默认值的工具调用配置
func DefaultToolCallingConfig() ToolCallingConfig {
	return ToolCallingConfig{
		MaxIterations: 5,
	}
}

// DefaultProactiveConfig 返回带有合理默认值的主动聊天配置
func DefaultProactiveConfig() ProactiveConfig {
	return ProactiveConfig{
		Enabled:            false,
		MaxMessagesPerDay:  5,
		MinIntervalMinutes: 60,
		Silence:            DefaultSilenceConfig(),
		Schedule:           DefaultScheduleConfig(),
		NewMember:          DefaultNewMemberConfig(),
		Decision:           DefaultDecisionConfig(),
	}
}

// DefaultSilenceConfig 返回带有合理默认值的静默检测配置
func DefaultSilenceConfig() SilenceConfig {
	return SilenceConfig{
		Enabled:              true,
		ThresholdMinutes:     60,
		CheckIntervalMinutes: 15,
	}
}

// DefaultScheduleConfig 返回带有合理默认值的定时聊天配置
func DefaultScheduleConfig() ScheduleConfig {
	return ScheduleConfig{
		Enabled: true,
		Times:   []string{"09:00", "12:00", "18:00"},
	}
}

// DefaultNewMemberConfig 返回带有合理默认值的新成员欢迎配置
func DefaultNewMemberConfig() NewMemberConfig {
	return NewMemberConfig{
		Enabled:       true,
		WelcomePrompt: "用友好的方式欢迎新成员加入",
	}
}

// DefaultDecisionConfig 返回带有合理默认值的决策模型配置
func DefaultDecisionConfig() DecisionConfig {
	return DecisionConfig{
		Model:          "",
		Temperature:    0.8,
		PromptTemplate: "",
		StreamEnabled:  true,
	}
}

// DefaultMediaConfig 返回带有合理默认值的媒体配置
func DefaultMediaConfig() MediaConfig {
	return MediaConfig{
		Enabled:    true,
		MaxSizeMB:  10,
		TimeoutSec: 30,
		Model:      "",
	}
}

// DefaultJSSandboxConfig 返回带有合理默认值的 JS 沙箱配置
func DefaultJSSandboxConfig() JSSandboxConfig {
	return JSSandboxConfig{
		Enabled:         true,
		TimeoutMs:       5000,
		MaxMemoryMB:     64,
		MaxOutputLength: 10000,
	}
}

// DefaultShutdownConfig 返回带有合理默认值的关闭配置
func DefaultShutdownConfig() ShutdownConfig {
	return ShutdownConfig{
		TimeoutSeconds: 30,
	}
}

// Validate 验证配置是否有效
func (m *MatrixConfig) Validate() error {
	if m.Homeserver == "" {
		return fmt.Errorf("homeserver is required")
	}
	if m.UserID == "" {
		return fmt.Errorf("user_id is required")
	}
	if !m.UseTokenAuth() && !m.UsePasswordAuth() {
		return fmt.Errorf("either password or access_token must be provided")
	}
	if m.EnableE2EE && m.E2EESessionPath == "" {
		return fmt.Errorf("e2ee_session_path is required when enable_e2ee is true")
	}
	if m.MaxConcurrentEvents < 0 {
		return fmt.Errorf("max_concurrent_events must be non-negative")
	}
	if m.MaxConcurrentEvents > 100 {
		slog.Warn("max_concurrent_events is very high, this may cause resource issues",
			"value", m.MaxConcurrentEvents)
	}
	return nil
}

// Validate 验证 AI 配置是否有效
func (a *AIConfig) Validate() error {
	if !a.Enabled {
		return nil
	}
	if a.Provider == "" {
		return fmt.Errorf("provider is required when AI is enabled")
	}
	if a.BaseURL == "" {
		return fmt.Errorf("base_url is required when AI is enabled")
	}
	if a.APIKey == "" {
		return fmt.Errorf("api_key is required when AI is enabled")
	}
	if a.DefaultModel == "" {
		return fmt.Errorf("default_model is required when AI is enabled")
	}
	if a.Temperature < 0 || a.Temperature > 2 {
		return fmt.Errorf("temperature must be between 0 and 2")
	}
	if a.TimeoutSeconds <= 0 {
		return fmt.Errorf("timeout_seconds must be positive")
	}
	if a.Media.Enabled {
		if a.Media.MaxSizeMB <= 0 {
			return fmt.Errorf("media.max_size_mb must be positive")
		}
		if a.Media.TimeoutSec <= 0 {
			return fmt.Errorf("media.timeout_sec must be positive")
		}
	}
	// 验证工具调用配置
	if err := a.ToolCalling.Validate(); err != nil {
		return fmt.Errorf("tool_calling: %w", err)
	}
	// 验证所有模型配置
	for name, modelCfg := range a.Models {
		if err := modelCfg.Validate(); err != nil {
			return fmt.Errorf("models[%s]: %w", name, err)
		}
	}
	return nil
}

// Validate 验证工具调用配置是否有效
func (t *ToolCallingConfig) Validate() error {
	if t.MaxIterations < 1 {
		return fmt.Errorf("max_iterations must be at least 1")
	}
	if t.MaxIterations > 20 {
		slog.Warn("max_iterations is very high, this may cause long response times",
			"value", t.MaxIterations)
	}
	return nil
}

// Validate 验证关闭配置是否有效
func (s *ShutdownConfig) Validate() error {
	if s.TimeoutSeconds < 5 {
		return fmt.Errorf("timeout_seconds must be at least 5 seconds")
	}
	if s.TimeoutSeconds > 300 {
		slog.Warn("shutdown timeout is very long, this may delay application exit",
			"timeout_seconds", s.TimeoutSeconds)
	}
	return nil
}

// Validate 验证模型配置是否有效
func (m *ModelConfig) Validate() error {
	if m.Model == "" {
		return fmt.Errorf("model is required in ModelConfig")
	}
	if m.Temperature < 0 || m.Temperature > 2 {
		return fmt.Errorf("temperature must be between 0 and 2")
	}
	if m.MaxTokens < 0 {
		return fmt.Errorf("max_tokens must be non-negative")
	}
	return nil
}

// Validate 验证主动聊天配置是否有效
func (p *ProactiveConfig) Validate() error {
	if !p.Enabled {
		return nil
	}
	if p.MaxMessagesPerDay < 0 {
		return fmt.Errorf("max_messages_per_day must be non-negative")
	}
	if p.MinIntervalMinutes < 0 {
		return fmt.Errorf("min_interval_minutes must be non-negative")
	}
	if err := p.Silence.Validate(); err != nil {
		return fmt.Errorf("silence config: %w", err)
	}
	if err := p.Schedule.Validate(); err != nil {
		return fmt.Errorf("schedule config: %w", err)
	}
	return nil
}

// Validate 验证静默检测配置是否有效
func (s *SilenceConfig) Validate() error {
	if !s.Enabled {
		return nil
	}
	if s.ThresholdMinutes <= 0 {
		return fmt.Errorf("threshold_minutes must be positive")
	}
	if s.CheckIntervalMinutes <= 0 {
		return fmt.Errorf("check_interval_minutes must be positive")
	}
	return nil
}

// Validate 验证定时聊天配置是否有效
func (s *ScheduleConfig) Validate() error {
	if !s.Enabled {
		return nil
	}
	if len(s.Times) == 0 {
		return fmt.Errorf("times must not be empty when schedule is enabled")
	}
	for i, t := range s.Times {
		// 严格验证格式为 "HH:MM"（必须 5 个字符）
		if len(t) != 5 {
			return fmt.Errorf("times[%d] invalid format %q: must be HH:MM (24-hour format)", i, t)
		}
		if _, err := time.Parse("15:04", t); err != nil {
			return fmt.Errorf("times[%d] invalid format %q: must be HH:MM (24-hour format)", i, t)
		}
	}
	return nil
}

// Validate 验证新成员欢迎配置是否有效
func (n *NewMemberConfig) Validate() error {
	if !n.Enabled {
		return nil
	}
	if n.WelcomePrompt == "" {
		return fmt.Errorf("welcome_prompt is required when new_member is enabled")
	}
	return nil
}

// Validate 验证决策模型配置是否有效
func (d *DecisionConfig) Validate() error {
	if d.Temperature < 0 || d.Temperature > 2 {
		return fmt.Errorf("temperature must be between 0 and 2")
	}
	return nil
}

// GetModelConfig 获取指定模型的配置
//
// 它首先查找模型特定配置，如果找到则返回继承全局配置后的模型配置。
// 如果未找到特定配置，则返回基于全局配置的默认模型配置。
//
// 参数:
//   - modelID: 模型标识符
//
// 返回值:
//   - ModelConfig: 合并后的模型配置
//   - bool: 是否找到了特定配置（false 表示使用默认配置）
func (a *AIConfig) GetModelConfig(modelID string) (ModelConfig, bool) {
	if config, ok := a.Models[modelID]; ok {
		// 合并配置：特定配置优先，未设置的字段使用全局配置
		if config.Provider == "" {
			config.Provider = a.Provider
		}
		if config.BaseURL == "" {
			config.BaseURL = a.BaseURL
		}
		if config.APIKey == "" {
			config.APIKey = a.APIKey
		}
		if config.MaxTokens == 0 {
			config.MaxTokens = a.MaxTokens
		}
		if config.Temperature == 0 {
			config.Temperature = a.Temperature
		}
		return config, true
	}

	// 返回默认配置
	return ModelConfig{
		Model:       modelID,
		Provider:    a.Provider,
		BaseURL:     a.BaseURL,
		APIKey:      a.APIKey,
		MaxTokens:   a.MaxTokens,
		Temperature: a.Temperature,
	}, false
}

// DefaultConfigPath 返回默认配置文件路径
func DefaultConfigPath() string {
	return filepath.Join(".", "config.yaml")
}

// Load 读取并解析指定路径的配置文件
// 如果路径为空，则使用默认路径 (./config.yaml)
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}

	// 检查文件权限
	if info, err := os.Stat(path); err == nil {
		perm := info.Mode().Perm()
		if perm != 0o600 {
			slog.Warn("配置文件权限过于宽松，建议设置为 0600",
				"path", path,
				"current_permission", fmt.Sprintf("%o", perm))
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := DefaultConfig()

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return cfg, nil
}

// LoadOrDefault 从指定路径读取配置，如果文件不存在则返回默认配置
func LoadOrDefault(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return DefaultConfig(), nil
	}

	return Load(path)
}

// DefaultConfig 返回带有合理默认值的配置
func DefaultConfig() *Config {
	return &Config{
		Matrix: MatrixConfig{
			Homeserver:          "https://matrix.org",
			UserID:              "",
			DeviceID:            "",
			DeviceName:          "Saber Bot",
			Password:            "",
			AccessToken:         "",
			EnableE2EE:          true,
			E2EESessionPath:     "./saber.session",
			PickleKeyPath:       "",
			MaxConcurrentEvents: 10,
		},
		AI: DefaultAIConfig(),
		MCP: MCPConfig{
			Enabled: true,
			Builtin: BuiltinConfig{
				WebSearch: WebSearchConfig{
					Instances:      nil,
					MaxResults:     5,
					TimeoutSeconds: 20,
				},
				JSSandbox: DefaultJSSandboxConfig(),
			},
		},
		Shutdown: DefaultShutdownConfig(),
	}
}

// ExampleConfig 返回示例配置内容。
func ExampleConfig() string {
	return `matrix:
  # Matrix 服务器地址
  homeserver: "https://matrix.org"
  # 完整的 Matrix 用户 ID（格式：@username:server.org）
  user_id: "@your-bot:matrix.org"
  # 设备标识符（可选，留空则服务器自动生成）
  device_id: "saber-bot-device"
  # 设备显示名称（可选）
  device_name: "Saber Bot"
  # 认证方式（二选一，access_token 优先级更高）
  # 方式 1: 使用 Access Token（推荐，更安全）
  access_token: "syt_xxxxxxxxxxxxx_xxxxxxxxxxxx"
  # 方式 2: 使用密码登录（首次登录使用）
  # password: "your-secure-password"
  # 启动时自动加入的房间列表（可选）
  # auto_join_rooms:
  #   - "!roomid1:matrix.org"
  #   - "#public-room:matrix.org"
  # 端到端加密（E2EE）配置
  enable_e2ee: true  # 启用端到端加密（默认启用）
  e2ee_session_path: "./saber.session"  # 加密会话文件路径
  # pickle_key_path: "./saber.session.key"  # pickle 密钥路径（可选，默认为 e2ee_session_path + ".key"）
  # 最大并发事件处理数（默认 10）
  max_concurrent_events: 10

ai:
  # 启用 AI 功能
  enabled: false
  # AI 提供商（如 openai, azure, anthropic）
  provider: "openai"
  # API 基础 URL
  base_url: "https://api.openai.com/v1"
  # API 密钥
  api_key: ""
  # 默认使用的模型
  default_model: "gpt-4o-mini"
  # 最大生成 token 数
  max_tokens: 256000
  # 生成温度（0-2）
  temperature: 0.7
  # 系统提示词（可选，用于自定义 AI 行为）
  # system_prompt: "You are a helpful assistant."
  # 每分钟请求限制（0 表示无限制）
  rate_limit_per_minute: 0
  # 上下文管理配置
  context:
    enabled: true
    max_messages: 50
    max_tokens: 8000
    expiry_minutes: 60
    # 不活跃房间清理阈值（小时，默认 24）
    inactive_room_hours: 24
  # 是否启用流式响应
  stream_enabled: true
  # 流式编辑配置
  stream_edit:
    enabled: true
    char_threshold: 300
    time_threshold_ms: 3000
    edit_interval_ms: 500
    max_edits: 5
  # 重试配置
  retry:
    enabled: true
    max_retries: 3
    initial_delay_ms: 1000
    max_delay_ms: 30000
    backoff_factor: 2.0
    fallback_enabled: true
    fallback_models: []
  # 工具调用配置
  tool_calling:
    # 最大工具调用迭代次数（默认 5）
    max_iterations: 5
  # 多模型配置示例
  models: {}
    # fast:
    #   model: "gpt-4o-mini"
    #   temperature: 0.3
    # creative:
    #   model: "gpt-4o"
    #   temperature: 0.9
  # 请求超时时间（秒）
  timeout_seconds: 30
  # 在私聊中自动回复（无需 !ai 前缀）
  direct_chat_auto_reply: true
  # 在群聊中 @mention 时自动回复（无需 !ai 前缀）
  group_chat_mention_reply: true
  # 回复机器人消息时自动回复（用于连续对话）
  reply_to_bot_reply: true
  # 主动聊天配置
  proactive:
    # 是否启用主动聊天
    enabled: false
    # 每天最大主动消息数
    max_messages_per_day: 5
    # 最小间隔时间（分钟）
    min_interval_minutes: 60
    # 静默检测配置
    silence:
      enabled: true
      threshold_minutes: 60
      check_interval_minutes: 15
    # 定时聊天配置
    schedule:
      enabled: true
      times: ["09:00", "12:00", "18:00"]
    # 新成员欢迎配置
    new_member:
      enabled: true
      welcome_prompt: "用友好的方式欢迎新成员加入"
    # 决策模型配置
    decision:
      model: ""
      temperature: 0.8
      prompt_template: ""
      # 是否启用流式请求（默认 true，可更快响应）
      stream_enabled: true
  # 媒体文件处理配置
  media:
    # 是否启用媒体处理（如图片理解）
    enabled: true
    # 最大文件大小（MB）
    max_size_mb: 10
    # 处理超时时间（秒）
    timeout_sec: 30
    # 图片识别专用模型（留空则使用默认模型）
    # model: "gpt-4o"

# MCP (Model Context Protocol) 配置
mcp:
  # 启用 MCP 功能
  enabled: true
  # 外部 MCP 服务器配置（可选）
  # servers:
  #   # stdio 类型服务器示例
  #   filesystem:
  #     type: stdio
  #     enabled: true
  #     command: "/path/to/mcp-server-filesystem"
  #     args: ["--root", "/home/user/documents"]
  #     timeout_seconds: 30
  #     # env:
  #     #   DEBUG: "1"
  #   # http 类型服务器示例
  #   remote-server:
  #     type: http
  #     enabled: false
  #     url: "https://mcp.example.com/api"
  #     token: "your-bearer-token"
  #     timeout_seconds: 30
  # 内置工具配置
  builtin:
    # web_search 搜索工具配置
    web_search:
      # SearXNG 实例列表（可选，留空使用默认实例）
      # instances:
      #   - "https://seek.fyi"
      #   - "https://search.femboy.ad"
      # 最大返回结果数（默认 5，最大 10）
      max_results: 5
      # 请求超时时间（秒，默认 20）
      timeout_seconds: 20
    # js_sandbox JS 沙箱工具配置
    js_sandbox:
      # 是否启用 JS 沙箱（默认启用）
      enabled: true
      # 执行超时时间（毫秒，默认 5000）
      timeout_ms: 5000
      # 最大内存限制 MB（默认 64）
      max_memory_mb: 64
      # 最大输出长度（字符，默认 10000）
      max_output_length: 10000

# 关闭配置
shutdown:
  # 关闭超时时间（秒，默认 30）
  timeout_seconds: 30
`
}

// GenerateExample 将示例配置写入文件。
func GenerateExample(path string) error {
	return os.WriteFile(path, []byte(ExampleConfig()), 0o600)
}
