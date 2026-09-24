// Package config 提供 YAML 配置文件的加载、验证和默认值管理。
package config

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 存储从 YAML 配置文件加载的应用程序配置
type Config struct {
	Agent     AgentConfig      `yaml:"agent"`
	Server    HTTPServerConfig `yaml:"server"`
	Execution ExecutionConfig  `yaml:"execution"`
	Commands  CommandConfig    `yaml:"commands"`
	Matrix    MatrixConfig     `yaml:"matrix"`
	Platforms PlatformsConfig  `yaml:"platforms"`
	AI        AIConfig         `yaml:"ai"`
	MCP       MCPConfig        `yaml:"mcp"`
	Shutdown  ShutdownConfig   `yaml:"shutdown"`
}

// ShutdownConfig 存储关闭配置
type ShutdownConfig struct {
	TimeoutSeconds int `yaml:"timeout_seconds"` // 关闭超时时间（秒）
}

// MatrixConfig 存储 Matrix 连接配置
type MatrixConfig struct {
	StreamEdit            StreamEditConfig `yaml:"-"`                        // 旧即时 Matrix 展示路径的内部默认值；持久化任务独立投递。
	DirectChatAutoReply   bool             `yaml:"direct_chat_auto_reply"`   // 在私聊中自动回复（无需 !ai 前缀）
	GroupChatMentionReply bool             `yaml:"group_chat_mention_reply"` // 在群聊中 @mention 时自动回复（无需 !ai 前缀）
	ReplyToBotReply       bool             `yaml:"reply_to_bot_reply"`       // 回复机器人自己的回复（用于连续对话）
	Proactive             ProactiveConfig  `yaml:"proactive"`                // 主动聊天配置
	Media                 MediaConfig      `yaml:"media"`                    // 媒体文件处理配置
	Meme                  MemeConfig       `yaml:"meme"`

	Homeserver             string   `yaml:"homeserver"`
	UserID                 string   `yaml:"user_id"`                   // 完整的 Matrix ID，如 @user:matrix.org
	DeviceID               string   `yaml:"device_id"`                 // 设备标识符
	DeviceName             string   `yaml:"device_name"`               // 设备显示名称
	Password               string   `yaml:"password"`                  // 密码登录（可选）
	AccessToken            string   `yaml:"access_token"`              // Token 登录（可选，优先级高于密码）
	AutoJoinRooms          []string `yaml:"auto_join_rooms"`           // 启动时自动加入的房间列表
	EnableE2EE             bool     `yaml:"enable_e2ee"`               // 启用端到端加密（可选）
	E2EESessionPath        string   `yaml:"e2ee_session_path"`         // 端到端加密会话文件路径（可选）
	PickleKeyPath          string   `yaml:"pickle_key_path"`           // E2EE pickle 密钥文件路径（可选，默认为 e2ee_session_path + ".key")
	MaxConcurrentEvents    int      `yaml:"max_concurrent_events"`     // 最大并发事件处理数（默认 10）
	StrictSessionPermCheck bool     `yaml:"strict_session_perm_check"` // 严格会话文件权限检查（默认 false，仅警告）
}

// AIConfig 存储 AI 服务配置
type AIConfig struct {
	ReasoningEffort string                    `yaml:"reasoning_effort,omitempty"` // 全局思考等级，空值使用上游默认。
	Enabled         bool                      `yaml:"enabled"`                    // 是否启用 AI 功能
	Providers       map[string]ProviderConfig `yaml:"providers"`                  // 多提供商配置
	DefaultModel    string                    `yaml:"default_model"`              // 默认使用的模型（完全限定名称，如 openai.gpt-4o-mini）

	MaxTokens          int                    `yaml:"max_tokens"`              // 最大生成 token 数
	Temperature        float64                `yaml:"temperature"`             // 生成温度（0-2）
	SystemPrompt       string                 `yaml:"system_prompt"`           // 系统提示词
	RateLimitPerMinute int                    `yaml:"rate_limit_per_minute"`   // 每分钟请求限制（0 表示无限制）
	Models             map[string]ModelConfig `yaml:"models"`                  // 模型别名配置
	TimeoutSeconds     int                    `yaml:"request_timeout_seconds"` // 请求超时时间（秒）
}

// AgentConfig 定义所有接入共用的任务执行策略。
type AgentConfig struct {
	ToolCallingConfig  `yaml:",inline"`
	Context            ContextConfig        `yaml:"context"`              // 上下文管理配置
	StreamEnabled      bool                 `yaml:"stream"`               // 是否启用流式响应
	TaskReceiptEnabled bool                 `yaml:"task_receipt_enabled"` // 是否在任务入库后发送接收回执
	Retry              RetryConfig          `yaml:"retry"`                // 重试配置
	CircuitBreaker     CircuitBreakerConfig `yaml:"circuit_breaker"`      // 熔断器配置
}

