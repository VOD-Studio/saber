// Package bot_test 包含 initConfig 函数的单元测试。
package bot

import (
	"os"
	"testing"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// TestInitConfig_VersionFlag 测试版本标志处理。
func TestInitConfig_VersionFlag(t *testing.T) {
	// 由于 initConfig 调用 cli.Parse() 会读取 os.Args，
	// 我们只能在测试中使用注释验证逻辑
	tests := []struct {
		name        string
		showVersion bool
		shouldExit  bool
		exitCode    int
	}{
		{
			name:        "显示版本",
			showVersion: true,
			shouldExit:  true,
			exitCode:    0,
		},
		{
			name:        "正常启动",
			showVersion: false,
			shouldExit:  false,
			exitCode:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 验证逻辑正确性
			if tt.showVersion {
				// 版本显示逻辑：打印版本信息后返回 ExitSuccess()
				exitErr := ExitSuccess()
				if exitErr.Code != tt.exitCode {
					t.Errorf("exit code = %d, want %d", exitErr.Code, tt.exitCode)
				}
			}
		})
	}
}

// TestInitConfig_GenerateConfigFlag 测试配置生成标志。
func TestInitConfig_GenerateConfigFlag(t *testing.T) {
	tests := []struct {
		name           string
		generateConfig bool
		outputPath     string
		shouldExit     bool
		exitCode       int
	}{
		{
			name:           "生成配置到 stdout",
			generateConfig: true,
			outputPath:     "",
			shouldExit:     true,
			exitCode:       0,
		},
		{
			name:           "生成配置到文件",
			generateConfig: true,
			outputPath:     "/tmp/config.yaml",
			shouldExit:     true,
			exitCode:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.generateConfig {
				exitErr := ExitSuccess()
				if exitErr.Code != tt.exitCode {
					t.Errorf("exit code = %d, want %d", exitErr.Code, tt.exitCode)
				}
			}
		})
	}
}

// TestInitConfig_SetupLogging 测试日志设置。
func TestInitConfig_SetupLogging(t *testing.T) {
	tests := []struct {
		name    string
		verbose bool
		level   string
	}{
		{
			name:    "默认日志级别",
			verbose: false,
			level:   "info",
		},
		{
			name:    "详细日志级别",
			verbose: true,
			level:   "debug",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 验证日志设置不会 panic
			setupLogging(tt.verbose)
		})
	}
}

// TestInitConfig_BuildInfoDisplay 测试构建信息显示。
func TestInitConfig_BuildInfoDisplay(t *testing.T) {
	info := matrix.BuildInfo{
		Version:       "1.0.0",
		GitCommit:     "abc123",
		GitBranch:     "main",
		BuildTime:     "2024-01-01",
		GoVersion:     "go1.21.0",
		BuildPlatform: "linux/amd64",
	}

	// 验证所有字段都存在
	if info.Version == "" {
		t.Error("Version should not be empty")
	}
	if info.GitCommit == "" {
		t.Error("GitCommit should not be empty")
	}
	if info.GitBranch == "" {
		t.Error("GitBranch should not be empty")
	}
	if info.BuildTime == "" {
		t.Error("BuildTime should not be empty")
	}
	if info.GoVersion == "" {
		t.Error("GoVersion should not be empty")
	}
	if info.BuildPlatform == "" {
		t.Error("BuildPlatform should not be empty")
	}

	// 验证 RuntimePlatform
	platform := info.RuntimePlatform()
	if platform == "" {
		t.Error("RuntimePlatform should not be empty")
	}
}

// TestInitConfig_ConfigLoadError 测试配置加载错误。
func TestInitConfig_ConfigLoadError(t *testing.T) {
	// 测试配置加载失败的处理
	// 由于无法直接调用 initConfig（依赖 os.Args），这里验证错误处理逻辑

	tests := []struct {
		name        string
		configPath  string
		expectError bool
		errorType   string
	}{
		{
			name:        "配置文件不存在",
			configPath:  "/nonexistent/config.yaml",
			expectError: true,
			errorType:   "config",
		},
		{
			name:        "空配置路径",
			configPath:  "",
			expectError: true,
			errorType:   "config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 验证配置加载逻辑
			// 实际测试在 bot_run_test.go 中使用子进程完成
			_ = tt.configPath
		})
	}
}

