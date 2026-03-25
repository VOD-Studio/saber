package matrix

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"
)

// TestMCPCommandRouter_RegisterSubcommand 测试子命令注册。
func TestMCPCommandRouter_RegisterSubcommand(t *testing.T) {
	router := NewMCPCommandRouter(nil, nil)

	handler := &mockMCPHandler{}
	router.RegisterSubcommand("test", handler)

	if len(router.subcommands) != 1 {
		t.Errorf("期望子命令数量为 1，实际为 %d", len(router.subcommands))
	}
}

// TestMCPCommandRouter_ListSubcommands 测试列出子命令。
func TestMCPCommandRouter_ListSubcommands(t *testing.T) {
	router := NewMCPCommandRouter(nil, nil)

	// 空路由器
	if len(router.ListSubcommands()) != 0 {
		t.Error("空路由器应该没有子命令")
	}

	// 注册子命令
	router.RegisterSubcommand("list", &mockMCPHandler{})

	list := router.ListSubcommands()
	if len(list) != 1 {
		t.Errorf("期望 1 个子命令，实际为 %d", len(list))
	}
}

// TestMCPCommandRouter_Handle_CaseInsensitive 测试子命令大小写不敏感。
func TestMCPCommandRouter_Handle_CaseInsensitive(t *testing.T) {
	router := NewMCPCommandRouter(nil, nil)

	handler := &mockMCPHandler{}
	router.RegisterSubcommand("LIST", handler) // 注册时使用大写

	// 查找时使用小写
	if router.subcommands["list"] == nil {
		t.Error("子命令应该大小写不敏感")
	}
}

// mockMCPHandler 是用于测试的模拟命令处理器。
type mockMCPHandler struct {
	called   bool
	callArgs []string
	err      error
}

func (m *mockMCPHandler) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	m.called = true
	m.callArgs = args
	return m.err
}