package persona

import (
	"context"
	"testing"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/chat/memory"
)

func TestChatCommand_QuotedCreateAndSessionBinding(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	adapter := memory.New("bot", chat.Capabilities{}, nil)
	message := chat.Message{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "room"}, ID: "source", SenderID: "user", Text: "/persona"}
	if err := svc.ChatCommand(ctx, message, adapter, "new", `robot "机器人" "你是一个友好的机器人" "测试人格"`); err != nil {
		t.Fatal(err)
	}
	if err := svc.ChatCommand(ctx, message, adapter, "set", "robot"); err != nil {
		t.Fatal(err)
	}
	if p := svc.GetSessionPersona(message.Session); p == nil || p.Prompt != "你是一个友好的机器人" {
		t.Fatalf("会话人格 = %+v", p)
	}
	if err := svc.ChatCommand(ctx, message, adapter, "clear", ""); err != nil {
		t.Fatal(err)
	}
	if svc.GetSessionPersona(message.Session) != nil {
		t.Fatal("清除后人格仍在")
	}
	if len(adapter.Replies()) != 3 {
		t.Fatalf("回执数量 = %d", len(adapter.Replies()))
	}
}
