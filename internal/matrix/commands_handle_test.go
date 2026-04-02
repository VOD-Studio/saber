//go:build goolm

package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/mcp"
)

// TestPingCommand_Handle 测试 PingCommand.Handle 方法。
func TestPingCommand_Handle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"event_id":"$ping_event:example.com"}`))
	}))
	defer server.Close()

	client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
	service := NewCommandService(client, id.UserID("@bot:example.com"), nil)

	cmd := NewPingCommand(service)
	err := cmd.Handle(context.Background(), id.UserID("@user:example.com"), id.RoomID("!room:example.com"), nil)
	if err != nil {
		t.Errorf("PingCommand.Handle() error = %v", err)
	}
}

// TestHelpCommand_Handle 测试 HelpCommand.Handle 方法。
func TestHelpCommand_Handle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"event_id":"$help_event:example.com"}`))
	}))
	defer server.Close()

	client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
	service := NewCommandService(client, id.UserID("@bot:example.com"), nil)

	// 注册一些命令用于显示帮助
	service.RegisterCommandWithDesc("ping", "检查机器人是否在线", &mockCommandHandler{})
	service.RegisterCommandWithDesc("help", "列出可用命令", &mockCommandHandler{})

	cmd := NewHelpCommand(service)
	err := cmd.Handle(context.Background(), id.UserID("@user:example.com"), id.RoomID("!room:example.com"), nil)
	if err != nil {
		t.Errorf("HelpCommand.Handle() error = %v", err)
	}
}

// TestHelpCommand_Handle_NoCommands 测试没有命令时的帮助。
func TestHelpCommand_Handle_NoCommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"event_id":"$help_event:example.com"}`))
	}))
	defer server.Close()

	client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
	service := NewCommandService(client, id.UserID("@bot:example.com"), nil)

	cmd := NewHelpCommand(service)
	err := cmd.Handle(context.Background(), id.UserID("@user:example.com"), id.RoomID("!room:example.com"), nil)
	if err != nil {
		t.Errorf("HelpCommand.Handle() error = %v", err)
	}
}

// TestVersionCommand_Handle 测试 VersionCommand.Handle 方法。
func TestVersionCommand_Handle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"event_id":"$version_event:example.com"}`))
	}))
	defer server.Close()

	client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
	buildInfo := &BuildInfo{
		Version:       "1.0.0",
		GitCommit:     "abc123",
		GitBranch:     "main",
		BuildTime:     "2024-01-01",
		GoVersion:     "go1.21.0",
		BuildPlatform: "linux/amd64",
	}
	service := NewCommandService(client, id.UserID("@bot:example.com"), buildInfo)

	cmd := NewVersionCommand(service)
	err := cmd.Handle(context.Background(), id.UserID("@user:example.com"), id.RoomID("!room:example.com"), nil)
	if err != nil {
		t.Errorf("VersionCommand.Handle() error = %v", err)
	}
}

// TestVersionCommand_Handle_NoBuildInfo 测试没有构建信息时的版本命令。
func TestVersionCommand_Handle_NoBuildInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"event_id":"$version_event:example.com"}`))
	}))
	defer server.Close()

	client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
	service := NewCommandService(client, id.UserID("@bot:example.com"), nil)

	cmd := NewVersionCommand(service)
	err := cmd.Handle(context.Background(), id.UserID("@user:example.com"), id.RoomID("!room:example.com"), nil)
	if err != nil {
		t.Errorf("VersionCommand.Handle() error = %v", err)
	}
}

// TestMCPListCommand_Handle 测试 MCPListCommand.Handle 方法。
func TestMCPListCommand_Handle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"event_id":"$mcp_event:example.com"}`))
	}))
	defer server.Close()

	client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
	service := NewCommandService(client, id.UserID("@bot:example.com"), nil)

	// 测试 MCP 未启用
	t.Run("mcp_disabled", func(t *testing.T) {
		cmd := NewMCPListCommand(service, nil)
		err := cmd.Handle(context.Background(), id.UserID("@user:example.com"), id.RoomID("!room:example.com"), nil)
		if err != nil {
			t.Errorf("MCPListCommand.Handle() error = %v", err)
		}
	})

	// 测试 MCP 管理器存在但未启用
	t.Run("mcp_manager_not_enabled", func(t *testing.T) {
		mcpMgr := mcp.NewManager(nil)
		cmd := NewMCPListCommand(service, mcpMgr)
		err := cmd.Handle(context.Background(), id.UserID("@user:example.com"), id.RoomID("!room:example.com"), nil)
		if err != nil {
			t.Errorf("MCPListCommand.Handle() error = %v", err)
		}
	})
}