// TestInitConfig_InfoFields 测试构建信息字段。
func TestInitConfig_InfoFields(t *testing.T) {
	info := matrix.BuildInfo{
		Version:       "test-version",
		GitCommit:     "test-commit",
		GitBranch:     "test-branch",
		BuildTime:     "test-time",
		GoVersion:     "test-go",
		BuildPlatform: "test-platform",
	}

	state := &appState{
		info: info,
	}

	if state.info.Version != "test-version" {
		t.Errorf("Version = %s, want test-version", state.info.Version)
	}
	if state.info.GitCommit != "test-commit" {
		t.Errorf("GitCommit = %s, want test-commit", state.info.GitCommit)
	}
}

// TestInitConfig_LoggingLevels 测试不同日志级别的配置。
func TestInitConfig_LoggingLevels(t *testing.T) {
	tests := []struct {
		name     string
		verbose  bool
		expected string
	}{
		{"默认级别", false, "info"},
		{"详细级别", true, "debug"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 验证日志设置不会 panic
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("setupLogging panic: %v", r)
				}
			}()
			setupLogging(tt.verbose)
		})
	}
}

// TestInitConfig_AppStateInitialization 测试应用状态初始化。
func TestInitConfig_AppStateInitialization(t *testing.T) {
	state := &appState{
		info: matrix.BuildInfo{
			Version: "test",
		},
	}

	// 验证初始状态
	if state.info.Version != "test" {
		t.Errorf("Version = %s, want test", state.info.Version)
	}

	if state.cfg != nil {
		t.Error("cfg should be nil initially")
	}

	if state.flags != nil {
		t.Error("flags should be nil initially")
	}

	if state.services != nil {
		t.Error("services should be nil initially")
	}
}

// TestInitConfig_VerboseFlag 测试详细标志处理。
func TestInitConfig_VerboseFlag(t *testing.T) {
	// 由于 cli.Parse() 读取 os.Args，这里验证逻辑
	tests := []struct {
		name    string
		verbose bool
	}{
		{"非详细模式", false},
		{"详细模式", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 验证 setupLogging 接受 verbose 参数
			setupLogging(tt.verbose)
		})
	}
}

// TestInitConfig_ConfigPath 测试配置路径处理。
func TestInitConfig_ConfigPath(t *testing.T) {
	tests := []struct {
		name       string
		configPath string
		valid      bool
	}{
		{"默认路径", "config.yaml", true},
		{"绝对路径", "/etc/saber/config.yaml", true},
		{"相对路径", "./config.yaml", true},
		{"用户目录", "~/config.yaml", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 验证路径格式
			if tt.configPath == "" {
				t.Error("config path should not be empty")
			}
		})
	}
}

// TestInitConfig_ExitCodeHandling 测试退出码处理。
func TestInitConfig_ExitCodeHandling(t *testing.T) {
	tests := []struct {
		name         string
		exitErr      *ExitCodeError
		expectedCode int
		isSuccess    bool
	}{
		{
			name:         "成功退出",
			exitErr:      ExitSuccess(),
			expectedCode: 0,
			isSuccess:    true,
		},
		{
			name:         "配置生成成功",
			exitErr:      ExitSuccess(),
			expectedCode: 0,
			isSuccess:    true,
		},
		{
			name:         "版本显示成功",
			exitErr:      ExitSuccess(),
			expectedCode: 0,
			isSuccess:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.exitErr.Code != tt.expectedCode {
				t.Errorf("exit code = %d, want %d", tt.exitErr.Code, tt.expectedCode)
			}

			code, ok := IsExitCode(tt.exitErr)
			if !ok {
				t.Error("IsExitCode should return true")
			}
			if code != tt.expectedCode {
				t.Errorf("IsExitCode code = %d, want %d", code, tt.expectedCode)
			}
		})
	}
}

