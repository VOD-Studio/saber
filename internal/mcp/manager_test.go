//go:build goolm

package mcp

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	appcontext "rua.plus/saber/internal/context"
)

func TestNewManager(t *testing.T) {
	tests := []struct {
		name           string
		cfg            *config.MCPConfig
		expectedEnable bool
	}{
		{
			name:           "空配置",
			cfg:            nil,
			expectedEnable: false,
		},
		{
			name: "禁用配置",
			cfg: &config.MCPConfig{
				Enabled: false,
			},
			expectedEnable: false,
		},
		{
			name: "启用配置",
			cfg: &config.MCPConfig{
				Enabled: true,
			},
			expectedEnable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := NewManager(tt.cfg)
			if mgr == nil {
				t.Fatal("NewManager returned nil")
			}
			if mgr.IsEnabled() != tt.expectedEnable {
				t.Errorf("IsEnabled() = %v, want %v", mgr.IsEnabled(), tt.expectedEnable)
			}
		})
	}
}

func TestNewManagerWithBuiltin(t *testing.T) {
	mgr := NewManagerWithBuiltin(nil)
	if mgr == nil {
		t.Fatal("NewManagerWithBuiltin returned nil")
	}
	if !mgr.IsEnabled() {
		t.Error("Manager created with NewManagerWithBuiltin should always be enabled")
	}
}

func TestManager_GetSession(t *testing.T) {
	mgr := NewManager(nil)

	session := mgr.GetSession("nonexistent")
	if session != nil {
		t.Error("GetSession for nonexistent server should return nil")
	}
}

func TestManager_GetClient(t *testing.T) {
	mgr := NewManager(nil)

	client := mgr.GetClient("nonexistent")
	if client != nil {
		t.Error("GetClient for nonexistent server should return nil")
	}
}

func TestManager_ListServers(t *testing.T) {
	tests := []struct {
		name          string
		cfg           *config.MCPConfig
		expectedCount int
	}{
		{
			name:          "空配置",
			cfg:           nil,
			expectedCount: 0,
		},
		{
			name: "空服务器列表",
			cfg: &config.MCPConfig{
				Enabled: true,
				Servers: map[string]config.ServerConfig{},
			},
			expectedCount: 0,
		},
		{
			name: "单个服务器",
			cfg: &config.MCPConfig{
				Enabled: true,
				Servers: map[string]config.ServerConfig{
					"test": {Type: "builtin", Enabled: true},
				},
			},
			expectedCount: 1,
		},
		{
			name: "多个服务器",
			cfg: &config.MCPConfig{
				Enabled: true,
				Servers: map[string]config.ServerConfig{
					"server1": {Type: "builtin", Enabled: true},
					"server2": {Type: "stdio", Enabled: false, Command: "/bin/test"},
				},
			},
			expectedCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := NewManager(tt.cfg)
			servers := mgr.ListServers()
			if len(servers) != tt.expectedCount {
				t.Errorf("ListServers() returned %d servers, want %d", len(servers), tt.expectedCount)
			}
		})
	}
}

func TestWithUserContext(t *testing.T) {
	ctx := context.Background()
	userID := id.UserID("@alice:example.com")
	roomID := id.RoomID("!room:example.com")

	ctx = appcontext.WithUserContext(ctx, userID, roomID)

	extractedUser, ok := appcontext.GetUserFromContext(ctx)
	if !ok || extractedUser != userID {
		t.Errorf("GetUserFromContext() = %q, ok = %v, want %q", extractedUser, ok, userID)
	}

	extractedRoom, ok := appcontext.GetRoomFromContext(ctx)
	if !ok || extractedRoom != roomID {
		t.Errorf("GetRoomFromContext() = %q, ok = %v, want %q", extractedRoom, ok, roomID)
	}
}

func TestGetUserFromContext_Missing(t *testing.T) {
	ctx := context.Background()

	userID, ok := appcontext.GetUserFromContext(ctx)
	if ok {
		t.Errorf("GetUserFromContext() = %q, ok = %v, want ok = false", userID, ok)
	}
}

func TestGetRoomFromContext_Missing(t *testing.T) {
	ctx := context.Background()

	roomID, ok := appcontext.GetRoomFromContext(ctx)
	if ok {
		t.Errorf("GetRoomFromContext() = %q, ok = %v, want ok = false", roomID, ok)
	}
}

func TestServerInfo(t *testing.T) {
	info := ServerInfo{
		Name:    "test_server",
		Type:    "builtin",
		Enabled: true,
	}

	if info.Name != "test_server" {
		t.Errorf("Name = %q, want %q", info.Name, "test_server")
	}
	if info.Type != "builtin" {
		t.Errorf("Type = %q, want %q", info.Type, "builtin")
	}
	if !info.Enabled {
		t.Error("Enabled should be true")
	}
}

func TestManager_CallTool_Disabled(t *testing.T) {
	mgr := NewManager(nil)
	ctx := context.Background()

	_, err := mgr.CallTool(ctx, "server", "tool", nil)
	if err == nil {
		t.Error("CallTool on disabled manager should return error")
	}
}

func TestManager_CallTool_MissingContext(t *testing.T) {
	mgr := NewManagerWithBuiltin(nil)
	ctx := context.Background()

	_, err := mgr.CallTool(ctx, "server", "tool", nil)
	if err == nil {
		t.Error("CallTool without user context should return error")
	}
}

func TestManager_CallTool_NonexistentServer(t *testing.T) {
	mgr := NewManagerWithBuiltin(nil)
	ctx := context.Background()
	ctx = appcontext.WithUserContext(ctx, "@user:example.com", "!room:example.com")

	_, err := mgr.CallTool(ctx, "nonexistent", "tool", nil)
	if err == nil {
		t.Error("CallTool with nonexistent server should return error")
	}
}

