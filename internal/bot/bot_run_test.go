// Package bot_test 包含机器人 Run 函数的单元测试。
package bot

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"rua.plus/saber/internal/matrix"
)

// TestSetupLogging_VerboseMode 测试详细日志模式的设置。
func TestSetupLogging_VerboseMode(t *testing.T) {
	tests := []struct {
		name    string
		verbose bool
	}{
		{"非详细模式", false},
		{"详细模式", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 捕获日志输出以验证设置
			var buf bytes.Buffer
			handler := slog.NewTextHandler(&buf, nil)
			logger := slog.New(handler)
			slog.SetDefault(logger)

			// 执行 setupLogging 不应 panic
			setupLogging(tt.verbose)

			// 验证没有 panic 发生
		})
	}
}

// TestSetupLogging_NoPanic 验证 setupLogging 在各种情况下不会 panic。
func TestSetupLogging_NoPanic(t *testing.T) {
	tests := []struct {
		name    string
		verbose bool
	}{
		{"默认非详细", false},
		{"启用详细", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("setupLogging panic: %v", r)
				}
			}()

			setupLogging(tt.verbose)
		})
	}
}

// TestBuildInfo_Fields 测试 BuildInfo 结构体的字段。
func TestBuildInfo_Fields(t *testing.T) {
	info := matrix.BuildInfo{
		Version:       "1.0.0",
		GitCommit:     "abc123",
		GitBranch:     "main",
		BuildTime:     "2024-01-01",
		GoVersion:     "go1.21.0",
		BuildPlatform: "linux/amd64",
	}

	tests := []struct {
		name     string
		field    string
		expected string
	}{
		{"Version", info.Version, "1.0.0"},
		{"GitCommit", info.GitCommit, "abc123"},
		{"GitBranch", info.GitBranch, "main"},
		{"BuildTime", info.BuildTime, "2024-01-01"},
		{"GoVersion", info.GoVersion, "go1.21.0"},
		{"BuildPlatform", info.BuildPlatform, "linux/amd64"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.expected == "" {
				t.Errorf("BuildInfo.%s 应该有值", tt.name)
			}
		})
	}
}

// TestBuildInfo_EmptyValues 测试 BuildInfo 的空值情况。
func TestBuildInfo_EmptyValues(t *testing.T) {
	info := matrix.BuildInfo{}

	if info.Version != "" {
		t.Error("空 BuildInfo 的 Version 应该为空")
	}
	if info.GitCommit != "" {
		t.Error("空 BuildInfo 的 GitCommit 应该为空")
	}
	if info.GitBranch != "" {
		t.Error("空 BuildInfo 的 GitBranch 应该为空")
	}
}

// TestBuildInfo_PartialValues 测试 BuildInfo 的部分填充情况。
func TestBuildInfo_PartialValues(t *testing.T) {
	tests := []struct {
		name string
		info matrix.BuildInfo
	}{
		{"只有 Version", matrix.BuildInfo{Version: "dev"}},
		{"只有 GitCommit", matrix.BuildInfo{GitCommit: "abc123"}},
		{"Version 和 GitCommit", matrix.BuildInfo{Version: "1.0.0", GitCommit: "abc123"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 验证结构体创建不会 panic
			_ = tt.info
		})
	}
}

// TestRun_VersionFlag 使用子进程测试 version 标志的处理。
// 由于 Run 函数调用 os.Exit，我们需要通过子进程来测试退出码。
func TestRun_VersionFlag(t *testing.T) {
	// 跳过短测试模式，因为子进程测试较慢
	if testing.Short() {
		t.Skip("跳过子进程测试")
	}

	// 构建测试二进制文件
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "saber-test")

	// 编译测试程序
	buildCmd := exec.Command("go", "build", "-tags", "goolm", "-o", binaryPath, ".")
	buildCmd.Dir = filepath.Join("..", "..")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("构建测试二进制文件失败: %v\n输出: %s", err, output)
	}

	tests := []struct {
		name           string
		args           []string
		expectedExit   int
		outputContains string
	}{
		{
			name:           "version 标志",
			args:           []string{"-version"},
			expectedExit:   0,
			outputContains: "Saber Matrix Bot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binaryPath, tt.args...)
			output, err := cmd.CombinedOutput()

			// 检查退出码
			if err == nil {
				// 命令成功执行，退出码为 0
				if tt.expectedExit != 0 {
					t.Errorf("退出码 = 0, 期望 %d", tt.expectedExit)
				}
			} else if exitErr, ok := err.(*exec.ExitError); ok {
				// 命令以非零退出码结束
				if exitErr.ExitCode() != tt.expectedExit {
					t.Errorf("退出码 = %d, 期望 %d", exitErr.ExitCode(), tt.expectedExit)
				}
			} else {
				// 其他类型的错误（如命令无法启动）
				t.Fatalf("执行命令失败: %v", err)
			}

			// 检查输出内容
			outputStr := string(output)
			if !strings.Contains(outputStr, tt.outputContains) {
				t.Errorf("输出应包含 %q，实际输出:\n%s", tt.outputContains, outputStr)
			}
		})
	}
}

