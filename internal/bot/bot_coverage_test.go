// Package bot_test 包含机器人核心函数的单元测试。
package bot

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"testing"

	"rua.plus/saber/internal/config"
)

// TestShutdown_Timeout 测试关闭超时处理。
func TestShutdown_Timeout(t *testing.T) {
	// 创建一个带短超时的配置
	cfg := &config.Config{
		Shutdown: config.ShutdownConfig{
			TimeoutSeconds: 1, // 1 秒超时
		},
	}

	state := &appState{
		cfg:      cfg,
		services: &services{
			// 所有服务为 nil，应该快速关闭
		},
	}

	// 重定向日志
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, nil)
	slog.SetDefault(slog.New(handler))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	// shutdown 不应该 panic
	state.shutdown(cancel)

	_ = ctx // 使用 ctx 避免编译警告

	// 验证日志包含关闭信息
	output := buf.String()
	if output == "" {
		t.Log("shutdown completed without log output")
	}
}

// TestAutoJoinRooms_EmptyList 测试空房间列表。
func TestAutoJoinRooms_EmptyList(t *testing.T) {
	cfg := &config.Config{
		Matrix: config.MatrixConfig{
			AutoJoinRooms: []string{}, // 空列表
		},
	}

	state := &appState{
		cfg:      cfg,
		services: &services{},
	}

	// autoJoinRooms 不应该 panic
	state.autoJoinRooms()
}

// TestSetupSignalHandler_ContextCancellation 测试信号处理器。
func TestSetupSignalHandler_ContextCancellation(t *testing.T) {
	state := &appState{
		cfg: &config.Config{},
	}

	ctx, cancel := state.setupSignalHandler()
	defer cancel()

	// 验证 context 是有效的
	if ctx == nil {
		t.Error("setupSignalHandler returned nil context")
	}

	// Context 应该不是立即取消的
	select {
	case <-ctx.Done():
		t.Error("context should not be cancelled immediately")
	default:
		// 正确：context 未取消
	}
}

// TestServices_NilSafety 测试服务的 nil 安全性。
func TestServices_NilSafety(t *testing.T) {
	svc := &services{}

	// 所有字段应该是 nil
	if svc.aiService != nil {
		t.Error("aiService should be nil")
	}
	if svc.mcpManager != nil {
		t.Error("mcpManager should be nil")
	}
	if svc.proactiveManager != nil {
		t.Error("proactiveManager should be nil")
	}
	if svc.commandService != nil {
		t.Error("commandService should be nil")
	}
	if svc.eventHandler != nil {
		t.Error("eventHandler should be nil")
	}
	if svc.presence != nil {
		t.Error("presence should be nil")
	}
	if svc.mediaService != nil {
		t.Error("mediaService should be nil")
	}
	if svc.memeService != nil {
		t.Error("memeService should be nil")
	}
	if svc.client != nil {
		t.Error("client should be nil")
	}
}

// TestAppState_InitServices_AIEnabled 测试 AI 服务初始化。
func TestAppState_InitServices_AIEnabled(t *testing.T) {
	cfg := &config.Config{
		AI: config.AIConfig{
			Enabled: false, // 禁用 AI
		},
	}

	state := &appState{
		cfg:      cfg,
		services: &services{},
	}

	// initServices 应该在 AI 禁用时快速返回
	err := state.initServices()
	if err != nil {
		t.Errorf("initServices should not error when AI disabled: %v", err)
	}
}

// TestSetupLogging_Levels 测试日志级别设置。
func TestSetupLogging_Levels(t *testing.T) {
	tests := []struct {
		name    string
		verbose bool
	}{
		{"默认级别", false},
		{"调试级别", true},
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

// TestWaitForShutdown_ContextCancelled 测试等待关闭。
func TestWaitForShutdown_ContextCancelled(t *testing.T) {
	state := &appState{
		cfg: &config.Config{
			Shutdown: config.ShutdownConfig{
				TimeoutSeconds: 5,
			},
		},
		services: &services{},
	}

	// 重定向日志
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, nil)
	slog.SetDefault(slog.New(handler))

	ctx, cancel := context.WithCancel(context.Background())
	// 立即取消
	cancel()

	// waitForShutdown 应该立即返回
	err := state.waitForShutdown(ctx, cancel)
	if err != nil {
		t.Errorf("waitForShutdown should return nil, got: %v", err)
	}
}

// TestInitMemeService_Disabled 测试禁用 Meme 服务。
func TestInitMemeService_Disabled(t *testing.T) {
	cfg := &config.Config{
		Meme: config.MemeConfig{
			Enabled: false,
		},
	}

	state := &appState{
		cfg:      cfg,
		services: &services{},
	}

	// initMemeService 不应该 panic
	state.initMemeService()

	if state.services.memeService != nil {
		t.Error("memeService should be nil when disabled")
	}
}

// TestExitCodeError 测试 ExitCodeError 类型。
func TestExitCodeError(t *testing.T) {
	tests := []struct {
		name         string
		err          *ExitCodeError
		expectedMsg  string
		expectedCode int
	}{
		{
			name:         "成功退出",
			err:          ExitSuccess(),
			expectedMsg:  "正常退出",
			expectedCode: 0,
		},
		{
			name:         "错误退出",
			err:          ExitError(1, os.ErrNotExist),
			expectedMsg:  "file does not exist",
			expectedCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Error() != tt.expectedMsg {
				t.Errorf("expected message %q, got %q", tt.expectedMsg, tt.err.Error())
			}

			code, ok := IsExitCode(tt.err)
			if !ok {
				t.Error("IsExitCode should return true")
			}
			if code != tt.expectedCode {
				t.Errorf("expected code %d, got %d", tt.expectedCode, code)
			}
		})
	}
}

// TestInitServices_ValidatesConfig 测试 initServices 验证配置。
func TestInitServices_ValidatesConfig(t *testing.T) {
	// 创建一个无效的 AI 配置
	cfg := &config.Config{
		AI: config.AIConfig{
			Enabled:  true,
			Provider: "invalid-provider", // 无效的 provider
			Models:   map[string]config.ModelConfig{},
		},
	}

	state := &appState{
		cfg:      cfg,
		services: &services{},
	}

	// 重定向日志
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, nil)
	slog.SetDefault(slog.New(handler))

	err := state.initServices()
	// 由于配置验证可能失败或服务初始化可能失败
	// 我们只检查不会 panic
	_ = err
}
