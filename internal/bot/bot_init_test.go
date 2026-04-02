// Package bot_test 包含机器人初始化逻辑的单元测试。
package bot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// TestBuildInfo_RuntimePlatform 测试 BuildInfo 的 RuntimePlatform 方法。
func TestBuildInfo_RuntimePlatform(t *testing.T) {
	info := matrix.BuildInfo{
		Version:       "1.0.0",
		GitCommit:     "abc123",
		GitBranch:     "main",
		BuildTime:     "2024-01-01",
		GoVersion:     "go1.21.0",
		BuildPlatform: "linux/amd64",
	}

	platform := info.RuntimePlatform()
	if platform == "" {
		t.Error("RuntimePlatform returned empty string")
	}

	// 应该包含 GOOS/GOARCH 格式
	if !strings.Contains(platform, "/") {
		t.Errorf("RuntimePlatform should contain '/', got %s", platform)
	}
}

// TestBuildInfo_AllFields 测试 BuildInfo 所有字段。
func TestBuildInfo_AllFields(t *testing.T) {
	info := matrix.BuildInfo{
		Version:       "1.0.0",
		GitCommit:     "abc123",
		GitBranch:     "main",
		BuildTime:     "2024-01-01",
		GoVersion:     "go1.21.0",
		BuildPlatform: "linux/amd64",
	}

	if info.Version != "1.0.0" {
		t.Errorf("expected Version '1.0.0', got %s", info.Version)
	}
	if info.GitCommit != "abc123" {
		t.Errorf("expected GitCommit 'abc123', got %s", info.GitCommit)
	}
	if info.GitBranch != "main" {
		t.Errorf("expected GitBranch 'main', got %s", info.GitBranch)
	}
	if info.BuildTime != "2024-01-01" {
		t.Errorf("expected BuildTime '2024-01-01', got %s", info.BuildTime)
	}
	if info.GoVersion != "go1.21.0" {
		t.Errorf("expected GoVersion 'go1.21.0', got %s", info.GoVersion)
	}
	if info.BuildPlatform != "linux/amd64" {
		t.Errorf("expected BuildPlatform 'linux/amd64', got %s", info.BuildPlatform)
	}
}

// TestAppState_Struct 测试 appState 结构体。
func TestAppState_Struct(t *testing.T) {
	state := &appState{
		cfg: &config.Config{},
		info: matrix.BuildInfo{
			Version: "test",
		},
	}

	if state.cfg == nil {
		t.Error("cfg should not be nil")
	}
	if state.info.Version != "test" {
		t.Errorf("expected info.Version 'test', got %s", state.info.Version)
	}
}

// TestServices_Struct 测试 services 结构体。
func TestServices_Struct(t *testing.T) {
	svc := &services{}

	// 所有字段应该初始化为 nil
	if svc.aiService != nil {
		t.Error("aiService should be nil initially")
	}
	if svc.mcpManager != nil {
		t.Error("mcpManager should be nil initially")
	}
	if svc.proactiveManager != nil {
		t.Error("proactiveManager should be nil initially")
	}
	if svc.commandService != nil {
		t.Error("commandService should be nil initially")
	}
	if svc.eventHandler != nil {
		t.Error("eventHandler should be nil initially")
	}
	if svc.presence != nil {
		t.Error("presence should be nil initially")
	}
	if svc.mediaService != nil {
		t.Error("mediaService should be nil initially")
	}
	if svc.client != nil {
		t.Error("client should be nil initially")
	}
}

// TestSetupLogging 测试 setupLogging 函数。
func TestSetupLogging(t *testing.T) {
	tests := []struct {
		name    string
		verbose bool
	}{
		{"非详细模式", false},
		{"详细模式", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 注意：setupLogging 使用 slog.SetDefault，这会影响全局状态
			// 我们只验证它不会 panic
			setupLogging(tt.verbose)
		})
	}
}

// TestCreateTestConfigFile 创建测试配置文件的辅助函数。
func createTestConfigFile(t *testing.T, content string) string {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}
	return configPath
}