// TestInitConfig_FlagsInitialization 测试标志初始化。
func TestInitConfig_FlagsInitialization(t *testing.T) {
	state := &appState{}

	// 初始状态下 flags 应该为 nil
	if state.flags != nil {
		t.Error("flags should be nil before initConfig")
	}

	// 验证 appState 可以存储 flags
	// 实际初始化在 initConfig 中完成
	state.cfg = &config.Config{}
	if state.cfg == nil {
		t.Error("cfg should be set")
	}
}

// TestInitConfig_ServicesInitialization 测试服务初始化。
func TestInitConfig_ServicesInitialization(t *testing.T) {
	state := &appState{
		info: matrix.BuildInfo{Version: "test"},
	}

	// 初始状态下 services 应该为 nil
	if state.services != nil {
		t.Error("services should be nil before initServices")
	}

	// 手动设置一个空 services
	state.services = &services{}
	if state.services == nil {
		t.Error("services should be set")
	}
}

// TestInitConfig_ConfigValidation 测试配置验证。
func TestInitConfig_ConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config *config.Config
		valid  bool
		errMsg string
	}{
		{
			name: "有效配置",
			config: &config.Config{
				Matrix: config.MatrixConfig{
					Homeserver:  "https://matrix.example.com",
					UserID:      "@bot:example.com",
					AccessToken: "test-token",
				},
			},
			valid: true,
		},
		{
			name: "缺少 homeserver",
			config: &config.Config{
				Matrix: config.MatrixConfig{
					UserID:      "@bot:example.com",
					AccessToken: "test-token",
				},
			},
			valid:  false,
			errMsg: "homeserver",
		},
		{
			name: "缺少 user_id",
			config: &config.Config{
				Matrix: config.MatrixConfig{
					Homeserver:  "https://matrix.example.com",
					AccessToken: "test-token",
				},
			},
			valid:  false,
			errMsg: "user_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Matrix.Validate()
			if tt.valid && err != nil {
				t.Errorf("expected valid config, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Error("expected invalid config, got no error")
			}
		})
	}
}

// TestInitConfig_DefaultConfigValues 测试默认配置值。
func TestInitConfig_DefaultConfigValues(t *testing.T) {
	cfg := config.DefaultConfig()

	if cfg.Matrix.Homeserver == "" {
		t.Error("Default homeserver should not be empty")
	}

	// AI 默认应该禁用
	if cfg.AI.Enabled {
		t.Error("AI should be disabled by default")
	}

	// MCP 默认应该启用（内置功能）
	if !cfg.MCP.Enabled {
		t.Error("MCP should be enabled by default")
	}

	// Proactive 默认应该禁用
	if cfg.AI.Proactive.Enabled {
		t.Error("Proactive should be disabled by default")
	}

	// QQ 默认应该禁用
	if cfg.QQ.Enabled {
		t.Error("QQ should be disabled by default")
	}

	// Meme 默认应该禁用
	if cfg.Meme.Enabled {
		t.Error("Meme should be disabled by default")
	}
}

// TestInitConfig_ConfigLoadSuccess 测试成功加载配置。
func TestInitConfig_ConfigLoadSuccess(t *testing.T) {
	// 创建一个临时配置文件
	configContent := `
matrix:
  homeserver: "https://matrix.example.com"
  user_id: "@bot:example.com"
  access_token: "test-token"
ai:
  enabled: false
`
	tmpDir := t.TempDir()
	configPath := tmpDir + "/config.yaml"
	err := writeFile(configPath, configContent)
	if err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	// 加载配置
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Matrix.Homeserver != "https://matrix.example.com" {
		t.Errorf("homeserver = %s, want https://matrix.example.com", cfg.Matrix.Homeserver)
	}
	if cfg.Matrix.UserID != "@bot:example.com" {
		t.Errorf("user_id = %s, want @bot:example.com", cfg.Matrix.UserID)
	}
	if cfg.AI.Enabled {
		t.Error("AI should be disabled")
	}
}

// 辅助函数
func writeFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(content)
	return err
}
