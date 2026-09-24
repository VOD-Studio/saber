package command_test

import (
	"context"
	"testing"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/command"
)

func TestRegistry_DispatchAndDescribe(t *testing.T) {
	r := command.New()
	var calls []string
	register := func(def command.Definition) {
		t.Helper()
		if err := r.Register(def); err != nil {
			t.Fatal(err)
		}
	}
	register(command.Definition{Descriptor: command.Descriptor{ID: "ai.chat", Path: []string{"ai"}, Scope: "conversation"}, Handle: func(_ context.Context, _ chat.Message, _ chat.Adapter, raw string) error {
		calls = append(calls, "chat:"+raw)
		return nil
	}})
	register(command.Definition{Descriptor: command.Descriptor{ID: "ai.clear", Path: []string{"ai", "clear"}, Scope: "conversation"}, Aliases: [][]string{{"ai-clear"}}, Authorize: func(i chat.Identity) bool { return i.SenderID == "admin" }, Handle: func(_ context.Context, _ chat.Message, _ chat.Adapter, raw string) error {
		calls = append(calls, "clear:"+raw)
		return nil
	}})
	register(command.Definition{Descriptor: command.Descriptor{ID: "task.logs", Path: []string{"task", "logs"}, Scope: "conversation"}, Capability: "file", Handle: func(_ context.Context, _ chat.Message, _ chat.Adapter, _ string) error {
		calls = append(calls, "logs")
		return nil
	}})
	if got := len(r.Describe(nil)); got != 2 {
		t.Fatalf("无媒体能力的目录包含 %d 项", got)
	}
	if got := len(r.Describe(map[string]bool{"file": true})); got != 3 {
		t.Fatalf("有媒体能力的目录包含 %d 项", got)
	}
	adapter := memory.New("bot", chat.Capabilities{}, func(ctx context.Context, m chat.Message, reply chat.Adapter) (agent.Result, error) {
		handled, err := r.Dispatch(ctx, m, reply, nil)
		if !handled {
			calls = append(calls, "model:"+m.Text)
		}
		return agent.Result{}, err
	})
	for _, tc := range []struct {
		text, sender string
	}{
		{"!ai  hello\nworld", "user"},
		{"/ai -- clear", "user"},
		{"/ai-clear", "admin"},
		{"/ai clear", "user"},
		{"/task logs 1", "user"},
		{"/unknown", "user"},
		{"//ai clear", "user"},
		{"看一下 /ai clear", "user"},
	} {
		_, err := adapter.Receive(context.Background(), chat.Message{Session: chat.Session{Conversation: "room"}, ID: tc.text, SenderID: tc.sender, Text: tc.text})
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"chat: hello\nworld", "chat:-- clear", "clear:", "model://ai clear", "model:看一下 /ai clear"}
	if len(calls) != len(want) {
		t.Fatalf("调用 = %v，期望 %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("调用 %d = %q，期望 %q", i, calls[i], want[i])
		}
	}
	if replies := adapter.Replies(); len(replies) != 3 || replies[0].Text != "没有执行此命令的权限" || replies[1].Text != "该平台暂不支持此命令" {
		t.Fatalf("回执 = %+v", replies)
	}
}