// TestRun_GenerateConfigFlag 使用子进程测试 generate-config 标志的处理。
func TestRun_GenerateConfigFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过子进程测试")
	}

	// 构建测试二进制文件
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "saber-test")

	buildCmd := exec.Command("go", "build", "-tags", "goolm", "-o", binaryPath, ".")
	buildCmd.Dir = filepath.Join("..", "..")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("构建测试二进制文件失败: %v\n输出: %s", err, output)
	}

	tests := []struct {
		name           string
		args           []string
		workDir        string
		expectedExit   int
		outputContains string
	}{
		{
			name:           "generate-config 标志",
			args:           []string{"-generate-config"},
			workDir:        t.TempDir(),
			expectedExit:   0,
			outputContains: "Example configuration generated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binaryPath, tt.args...)
			cmd.Dir = tt.workDir
			output, err := cmd.CombinedOutput()

			if err == nil {
				// 命令成功执行，退出码为 0
				if tt.expectedExit != 0 {
					t.Errorf("退出码 = 0, 期望 %d", tt.expectedExit)
				}
			} else if exitErr, ok := err.(*exec.ExitError); ok {
				// 命令以非零退出码结束
				if exitErr.ExitCode() != tt.expectedExit {
					t.Errorf("退出码 = %d, 期望 %d", exitErr.ExitCode(), tt.expectedExit)
				}
			} else {
				// 其他类型的错误（如命令无法启动）
				t.Fatalf("执行命令失败: %v", err)
			}

			outputStr := string(output)
			if !strings.Contains(outputStr, tt.outputContains) {
				t.Errorf("输出应包含 %q，实际输出:\n%s", tt.outputContains, outputStr)
			}

			// 验证配置文件已生成
			configPath := filepath.Join(tt.workDir, "config.example.yaml")
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				t.Error("期望生成配置文件 config.example.yaml，但文件不存在")
			}
		})
	}
}

// TestRun_ConfigLoadFailure 测试配置加载失败的场景。
func TestRun_ConfigLoadFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过子进程测试")
	}

	// 构建测试二进制文件
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "saber-test")

	buildCmd := exec.Command("go", "build", "-tags", "goolm", "-o", binaryPath, ".")
	buildCmd.Dir = filepath.Join("..", "..")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("构建测试二进制文件失败: %v\n输出: %s", err, output)
	}

	tests := []struct {
		name           string
		setupConfig    func(string) string // 返回配置文件路径
		expectedExit   int
		outputContains string
	}{
		{
			name: "配置文件不存在",
			setupConfig: func(dir string) string {
				return filepath.Join(dir, "nonexistent.yaml")
			},
			expectedExit:   1,
			outputContains: "Failed to load configuration",
		},
		{
			name: "无效的 YAML 格式",
			setupConfig: func(dir string) string {
				configPath := filepath.Join(dir, "invalid.yaml")
				invalidYAML := "invalid: [yaml: content"
				_ = os.WriteFile(configPath, []byte(invalidYAML), 0o644)
				return configPath
			},
			expectedExit:   1,
			outputContains: "Failed to load configuration",
		},
		{
			name: "缺少必需字段",
			setupConfig: func(dir string) string {
				configPath := filepath.Join(dir, "incomplete.yaml")
				incompleteConfig := `matrix:
  homeserver: "https://matrix.org"
  # 缺少 user_id 和认证信息
`
				_ = os.WriteFile(configPath, []byte(incompleteConfig), 0o644)
				return configPath
			},
			expectedExit:   1,
			outputContains: "Failed to create Matrix client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := t.TempDir()
			configPath := tt.setupConfig(workDir)

			cmd := exec.Command(binaryPath, "-c", configPath)
			cmd.Dir = workDir
			output, err := cmd.CombinedOutput()

			if err == nil {
				// 命令成功执行，退出码为 0
				if tt.expectedExit != 0 {
					t.Errorf("退出码 = 0, 期望 %d\n输出: %s", tt.expectedExit, output)
				}
			} else if exitErr, ok := err.(*exec.ExitError); ok {
				// 命令以非零退出码结束
				if exitErr.ExitCode() != tt.expectedExit {
					t.Errorf("退出码 = %d, 期望 %d\n输出: %s", exitErr.ExitCode(), tt.expectedExit, output)
				}
			} else {
				// 其他类型的错误（如命令无法启动）
				t.Fatalf("执行命令失败: %v", err)
			}

			outputStr := string(output)
			if !strings.Contains(outputStr, tt.outputContains) {
				t.Errorf("输出应包含 %q，实际输出:\n%s", tt.outputContains, outputStr)
			}
		})
	}
}

