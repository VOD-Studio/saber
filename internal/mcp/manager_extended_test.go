//go:build goolm

// Package mcp 提供 MCP 测试。
package mcp

import (
	"context"
	"testing"

	"rua.plus/saber/internal/config"
)

// TestNewManagerWithBuiltin_NilConfig 测试 nil 配置。
func TestNewManagerWithBuiltin_NilConfig(t *testing.T) {
	mgr := NewManagerWithBuiltin(nil)
	if mgr == nil {
		t.Fatal("NewManagerWithBuiltin returned nil")
	}
}

// TestNewManagerWithBuiltin_WithConfig 测试带配置创建。
func TestNewManagerWithBuiltin_WithConfig(t *testing.T) {
	cfg := &config.MCPConfig{
		Enabled: true,
		Servers: map[string]config.ServerConfig{},
	}

	mgr := NewManagerWithBuiltin(cfg)
	if mgr == nil {
		t.Fatal("NewManagerWithBuiltin returned nil")
	}
	if !mgr.IsEnabled() {
		t.Error("Manager should be enabled")
	}
}

// TestManager_InitBuiltinServers 测试初始化内置服务器。
func TestManager_InitBuiltinServers(t *testing.T) {
	mgr := NewManagerWithBuiltin(nil)

	err := mgr.InitBuiltinServers(context.Background())
	if err != nil {
		t.Errorf("InitBuiltinServers failed: %v", err)
	}
}

// TestManager_Init_NilConfig 测试 nil 配置初始化。
func TestManager_Init_NilConfig(t *testing.T) {
	mgr := NewManager(nil)

	err := mgr.Init(context.Background())
	if err != nil {
		t.Errorf("Init with nil config should not error: %v", err)
	}
}

// TestManager_Init_Disabled 测试禁用配置初始化。
func TestManager_Init_Disabled(t *testing.T) {
	cfg := &config.MCPConfig{
		Enabled: false,
		Servers: map[string]config.ServerConfig{
			"test": {Command: "echo"},
		},
	}

	mgr := NewManager(cfg)
	err := mgr.Init(context.Background())
	if err != nil {
		t.Errorf("Init with disabled config should not error: %v", err)
	}
}

// TestManager_ListTools 测试列出工具。
func TestManager_ListTools(t *testing.T) {
	t.Run("disabled manager", func(t *testing.T) {
		mgr := NewManager(nil)
		tools := mgr.ListTools()
		if tools != nil {
			t.Error("ListTools should return nil for disabled manager")
		}
	})

	t.Run("enabled manager", func(t *testing.T) {
		mgr := NewManagerWithBuiltin(nil)
		_ = mgr.InitBuiltinServers(context.Background())

		tools := mgr.ListTools()
		// 可能有内置工具
		_ = tools
	})
}

// TestManager_GetServerForTool 测试获取工具服务器。
func TestManager_GetServerForTool(t *testing.T) {
	mgr := NewManager(nil)

	server := mgr.GetServerForTool("nonexistent")
	if server != "" {
		t.Errorf("GetServerForTool for nonexistent tool should return empty string, got %q", server)
	}
}

// TestManager_Close 测试关闭管理器。
func TestManager_Close(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		mgr := NewManager(nil)
		// Close 不应该 panic
		mgr.Close()
	})

	t.Run("empty servers", func(t *testing.T) {
		cfg := &config.MCPConfig{
			Enabled: true,
			Servers: map[string]config.ServerConfig{},
		}
		mgr := NewManager(cfg)
		mgr.Close()
	})

	t.Run("multiple calls", func(t *testing.T) {
		mgr := NewManager(nil)
		mgr.Close()
		mgr.Close() // 应该安全
	})
}

// TestValidateServerConfig 测试服务器配置验证。
func TestValidateServerConfig_Extended(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.ServerConfig
		wantErr bool
	}{
		{
			name: "builtin type",
			cfg: config.ServerConfig{
				Type:    "builtin",
				Enabled: true,
			},
			wantErr: false,
		},
		{
			name: "stdio type with command",
			cfg: config.ServerConfig{
				Type:    "stdio",
				Command: "/usr/bin/test",
				Enabled: true,
			},
			wantErr: false,
		},
		{
			name: "unknown type",
			cfg: config.ServerConfig{
				Type:    "unknown",
				Enabled: true,
			},
			wantErr: true,
		},
		{
			name: "empty type",
			cfg: config.ServerConfig{
				Type:    "",
				Enabled: true,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateServerConfig(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateServerConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}