func TestMockManager_IsEnabled(t *testing.T) {
	mgr := NewMockManager(true)
	if !mgr.IsEnabled() {
		t.Error("MockManager with enabled=true should return true")
	}

	mgr.SetEnabled(false)
	if mgr.IsEnabled() {
		t.Error("MockManager after SetEnabled(false) should return false")
	}
}

func TestMockManager_Server(t *testing.T) {
	mgr := NewMockManager(true)
	server := mgr.GetServer()

	if server == nil {
		t.Fatal("GetServer returned nil")
	}
	if server.ToolCount() != 0 {
		t.Errorf("New server should have 0 tools, got %d", server.ToolCount())
	}
}

func TestMockManager_RegisterTool(t *testing.T) {
	mgr := NewMockManager(true)
	mgr.RegisterTool(TestFixtures.EchoTool)

	tools := mgr.ListTools()
	if len(tools) != 1 {
		t.Errorf("ListTools() returned %d tools, want 1", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("Tool name = %q, want %q", tools[0].Name, "echo")
	}
}

func TestMockManager_CallTool(t *testing.T) {
	mgr := NewMockManager(true)
	mgr.RegisterTool(TestFixtures.EchoTool)

	ctx := context.Background()
	ctx = appcontext.WithUserContext(ctx, "@user:example.com", "!room:example.com")

	result, err := mgr.CallTool(ctx, "mock", "echo", map[string]any{"message": "hello"})
	if err != nil {
		t.Errorf("CallTool() error = %v", err)
	}
	if result == nil {
		t.Error("CallTool() returned nil result")
	}
}

func TestMockManager_CallTool_Disabled(t *testing.T) {
	mgr := NewMockManager(false)
	ctx := context.Background()
	ctx = appcontext.WithUserContext(ctx, "@user:example.com", "!room:example.com")

	_, err := mgr.CallTool(ctx, "mock", "echo", nil)
	if err == nil {
		t.Error("CallTool on disabled manager should return error")
	}
}

func TestMockMCPServer_RegisterTool(t *testing.T) {
	server := NewMockMCPServer()

	if server.ToolCount() != 0 {
		t.Errorf("New server should have 0 tools, got %d", server.ToolCount())
	}

	server.RegisterTool(TestFixtures.EchoTool)
	if server.ToolCount() != 1 {
		t.Errorf("After registering one tool, count should be 1, got %d", server.ToolCount())
	}

	tool := server.GetTool("echo")
	if tool.Name != "echo" {
		t.Errorf("Tool name = %q, want %q", tool.Name, "echo")
	}
}

func TestMockMCPServer_UnregisterTool(t *testing.T) {
	server := NewMockMCPServer()
	server.RegisterTool(TestFixtures.EchoTool)

	server.UnregisterTool("echo")
	if server.ToolCount() != 0 {
		t.Errorf("After unregistering, count should be 0, got %d", server.ToolCount())
	}

	tool := server.GetTool("echo")
	if tool != nil {
		t.Error("GetTool after unregister should return nil")
	}
}

func TestMockMCPServer_ListTools(t *testing.T) {
	server := NewMockMCPServer()
	server.RegisterTool(TestFixtures.EchoTool)
	server.RegisterTool(TestFixtures.ErrorTool)

	tools := server.ListTools()
	if len(tools) != 2 {
		t.Errorf("ListTools() returned %d tools, want 2", len(tools))
	}
}

func TestMockMCPServer_Clear(t *testing.T) {
	server := NewMockMCPServer()
	server.RegisterTool(TestFixtures.EchoTool)
	server.RegisterTool(TestFixtures.ErrorTool)

	server.Clear()
	if server.ToolCount() != 0 {
		t.Errorf("After Clear(), count should be 0, got %d", server.ToolCount())
	}
}

func TestNewTestMCPServerWithFixtures(t *testing.T) {
	server := NewTestMCPServerWithFixtures()
	if server.ToolCount() != 4 {
		t.Errorf("Server with default fixtures should have 4 tools, got %d", server.ToolCount())
	}

	server2 := NewTestMCPServerWithFixtures(TestFixtures.EchoTool)
	if server2.ToolCount() != 1 {
		t.Errorf("Server with one fixture should have 1 tool, got %d", server2.ToolCount())
	}
}

func TestNewTestUserContext(t *testing.T) {
	ctx := NewTestUserContext(1, 2)

	userID, ok := appcontext.GetUserFromContext(ctx)
	if !ok {
		t.Error("GetUserFromContext should return ok=true")
	}
	expectedUser := id.UserID("@user1:example.com")
	if userID != expectedUser {
		t.Errorf("userID = %q, want %q", userID, expectedUser)
	}

	roomID, ok := appcontext.GetRoomFromContext(ctx)
	if !ok {
		t.Error("GetRoomFromContext should return ok=true")
	}
	expectedRoom := id.RoomID("!room2:example.com")
	if roomID != expectedRoom {
		t.Errorf("roomID = %q, want %q", roomID, expectedRoom)
	}
}

func TestNewTestMCPServerConfig(t *testing.T) {
	cfg := NewTestMCPServerConfig(true)
	if !cfg.Enabled {
		t.Error("Config should be enabled")
	}
	if cfg.Servers == nil {
		t.Error("Servers map should be initialized")
	}

	cfg2 := NewTestMCPServerConfig(false)
	if cfg2.Enabled {
		t.Error("Config should be disabled")
	}
}
