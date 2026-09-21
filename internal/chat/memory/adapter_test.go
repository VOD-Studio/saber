package memory_test

import (
	"context"
	"testing"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/chat/memory"
)

func TestAdapter_SourceAndEditIsolation(t *testing.T) {
	adapter := memory.New("account", chat.Capabilities{Edit: true}, func(_ context.Context, m chat.Message, _ chat.Adapter) (agent.Result, error) {
		if m.Session.Platform != "memory" || m.Session.Account != "account" {
			t.Fatal("untrusted source accepted")
		}
		return agent.Result{Status: agent.Completed}, nil
	})
	if _, err := adapter.Receive(context.Background(), chat.Message{Session: chat.Session{Platform: "forged", Account: "other", Conversation: "room"}}); err != nil {
		t.Fatal(err)
	}
	session := chat.Session{Platform: "memory", Account: "account", Conversation: "room"}
	id, err := adapter.Send(context.Background(), chat.Reply{Session: session, Text: "old"})
	if err != nil {
		t.Fatal(err)
	}
	other := session
	other.Conversation = "other"
	if err := adapter.Edit(context.Background(), id, chat.Reply{Session: other, Text: "bad"}); err == nil {
		t.Fatal("cross-session edit accepted")
	}
	other = session
	other.Account = "other"
	if _, err := adapter.Send(context.Background(), chat.Reply{Session: other}); err == nil {
		t.Fatal("cross-account send accepted")
	}
	if err := adapter.Edit(context.Background(), id, chat.Reply{Session: session, Text: "new"}); err != nil {
		t.Fatal(err)
	}
	snapshot := adapter.Replies()
	snapshot[0].Text = "mutated"
	if adapter.Replies()[0].Text != "new" {
		t.Fatal("snapshot aliases adapter state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Send(ctx, chat.Reply{Session: session}); err == nil {
		t.Fatal("send after cancellation")
	}
	if err := adapter.SetTyping(context.Background(), session, true); err != nil {
		t.Fatal(err)
	}
	if !adapter.IsTyping(session) {
		t.Fatal("typing missing")
	}
	if err := adapter.SetTyping(context.Background(), session, false); err != nil {
		t.Fatal(err)
	}
	if adapter.IsTyping(session) {
		t.Fatal("typing not cleared")
	}
}
