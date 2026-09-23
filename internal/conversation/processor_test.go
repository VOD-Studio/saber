package conversation_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/conversation"
)

func history(t *testing.T) *conversation.ContextManager {
	t.Helper()
	h := conversation.NewContextManager(config.DefaultContextConfig())
	t.Cleanup(h.Stop)
	return h
}
func message(room, text string) chat.Message {
	return chat.Message{Session: chat.Session{Conversation: room}, SenderID: "user", ID: "incoming", Text: text}
}

func TestProcessor_MemoryHistoryAndCapabilities(t *testing.T) {
	for _, edit := range []bool{false, true} {
		t.Run(fmt.Sprint(edit), func(t *testing.T) {
			h := history(t)
			calls := 0
			runtime := agent.Runtime{Model: func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Response, error) {
				calls++
				identity, ok := chat.IdentityFromContext(ctx)
				if !ok || identity.Session.Account != "account" {
					t.Fatal("identity lost")
				}
				want := 2
				if calls == 2 {
					want = 4
				}
				if len(req.Messages) != want {
					t.Fatalf("history duplicated or lost: %+v", req.Messages)
				}
				if calls == 2 && req.Messages[2].Content != "answer" {
					t.Fatal("missing previous answer")
				}
				emit(agent.Event{Kind: agent.TextDelta, Text: "ans"})
				emit(agent.Event{Kind: agent.TextDelta, Text: "wer"})
				return agent.Response{Content: "answer", FinishReason: "stop"}, nil
			}}
			processor := &conversation.Processor{Run: runtime.Run, History: h}
			adapter := memory.New("account", chat.Capabilities{Edit: edit, Typing: true, Reply: true}, func(ctx context.Context, m chat.Message, a chat.Adapter) (agent.Result, error) {
				return processor.Handle(ctx, m, agent.Request{Stream: true, Messages: []openai.ChatCompletionMessage{{Role: "system", Content: "system"}}}, a)
			})
			for _, text := range []string{"first", "second"} {
				if result, err := adapter.Receive(context.Background(), message("room", text)); err != nil || result.Status != agent.Completed {
					t.Fatalf("%+v %v", result, err)
				}
			}
			replies := adapter.Replies()
			if len(replies) != 2 || replies[0].Text != "answer" || replies[0].ReplyTo != "incoming" {
				t.Fatalf("%+v", replies)
			}
			if adapter.IsTyping(chat.Session{Platform: "memory", Account: "account", Conversation: "room"}) {
				t.Fatal("typing not cleared")
			}
		})
	}
}

func TestDeliver_ReplyStateFailureKeepsPartialContent(t *testing.T) {
	adapter := memory.New("account", chat.Capabilities{Edit: true, ReplyState: true}, nil)
	message := chat.Message{Session: chat.Session{Platform: "memory", Account: "account", Conversation: "room"}, ID: "question"}
	_, err := conversation.Deliver(context.Background(), func(_ context.Context, _ agent.Request, emit func(agent.Event)) (agent.Result, error) {
		emit(agent.Event{Kind: agent.ModelStarted})
		emit(agent.Event{Kind: agent.ThinkingDelta, Text: "摘要"})
		emit(agent.Event{Kind: agent.TextDelta, Text: "部分正文"})
		return agent.Result{Status: agent.Failed}, errors.New("upstream failed")
	}, agent.Request{}, message, adapter, chat.Display{}, nil)
	if err == nil {
		t.Fatal("运行错误未返回")
	}
	replies := adapter.Replies()
	if len(replies) != 1 || replies[0].Status != chat.ReplyFailed || !strings.Contains(replies[0].Text, "upstream failed") || !strings.Contains(replies[0].Text, "部分正文") || replies[0].Thinking != "摘要" {
		t.Fatalf("回复未保留部分内容: %+v", replies)
	}
}

