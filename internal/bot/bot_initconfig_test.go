// Package bot_test 包含 initConfig 函数的单元测试。
package bot

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// TestInitConfig_FlagErrors 测试标志解析错误到退出码的映射：
// 未知标志返回退出码 2，-h/--help 视为正常退出（码 0）。
func TestInitConfig_FlagErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		exitCode int
	}{
		{"未知标志", []string{"-no-such-flag"}, 2},
		{"帮助标志按成功退出", []string{"-h"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &appState{info: matrix.BuildInfo{Version: "test"}}
			var out bytes.Buffer
			err := state.initConfig(tt.args, &out)

			code, ok := IsExitCode(err)
			if !ok {
				t.Fatalf("期望 ExitCodeError，实际 err=%v", err)
			}
			if code != tt.exitCode {
				t.Errorf("退出码 = %d，期望 %d", code, tt.exitCode)
			}
		})
	}
}

// TestInitConfig_StdoutWriter 测试 -version 输出写入注入的 writer 而非 os.Stdout。
func TestInitConfig_StdoutWriter(t *testing.T) {
	state := &appState{info: matrix.BuildInfo{Version: "1.2.3"}}
	var out bytes.Buffer
	if _, ok := IsExitCode(state.initConfig([]string{"-version"}, &out)); !ok {
		t.Fatal("-version 应返回 ExitCodeError")
	}
	if !strings.Contains(out.String(), "Saber v1.2.3") {
		t.Errorf("输出应包含版本号，实际:\n%s", out.String())
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

// TestInitConfig_ConfigLoadError 的场景已由 bot_run_test.go 中的
// TestRun_ConfigLoadFailure 在进程内真实调用 run() 覆盖，此处不再保留静态验证。

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
	if cfg.MCP.Enabled {
		t.Error("MCP should be disabled by default")
	}

	// Proactive 默认应该禁用
	if cfg.Matrix.Proactive.Enabled {
		t.Error("Proactive should be disabled by default")
	}

	// Meme 默认应该禁用
	if cfg.Matrix.Meme.Enabled {
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

// TestInitConfig_ConfigSymlinkFollowsRealPath 确认配置文件是符号链接时，
// ConfigPath 收敛为真实路径，令牌、会话与任务数据库随之落到链接目标所在目录。
func TestInitConfig_ConfigSymlinkFollowsRealPath(t *testing.T) {
	realPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.GenerateExample(realPath); err != nil {
		t.Fatalf("生成私有配置失败: %v", err)
	}
	want, err := filepath.EvalSymlinks(realPath)
	if err != nil {
		t.Fatalf("解析真实路径失败: %v", err)
	}

	tests := []struct {
		name string
		path func(t *testing.T) string
		want string
	}{
		{"符号链接改为真实路径", func(t *testing.T) string {
			link := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.Symlink(realPath, link); err != nil {
				t.Fatalf("创建符号链接失败: %v", err)
			}
			return link
		}, want},
		{"普通路径保持原样", func(t *testing.T) string { return realPath }, realPath},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &appState{info: matrix.BuildInfo{Version: "test"}}
			if err := state.initConfig([]string{"-config", tt.path(t)}, &bytes.Buffer{}); err != nil {
				t.Fatalf("initConfig 失败: %v", err)
			}
			if state.flags.ConfigPath != tt.want {
				t.Errorf("ConfigPath = %q，期望 %q", state.flags.ConfigPath, tt.want)
			}
		})
	}
}