// ContextConfig 存储上下文管理配置
type ContextConfig struct {
	Enabled           bool `yaml:"enabled"`          // 是否启用上下文管理
	MaxMessages       int  `yaml:"max_messages"`     // 最大保留消息数
	MaxTokens         int  `yaml:"max_input_tokens"` // 最大 token 数
	ExpiryMinutes     int  `yaml:"-"`                // 上下文过期时间（分钟）
	InactiveRoomHours int  `yaml:"-"`                // 不活跃房间清理阈值（小时）
}

// StreamEditConfig 存储 Matrix 专属的流式编辑配置。
type StreamEditConfig struct {
	Enabled         bool `yaml:"enabled"`           // 是否启用流式编辑
	CharThreshold   int  `yaml:"char_threshold"`    // 触发编辑的字符阈值
	TimeThresholdMs int  `yaml:"time_threshold_ms"` // 触发编辑的时间阈值（毫秒）
	EditIntervalMs  int  `yaml:"edit_interval_ms"`  // 编辑间隔（毫秒）
	MaxEdits        int  `yaml:"max_edits"`         // 最大编辑次数
}

// CircuitBreakerConfig 存储熔断器配置
type CircuitBreakerConfig struct {
	Enabled          bool `yaml:"enabled"`           // 是否启用熔断器
	FailureThreshold int  `yaml:"failure_threshold"` // 触发熔断的失败次数阈值
	ResetTimeout     int  `yaml:"reset_timeout"`     // 熔断后重置时间（秒）
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
	// MaxIterations 限制模型轮数，包含最终回答，默认 5。
	MaxIterations int `yaml:"max_rounds"`
	// TimeoutSeconds 限制整次 Agent 运行，零值使用 600 秒。
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// MaxToolOutputBytes 限制每条工具结果，零值使用 32 KiB。
	MaxToolOutputBytes int `yaml:"max_tool_output_bytes"`
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
	RequestTimeoutSeconds int    `yaml:"request_timeout_seconds,omitempty"` // 单次模型请求总时限，包含流式读取；零值继承全局。
	API                   string `yaml:"api,omitempty"`                     // 协议（覆盖提供商）；空值沿用原有 Chat Completions。
	ReasoningEffort       string `yaml:"reasoning_effort,omitempty"`        // 思考等级；空值继承提供商或全局设置。

	Model       string   `yaml:"model"`                 // 模型标识符
	Provider    string   `yaml:"provider"`              // 提供商（覆盖全局）
	BaseURL     string   `yaml:"base_url"`              // API URL（覆盖全局）
	APIKey      string   `yaml:"api_key"`               // API 密钥（覆盖全局）
	MaxTokens   int      `yaml:"max_tokens"`            // 最大 token 数（覆盖全局）
	Temperature *float64 `yaml:"temperature,omitempty"` // nil 继承全局，显式 0 保持零温度。
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
	PersistState       bool            `yaml:"persist_state"`        // 是否持久化状态
	StatePath          string          `yaml:"state_path"`           // 状态文件路径
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

// MemeConfig 存储 meme/GIF 搜索配置
type MemeConfig struct {
	Enabled        bool   `yaml:"enabled"`         // 是否启用 meme 功能
	APIKey         string `yaml:"api_key"`         // Klipy API Key
	MaxResults     int    `yaml:"max_results"`     // 最大返回结果数（默认 5）
	TimeoutSeconds int    `yaml:"timeout_seconds"` // 请求超时时间（秒，默认 10）
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
		Enabled:            false,
		Providers:          make(map[string]ProviderConfig),
		DefaultModel:       "",
		MaxTokens:          8192,
		Temperature:        0.7,
		SystemPrompt:       "",
		RateLimitPerMinute: 0,
		Models:             make(map[string]ModelConfig),
		TimeoutSeconds:     120,
	}
}

// DefaultAgentConfig 返回通用任务默认策略。
func DefaultAgentConfig() AgentConfig {
	return AgentConfig{
		Context:            DefaultContextConfig(),
		StreamEnabled:      true,
		TaskReceiptEnabled: false,
		Retry:              DefaultRetryConfig(),
		ToolCallingConfig:  DefaultToolCallingConfig(),
		CircuitBreaker:     DefaultCircuitBreakerConfig(),
	}
}

