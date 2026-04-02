// Package qq 提供 QQ 机器人的适配器实现。
package qq

import (
	"testing"

	"rua.plus/saber/internal/config"
)

// TestNewAdapter 测试创建适配器。
func TestNewAdapter(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.QQConfig
		aiCfg     *config.AIConfig
		wantErr   bool
		errContains string
	}{
		{
			name: "QQ配置为空",
			cfg:  nil,
			aiCfg: &config.AIConfig{},
			wantErr: true,
			errContains: "QQ配置不能为空",
		},
		{
			name: "AI配置为空",
			cfg:  &config.QQConfig{AppID: "test", AppSecret: "secret"},
			aiCfg: nil,
			wantErr: true,
			errContains: "AI配置不能为空",
		},
		{
			name: "有效配置",
			cfg:  &config.QQConfig{AppID: "test-app-id", AppSecret: "test-secret"},
			aiCfg: &config.AIConfig{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, err := NewAdapter(tt.cfg, tt.aiCfg, nil, nil)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if adapter == nil {
					t.Error("expected adapter, got nil")
				}
			}
		})
	}
}

// TestAdapter_IsEnabled 测试适配器启用状态。
func TestAdapter_IsEnabled(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
	}{
		{"启用", true},
		{"禁用", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.QQConfig{
				AppID:     "test",
				AppSecret: "secret",
				Enabled:   tt.enabled,
			}

			adapter, err := NewAdapter(cfg, &config.AIConfig{}, nil, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if adapter.IsEnabled() != tt.enabled {
				t.Errorf("IsEnabled() = %v, want %v", adapter.IsEnabled(), tt.enabled)
			}
		})
	}
}

// TestAdapter_GetClient 测试获取客户端。
func TestAdapter_GetClient(t *testing.T) {
	cfg := &config.QQConfig{
		AppID:     "test-app-id",
		AppSecret: "test-secret",
	}

	adapter, err := NewAdapter(cfg, &config.AIConfig{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	client := adapter.GetClient()
	if client == nil {
		t.Error("GetClient returned nil")
	}
}

// TestAdapter_GetConfig 测试获取配置。
func TestAdapter_GetConfig(t *testing.T) {
	cfg := &config.QQConfig{
		AppID:     "test-app-id",
		AppSecret: "test-secret",
		Enabled:   true,
	}

	adapter, err := NewAdapter(cfg, &config.AIConfig{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := adapter.GetConfig()
	if got == nil {
		t.Error("GetConfig returned nil")
		return
	}
	if got.AppID != cfg.AppID {
		t.Errorf("AppID = %q, want %q", got.AppID, cfg.AppID)
	}
}

// TestAdapter_Stop 测试停止未启动的适配器。
func TestAdapter_Stop(t *testing.T) {
	cfg := &config.QQConfig{
		AppID:     "test-app-id",
		AppSecret: "test-secret",
	}

	adapter, err := NewAdapter(cfg, &config.AIConfig{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 停止未启动的适配器应该安全
	adapter.Stop()

	// 多次调用 Stop 应该安全
	adapter.Stop()
}

// TestAIServiceAdapter 测试 AI 服务适配器。
func TestAIServiceAdapter(t *testing.T) {
	t.Run("nil service", func(t *testing.T) {
		adapter := &aiServiceAdapter{svc: nil}
		// 应该 panic 或返回 false，这里测试不会崩溃
		defer func() {
			if r := recover(); r != nil {
				// 预期的 panic
			}
		}()
		_ = adapter.IsEnabled()
	})
}