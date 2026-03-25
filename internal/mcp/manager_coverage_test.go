// Package mcp_test 包含 MCP 管理器的单元测试。
package mcp

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/config"
)

// TestNewManager_NilConfig 测试空配置创建管理器。
func TestNewManager_NilConfig(t *testing.T) {
	mgr := NewManager(nil)
	if mgr == nil {
		t.Fatal("NewManager returned nil")
	}

	if mgr.IsEnabled() {
		t.Error("Manager should be disabled with nil config")
	}
}

// TestNewManager_DisabledConfig 测试禁用配置。
func TestNewManager_DisabledConfig(t *testing.T) {
	cfg := &config.MCPConfig{
		Enabled: false,
	}

	mgr := NewManager(cfg)
	if mgr == nil {
		t.Fatal("NewManager returned nil")
	}

	if mgr.IsEnabled() {
		t.Error("Manager should be disabled when config.Enabled is false")
	}
}

// TestNewManager_EnabledConfig 测试启用配置。
func TestNewManager_EnabledConfig(t *testing.T) {
	cfg := &config.MCPConfig{
		Enabled: true,
	}

	mgr := NewManager(cfg)
	if mgr == nil {
		t.Fatal("NewManager returned nil")
	}

	if !mgr.IsEnabled() {
		t.Error("Manager should be enabled when config.Enabled is true")
	}
}

// TestManager_ListServers_Empty 测试空服务器列表。
func TestManager_ListServers_Empty(t *testing.T) {
	cfg := &config.MCPConfig{
		Enabled: true,
		Servers: make(map[string]config.ServerConfig),
	}

	mgr := NewManager(cfg)
	servers := mgr.ListServers()

	if len(servers) != 0 {
		t.Errorf("expected empty server list, got %d servers", len(servers))
	}
}

// TestManager_GetSession_Nil 测试获取不存在的会话。
func TestManager_GetSession_Nil(t *testing.T) {
	cfg := &config.MCPConfig{Enabled: true}
	mgr := NewManager(cfg)

	session := mgr.GetSession("nonexistent")
	if session != nil {
		t.Error("GetSession should return nil for nonexistent server")
	}
}

// TestManager_GetClient_Nil 测试获取不存在的客户端。
func TestManager_GetClient_Nil(t *testing.T) {
	cfg := &config.MCPConfig{Enabled: true}
	mgr := NewManager(cfg)

	client := mgr.GetClient("nonexistent")
	if client != nil {
		t.Error("GetClient should return nil for nonexistent server")
	}
}

// TestManager_ListTools_Empty 测试空工具列表。
func TestManager_ListTools_Empty(t *testing.T) {
	cfg := &config.MCPConfig{Enabled: true}
	mgr := NewManager(cfg)

	tools := mgr.ListTools()
	// 返回空切片而不是 nil
	if len(tools) != 0 {
		t.Errorf("expected empty tools list, got %d tools", len(tools))
	}
}

// TestManager_GetServerForTool_Empty 测试空工具查找。
func TestManager_GetServerForTool_Empty(t *testing.T) {
	cfg := &config.MCPConfig{Enabled: true}
	mgr := NewManager(cfg)

	server := mgr.GetServerForTool("nonexistent_tool")
	if server != "" {
		t.Errorf("expected empty server name, got %q", server)
	}
}

// TestManager_Close_NoClients 测试关闭无客户端的管理器。
func TestManager_Close_NoClients(t *testing.T) {
	cfg := &config.MCPConfig{Enabled: true}
	mgr := NewManager(cfg)

	err := mgr.Close()
	if err != nil {
		t.Errorf("Close should not error with no clients: %v", err)
	}
}

// TestManager_InitBuiltinServers_Disabled 测试禁用时初始化内置服务器。
func TestManager_InitBuiltinServers_Disabled(t *testing.T) {
	cfg := &config.MCPConfig{Enabled: false}
	mgr := NewManager(cfg)

	ctx := context.Background()
	err := mgr.InitBuiltinServers(ctx)
	// 禁用时应该快速返回，无错误
	if err != nil {
		t.Errorf("InitBuiltinServers should not error when disabled: %v", err)
	}
}

// TestManager_Init_Disabled 测试禁用时初始化。
func TestManager_Init_Disabled(t *testing.T) {
	cfg := &config.MCPConfig{Enabled: false}
	mgr := NewManager(cfg)

	ctx := context.Background()
	err := mgr.Init(ctx)
	// 禁用时应该快速返回
	if err != nil {
		t.Errorf("Init should not error when disabled: %v", err)
	}
}

// TestManager_InvalidateToolCache 测试工具缓存失效。
func TestManager_InvalidateToolCache(t *testing.T) {
	cfg := &config.MCPConfig{Enabled: true}
	mgr := NewManager(cfg)

	// 设置缓存有效
	mgr.toolCacheValid = true

	// 失效缓存
	mgr.InvalidateToolCache()

	// 验证缓存已失效
	if mgr.toolCacheValid {
		t.Error("toolCacheValid should be false after InvalidateToolCache")
	}
}

// TestRateLimiter_Basic 测试基本速率限制。
func TestRateLimiter_Basic(t *testing.T) {
	limiter := NewRateLimiter(10)
	userID := id.UserID("@test:example.com")
	roomID := id.RoomID("!test:example.com")

	// 前 10 次应该允许
	for i := range 10 {
		if !limiter.Allow(userID, roomID) {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// 第 11 次应该被拒绝
	if limiter.Allow(userID, roomID) {
		t.Error("request 11 should be denied")
	}
}

// TestServerInfo_Struct 测试 ServerInfo 结构体。
func TestServerInfo_Struct(t *testing.T) {
	info := ServerInfo{
		Name:    "test-server",
		Type:    "stdio",
		Enabled: true,
	}

	if info.Name != "test-server" {
		t.Errorf("expected Name 'test-server', got %q", info.Name)
	}
	if info.Type != "stdio" {
		t.Errorf("expected Type 'stdio', got %q", info.Type)
	}
	if !info.Enabled {
		t.Error("Enabled should be true")
	}
}
