//go:build goolm

package mcp

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rua.plus/saber/internal/config"
)

func TestMCPServerFactory_Interface(t *testing.T) {
	// 验证所有工厂都实现了 MCPServerFactory 接口
	var _ MCPServerFactory = NewBuiltinFactory(nil)
	var _ MCPServerFactory = NewStdioFactory()
	var _ MCPServerFactory = NewHTTPFactory()
}

func TestBuiltinFactory_Type(t *testing.T) {
	factory := NewBuiltinFactory(nil)
	if factory.Type() != ServerTypeBuiltin {
		t.Errorf("BuiltinFactory.Type() = %q, want %q", factory.Type(), ServerTypeBuiltin)
	}
}

func TestStdioFactory_Type(t *testing.T) {
	factory := NewStdioFactory()
	if factory.Type() != ServerTypeStdio {
		t.Errorf("StdioFactory.Type() = %q, want %q", factory.Type(), ServerTypeStdio)
	}
}

func TestHTTPFactory_Type(t *testing.T) {
	factory := NewHTTPFactory()
	if factory.Type() != ServerTypeHTTP {
		t.Errorf("HTTPFactory.Type() = %q, want %q", factory.Type(), ServerTypeHTTP)
	}
}

func TestBuiltinFactory_Create_InvalidName(t *testing.T) {
	factory := NewBuiltinFactory(nil)
	ctx := context.Background()

	_, _, err := factory.Create(ctx, "invalid_server", nil)
	if err == nil {
		t.Error("Create with invalid server name should return error")
	}
}

func TestBuiltinFactory_Create_ValidName(t *testing.T) {
	factory := NewBuiltinFactory(&config.BuiltinConfig{})
	ctx := context.Background()

	client, session, err := factory.Create(ctx, "web_fetch", nil)
	if err != nil {
		t.Fatalf("Create web_fetch failed: %v", err)
	}
	if client == nil {
		t.Error("Client should not be nil")
	}
	if session == nil {
		t.Error("Session should not be nil")
	}

	// 清理
	_ = session.Close()
}

func TestStdioFactory_Create_MissingCommand(t *testing.T) {
	factory := NewStdioFactory()
	ctx := context.Background()

	cfg := &config.ServerConfig{
		Type:    ServerTypeStdio,
		Command: "", // 缺少命令
	}

	_, _, err := factory.Create(ctx, "test", cfg)
	if err == nil {
		t.Error("Create with missing command should return error")
	}
}

func TestStdioFactory_Create_CommandNotInWhitelist(t *testing.T) {
	factory := NewStdioFactory()
	ctx := context.Background()

	cfg := &config.ServerConfig{
		Type:            ServerTypeStdio,
		Command:         "/bin/echo",
		AllowedCommands: []string{"/bin/ls"}, // 白名单不包含 /bin/echo
	}

	_, _, err := factory.Create(ctx, "test", cfg)
	if err == nil {
		t.Error("Create with command not in whitelist should return error")
	}
}

func TestHTTPFactory_Create_MissingURL(t *testing.T) {
	factory := NewHTTPFactory()
	ctx := context.Background()

	cfg := &config.ServerConfig{
		Type:  ServerTypeHTTP,
		URL:   "", // 缺少 URL
		Token: "test-token",
	}

	_, _, err := factory.Create(ctx, "test", cfg)
	if err == nil {
		t.Error("Create with missing URL should return error")
	}
}

func TestHTTPFactory_Create_MissingToken(t *testing.T) {
	factory := NewHTTPFactory()
	ctx := context.Background()

	cfg := &config.ServerConfig{
		Type:  ServerTypeHTTP,
		URL:   "https://example.com/api",
		Token: "", // 缺少 Token
	}

	_, _, err := factory.Create(ctx, "test", cfg)
	if err == nil {
		t.Error("Create with missing token should return error")
	}
}

func TestDefaultFactories(t *testing.T) {
	factories := DefaultFactories(nil)
	if len(factories) != 3 {
		t.Errorf("DefaultFactories returned %d factories, want 3", len(factories))
	}

	if _, ok := factories[ServerTypeBuiltin]; !ok {
		t.Error("DefaultFactories missing builtin factory")
	}
	if _, ok := factories[ServerTypeStdio]; !ok {
		t.Error("DefaultFactories missing stdio factory")
	}
	if _, ok := factories[ServerTypeHTTP]; !ok {
		t.Error("DefaultFactories missing http factory")
	}
}

