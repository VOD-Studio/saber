// Package qq 提供 QQ 机器人的适配器实现。
package qq

import (
	"context"
	"testing"

	"rua.plus/saber/internal/config"
)

// TestNewClient 测试创建客户端。
func TestNewClient(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.QQConfig
		wantErr bool
	}{
		{
			name: "有效配置",
			cfg: &config.QQConfig{
				AppID:     "test-app-id",
				AppSecret: "test-secret",
			},
			wantErr: false,
		},
		{
			name:    "nil配置",
			cfg:     nil,
			wantErr: true,
		},
		{
			name: "空AppID",
			cfg: &config.QQConfig{
				AppID:     "",
				AppSecret: "test-secret",
			},
			wantErr: false, // NewClient 不验证空 AppID
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(tt.cfg)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && client == nil {
				t.Error("expected client, got nil")
			}
		})
	}
}

// TestClient_GetConfig 测试获取配置。
func TestClient_GetConfig(t *testing.T) {
	cfg := &config.QQConfig{
		AppID:     "test-app-id",
		AppSecret: "test-secret",
		Enabled:   true,
		Sandbox:   false,
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := client.GetConfig()
	if got == nil {
		t.Error("GetConfig returned nil")
		return
	}
	if got.AppID != cfg.AppID {
		t.Errorf("AppID = %q, want %q", got.AppID, cfg.AppID)
	}
	if got.Enabled != cfg.Enabled {
		t.Errorf("Enabled = %v, want %v", got.Enabled, cfg.Enabled)
	}
}

// TestClient_GetAPI 测试获取 API。
func TestClient_GetAPI(t *testing.T) {
	cfg := &config.QQConfig{
		AppID:     "test-app-id",
		AppSecret: "test-secret",
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 未启动时 API 应为 nil
	api := client.GetAPI()
	if api != nil {
		t.Error("expected nil API before Start")
	}
}

// TestClient_GetTokenSource 测试获取 TokenSource。
func TestClient_GetTokenSource(t *testing.T) {
	cfg := &config.QQConfig{
		AppID:     "test-app-id",
		AppSecret: "test-secret",
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 未启动时 TokenSource 应为 nil
	ts := client.GetTokenSource()
	if ts != nil {
		t.Error("expected nil TokenSource before Start")
	}
}

// TestClient_Stop 测试停止客户端。
func TestClient_Stop(t *testing.T) {
	cfg := &config.QQConfig{
		AppID:     "test-app-id",
		AppSecret: "test-secret",
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 停止未启动的客户端应该安全
	client.Stop()

	// 多次调用 Stop 应该安全
	client.Stop()
}

// TestClient_Start_Stop 测试启动和停止。
func TestClient_Start_Stop(t *testing.T) {
	cfg := &config.QQConfig{
		AppID:           "test-app-id",
		AppSecret:       "test-secret",
		TimeoutSeconds:  5,
		Sandbox:         true, // 使用沙盒模式
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx := context.Background()

	// 启动客户端
	err = client.Start(ctx)
	if err != nil {
		t.Errorf("Start failed: %v", err)
	}

	// 验证 API 已创建
	if client.GetAPI() == nil {
		t.Error("API should not be nil after Start")
	}

	// 验证 TokenSource 已创建
	if client.GetTokenSource() == nil {
		t.Error("TokenSource should not be nil after Start")
	}

	// 停止客户端
	client.Stop()
}