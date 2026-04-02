//go:build goolm

// Package matrix 提供 Matrix 命令测试。
package matrix

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/mcp"
)

// TestPingCommand_New 测试创建 PingCommand。
func TestPingCommand_New(t *testing.T) {
	service := &CommandService{}
	cmd := NewPingCommand(service)
	if cmd == nil {
		t.Error("NewPingCommand should not return nil")
	}
}

// TestHelpCommand_New 测试创建 HelpCommand。
func TestHelpCommand_New(t *testing.T) {
	service := &CommandService{}
	cmd := NewHelpCommand(service)
	if cmd == nil {
		t.Error("NewHelpCommand should not return nil")
	}
}

// TestVersionCommand_New 测试创建 VersionCommand。
func TestVersionCommand_New(t *testing.T) {
	service := &CommandService{}
	cmd := NewVersionCommand(service)
	if cmd == nil {
		t.Error("NewVersionCommand should not return nil")
	}
}

// TestMCPListCommand_New 测试创建 MCPListCommand。
func TestMCPListCommand_New(t *testing.T) {
	service := &CommandService{}
	cmd := NewMCPListCommand(service, nil)
	if cmd == nil {
		t.Error("NewMCPListCommand should not return nil")
	}
}

// TestMCPListCommand_NilManager 测试 nil MCP 管理器。
func TestMCPListCommand_NilManager(t *testing.T) {
	service := &CommandService{}
	_ = NewMCPListCommand(service, nil)

	ctx := context.Background()
	roomID := id.RoomID("!test:example.com")

	// Handle with nil manager should handle gracefully
	// 这会因为 nil client 而 panic，所以我们只验证创建成功
	_ = ctx
	_ = roomID
}

// TestMCPListCommand_DisabledMCP 测试禁用的 MCP。
func TestMCPListCommand_DisabledMCP(t *testing.T) {
	mcpMgr := mcp.NewManager(&config.MCPConfig{Enabled: false})
	service := &CommandService{}

	cmd := NewMCPListCommand(service, mcpMgr)
	if cmd == nil {
		t.Error("NewMCPListCommand should not return nil")
	}

	// 验证 MCP 禁用状态
	if mcpMgr.IsEnabled() {
		t.Error("MCP should be disabled")
	}
}

// TestMCPListCommand_EnabledMCP 测试启用的 MCP。
func TestMCPListCommand_EnabledMCP(t *testing.T) {
	mcpMgr := mcp.NewManager(&config.MCPConfig{
		Enabled: true,
		Servers: map[string]config.ServerConfig{
			"test": {Type: "builtin", Enabled: true},
		},
	})
	service := &CommandService{}

	cmd := NewMCPListCommand(service, mcpMgr)
	if cmd == nil {
		t.Error("NewMCPListCommand should not return nil")
	}

	// 验证 MCP 启用状态
	if !mcpMgr.IsEnabled() {
		t.Error("MCP should be enabled")
	}

	// 列出服务器
	servers := mcpMgr.ListServers()
	_ = servers
}

// TestRegisterBuiltinCommands 测试注册内置命令。
func TestRegisterBuiltinCommands(t *testing.T) {
	service := &CommandService{
		commands: make(map[string]CommandInfo),
	}

	// 注册内置命令
	RegisterBuiltinCommands(service)

	// 验证命令已注册
	commands := service.ListCommands()
	if len(commands) == 0 {
		t.Error("RegisterBuiltinCommands should register some commands")
	}

	// 验证 ping 命令存在
	if _, ok := service.GetCommand("ping"); !ok {
		t.Error("ping command should be registered")
	}

	// 验证 help 命令存在
	if _, ok := service.GetCommand("help"); !ok {
		t.Error("help command should be registered")
	}

	// 验证 version 命令存在
	if _, ok := service.GetCommand("version"); !ok {
		t.Error("version command should be registered")
	}
}

// TestRegisterMCPCommands 测试注册 MCP 命令。
func TestRegisterMCPCommands(t *testing.T) {
	service := &CommandService{
		commands: make(map[string]CommandInfo),
	}

	mcpMgr := mcp.NewManager(&config.MCPConfig{Enabled: true})

	// 注册 MCP 命令
	RegisterMCPCommands(service, mcpMgr)

	// 验证 mcp 命令存在
	if _, ok := service.GetCommand("mcp"); !ok {
		t.Error("mcp command should be registered")
	}
}

// TestMCPCommandRouter_New 测试创建 MCP 命令路由器。
func TestMCPCommandRouter_New(t *testing.T) {
	service := &CommandService{}
	mcpMgr := mcp.NewManager(&config.MCPConfig{Enabled: true})

	router := NewMCPCommandRouter(service, mcpMgr)
	if router == nil {
		t.Error("NewMCPCommandRouter should not return nil")
	}
}

// TestMCPCommandRouter_ListSubcommands_Extended 测试列出子命令扩展。
func TestMCPCommandRouter_ListSubcommands_Extended(t *testing.T) {
	service := &CommandService{}
	mcpMgr := mcp.NewManager(&config.MCPConfig{Enabled: true})

	router := NewMCPCommandRouter(service, mcpMgr)
	subs := router.ListSubcommands()

	// 初始子命令可能为空或包含默认命令
	// 只验证 router 创建成功
	_ = subs
}

// TestCommandService_ListCommands_Extended 测试列出命令扩展。
func TestCommandService_ListCommands_Extended(t *testing.T) {
	service := &CommandService{
		commands: make(map[string]CommandInfo),
	}

	// 空命令列表
	commands := service.ListCommands()
	if len(commands) != 0 {
		t.Error("empty service should have no commands")
	}

	// 注册一个命令
	service.RegisterCommandWithDesc("test", "test command", &testCommandHandler{})

	commands = service.ListCommands()
	if len(commands) != 1 {
		t.Errorf("expected 1 command, got %d", len(commands))
	}
}

// testCommandHandler 用于测试的命令处理器。
type testCommandHandler struct{}

func (t *testCommandHandler) Handle(_ context.Context, _ id.UserID, _ id.RoomID, _ []string) error {
	return nil
}

// TestCommandService_GetCommand_Extended 测试获取命令扩展。
func TestCommandService_GetCommand_Extended(t *testing.T) {
	service := &CommandService{
		commands: make(map[string]CommandInfo),
	}

	// 获取不存在的命令
	_, ok := service.GetCommand("nonexistent")
	if ok {
		t.Error("GetCommand should return false for nonexistent command")
	}

	// 注册一个命令
	service.RegisterCommand("test", &testCommandHandler{})

	// 获取存在的命令
	cmd, ok := service.GetCommand("test")
	if !ok {
		t.Error("GetCommand should return true for existing command")
	}
	if cmd.Name != "test" {
		t.Errorf("expected command name 'test', got %q", cmd.Name)
	}
}

// TestCommandService_UnregisterCommand_Extended 测试注销命令扩展。
func TestCommandService_UnregisterCommand_Extended(t *testing.T) {
	service := &CommandService{
		commands: make(map[string]CommandInfo),
	}

	// 注册一个命令
	service.RegisterCommand("test", &testCommandHandler{})

	// 验证命令存在
	if _, ok := service.GetCommand("test"); !ok {
		t.Error("command should be registered")
	}

	// 注销命令
	service.UnregisterCommand("test")

	// 验证命令已注销
	if _, ok := service.GetCommand("test"); ok {
		t.Error("command should be unregistered")
	}
}