func TestDefaultFactories_WithConfig(t *testing.T) {
	cfg := &config.MCPConfig{
		Builtin: config.BuiltinConfig{
			WebSearch: config.WebSearchConfig{
				MaxResults: 10,
			},
		},
	}

	factories := DefaultFactories(cfg)
	if len(factories) != 3 {
		t.Errorf("DefaultFactories returned %d factories, want 3", len(factories))
	}

	builtinFactory, ok := factories[ServerTypeBuiltin].(*BuiltinFactory)
	if !ok {
		t.Fatal("Builtin factory has wrong type")
	}
	if builtinFactory.builtinCfg == nil {
		t.Error("BuiltinFactory should have non-nil config")
	}
}

// MockFactory 用于测试的自定义工厂。
type MockFactory struct {
	createErr error
}

// Create 实现 MCPServerFactory 接口。
func (f *MockFactory) Create(_ context.Context, _ string, _ *config.ServerConfig) (*mcp.Client, *mcp.ClientSession, error) {
	if f.createErr != nil {
		return nil, nil, f.createErr
	}
	return nil, nil, nil
}

// Type 返回工厂类型。
func (f *MockFactory) Type() string {
	return "mock"
}

// MockBuiltinFactory 用于测试覆盖默认 builtin 工厂。
type MockBuiltinFactory struct{}

// Create 实现 MCPServerFactory 接口。
func (f *MockBuiltinFactory) Create(_ context.Context, _ string, _ *config.ServerConfig) (*mcp.Client, *mcp.ClientSession, error) {
	return nil, nil, nil
}

// Type 返回 "builtin" 以覆盖默认工厂。
func (f *MockBuiltinFactory) Type() string {
	return ServerTypeBuiltin
}

func TestMockFactory_ImplementsInterface(t *testing.T) {
	var _ MCPServerFactory = &MockFactory{}
}

func TestManager_RegisterFactory(t *testing.T) {
	mgr := NewManager(nil)
	mockFactory := &MockFactory{}

	// 注册自定义工厂
	mgr.RegisterFactory(mockFactory)

	// 验证工厂已注册
	if mgr.factories == nil {
		t.Fatal("factories map should be initialized")
	}
	if _, ok := mgr.factories["mock"]; !ok {
		t.Error("mock factory should be registered")
	}
}

func TestManager_RegisterFactory_OverrideDefault(t *testing.T) {
	mgr := NewManager(nil)

	// 创建一个自定义的 builtin 工厂来测试覆盖
	customBuiltin := &MockBuiltinFactory{}
	mgr.RegisterFactory(customBuiltin)

	// 检查覆盖后的工厂
	registered := mgr.factories[ServerTypeBuiltin]
	if registered == nil {
		t.Fatal("builtin factory should not be nil")
	}
	// 验证类型是我们自定义的工厂
	if _, ok := registered.(*MockBuiltinFactory); !ok {
		t.Error("custom factory should override default builtin factory")
	}
}

func TestManager_Init_WithFactory(t *testing.T) {
	cfg := &config.MCPConfig{
		Enabled: true,
		Servers: map[string]config.ServerConfig{
			"web_fetch": {
				Type:    ServerTypeBuiltin,
				Enabled: true,
			},
		},
	}

	mgr := NewManager(cfg)
	ctx := context.Background()

	err := mgr.Init(ctx)
	if err != nil {
		t.Errorf("Init failed: %v", err)
	}

	// 验证服务器已连接
	session := mgr.GetSession("web_fetch")
	if session == nil {
		t.Error("web_fetch session should not be nil after Init")
	}

	// 清理
	_ = mgr.Close()
}

func TestManager_Init_UnknownServerType(t *testing.T) {
	cfg := &config.MCPConfig{
		Enabled: true,
		Servers: map[string]config.ServerConfig{
			"unknown": {
				Type:    "unknown_type",
				Enabled: true,
			},
		},
	}

	mgr := NewManager(cfg)
	ctx := context.Background()

	// Init 应该不会因为未知类型而 panic
	err := mgr.Init(ctx)
	if err != nil {
		t.Errorf("Init should not return error for unknown type, got: %v", err)
	}

	// 未知类型的服务器不应被连接
	session := mgr.GetSession("unknown")
	if session != nil {
		t.Error("unknown type server should not be connected")
	}
}