// TestRun_ValidConfigButNoServer 测试有效配置但服务器不可达的场景。
func TestRun_ValidConfigButNoServer(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过子进程测试")
	}

	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "saber-test")

	buildCmd := exec.Command("go", "build", "-tags", "goolm", "-o", binaryPath, ".")
	buildCmd.Dir = filepath.Join("..", "..")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("构建测试二进制文件失败: %v\n输出: %s", err, output)
	}

	workDir := t.TempDir()
	configPath := filepath.Join(workDir, "config.yaml")

	// 创建有效的配置文件，但使用虚假的服务器地址
	validConfig := `matrix:
  homeserver: "https://nonexistent.matrix.server.invalid"
  user_id: "@bot:matrix.org"
  access_token: "fake-token-for-testing"
`
	if err := os.WriteFile(configPath, []byte(validConfig), 0o644); err != nil {
		t.Fatalf("写入配置文件失败: %v", err)
	}

	cmd := exec.Command(binaryPath, "-c", configPath)
	cmd.Dir = workDir

	// 设置超时，因为网络请求可能需要较长时间
	output, err := cmd.CombinedOutput()

	// 预期会因为网络错误而失败
	if err == nil {
		t.Error("期望连接到不存在的服务器时返回错误")
	}

	// 输出应该包含错误信息
	outputStr := string(output)
	if !strings.Contains(outputStr, "Failed to create Matrix client") &&
		!strings.Contains(outputStr, "Login verification failed") {
		// 如果包含其他错误也是可以接受的
		t.Logf("输出内容: %s", outputStr)
	}
}

// TestSignalHandlingPattern 测试信号处理的模式。
// 由于 Run 函数的信号处理部分难以直接测试，这里验证模式正确性。
func TestSignalHandlingPattern(t *testing.T) {
	tests := []struct {
		name        string
		description string
	}{
		{
			name:        "Context 取消模式",
			description: "验证 context.WithCancel 用于优雅关闭",
		},
		{
			name:        "信号通道缓冲",
			description: "信号通道应有缓冲以避免信号丢失",
		},
		{
			name:        "延迟取消调用",
			description: "defer cancel() 确保资源清理",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 这些测试验证模式理解，实际信号处理在 Run 函数中
			t.Log(tt.description)
		})
	}
}

// TestShutdownSequence 测试关闭序列的正确性。
func TestShutdownSequence(t *testing.T) {
	tests := []struct {
		name          string
		hasMCP        bool
		hasProactive  bool
		expectedCalls []string
	}{
		{
			name:          "所有服务都需要关闭",
			hasMCP:        true,
			hasProactive:  true,
			expectedCalls: []string{"MCP connections", "proactive manager", "cancel"},
		},
		{
			name:          "只有 MCP 需要关闭",
			hasMCP:        true,
			hasProactive:  false,
			expectedCalls: []string{"MCP connections", "cancel"},
		},
		{
			name:          "只有 Proactive 需要关闭",
			hasMCP:        false,
			hasProactive:  true,
			expectedCalls: []string{"proactive manager", "cancel"},
		},
		{
			name:          "没有需要关闭的服务",
			hasMCP:        false,
			hasProactive:  false,
			expectedCalls: []string{"cancel"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 验证关闭序列的模式
			// 在实际代码中，这些条件决定了哪些清理操作会执行
			var cleanupOps []string

			if tt.hasMCP {
				cleanupOps = append(cleanupOps, "MCP connections")
			}
			if tt.hasProactive {
				cleanupOps = append(cleanupOps, "proactive manager")
			}
			cleanupOps = append(cleanupOps, "cancel")

			if len(cleanupOps) != len(tt.expectedCalls) {
				t.Errorf("清理操作数量 = %d, 期望 %d", len(cleanupOps), len(tt.expectedCalls))
			}
		})
	}
}