// TestSendText 测试 SendText 方法。
func TestSendText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"event_id":"$send_event:example.com"}`))
	}))
	defer server.Close()

	client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
	service := NewCommandService(client, id.UserID("@bot:example.com"), nil)

	// 测试发送普通文本消息
	err := service.SendText(context.Background(), id.RoomID("!room:example.com"), "Hello, World!")
	if err != nil {
		t.Errorf("SendText() error = %v", err)
	}
}

// TestSendText_WithEventID 测试带 EventID 的 SendText 方法。
func TestSendText_WithEventID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"event_id":"$reply_event:example.com"}`))
	}))
	defer server.Close()

	client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
	service := NewCommandService(client, id.UserID("@bot:example.com"), nil)

	// 测试带 EventID 的发送（会使用回复）
	ctx := WithEventID(context.Background(), id.EventID("$original:example.com"))
	err := service.SendText(ctx, id.RoomID("!room:example.com"), "Reply message")
	if err != nil {
		t.Errorf("SendText() with EventID error = %v", err)
	}
}

// TestSendFormattedText 测试 SendFormattedText 方法。
func TestSendFormattedText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"event_id":"$formatted_event:example.com"}`))
	}))
	defer server.Close()

	client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
	service := NewCommandService(client, id.UserID("@bot:example.com"), nil)

	html := "<strong>Bold text</strong>"
	plain := "Bold text"
	err := service.SendFormattedText(context.Background(), id.RoomID("!room:example.com"), html, plain)
	if err != nil {
		t.Errorf("SendFormattedText() error = %v", err)
	}
}

// TestMCPCommandRouter_Handle 测试 MCPCommandRouter.Handle 方法。
func TestMCPCommandRouter_Handle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"event_id":"$mcp_router_event:example.com"}`))
	}))
	defer server.Close()

	client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
	service := NewCommandService(client, id.UserID("@bot:example.com"), nil)
	mcpMgr := mcp.NewManager(nil)

	router := NewMCPCommandRouter(service, mcpMgr)
	router.RegisterSubcommand("list", NewMCPListCommand(service, mcpMgr))

	// 测试 list 子命令
	err := router.Handle(context.Background(), id.UserID("@user:example.com"), id.RoomID("!room:example.com"), []string{"list"})
	if err != nil {
		t.Errorf("MCPCommandRouter.Handle() error = %v", err)
	}
}

// TestMCPCommandRouter_Handle_NoSubcommand 测试没有子命令的情况。
func TestMCPCommandRouter_Handle_NoSubcommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"event_id":"$mcp_router_event:example.com"}`))
	}))
	defer server.Close()

	client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
	service := NewCommandService(client, id.UserID("@bot:example.com"), nil)
	mcpMgr := mcp.NewManager(nil)

	router := NewMCPCommandRouter(service, mcpMgr)

	// 没有子命令，应该显示帮助
	err := router.Handle(context.Background(), id.UserID("@user:example.com"), id.RoomID("!room:example.com"), []string{})
	if err != nil {
		t.Errorf("MCPCommandRouter.Handle() error = %v", err)
	}
}

// TestMCPCommandRouter_Handle_UnknownSubcommand 测试未知子命令。
func TestMCPCommandRouter_Handle_UnknownSubcommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"event_id":"$mcp_router_event:example.com"}`))
	}))
	defer server.Close()

	client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
	service := NewCommandService(client, id.UserID("@bot:example.com"), nil)
	mcpMgr := mcp.NewManager(nil)

	router := NewMCPCommandRouter(service, mcpMgr)

	// 未知子命令
	err := router.Handle(context.Background(), id.UserID("@user:example.com"), id.RoomID("!room:example.com"), []string{"unknown"})
	if err != nil {
		t.Errorf("MCPCommandRouter.Handle() error = %v", err)
	}
}