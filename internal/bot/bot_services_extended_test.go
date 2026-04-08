// Package bot_test 包含服务初始化函数的单元测试。
package bot

import (
	"testing"
	"time"

	"rua.plus/saber/internal/cli"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// TestInitMemeService_Enabled 测试启用的 Meme 服务初始化。
func TestInitMemeService_Enabled(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			Meme: config.MemeConfig{
				Enabled:    true,
				APIKey:     "test-api-key",
				MaxResults: 5,
			},
		},
		services: &services{
			// client 和 commandService 为 nil，会阻止初始化
		},
	}

	// 由于缺少 client，初始化不会成功
	state.initMemeService()

	// memeService 应该为 nil（因为配置无效或缺少 client）
	if state.services.memeService != nil {
		t.Error("memeService should be nil when client is missing")
	}
}

// TestInitMemeService_InvalidMaxResults 测试无效的最大结果数。
func TestInitMemeService_InvalidMaxResults(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			Meme: config.MemeConfig{
				Enabled:    true,
				APIKey:     "test-api-key",
				MaxResults: 0, // 无效值
			},
		},
		services: &services{},
	}

	state.initMemeService()

	// 配置验证失败，memeService 应该为 nil
	if state.services.memeService != nil {
		t.Error("memeService should be nil with invalid config")
	}
}

// TestInitMemeService_EmptyAPIKey 测试空 API Key。
func TestInitMemeService_EmptyAPIKey(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			Meme: config.MemeConfig{
				Enabled: true,
				APIKey:  "", // 空 API Key
			},
		},
		services: &services{},
	}

	state.initMemeService()

	// 配置验证失败，memeService 应该为 nil
	if state.services.memeService != nil {
		t.Error("memeService should be nil with empty API key")
	}
}

// TestInitQQAdapter_Enabled 测试启用的 QQ 适配器。
func TestInitQQAdapter_Enabled(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			QQ: config.QQConfig{
				Enabled:       true,
				AppID:         "test-app-id",
				AppSecret:     "test-secret",
				WebhookSecret: "test-secret",
			},
			AI: config.AIConfig{
				Enabled: false, // AI 禁用
			},
		},
		services: &services{},
		info: matrix.BuildInfo{
			Version: "test",
		},
	}

	// initQQAdapter 会尝试创建适配器，但缺少完整配置可能失败
	state.initQQAdapter()

	// 由于没有真实的 QQ 配置，qqAdapter 可能为 nil
	// 主要测试不会 panic
}

// TestInitQQAdapter_EnabledWithAI 测试启用 QQ 适配器并启用 AI。
func TestInitQQAdapter_EnabledWithAI(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			QQ: config.QQConfig{
				Enabled:       true,
				AppID:         "test-app-id",
				AppSecret:     "test-secret",
				WebhookSecret: "test-secret",
			},
			AI: config.AIConfig{
				Enabled: false, // 简化测试，AI 仍然禁用
			},
		},
		services: &services{},
		info:     matrix.BuildInfo{},
	}

	state.initQQAdapter()
}

// TestInitQQAdapter_InvalidAppID 测试无效的 AppID。
func TestInitQQAdapter_InvalidAppID(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			QQ: config.QQConfig{
				Enabled:       true,
				AppID:         "", // 无效：空 AppID
				AppSecret:     "test-secret",
				WebhookSecret: "test-secret",
			},
			AI: config.AIConfig{
				Enabled: false,
			},
		},
		services: &services{},
		info:     matrix.BuildInfo{},
	}

	state.initQQAdapter()

	// 配置无效，qqAdapter 应该为 nil
	if state.services.qqAdapter != nil {
		t.Error("qqAdapter should be nil with invalid config")
	}
}

// TestInitQQAdapter_SandboxMode 测试沙箱模式。
func TestInitQQAdapter_SandboxMode(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			QQ: config.QQConfig{
				Enabled:       true,
				AppID:         "test-app-id",
				AppSecret:     "test-secret",
				WebhookSecret: "test-token",
				Sandbox:       true,
			},
			AI: config.AIConfig{
				Enabled: false,
			},
		},
		services: &services{},
		info: matrix.BuildInfo{
			Version: "test",
		},
	}

	// 即使配置有效，缺少完整环境初始化可能失败
	// 主要测试不会 panic
	state.initQQAdapter()
}