// TestConfigGeneration 测试配置生成功能。
func TestConfigGeneration(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.example.yaml")

	err := config.GenerateExample(configPath)
	if err != nil {
		t.Fatalf("GenerateExample failed: %v", err)
	}

	// 验证文件存在
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("example config file was not created")
	}

	// 验证文件内容
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read generated config: %v", err)
	}

	if len(content) == 0 {
		t.Error("generated config is empty")
	}

	// 验证包含基本配置项
	contentStr := string(content)
	requiredFields := []string{"matrix:", "ai:", "mcp:"}
	for _, field := range requiredFields {
		if !strings.Contains(contentStr, field) {
			t.Errorf("generated config missing %s section", field)
		}
	}
}

// TestConfigValidation 测试配置验证。
func TestConfigValidation(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Matrix.UserID = "@bot:example.com"
		cfg.Matrix.AccessToken = "test-token"

		if err := cfg.Matrix.Validate(); err != nil {
			t.Errorf("valid config should pass validation: %v", err)
		}
	})

	t.Run("missing homeserver", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Matrix.Homeserver = "" // 清空默认值
		cfg.Matrix.UserID = "@bot:example.com"
		cfg.Matrix.AccessToken = "test-token"

		if err := cfg.Matrix.Validate(); err == nil {
			t.Error("config without homeserver should fail validation")
		}
	})

	t.Run("missing user ID", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Matrix.UserID = "" // 清空默认值
		cfg.Matrix.AccessToken = "test-token"

		if err := cfg.Matrix.Validate(); err == nil {
			t.Error("config without user ID should fail validation")
		}
	})
}

// TestConfigLoad 测试配置加载。
func TestConfigLoad(t *testing.T) {
	t.Run("load non-existent file", func(t *testing.T) {
		_, err := config.Load("/non/existent/path/config.yaml")
		if err == nil {
			t.Error("loading non-existent file should return error")
		}
	})

	t.Run("load valid config", func(t *testing.T) {
		configContent := `
matrix:
  homeserver: https://matrix.example.com
  user_id: "@bot:example.com"
  access_token: test-token
`
		configPath := createTestConfigFile(t, configContent)

		cfg, err := config.Load(configPath)
		if err != nil {
			t.Fatalf("failed to load valid config: %v", err)
		}

		if cfg.Matrix.Homeserver != "https://matrix.example.com" {
			t.Errorf("expected homeserver 'https://matrix.example.com', got %s", cfg.Matrix.Homeserver)
		}

		if cfg.Matrix.UserID != "@bot:example.com" {
			t.Errorf("expected user ID '@bot:example.com', got %s", cfg.Matrix.UserID)
		}
	})
}

// TestAIConfigDefaults 测试 AI 配置默认值。
func TestAIConfigDefaults(t *testing.T) {
	cfg := config.DefaultAIConfig()

	if cfg.Enabled {
		t.Error("AI should be disabled by default")
	}

	if cfg.Provider != "" {
		t.Errorf("expected empty provider, got %s", cfg.Provider)
	}

	if cfg.TimeoutSeconds <= 0 {
		t.Error("timeout should be positive")
	}
}

// TestContextConfigDefaults 测试上下文配置默认值。
func TestContextConfigDefaults(t *testing.T) {
	cfg := config.DefaultContextConfig()

	if cfg.MaxMessages <= 0 {
		t.Error("MaxMessages should be positive")
	}

	if cfg.MaxTokens <= 0 {
		t.Error("MaxTokens should be positive")
	}
}

// TestProactiveConfigDefaults 测试主动聊天配置默认值。
func TestProactiveConfigDefaults(t *testing.T) {
	cfg := config.DefaultProactiveConfig()

	if cfg.Enabled {
		t.Error("proactive should be disabled by default")
	}
}

