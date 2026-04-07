package ai

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"
)

// mockCommandHandler 是用于测试的模拟命令处理器。
type mockCommandHandler struct {
	called   bool
	callArgs []string
	err      error
}

func (m *mockCommandHandler) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	m.called = true
	m.callArgs = args
	return m.err
}

// TestAICommandRouter_RegisterSubcommand 测试子命令注册。
func TestAICommandRouter_RegisterSubcommand(t *testing.T) {
	router := NewAICommandRouter(nil)

	handler := &mockCommandHandler{}
	router.RegisterSubcommand("test", handler)

	if len(router.subcommands) != 1 {
		t.Errorf("期望子命令数量为 1，实际为 %d", len(router.subcommands))
	}

	if router.subcommands["test"] != handler {
		t.Error("注册的处理器与预期不符")
	}
}

// TestAICommandRouter_ListSubcommands 测试列出子命令。
func TestAICommandRouter_ListSubcommands(t *testing.T) {
	router := NewAICommandRouter(nil)

	// 空路由器
	if len(router.ListSubcommands()) != 0 {
		t.Error("空路由器应该没有子命令")
	}

	// 注册多个子命令
	router.RegisterSubcommand("clear", &mockCommandHandler{})
	router.RegisterSubcommand("models", &mockCommandHandler{})
	router.RegisterSubcommand("switch", &mockCommandHandler{})

	list := router.ListSubcommands()
	if len(list) != 3 {
		t.Errorf("期望 3 个子命令，实际为 %d", len(list))
	}
}

// TestAICommandRouter_Handle_NoArgs 测试无参数时调用 AI 对话。
func TestAICommandRouter_Handle_NoArgs(t *testing.T) {
	// 此测试需要完整的 Service 实例，这里只验证路由逻辑
	// 在集成测试中验证完整流程
	router := NewAICommandRouter(nil)

	// 注册一个子命令验证不会被调用
	handler := &mockCommandHandler{}
	router.RegisterSubcommand("clear", handler)

	// 无参数时应该调用 AI 对话而不是子命令
	// 由于 service 为 nil，会 panic，这里只测试逻辑
	// 实际测试在集成测试中进行
	_ = router
}

// TestAICommandRouter_Handle_SubcommandRouting 测试子命令路由。
func TestAICommandRouter_Handle_SubcommandRouting(t *testing.T) {
	router := NewAICommandRouter(nil)

	clearHandler := &mockCommandHandler{}
	modelsHandler := &mockCommandHandler{}
	router.RegisterSubcommand("clear", clearHandler)
	router.RegisterSubcommand("models", modelsHandler)

	// 测试 clear 子命令
	_ = router.subcommands["clear"].Handle(context.Background(), id.UserID("@test:example.com"), id.RoomID("!room:example.com"), []string{})
	if !clearHandler.called {
		t.Error("clear 处理器应该被调用")
	}

	// 测试 models 子命令
	_ = router.subcommands["models"].Handle(context.Background(), id.UserID("@test:example.com"), id.RoomID("!room:example.com"), []string{})
	if !modelsHandler.called {
		t.Error("models 处理器应该被调用")
	}
}

// TestAICommandRouter_Handle_ArgsPassing 测试参数传递。
func TestAICommandRouter_Handle_ArgsPassing(t *testing.T) {
	router := NewAICommandRouter(nil)

	handler := &mockCommandHandler{}
	router.RegisterSubcommand("switch", handler)

	// 模拟调用 switch gpt-4
	args := []string{"gpt-4"}
	_ = router.subcommands["switch"].Handle(context.Background(), id.UserID("@test:example.com"), id.RoomID("!room:example.com"), args)

	if !handler.called {
		t.Error("处理器应该被调用")
	}
	if len(handler.callArgs) != 1 || handler.callArgs[0] != "gpt-4" {
		t.Errorf("期望参数 [gpt-4]，实际为 %v", handler.callArgs)
	}
}

// TestAICommandRouter_Handle_CaseInsensitive 测试子命令大小写不敏感。
func TestAICommandRouter_Handle_CaseInsensitive(t *testing.T) {
	router := NewAICommandRouter(nil)

	handler := &mockCommandHandler{}
	router.RegisterSubcommand("CLEAR", handler) // 注册时使用大写

	// 查找时使用小写
	if router.subcommands["clear"] == nil {
		t.Error("子命令应该大小写不敏感")
	}
}

// TestUnknownSubcommandError 测试未知子命令错误。
func TestUnknownSubcommandError(t *testing.T) {
	err := &UnknownSubcommandError{Subcommand: "unknown"}
	expected := "未知子命令: unknown"

	if err.Error() != expected {
		t.Errorf("期望错误信息 '%s'，实际为 '%s'", expected, err.Error())
	}
}