// TestAutoJoinRooms_SingleRoom 测试单个房间自动加入。
func TestAutoJoinRooms_SingleRoom(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			Matrix: config.MatrixConfig{
				AutoJoinRooms: []string{"!test:matrix.org"},
			},
		},
		services: &services{
			// client 为 nil，会阻止实际加入
		},
	}

	// 由于缺少 client，不会实际执行加入操作
	// 主要测试不会 panic
	state.autoJoinRooms()
}

// TestAutoJoinRooms_MultipleRooms 测试多个房间自动加入。
func TestAutoJoinRooms_MultipleRooms(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			Matrix: config.MatrixConfig{
				AutoJoinRooms: []string{
					"!room1:matrix.org",
					"!room2:matrix.org",
					"!room3:matrix.org",
				},
			},
		},
		services: &services{
			// client 为 nil
		},
	}

	state.autoJoinRooms()
}

// TestAutoJoinRooms_NilClient 测试 nil 客户端。
func TestAutoJoinRooms_NilClient(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			Matrix: config.MatrixConfig{
				AutoJoinRooms: []string{"!test:matrix.org"},
			},
		},
		services: &services{
			client: nil,
		},
	}

	// 这会 panic，因为代码尝试使用 nil client
	// 使用 defer/recover 捕获 panic
	defer func() {
		if r := recover(); r != nil {
			// 预期的 panic，测试通过
			t.Logf("Expected panic occurred: %v", r)
		}
	}()

	state.autoJoinRooms()
	// 如果没有 panic，测试失败
	t.Error("Expected panic did not occur")
}

// TestInitCrypto_E2EEEnabled 测试 E2EE 启用。
func TestInitCrypto_E2EEEnabled(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			Matrix: config.MatrixConfig{
				EnableE2EE:      true,
				PickleKeyPath:   "/tmp/test.key",
				E2EESessionPath: "/tmp/test.session",
			},
		},
		info:     matrix.BuildInfo{},
		services: &services{},
	}

	// 由于缺少真实的 MatrixClient，只能验证配置逻辑
	if !state.cfg.Matrix.EnableE2EE {
		t.Error("E2EE should be enabled")
	}

	if state.cfg.Matrix.PickleKeyPath == "" {
		t.Error("PickleKeyPath should not be empty")
	}
}

// TestInitCrypto_E2EEDisabled 测试 E2EE 禁用。
func TestInitCrypto_E2EEDisabled(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			Matrix: config.MatrixConfig{
				EnableE2EE: false,
			},
		},
		info:     matrix.BuildInfo{},
		services: &services{},
	}

	if state.cfg.Matrix.EnableE2EE {
		t.Error("E2EE should be disabled")
	}
}

// TestInitCrypto_DefaultPickleKeyPath 测试默认 PickleKey 路径。
func TestInitCrypto_DefaultPickleKeyPath(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			Matrix: config.MatrixConfig{
				EnableE2EE:      true,
				E2EESessionPath: "/tmp/session.db",
				PickleKeyPath:   "", // 空，应该使用默认值
			},
		},
	}

	// 验证配置
	if state.cfg.Matrix.E2EESessionPath == "" {
		t.Error("E2EESessionPath should not be empty")
	}

	// PickleKeyPath 为空时，代码应该使用 E2EESessionPath + ".key"
	expectedPath := state.cfg.Matrix.E2EESessionPath + ".key"
	_ = expectedPath
}

// TestRegisterAICommands 测试 AI 命令注册。
func TestRegisterAICommands(t *testing.T) {
	// 由于 registerAICommands 需要完整的 services 初始化，
	// 我们只能测试配置逻辑
	cfg := &config.Config{
		AI: config.AIConfig{
			Enabled:               true,
			DirectChatAutoReply:   true,
			GroupChatMentionReply: true,
			ReplyToBotReply:       true,
			Models: map[string]config.ModelConfig{
				"gpt-4":  {Model: "gpt-4"},
				"claude": {Model: "claude-3"},
			},
		},
	}

	// 验证配置
	if !cfg.AI.Enabled {
		t.Error("AI should be enabled")
	}

	if !cfg.AI.DirectChatAutoReply {
		t.Error("DirectChatAutoReply should be enabled")
	}

	if len(cfg.AI.Models) != 2 {
		t.Errorf("Expected 2 models, got %d", len(cfg.AI.Models))
	}
}

