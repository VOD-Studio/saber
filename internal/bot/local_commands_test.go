package bot

import (
	"context"
	"testing"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/command"
)

// TestLocalCommands_Dispatch 验证本机指令复用共享命令语义并捕获回执文本。
func TestLocalCommands_Dispatch(t *testing.T) {
	registry := command.New()
	if err := registry.Register(command.Definition{
		Descriptor: command.Descriptor{ID: "ping", Path: []string{"ping"}, Description: "在线检查", Scope: "conversation"},
		Handle: func(_ context.Context, message chat.Message, adapter chat.Adapter, _ string) error {
			_, err := adapter.Send(context.Background(), chat.Reply{Session: message.Session, ReplyTo: message.ID, Text: "🏓 Pong!"})
			return err
		},
	}); err != nil {
		t.Fatalf("注册命令失败: %v", err)
	}
	local := localCommands{registry: registry, capabilities: func(string) map[string]bool { return map[string]bool{} }}
	session := chat.Session{Platform: "terminal", Account: "saber", Conversation: "demo"}

	reply, handled, err := local.Dispatch(context.Background(), session, "501", "c1", "/ping")
	if err != nil || !handled {
		t.Fatalf("Dispatch() handled=%t err=%v", handled, err)
	}
	if reply != "🏓 Pong!" {
		t.Fatalf("Dispatch() reply = %q", reply)
	}

	// 非命令正文不处理，交由普通消息链路。
	if _, handled, err = local.Dispatch(context.Background(), session, "501", "c2", "普通消息"); err != nil || handled {
		t.Fatalf("非命令 Dispatch() handled=%t err=%v", handled, err)
	}

	// 未装配注册表时明确不处理，避免伪装成功。
	if _, handled, err = (localCommands{}).Dispatch(context.Background(), session, "501", "c3", "/ping"); err != nil || handled {
		t.Fatalf("空注册表 Dispatch() handled=%t err=%v", handled, err)
	}
}
