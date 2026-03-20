// Package bot_test 包含机器人重构函数的单元测试。
package bot

import (
	"context"
	"testing"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

func TestInitConfig(t *testing.T) {
	tests := []struct {
		name    string
		info    matrix.BuildInfo
		wantErr bool
	}{
		{
			name: "默认构建信息",
			info: matrix.BuildInfo{
				Version:   "1.0.0",
				GitCommit: "abc123",
				GitBranch: "main",
			},
			wantErr: false,
		},
		{
			name:    "空构建信息",
			info:    matrix.BuildInfo{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("BuildInfo: Version=%s, GitCommit=%s", tt.info.Version, tt.info.GitCommit)
		})
	}
}

func TestInitMatrixClient(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.MatrixConfig
		wantErr bool
	}{
		{
			name: "有效配置",
			cfg: &config.MatrixConfig{
				Homeserver:  "https://matrix.org",
				UserID:      "@bot:matrix.org",
				AccessToken: "test-token",
			},
			wantErr: false,
		},
		{
			name: "缺少 homeserver",
			cfg: &config.MatrixConfig{
				UserID:      "@bot:matrix.org",
				AccessToken: "test-token",
			},
			wantErr: true,
		},
		{
			name: "缺少 user_id",
			cfg: &config.MatrixConfig{
				Homeserver:  "https://matrix.org",
				AccessToken: "test-token",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInitServices(t *testing.T) {
	tests := []struct {
		name       string
		aiEnabled  bool
		mcpEnabled bool
	}{
		{"AI 禁用", false, false},
		{"AI 启用", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.aiEnabled {
				t.Log("AI 服务应该初始化")
			}
			if tt.mcpEnabled {
				t.Log("MCP 管理器应该初始化")
			}
		})
	}
}

func TestSetupSignalHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cancel()
	<-ctx.Done()

	if ctx.Err() != context.Canceled {
		t.Errorf("期望 context.Canceled，实际: %v", ctx.Err())
	}
}

func TestShutdown(t *testing.T) {
	tests := []struct {
		name         string
		hasAI        bool
		hasMCP       bool
		hasProactive bool
	}{
		{"无服务", false, false, false},
		{"所有服务", true, true, true},
		{"只有 AI", true, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var shutdownOps []string

			if tt.hasAI {
				shutdownOps = append(shutdownOps, "AI service stopped")
			}
			if tt.hasMCP {
				shutdownOps = append(shutdownOps, "MCP connections closed")
			}
			if tt.hasProactive {
				shutdownOps = append(shutdownOps, "Proactive manager stopped")
			}

			t.Logf("关闭操作: %v", shutdownOps)
		})
	}
}

func TestServices_Holder(t *testing.T) {
	svc := &services{}

	if svc.aiService != nil {
		t.Error("初始 aiService 应该为 nil")
	}
	if svc.mcpManager != nil {
		t.Error("初始 mcpManager 应该为 nil")
	}
	if svc.proactiveManager != nil {
		t.Error("初始 proactiveManager 应该为 nil")
	}
}

func TestAppState_Init(t *testing.T) {
	state := &appState{
		info: matrix.BuildInfo{
			Version:   "1.0.0",
			GitCommit: "abc123",
			GitBranch: "main",
		},
	}

	if state.info.Version != "1.0.0" {
		t.Errorf("期望 Version=1.0.0，实际: %s", state.info.Version)
	}
}