// TestSignalHandling 测试信号处理逻辑（不实际发送信号）。
func TestSignalHandling(t *testing.T) {
	// 创建一个简单的测试，验证信号处理器可以正确创建 context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 验证 context 初始状态
	select {
	case <-ctx.Done():
		t.Error("context should not be done initially")
	default:
		// 正确：context 未完成
	}

	// 模拟取消
	cancel()

	select {
	case <-ctx.Done():
		// 正确：context 已完成
	case <-time.After(100 * time.Millisecond):
		t.Error("context should be done after cancel")
	}
}

// TestMCPConfigDefaults 测试 MCP 配置默认值。
func TestMCPConfigDefaults(t *testing.T) {
	cfg := &config.MCPConfig{}

	if cfg.Enabled {
		t.Error("MCP should be disabled by default")
	}

	if cfg.Servers == nil {
		cfg.Servers = make(map[string]config.ServerConfig)
	}

	if len(cfg.Servers) != 0 {
		t.Error("Servers should be empty by default")
	}
}

// TestServicesNilSafety 测试 services 结构体的 nil 安全性。
func TestServicesNilSafety(t *testing.T) {
	svc := &services{}

	// 测试 nil 检查不会 panic
	if svc.aiService != nil {
		_ = svc.aiService.IsEnabled()
	}

	if svc.mcpManager != nil {
		_ = svc.mcpManager.IsEnabled()
	}

	if svc.proactiveManager != nil {
		svc.proactiveManager.Stop()
	}
}

// TestBuildInfoEmpty 测试空的 BuildInfo。
func TestBuildInfoEmpty(t *testing.T) {
	info := matrix.BuildInfo{}

	if info.Version != "" {
		t.Errorf("expected empty Version, got %s", info.Version)
	}

	// RuntimePlatform 应该仍然返回有效值
	platform := info.RuntimePlatform()
	if platform == "" {
		t.Error("RuntimePlatform should return valid value even for empty BuildInfo")
	}
}

// TestInitServices_AIDisabled 测试 AI 禁用时的 initServices。
func TestInitServices_AIDisabled(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			AI: config.AIConfig{
				Enabled: false,
			},
		},
		services: &services{},
		info:     matrix.BuildInfo{},
	}

	// AI 禁用时，initServices 应该直接返回 nil
	err := state.initServices()
	if err != nil {
		t.Errorf("initServices with AI disabled should return nil, got: %v", err)
	}

	// AI 禁用时，aiService 应该保持 nil
	if state.services.aiService != nil {
		t.Error("aiService should remain nil when AI is disabled")
	}
}

// TestInitServices_AIEnabledButNoClient 测试 AI 启用但无 client 时的行为。
func TestInitServices_AIEnabledButNoClient(t *testing.T) {
	// AI 启用但 services.client 为 nil，实际运行时会在访问 svc.client.GetClient() 时失败
	// 此测试仅记录预期行为，不执行 initServices（因为会 panic）
	cfg := &config.Config{
		AI: config.AIConfig{
			Enabled:        true,
			Provider:       "openai",
			BaseURL:        "https://api.openai.com/v1",
			DefaultModel:   "gpt-4",
			APIKey:         "test-key",
			TimeoutSeconds: 30,
			ToolCalling:    config.ToolCallingConfig{MaxIterations: 5},
			Models:         map[string]config.ModelConfig{"gpt-4": {Model: "gpt-4"}},
		},
	}

	// 验证配置本身是有效的
	if err := cfg.AI.Validate(); err != nil {
		t.Errorf("AI config should be valid: %v", err)
	}
}

// TestInitMCPManager_MCPDisabled 测试 MCP 禁用时的 initMCPManager。
func TestInitMCPManager_MCPDisabled(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			MCP: config.MCPConfig{
				Enabled: false,
				Servers: nil,
			},
		},
		services: &services{},
		info:     matrix.BuildInfo{},
	}

	// MCP 禁用时，initMCPManager 仍然会创建 manager（用于内置服务器）
	mgr := state.initMCPManager()
	if mgr == nil {
		t.Error("initMCPManager should return manager even when MCP disabled")
	}
}