func TestProcessor_SessionIsolation(t *testing.T) {
	h := history(t)
	processor := &conversation.Processor{History: h, Run: func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Result, error) {
		if len(req.Messages) != 1 {
			t.Fatalf("history leaked: %+v", req.Messages)
		}
		return agent.Result{Status: agent.Completed, Content: "ok"}, nil
	}}
	handler := func(ctx context.Context, m chat.Message, a chat.Adapter) (agent.Result, error) {
		return processor.Handle(ctx, m, agent.Request{}, a)
	}
	for _, account := range []string{"a", "b"} {
		adapter := memory.New(account, chat.Capabilities{}, handler)
		for _, thread := range []string{"", "thread"} {
			msg := message("same", "hi")
			msg.Session.Thread = thread
			if _, err := adapter.Receive(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(h.ListActiveRooms()) != 4 {
		t.Fatal("session keys collided")
	}
	a := chat.Session{Platform: "a", Account: "b:c", Conversation: "d"}
	b := chat.Session{Platform: "a:b", Account: "c", Conversation: "d"}
	if a.Key() == b.Key() {
		t.Fatal("delimiter collision")
	}
	if (chat.Session{Platform: "other", Account: "a", Conversation: "same"}).Key() == (chat.Session{Platform: "memory", Account: "a", Conversation: "same"}).Key() {
		t.Fatal("platform collision")
	}
}

func TestProcessor_QueueCancellationAndIndependentSessions(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var mu sync.Mutex
	var requests []string
	processor := &conversation.Processor{History: history(t), Run: func(ctx context.Context, req agent.Request, _ func(agent.Event)) (agent.Result, error) {
		text := req.Messages[len(req.Messages)-1].Content
		mu.Lock()
		requests = append(requests, text)
		mu.Unlock()
		if text == "first" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return agent.Result{Status: agent.Cancelled}, ctx.Err()
			}
		}
		return agent.Result{Status: agent.Completed, Content: "ok"}, nil
	}}
	adapter := memory.New("a", chat.Capabilities{}, func(ctx context.Context, m chat.Message, a chat.Adapter) (agent.Result, error) {
		return processor.Handle(ctx, m, agent.Request{}, a)
	})
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	firstDone := make(chan error, 1)
	go func() { _, err := adapter.Receive(firstCtx, message("room", "first")); firstDone <- err }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := adapter.Receive(ctx, message("room", "cancelled"))
	if !errors.Is(err, context.DeadlineExceeded) || result.Status != agent.TimedOut {
		t.Fatalf("%+v %v", result, err)
	}
	otherCtx, otherCancel := context.WithTimeout(context.Background(), time.Second)
	defer otherCancel()
	if _, err := adapter.Receive(otherCtx, message("other", "independent")); err != nil {
		t.Fatal("another session blocked", err)
	}
	firstCancel()
	if !errors.Is(<-firstDone, context.Canceled) {
		t.Fatal("first was not cancelled")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(requests, ",") != "first,independent" {
		t.Fatalf("cancelled request ran: %v", requests)
	}
}

func TestProcessor_AttachmentAndFailedDelivery(t *testing.T) {
	h := history(t)
	calls := 0
	processor := &conversation.Processor{History: h, Run: func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Result, error) {
		calls++
		if len(req.Messages) != 1 || len(req.Messages[0].MultiContent) != 2 || req.Messages[0].MultiContent[0].Text != "image" {
			t.Fatalf("duplicated multimodal input: %+v", req.Messages)
		}
		return agent.Result{Status: agent.Completed, Content: "answer"}, nil
	}}
	msg := message("room", "image")
	msg.Session.Platform = "test"
	msg.Session.Account = "account"
	msg.Attachments = []chat.Attachment{{Kind: "image", Name: "pic", URL: "data:image/png;base64,AA=="}}
	result, err := processor.Handle(context.Background(), msg, agent.Request{}, failingAdapter{})
	if result.Status != agent.Completed || err == nil || calls != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	stored := h.GetContext(msg.Session.Key())
	if len(stored) != 2 || stored[1].Content != "answer" || strings.Contains(stored[0].Content, "base64") {
		t.Fatalf("invalid history: %+v", stored)
	}
}

type failingAdapter struct{}

func (failingAdapter) Capabilities() chat.Capabilities { return chat.Capabilities{} }
func (failingAdapter) Send(context.Context, chat.Reply) (string, error) {
	return "", errors.New("disconnected")
}
func (failingAdapter) Edit(context.Context, string, chat.Reply) error {
	return errors.New("unexpected edit")
}
func (failingAdapter) SetTyping(context.Context, chat.Session, bool) error {
	return errors.New("unexpected typing")
}

func TestProcessor_ClearWaitsForCurrentRun(t *testing.T) {
	h := history(t)
	started := make(chan struct{})
	release := make(chan struct{})
	processor := &conversation.Processor{History: h, Run: func(_ context.Context, _ agent.Request, _ func(agent.Event)) (agent.Result, error) {
		close(started)
		<-release
		return agent.Result{Status: agent.Completed, Content: "answer"}, nil
	}}
	adapter := memory.New("a", chat.Capabilities{}, func(ctx context.Context, m chat.Message, a chat.Adapter) (agent.Result, error) {
		return processor.Handle(ctx, m, agent.Request{}, a)
	})
	done := make(chan error, 1)
	go func() { _, err := adapter.Receive(context.Background(), message("room", "hi")); done <- err }()
	<-started
	session := chat.Session{Platform: "memory", Account: "a", Conversation: "room"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := processor.Clear(ctx, session)
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("clear bypassed running turn: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := processor.Clear(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if got := h.GetContext(session.Key()); len(got) != 0 {
		t.Fatalf("completed answer survived clear: %+v", got)
	}
}