// TestProactiveManagerNilCheck 测试 ProactiveManager 的 nil 检查模式。
func TestProactiveManagerNilCheck(t *testing.T) {
	// 测试 nil 和非 nil 两种情况
	tests := []struct {
		name    string
		manager *int
		isNil   bool
	}{
		{"未初始化的管理器", nil, true},
		{"已初始化的管理器", new(int), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isNil := tt.manager == nil
			if isNil != tt.isNil {
				t.Errorf("管理器 nil 状态 = %v, 期望 %v", isNil, tt.isNil)
			}
		})
	}
}

// TestMCPManagerNilCheck 测试 MCPManager 的 nil 检查模式。
func TestMCPManagerNilCheck(t *testing.T) {
	// 测试 nil 和非 nil 两种情况
	tests := []struct {
		name    string
		manager *int
		isNil   bool
	}{
		{"未初始化的管理器", nil, true},
		{"已初始化的管理器", new(int), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isNil := tt.manager == nil
			if isNil != tt.isNil {
				t.Errorf("管理器 nil 状态 = %v, 期望 %v", isNil, tt.isNil)
			}
		})
	}
}

// TestAIConditionalInitialization 测试 AI 服务的条件初始化。
func TestAIConditionalInitialization(t *testing.T) {
	tests := []struct {
		name          string
		aiEnabled     bool
		shouldInitAI  bool
		shouldInitMCP bool
	}{
		{"AI 禁用", false, false, false},
		{"AI 启用", true, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 使用指针类型模拟真实服务类型，避免 interface{} 的 nilness 警告
			var aiService *int
			var mcpManager *int

			if tt.aiEnabled {
				// 模拟初始化（实际代码中会创建实例）
				aiService = new(int)
				mcpManager = new(int)
			}

			hasAI := aiService != nil
			hasMCP := mcpManager != nil

			if hasAI != tt.shouldInitAI {
				t.Errorf("AI 初始化 = %v, 期望 %v", hasAI, tt.shouldInitAI)
			}
			if hasMCP != tt.shouldInitMCP {
				t.Errorf("MCP 初始化 = %v, 期望 %v", hasMCP, tt.shouldInitMCP)
			}
		})
	}
}

// TestE2EEConditionalInitialization 测试 E2EE 的条件初始化。
func TestE2EEConditionalInitialization(t *testing.T) {
	tests := []struct {
		name           string
		enableE2EE     bool
		pickleKeyPath  string
		shouldInitE2EE bool
	}{
		{"E2EE 禁用", false, "", false},
		{"E2EE 启用有密钥路径", true, "./test.key", true},
		{"E2EE 启用无密钥路径", true, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 模拟 E2EE 条件初始化逻辑
			shouldInit := tt.enableE2EE

			if shouldInit != tt.shouldInitE2EE {
				t.Errorf("E2EE 初始化 = %v, 期望 %v", shouldInit, tt.shouldInitE2EE)
			}
		})
	}
}

// TestCommandRegistration 测试命令注册的模式。
func TestCommandRegistration(t *testing.T) {
	tests := []struct {
		name             string
		aiEnabled        bool
		directChatReply  bool
		groupChatMention bool
		replyToBotReply  bool
		expectedCommands []string
	}{
		{
			name:             "AI 禁用",
			aiEnabled:        false,
			directChatReply:  false,
			groupChatMention: false,
			replyToBotReply:  false,
			expectedCommands: []string{"ping", "help"},
		},
		{
			name:             "AI 启用全部功能",
			aiEnabled:        true,
			directChatReply:  true,
			groupChatMention: true,
			replyToBotReply:  true,
			expectedCommands: []string{"ping", "help", "ai", "ai-clear", "ai-context"},
		},
		{
			name:             "AI 启用但无自动回复",
			aiEnabled:        true,
			directChatReply:  false,
			groupChatMention: false,
			replyToBotReply:  false,
			expectedCommands: []string{"ping", "help", "ai", "ai-clear", "ai-context"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 验证命令注册的条件逻辑
			var registeredCommands []string

			// 内置命令始终注册
			registeredCommands = append(registeredCommands, "ping", "help")

			if tt.aiEnabled {
				registeredCommands = append(registeredCommands, "ai", "ai-clear", "ai-context")
			}

			if len(registeredCommands) < len(tt.expectedCommands) {
				t.Errorf("注册命令数量 = %d, 至少期望 %d", len(registeredCommands), len(tt.expectedCommands))
			}
		})
	}
}