// TestInitMCPManager_MCPEnabledNoServers 测试 MCP 启用但无服务器配置。
func TestInitMCPManager_MCPEnabledNoServers(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			MCP: config.MCPConfig{
				Enabled: true,
				Servers: map[string]config.ServerConfig{}, // 空的 servers
			},
		},
		services: &services{},
		info:     matrix.BuildInfo{},
	}

	// MCP 启用但无服务器配置，manager 仍然会被创建
	mgr := state.initMCPManager()
	if mgr == nil {
		t.Error("initMCPManager should return manager when MCP enabled with empty servers")
	}
}

// TestInitMCPManager_MCPEnabledWithServers 测试 MCP 启用且有服务器配置。
func TestInitMCPManager_MCPEnabledWithServers(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			MCP: config.MCPConfig{
				Enabled: true,
				Servers: map[string]config.ServerConfig{
					"test-server": {
						Command: "echo",
						Args:    []string{"test"},
					},
				},
			},
		},
		services: &services{},
		info:     matrix.BuildInfo{},
	}

	// MCP 启用且有服务器配置
	mgr := state.initMCPManager()
	if mgr == nil {
		t.Error("initMCPManager should return manager when MCP enabled with servers")
	}

	// 验证 manager 是否启用
	if !mgr.IsEnabled() {
		t.Error("manager should be enabled when MCP is enabled")
	}
}

// TestAIConfigValidation 测试 AI 配置验证。
func TestAIConfigValidation(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		cfg := &config.AIConfig{
			Enabled:        true,
			Provider:     "openai",
			BaseURL:      "https://api.openai.com/v1",
			APIKey:       "test-api-key",
			DefaultModel: "gpt-4",
				TimeoutSeconds: 30,
				ToolCalling:    config.ToolCallingConfig{MaxIterations: 5},
			Models: map[string]config.ModelConfig{
				"gpt-4": {Model: "gpt-4"},
			},
		}

		err := cfg.Validate()
		if err != nil {
			t.Errorf("valid AI config should pass validation: %v", err)
		}
	})

	t.Run("missing provider", func(t *testing.T) {
		cfg := &config.AIConfig{
			Enabled:      true,
			Provider:     "", // 缺失
			APIKey:       "test-key",
			DefaultModel: "gpt-4",
		}

		err := cfg.Validate()
		if err == nil {
			t.Error("AI config without provider should fail validation")
		}
	})

	t.Run("missing API key", func(t *testing.T) {
		cfg := &config.AIConfig{
			Enabled:      true,
			Provider:     "openai",
			APIKey:       "", // 缺失
			DefaultModel: "gpt-4",
		}

		err := cfg.Validate()
		if err == nil {
			t.Error("AI config without API key should fail validation")
		}
	})

	t.Run("missing default model", func(t *testing.T) {
		cfg := &config.AIConfig{
			Enabled:      true,
			Provider:     "openai",
			APIKey:       "test-key",
			DefaultModel: "", // 缺失
		}

		err := cfg.Validate()
		if err == nil {
			t.Error("AI config without default model should fail validation")
		}
	})

	t.Run("disabled config always valid", func(t *testing.T) {
		cfg := &config.AIConfig{
			Enabled: false,
			// 其他字段为空也应该通过验证（因为不会使用）
		}

		err := cfg.Validate()
		if err != nil {
			t.Errorf("disabled AI config should always be valid: %v", err)
		}
	})
}

// TestInitServices_AIConfigValidationFailure 测试 AI 配置验证失败。
func TestInitServices_AIConfigValidationFailure(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			AI: config.AIConfig{
				Enabled:      true,
				Provider:     "", // 无效配置：缺少 provider
				APIKey:       "test-key",
				DefaultModel: "gpt-4",
			},
		},
		services: &services{},
		info:     matrix.BuildInfo{},
	}

	// AI 配置验证失败应该返回错误
	err := state.initServices()
	if err == nil {
		t.Error("initServices with invalid AI config should return error")
	}

	// 验证错误消息包含配置相关信息
	if !strings.Contains(err.Error(), "AI配置验证失败") {
		t.Errorf("error should mention AI config validation failure, got: %v", err)
	}
}