// DefaultContextConfig 返回带有合理默认值的上下文配置
func DefaultContextConfig() ContextConfig {
	return ContextConfig{
		Enabled:           true,
		MaxMessages:       50,
		MaxTokens:         32768,
		ExpiryMinutes:     0,
		InactiveRoomHours: 0,
	}
}

// DefaultStreamEditConfig 返回 Matrix 流式编辑的默认值。
func DefaultStreamEditConfig() StreamEditConfig {
	return StreamEditConfig{
		Enabled:         true,
		CharThreshold:   300,
		TimeThresholdMs: 3000,
		EditIntervalMs:  500,
		MaxEdits:        5,
	}
}

// DefaultCircuitBreakerConfig 返回带有合理默认值的熔断器配置
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		Enabled:          false, // 默认禁用，保持向后兼容
		FailureThreshold: 5,
		ResetTimeout:     30,
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
		FallbackEnabled: false,
		FallbackModels:  []string{},
	}
}

// DefaultToolCallingConfig 返回带有合理默认值的工具调用配置
func DefaultToolCallingConfig() ToolCallingConfig {
	return ToolCallingConfig{
		MaxIterations:      5,
		TimeoutSeconds:     600,
		MaxToolOutputBytes: 32768,
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

// DefaultMemeConfig 返回带有合理默认值的 meme 配置
func DefaultMemeConfig() MemeConfig {
	return MemeConfig{
		Enabled:        false,
		APIKey:         "",
		MaxResults:     5,
		TimeoutSeconds: 10,
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

// Validate 在创建已启用的 Matrix 客户端时验证连接配置。
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
	if m.Media.Enabled && (m.Media.MaxSizeMB <= 0 || m.Media.TimeoutSec <= 0) {
		return fmt.Errorf("matrix.media requires positive max_size_mb and timeout_sec")
	}
	if err := m.Proactive.Validate(); err != nil {
		return err
	}
	if err := m.Meme.Validate(); err != nil {
		return err
	}
	return nil
}

// Validate 验证 AI 配置是否有效
func (a *AIConfig) Validate() error {
	if !a.Enabled {
		return nil
	}

	// 验证默认模型
	if a.DefaultModel == "" {
		return fmt.Errorf("default_model is required when AI is enabled")
	}

	// 验证默认模型格式和提供商存在性
	provider, modelID, err := ParseModelID(a.DefaultModel)
	if err != nil {
		return fmt.Errorf("default_model: %w", err)
	}
	if _, ok := a.Providers[provider]; !ok {
		return fmt.Errorf("default_model: provider %q not found in providers config", provider)
	}

	// 验证各提供商配置
	for name, p := range a.Providers {
		if err := p.Validate(name); err != nil {
			return fmt.Errorf("providers.%s: %w", name, err)
		}
		a.Providers[name] = p
		// 检查默认模型是否存在于提供商的模型列表中
		if name == provider {
			if _, found := p.Models[modelID]; !found {
				// 模型未显式配置，但允许使用（使用提供商默认配置）
				slog.Debug("default model not explicitly configured in provider, will use provider defaults",
					"provider", provider, "model", modelID)
			}
		}
	}

	// 验证基本参数
	if a.Temperature < 0 || a.Temperature > 2 {
		return fmt.Errorf("temperature must be between 0 and 2")
	}
	if a.TimeoutSeconds <= 0 || int64(a.TimeoutSeconds) > 9223372036 {
		return fmt.Errorf("request_timeout_seconds must be positive and not overflow time.Duration")
	}
	if a.MaxTokens < 0 || a.RateLimitPerMinute < 0 {
		return fmt.Errorf("max_tokens and rate_limit_per_minute must be non-negative")
	}
	// 验证所有模型别名配置
	for name, modelCfg := range a.Models {
		if err := modelCfg.Validate(); err != nil {
			return fmt.Errorf("models[%s]: %w", name, err)
		}
	}

	return nil
}

// Validate 验证任务限制与上下文预算。
func (a *AgentConfig) Validate() error {
	if err := a.ToolCallingConfig.Validate(); err != nil {
		return err
	}
	if a.Context.MaxMessages < 1 || a.Context.MaxTokens < 1 {
		return fmt.Errorf("context max_messages and max_input_tokens must be positive")
	}
	if a.Retry.MaxRetries < 0 || a.Retry.InitialDelayMs < 0 || a.Retry.MaxDelayMs < a.Retry.InitialDelayMs || int64(a.Retry.MaxDelayMs) > 9223372036854 || a.Retry.BackoffFactor < 1 {
		return fmt.Errorf("invalid retry limits")
	}
	if a.Retry.FallbackEnabled && len(a.Retry.FallbackModels) == 0 {
		return fmt.Errorf("retry.fallback_models is required when fallback is enabled")
	}
	// 验证熔断器配置
	if a.CircuitBreaker.Enabled {
		if a.CircuitBreaker.FailureThreshold <= 0 {
			return fmt.Errorf("circuit_breaker.failure_threshold must be positive")
		}
		if a.CircuitBreaker.ResetTimeout <= 0 {
			return fmt.Errorf("circuit_breaker.reset_timeout must be positive")
		}
	}
	return nil
}

// Validate 验证工具调用配置是否有效
func (t *ToolCallingConfig) Validate() error {
	if t.TimeoutSeconds == 0 {
		t.TimeoutSeconds = 600
	}
	if t.MaxToolOutputBytes == 0 {
		t.MaxToolOutputBytes = 32768
	}
	// 防止秒数转换为 time.Duration 纳秒时溢出。
	if t.TimeoutSeconds < 0 || int64(t.TimeoutSeconds) > 9223372036 {
		return fmt.Errorf("timeout_seconds must be between 0 and 9223372036")
	}
	if t.MaxToolOutputBytes < 0 || t.MaxToolOutputBytes > 0 && t.MaxToolOutputBytes < 128 {
		return fmt.Errorf("max_tool_output_bytes must be 0 or at least 128")
	}
	if t.MaxIterations < 1 {
		return fmt.Errorf("max_rounds must be at least 1")
	}
	if t.MaxIterations > 20 {
		slog.Warn("max_rounds is very high, this may cause long response times",
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
	if m.RequestTimeoutSeconds < 0 || int64(m.RequestTimeoutSeconds) > 9223372036 {
		return fmt.Errorf("invalid request_timeout_seconds")
	}
	if err := ValidateAPI(m.API); err != nil {
		return err
	}
	if m.Model == "" {
		return fmt.Errorf("model is required in ModelConfig")
	}
	if m.Temperature != nil && (*m.Temperature < 0 || *m.Temperature > 2) {
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

// Validate 验证 meme 配置是否有效
func (m *MemeConfig) Validate() error {
	if !m.Enabled {
		return nil
	}
	if m.APIKey == "" {
		return fmt.Errorf("api_key is required when meme is enabled")
	}
	if m.MaxResults <= 0 {
		return fmt.Errorf("max_results must be positive")
	}
	if m.TimeoutSeconds <= 0 {
		return fmt.Errorf("timeout_seconds must be positive")
	}
	return nil
}

// GetModelConfig 解析完全限定模型名称或显式别名，合并模型、提供商与全局参数。
func (a *AIConfig) GetModelConfig(modelID string) (ModelConfig, bool) {
	if provider, model, err := ParseModelID(modelID); err == nil {
		if p, ok := a.Providers[provider]; ok {
			if p.Type == "" {
				p.Type = provider
			}
			cfg, found := p.GetModelConfig(model)
			return a.mergeProviderConfig(cfg, p), found
		}
	}
	if cfg, ok := a.Models[modelID]; ok {
		provider := cfg.Provider
		if provider == "" {
			provider, _, _ = ParseModelID(a.DefaultModel)
		}
		if p, ok := a.Providers[provider]; ok {
			// 别名的 provider 是提供商配置名，客户端使用其 type。
			cfg.Provider = ""
			if p.Type == "" {
				p.Type = provider
			}
			return a.mergeProviderConfig(cfg, p), true
		}
	}
	return ModelConfig{}, false
}

// mergeProviderConfig 合并提供商配置到模型配置。
func (a *AIConfig) mergeProviderConfig(cfg ModelConfig, providerCfg ProviderConfig) ModelConfig {
	if cfg.RequestTimeoutSeconds == 0 {
		cfg.RequestTimeoutSeconds = a.TimeoutSeconds
	}
	// Model 字段由调用方设置，这里不处理
	if cfg.API == "" {
		cfg.API = providerCfg.API
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = providerCfg.BaseURL
	}
	if cfg.APIKey == "" {
		cfg.APIKey = providerCfg.APIKey
	}
	if cfg.Provider == "" {
		cfg.Provider = providerCfg.Type
	}
	if cfg.ReasoningEffort == "" {
		cfg.ReasoningEffort = providerCfg.ReasoningEffort
	}
	if cfg.ReasoningEffort == "" {
		cfg.ReasoningEffort = a.ReasoningEffort
	}
	// 继承全局默认值
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = a.MaxTokens
	}
	if cfg.Temperature == nil {
		cfg.Temperature = new(a.Temperature)
	}
	return cfg
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

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := DefaultConfig()

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("config must contain exactly one YAML document")
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

// DefaultMatrixConfig 返回带有合理默认值的 Matrix 配置
func DefaultMatrixConfig() MatrixConfig {
	return MatrixConfig{
		StreamEdit:             DefaultStreamEditConfig(),
		DirectChatAutoReply:    true,
		GroupChatMentionReply:  true,
		ReplyToBotReply:        true,
		Proactive:              DefaultProactiveConfig(),
		Media:                  DefaultMediaConfig(),
		Meme:                   DefaultMemeConfig(),
		Homeserver:             "https://matrix.org",
		UserID:                 "",
		DeviceID:               "",
		DeviceName:             "Saber Bot",
		Password:               "",
		AccessToken:            "",
		EnableE2EE:             true,
		E2EESessionPath:        "./saber.session",
		PickleKeyPath:          "",
		MaxConcurrentEvents:    10,
		StrictSessionPermCheck: false,
	}
}

// DefaultMCPConfig 返回带有合理默认值的 MCP 配置
func DefaultMCPConfig() MCPConfig {
	return MCPConfig{
		Enabled: false,
		Builtin: BuiltinConfig{
			WebSearch: WebSearchConfig{
				Instances:      nil,
				MaxResults:     5,
				TimeoutSeconds: 20,
			},
			JSSandbox: DefaultJSSandboxConfig(),
		},
	}
}

// DefaultConfig 返回带有合理默认值的配置
func DefaultConfig() *Config {
	return &Config{
		Server:    HTTPServerConfig{Listen: "127.0.0.1:8320", TokenFile: ".saber-token"},
		Agent:     DefaultAgentConfig(),
		Matrix:    DefaultMatrixConfig(),
		Platforms: DefaultPlatformsConfig(),
		AI:        DefaultAIConfig(),
		MCP:       DefaultMCPConfig(),
		Shutdown:  DefaultShutdownConfig(),
	}
}

// ExampleConfig 返回示例配置内容。
func ExampleConfig() string {
	return `# 默认启动常驻服务：saber；另开终端执行 saber chat。
server:
  listen: "127.0.0.1:8320"
  token_file: ".saber-token" # 首次启动自动创建，相对于配置文件目录

ai:
  enabled: false # 配置提供商及 default_model 后开启
  providers: {}
  # providers:
  #   podlink-responses:
  #     type: openai
  #     api: openai-responses
  #     base_url: "http://127.0.0.1:8317/v1"
  #     api_key: ""
  #     models:
  #       gpt-5.6-sol:
  #         model: gpt-5.6-sol
  #         reasoning_effort: high # 按模型支持情况设置；省略则用上游默认
  default_model: "" # 例如 podlink-responses.gpt-5.6-sol
  max_tokens: 8192 # 每次生成预算；模型配置可覆盖，并非模型能力上限
  temperature: 0.7 # Responses 仅在 reasoning_effort: none 时发送
  request_timeout_seconds: 120 # 单次请求总时限，包含流式读取

agent:
  stream: true # 模型传输开关，所有接入共同遵守
  task_receipt_enabled: false # 默认不发送「已接收，任务 #...」；结果仍正常投递
  max_rounds: 5 # 包含最终回答
  timeout_seconds: 600 # 整次任务，包含请求、重试等待与工具执行
  max_tool_output_bytes: 32768
  context:
    enabled: true
    max_messages: 50
    max_input_tokens: 32768 # 保守估算输入预算，不删除数据库历史
  retry:
    enabled: true
    max_retries: 3
    initial_delay_ms: 1000
    max_delay_ms: 30000
    backoff_factor: 2
    fallback_enabled: false
    fallback_models: []

# 可选接入与执行能力；详细配置见 docs/configuration.md、docs/execution.md。
platforms:
  terminal:
    enabled: true # 本机 HTTP 聊天入口（saber chat / TUI）
  matrix:
    enabled: false # Matrix 聊天入口，账号等明细在顶层 matrix: 配置
  violet:
    enabled: false # Violet Bot API 接入，凭据在 Violet 管理端签发
mcp:
  enabled: false # 开启后仍须 execution 中的身份及工具授权
execution:
  enabled: false
commands:
  admins: [] # 全局模型切换、共享人格创建和删除；按 platform/account/users 精确配置
  session_writers: [] # 当前会话上下文和人格修改；按 platform/account/room/users 精确配置
  legacy_persona_account: "" # 确认旧 room_personas 全属此 Matrix bot 时填其完整用户 ID，空值保留待核查
shutdown:
  timeout_seconds: 30
`
}

// GenerateExample 将示例配置写入文件。
func GenerateExample(path string) error {
	return os.WriteFile(path, []byte(ExampleConfig()), 0o600)
}