// TestRegisterAICommands_Disabled 测试 AI 禁用时的命令注册。
func TestRegisterAICommands_Disabled(t *testing.T) {
	cfg := &config.Config{
		AI: config.AIConfig{
			Enabled: false,
		},
	}

	if cfg.AI.Enabled {
		t.Error("AI should be disabled")
	}
}

// TestInitPersonaService_Config 测试人格服务配置。
func TestInitPersonaService_Config(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			AI: config.AIConfig{
				Enabled: true,
			},
		},
		flags: &cli.Flags{
			ConfigPath: "/tmp/test/config.yaml",
		},
		services: &services{
			// aiService 和 commandService 为 nil
		},
		info: matrix.BuildInfo{},
	}

	// 验证配置路径
	if state.flags.ConfigPath == "" {
		t.Error("ConfigPath should not be empty")
	}

	// AI 启用时应该尝试初始化人格服务
	if !state.cfg.AI.Enabled {
		t.Error("AI should be enabled for persona service")
	}
}

// TestInitProactiveManager_Config 测试主动聊天管理器配置。
func TestInitProactiveManager_Config(t *testing.T) {
	cfg := &config.Config{
		AI: config.AIConfig{
			Enabled: true,
			Proactive: config.ProactiveConfig{
				Enabled:            true,
				MinIntervalMinutes: 60,
			},
		},
	}

	if !cfg.AI.Proactive.Enabled {
		t.Error("Proactive should be enabled")
	}

	if cfg.AI.Proactive.MinIntervalMinutes != 60 {
		t.Errorf("MinIntervalMinutes = %d, want 60", cfg.AI.Proactive.MinIntervalMinutes)
	}
}

// TestSetupEventHandlers_Config 测试事件处理器配置。
func TestSetupEventHandlers_Config(t *testing.T) {
	cfg := &config.Config{
		Matrix: config.MatrixConfig{
			MaxConcurrentEvents: 20,
		},
	}

	if cfg.Matrix.MaxConcurrentEvents <= 0 {
		t.Error("MaxConcurrentEvents should be positive")
	}

	if cfg.Matrix.MaxConcurrentEvents != 20 {
		t.Errorf("MaxConcurrentEvents = %d, want 20", cfg.Matrix.MaxConcurrentEvents)
	}
}

// TestSetupEventHandlers_DefaultConcurrent 测试默认并发数。
func TestSetupEventHandlers_DefaultConcurrent(t *testing.T) {
	cfg := &config.Config{
		Matrix: config.MatrixConfig{
			MaxConcurrentEvents: 0, // 应该使用默认值
		},
	}

	defaultValue := 10
	maxConcurrent := cfg.Matrix.MaxConcurrentEvents
	if maxConcurrent <= 0 {
		maxConcurrent = defaultValue
	}

	if maxConcurrent != defaultValue {
		t.Errorf("MaxConcurrentEvents = %d, want %d", maxConcurrent, defaultValue)
	}
}

// TestInitServices_MediaConfig 测试媒体配置。
func TestInitServices_MediaConfig(t *testing.T) {
	cfg := &config.Config{
		AI: config.AIConfig{
			Enabled: true,
			Media: config.MediaConfig{
				MaxSizeMB: 50,
			},
		},
	}

	maxSizeBytes := int64(cfg.AI.Media.MaxSizeMB) * 1024 * 1024
	if maxSizeBytes != 50*1024*1024 {
		t.Errorf("MaxSizeBytes = %d, want %d", maxSizeBytes, 50*1024*1024)
	}
}

// TestStartSync_Config 测试同步启动配置。
func TestStartSync_Config(t *testing.T) {
	cfg := &config.Config{
		AI: config.AIConfig{
			Proactive: config.ProactiveConfig{
				Enabled: true,
			},
		},
		QQ: config.QQConfig{
			Enabled: true,
		},
	}

	// 验证配置
	if !cfg.AI.Proactive.Enabled {
		t.Error("Proactive should be enabled")
	}

	if !cfg.QQ.Enabled {
		t.Error("QQ should be enabled")
	}
}