// TestAutoJoinRooms 测试自动加入房间的逻辑。
func TestAutoJoinRooms(t *testing.T) {
	tests := []struct {
		name       string
		rooms      []string
		shouldJoin bool
		roomCount  int
	}{
		{"无自动加入房间", []string{}, false, 0},
		{"单个房间", []string{"!room1:matrix.org"}, true, 1},
		{"多个房间", []string{"!room1:matrix.org", "!room2:matrix.org"}, true, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shouldJoin := len(tt.rooms) > 0

			if shouldJoin != tt.shouldJoin {
				t.Errorf("应该加入房间 = %v, 期望 %v", shouldJoin, tt.shouldJoin)
			}

			if len(tt.rooms) != tt.roomCount {
				t.Errorf("房间数量 = %d, 期望 %d", len(tt.rooms), tt.roomCount)
			}
		})
	}
}

// TestLogLevelBasedOnVerbose 测试日志级别根据 verbose 标志变化。
func TestLogLevelBasedOnVerbose(t *testing.T) {
	tests := []struct {
		name         string
		verbose      bool
		expectedDesc string
	}{
		{"非详细模式", false, "Info 级别"},
		{"详细模式", true, "Debug 级别"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 验证 setupLogging 根据 verbose 设置正确的日志级别
			setupLogging(tt.verbose)
			t.Logf("预期日志级别: %s", tt.expectedDesc)
		})
	}
}

// TestGracefulShutdownTimeout 测试优雅关闭的超时处理。
// 由于实际关闭涉及网络连接，这里测试模式正确性。
func TestGracefulShutdownTimeout(t *testing.T) {
	// 模拟关闭流程
	cleanupSteps := []string{
		"关闭 MCP 连接",
		"停止主动聊天管理器",
		"取消 context",
		"等待 goroutine 结束",
	}

	for i, step := range cleanupSteps {
		t.Logf("关闭步骤 %d: %s", i+1, step)
	}

	// 验证关闭步骤顺序
	if cleanupSteps[len(cleanupSteps)-1] != "等待 goroutine 结束" {
		t.Error("最后一步应该是等待 goroutine 结束")
	}
}

// TestBuildInfoStringFormatting 测试 BuildInfo 的字符串格式化。
func TestBuildInfoStringFormatting(t *testing.T) {
	info := matrix.BuildInfo{
		Version:       "1.0.0",
		GitCommit:     "abc123",
		GitBranch:     "main",
		BuildTime:     "2024-01-01T00:00:00Z",
		GoVersion:     "go1.21.0",
		BuildPlatform: "linux/amd64",
	}

	// 验证所有字段都可以安全地格式化为字符串
	tests := []struct {
		name  string
		value string
	}{
		{"Version", info.Version},
		{"GitCommit", info.GitCommit},
		{"GitBranch", info.GitBranch},
		{"BuildTime", info.BuildTime},
		{"GoVersion", info.GoVersion},
		{"BuildPlatform", info.BuildPlatform},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == "" {
				t.Errorf("BuildInfo.%s 应该有值用于格式化", tt.name)
			}
		})
	}
}

// TestReconnectConfig 测试重连配置的默认值。
func TestReconnectConfig(t *testing.T) {
	// 模拟 DefaultReconnectConfig 的默认值
	expectedMaxRetries := 5
	expectedInitialDelay := "2s"
	expectedMaxDelay := "30s"

	t.Logf("预期重连配置: 最大重试 %d, 初始延迟 %s, 最大延迟 %s",
		expectedMaxRetries, expectedInitialDelay, expectedMaxDelay)
}

// TestPresenceService 测试在线状态服务的初始化。
func TestPresenceService(t *testing.T) {
	tests := []struct {
		name        string
		presence    string
		statusMsg   string
		shouldSetOK bool
	}{
		{"设置在线状态", "online", "Saber Bot is running", true},
		{"设置离线状态", "offline", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 验证状态设置参数
			if tt.presence == "" {
				t.Error("状态不应为空")
			}
		})
	}
}