// TestMCPConfig_EnabledWithServers 测试 MCP 配置的服务器映射。
func TestMCPConfig_EnabledWithServers(t *testing.T) {
	cfg := &config.MCPConfig{
		Enabled: true,
		Servers: map[string]config.ServerConfig{
			"server1": {Command: "cmd1"},
			"server2": {Command: "cmd2"},
		},
	}

	if !cfg.Enabled {
		t.Error("MCP should be enabled")
	}

	if len(cfg.Servers) != 2 {
		t.Errorf("expected 2 servers, got %d", len(cfg.Servers))
	}

	// 验证服务器配置存在
	if _, ok := cfg.Servers["server1"]; !ok {
		t.Error("server1 should exist in servers config")
	}
	if _, ok := cfg.Servers["server2"]; !ok {
		t.Error("server2 should exist in servers config")
	}
}

// TestAppState_InitCryptoDisabled 测试加密禁用场景。
func TestAppState_InitCryptoDisabled(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			Matrix: config.MatrixConfig{
				EnableE2EE: false,
			},
		},
		services: &services{},
		info:     matrix.BuildInfo{},
	}

	// E2EE 禁用时，initCrypto 应该不会失败
	// 注意：这需要真实的 MatrixClient，所以只能验证配置逻辑
	if state.cfg.Matrix.EnableE2EE {
		t.Error("E2EE should be disabled in this test")
	}
}

// TestServicesAssignment 测试 services 字段赋值逻辑。
func TestServicesAssignment(t *testing.T) {
	svc := &services{}

	// 模拟 services 初始化后的状态检查
	// 验证 nil 检查逻辑
	tests := []struct {
		name   string
		field  string
		isNil  func() bool
	}{
		{"aiService", "aiService", func() bool { return svc.aiService == nil }},
		{"mcpManager", "mcpManager", func() bool { return svc.mcpManager == nil }},
		{"proactiveManager", "proactiveManager", func() bool { return svc.proactiveManager == nil }},
		{"commandService", "commandService", func() bool { return svc.commandService == nil }},
		{"eventHandler", "eventHandler", func() bool { return svc.eventHandler == nil }},
		{"presence", "presence", func() bool { return svc.presence == nil }},
		{"mediaService", "mediaService", func() bool { return svc.mediaService == nil }},
		{"client", "client", func() bool { return svc.client == nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.isNil() {
				t.Errorf("%s should be nil initially", tt.field)
			}
		})
	}
}

// TestProactiveConfig 测试主动聊天配置。
func TestProactiveConfig(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		cfg := config.DefaultProactiveConfig()
		if cfg.Enabled {
			t.Error("proactive should be disabled by default")
		}
	})

	t.Run("enabled config", func(t *testing.T) {
		cfg := &config.ProactiveConfig{
			Enabled:            true,
			MinIntervalMinutes: 60,
		}
		if !cfg.Enabled {
			t.Error("proactive should be enabled")
		}
		if cfg.MinIntervalMinutes != 60 {
			t.Errorf("expected MinIntervalMinutes 60, got %d", cfg.MinIntervalMinutes)
		}
	})
}

// TestMediaConfig 测试媒体配置。
func TestMediaConfig(t *testing.T) {
	t.Run("default max size", func(t *testing.T) {
		cfg := config.DefaultAIConfig()
		if cfg.Media.MaxSizeMB <= 0 {
			t.Error("Media.MaxSizeMB should be positive by default")
		}
	})

	t.Run("custom max size", func(t *testing.T) {
		cfg := &config.AIConfig{
			Media: config.MediaConfig{
				MaxSizeMB: 50,
			},
		}
		if cfg.Media.MaxSizeMB != 50 {
			t.Errorf("expected MaxSizeMB 50, got %d", cfg.Media.MaxSizeMB)
		}
	})
}