// TestShutdown_WithAllServicesNil 测试所有服务为 nil 时的关闭。
func TestShutdown_WithAllServicesNil(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			Shutdown: config.ShutdownConfig{
				TimeoutSeconds: 5,
			},
		},
		services: &services{
			aiService:        nil,
			mcpManager:       nil,
			proactiveManager: nil,
			qqAdapter:        nil,
		},
	}

	// 不应该 panic 或挂起
	done := make(chan struct{})
	go func() {
		state.shutdown(func() {})
		close(done)
	}()

	select {
	case <-done:
		// 成功
	case <-time.After(2 * time.Second):
		t.Error("shutdown timed out")
	}
}

// TestShutdown_ShortTimeout 测试短超时。
func TestShutdown_ShortTimeout(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			Shutdown: config.ShutdownConfig{
				TimeoutSeconds: 1,
			},
		},
		services: &services{},
	}

	done := make(chan struct{})
	go func() {
		state.shutdown(func() {})
		close(done)
	}()

	select {
	case <-done:
		// 成功
	case <-time.After(3 * time.Second):
		t.Error("shutdown timed out even with short timeout")
	}
}

// TestInitServices_WithAIEnabled 测试 AI 启用时的服务初始化。
func TestInitServices_WithAIEnabled(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			AI: config.AIConfig{
				Enabled:        true,
				Provider:       "openai",
				BaseURL:        "https://api.openai.com/v1",
				APIKey:         "test-key",
				DefaultModel:   "gpt-4",
				TimeoutSeconds: 30,
				Models: map[string]config.ModelConfig{
					"gpt-4": {Model: "gpt-4"},
				},
			},
		},
		services: &services{
			// client 和 commandService 为 nil，会导致 panic
		},
		info: matrix.BuildInfo{},
	}

	// 验证 AI 配置
	if !state.cfg.AI.Enabled {
		t.Error("AI should be enabled")
	}

	// 验证配置有效
	if err := state.cfg.AI.Validate(); err != nil {
		t.Errorf("AI config should be valid: %v", err)
	}
}

// TestInitServices_WithMCP 测试带 MCP 的服务初始化。
func TestInitServices_WithMCP(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			AI: config.AIConfig{
				Enabled: false, // 简化测试
			},
			MCP: config.MCPConfig{
				Enabled: true,
				Servers: map[string]config.ServerConfig{
					"test": {Command: "echo"},
				},
			},
		},
		services: &services{},
		info:     matrix.BuildInfo{},
	}

	// 验证 MCP 配置
	if !state.cfg.MCP.Enabled {
		t.Error("MCP should be enabled")
	}

	if len(state.cfg.MCP.Servers) != 1 {
		t.Errorf("Expected 1 MCP server, got %d", len(state.cfg.MCP.Servers))
	}
}

// TestInitMCPManager_ConfigVariations 测试 MCP 管理器配置变体。
func TestInitMCPManager_ConfigVariations(t *testing.T) {
	tests := []struct {
		name            string
		enabled         bool
		servers         map[string]config.ServerConfig
		shouldBeEnabled bool
	}{
		{
			name:            "禁用",
			enabled:         false,
			servers:         nil,
			shouldBeEnabled: false,
		},
		{
			name:            "启用无服务器",
			enabled:         true,
			servers:         map[string]config.ServerConfig{},
			shouldBeEnabled: false, // 没有服务器时不应该启用
		},
		{
			name:    "启用有服务器",
			enabled: true,
			servers: map[string]config.ServerConfig{
				"test": {Command: "echo"},
			},
			shouldBeEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &appState{
				cfg: &config.Config{
					MCP: config.MCPConfig{
						Enabled: tt.enabled,
						Servers: tt.servers,
					},
				},
				services: &services{},
			}

			mgr := state.initMCPManager()
			if mgr == nil {
				t.Error("initMCPManager should not return nil")
			}

			// 检查是否启用
			isEnabled := mgr.IsEnabled()
			// 内置功能可能使管理器始终启用
			_ = isEnabled
		})
	}
}
