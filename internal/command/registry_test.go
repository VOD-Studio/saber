package command_test

import (
	"context"
	"errors"
	"strings"
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
		{"/ai\u00a0你好", "user"},
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
	want := []string{"chat: hello\nworld", "chat:你好", "chat:-- clear", "clear:", "model://ai clear", "model:看一下 /ai clear"}
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

type fakeReceipts struct{ state, body string }

type imageAdapter struct {
	*memory.Adapter
	images int
}

func (a *imageAdapter) SendImage(_ context.Context, _ chat.Reply, _ []byte, _, _ string, _, _ int) (string, error) {
	a.images++
	return "image-id", nil
}

func (f *fakeReceipts) ClaimCommand(_ context.Context, _ chat.Message, _ string) (string, string, error) {
	if f.state == "" {
		f.state = "running"
		return "new", "", nil
	}
	return f.state, f.body, nil
}

func (f *fakeReceipts) CompleteCommand(_ context.Context, _ chat.Message, _, body string) error {
	f.state, f.body = "done", body
	return nil
}

func TestRegistry_OnceReplaysReceiptWithoutRepeatingAction(t *testing.T) {
	r := command.New()
	receipts := &fakeReceipts{}
	r.SetReceipts(receipts)
	runs := 0
	err := r.Register(command.Definition{Descriptor: command.Descriptor{ID: "ai.switch", Path: []string{"ai", "switch"}, Scope: "global"}, Once: true,
		Handle: func(ctx context.Context, m chat.Message, a chat.Adapter, _ string) error {
			runs++
			_, err := a.Send(ctx, chat.Reply{Session: m.Session, ReplyTo: m.ID, Text: "已切换"})
			return err
		}})
	if err != nil {
		t.Fatal(err)
	}
	adapter := memory.New("bot", chat.Capabilities{}, func(ctx context.Context, m chat.Message, a chat.Adapter) (agent.Result, error) {
		_, err := r.Dispatch(ctx, m, a, nil)
		return agent.Result{}, err
	})
	message := chat.Message{Session: chat.Session{Conversation: "room"}, ID: "event", SenderID: "user", Text: "/ai switch next"}
	for range 2 {
		if _, err := adapter.Receive(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	if runs != 1 {
		t.Fatalf("副作用执行次数 = %d", runs)
	}
	replies := adapter.Replies()
	if len(replies) != 2 || replies[0].TransactionID == "" || replies[0].TransactionID != replies[1].TransactionID || replies[0].Text != replies[1].Text {
		t.Fatalf("重复回执 = %+v", replies)
	}
	receipts.state = "running"
	if _, err := adapter.Receive(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || len(adapter.Replies()) != 3 || adapter.Replies()[2].Text == "已切换" {
		t.Fatal("状态不确定时重复执行了命令")
	}
}

func TestRegistry_OnceImageDoesNotResendOnDuplicate(t *testing.T) {
	r := command.New()
	r.SetReceipts(&fakeReceipts{})
	if err := r.Register(command.Definition{Descriptor: command.Descriptor{ID: "meme", Path: []string{"meme"}, Scope: "conversation"}, Once: true, Handle: func(ctx context.Context, m chat.Message, a chat.Adapter, _ string) error {
		_, err := a.(chat.ImageAdapter).SendImage(ctx, chat.Reply{Session: m.Session, Text: "cat"}, []byte("gif"), "image/gif", "cat.gif", 1, 1)
		return err
	}}); err != nil {
		t.Fatal(err)
	}
	adapter := &imageAdapter{Adapter: memory.New("bot", chat.Capabilities{}, nil)}
	message := chat.Message{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "room"}, ID: "event", SenderID: "user", Text: "/meme cat"}
	for range 2 {
		if handled, err := r.Dispatch(context.Background(), message, adapter, nil); !handled || err != nil {
			t.Fatalf("命令分发 = %v,%v", handled, err)
		}
	}
	if adapter.images != 1 || len(adapter.Replies()) != 1 {
		t.Fatalf("重复投递 images=%d replies=%+v", adapter.images, adapter.Replies())
	}
}

// TestRegistry_Fallback 验证未命中命令路径时先经过兜底处理器，且不改变已注册命令语义。
func TestRegistry_Fallback(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		fallback    command.Fallback
		wantCalls   int
		wantUnknown bool
		wantErr     bool
	}{
		{
			name: "兜底处理未知命令",
			text: "/deploy 参数",
			fallback: func(context.Context, chat.Message, chat.Adapter) (bool, error) {
				return true, nil
			},
			wantCalls: 1,
		},
		{
			name: "兜底放行后回复未知命令",
			text: "/deploy 参数",
			fallback: func(context.Context, chat.Message, chat.Adapter) (bool, error) {
				return false, nil
			},
			wantCalls:   1,
			wantUnknown: true,
		},
		{
			name: "兜底报错不再追加提示",
			text: "/deploy",
			fallback: func(context.Context, chat.Message, chat.Adapter) (bool, error) {
				return false, errors.New("boom")
			},
			wantCalls: 1,
			wantErr:   true,
		},
		{
			name: "已注册命令不进入兜底",
			text: "/ping",
			fallback: func(context.Context, chat.Message, chat.Adapter) (bool, error) {
				return true, nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := command.New()
			if err := r.Register(command.Definition{Descriptor: command.Descriptor{ID: "ping", Path: []string{"ping"}, Scope: "conversation"}, Handle: func(_ context.Context, message chat.Message, reply chat.Adapter, _ string) error {
				_, err := reply.Send(context.Background(), chat.Reply{Session: message.Session, Text: "pong"})
				return err
			}}); err != nil {
				t.Fatal(err)
			}
			var calls int
			var seen string
			r.SetFallback(func(_ context.Context, message chat.Message, _ chat.Adapter) (bool, error) {
				calls++
				seen = message.Text
				return tt.fallback(context.Background(), message, nil)
			})
			adapter := memory.New("bot", chat.Capabilities{}, nil)
			message := chat.Message{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "room"}, ID: "$1", SenderID: "user", Text: tt.text}
			handled, err := r.Dispatch(context.Background(), message, adapter, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Dispatch() err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && !handled {
				t.Fatal("Dispatch() 应始终报告已处理")
			}
			if calls != tt.wantCalls {
				t.Fatalf("兜底调用次数 = %d, want %d", calls, tt.wantCalls)
			}
			if tt.wantCalls > 0 && seen != tt.text {
				t.Fatalf("兜底收到的正文 = %q, want %q", seen, tt.text)
			}
			replies := adapter.Replies()
			switch {
			case tt.wantUnknown:
				if len(replies) != 1 || !strings.Contains(replies[0].Text, "未知命令") {
					t.Fatalf("未知命令回复 = %+v", replies)
				}
			case tt.wantCalls == 0:
				if len(replies) != 1 || replies[0].Text != "pong" {
					t.Fatalf("已注册命令回复 = %+v", replies)
				}
			default:
				if len(replies) != 0 {
					t.Fatalf("兜底处理时不应回复未知命令: %+v", replies)
				}
			}
		})
	}
}
