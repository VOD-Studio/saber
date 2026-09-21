package matrix_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/conversation"
	"rua.plus/saber/internal/matrix"
)

func TestChatAdapter_SharedPipelineWithMemory(t *testing.T) {
	var mu sync.Mutex
	var matrixMessages []event.MessageEventContent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/send/") {
			var content event.MessageEventContent
			if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
				t.Error(err)
			}
			mu.Lock()
			matrixMessages = append(matrixMessages, content)
			mu.Unlock()
			if _, err := fmt.Fprint(w, `{"event_id":"$out"}`); err != nil {
				t.Error(err)
			}
		} else {
			if _, err := fmt.Fprint(w, `{"event_id":"$incoming","sender":"@user:local","type":"m.room.message","content":{"msgtype":"m.text","body":"original"}}`); err != nil {
				t.Error(err)
			}
		}
	}))
	defer server.Close()
	client, err := mautrix.NewClient(server.URL, "@bot:local", "test")
	if err != nil {
		t.Fatal(err)
	}
	commands := matrix.NewCommandService(client, "@bot:local", &matrix.BuildInfo{})
	h := conversation.NewContextManager(config.DefaultContextConfig())
	defer h.Stop()
	requests := map[string][][]openai.ChatCompletionMessage{}
	identities := map[string]int{}
	runtime := agent.Runtime{
		Model: func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Response, error) {
			identity, ok := chat.IdentityFromContext(ctx)
			if !ok {
				t.Fatal("missing generic identity")
			}
			platform := identity.Session.Platform
			requests[platform] = append(requests[platform], append([]openai.ChatCompletionMessage(nil), req.Messages...))
			if req.Messages[len(req.Messages)-1].Role == "user" {
				return agent.Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{{ID: "lookup", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "lookup", Arguments: `{}`}}}}, nil
			}
			emit(agent.Event{Kind: agent.TextDelta, Text: "answer"})
			return agent.Response{Content: "answer", FinishReason: "stop"}, nil
		},
		Execute: func(ctx context.Context, _ string, _ map[string]any) (agent.ToolOutput, error) {
			identity, _ := chat.IdentityFromContext(ctx)
			identities[identity.Session.Platform]++
			return agent.ToolOutput{Value: "found"}, nil
		},
	}
	processor := &conversation.Processor{Run: runtime.Run, History: h}
	var incoming []chat.Message
	handler := func(ctx context.Context, message chat.Message, reply chat.Adapter) (agent.Result, error) {
		incoming = append(incoming, message)
		return processor.Handle(ctx, message, agent.Request{Stream: true, Messages: []openai.ChatCompletionMessage{{Role: "system", Content: "system"}}, Tools: []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "lookup"}}}}, reply)
	}
	matrixAdapter := matrix.NewChatAdapter(commands, nil, config.MediaConfig{}, true, handler)
	commands.RegisterCommand("ai", matrixAdapter)
	memoryAdapter := memory.New("@bot:local", chat.Capabilities{Edit: false, Reply: true, Typing: true}, handler)
	for _, text := range []string{"first", "second"} {
		evt := &event.Event{Type: event.EventMessage, ID: id.EventID("$" + text), Sender: "@user:local", RoomID: "!room:local", Content: event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: "!ai " + text}}}
		if err := commands.HandleEvent(context.Background(), evt); err != nil {
			t.Fatal(err)
		}
	}
	for _, text := range []string{"first", "second"} {
		if _, err := memoryAdapter.Receive(context.Background(), chat.Message{Session: chat.Session{Conversation: "!room:local"}, SenderID: "@user:local", ID: "$" + text, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(requests["matrix"], requests["memory"]) {
		t.Fatalf("platforms used different history/model pipeline:\n%+v\n%+v", requests["matrix"], requests["memory"])
	}
	if identities["matrix"] != 2 || identities["memory"] != 2 || len(h.ListActiveRooms()) != 2 {
		t.Fatalf("scope isolation failed: %v", identities)
	}
	if incoming[0].ID != "$first" || incoming[0].Session.Account != "@bot:local" || incoming[0].Text != "first" {
		t.Fatalf("normalization failed: %+v", incoming[0])
	}
	if replies := memoryAdapter.Replies(); len(replies) != 2 || replies[0].Text != "answer" || replies[0].ReplyTo != "$first" {
		t.Fatalf("non-edit fallback failed: %+v", replies)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(matrixMessages) != 4 {
		t.Fatalf("expected preview and final edits, got %d", len(matrixMessages))
	}
	if matrixMessages[0].RelatesTo.GetReplyTo() != "$first" || matrixMessages[1].RelatesTo.Type != event.RelReplace || matrixMessages[1].NewContent.Body != "answer" || matrixMessages[1].NewContent.RelatesTo.GetReplyTo() != "$first" {
		t.Fatalf("Matrix reply/edit relations lost: %+v", matrixMessages)
	}
}

func TestChatAdapter_RelationsAndAccountGuard(t *testing.T) {
	client, err := mautrix.NewClient("http://unused.invalid", "@bot:local", "test")
	if err != nil {
		t.Fatal(err)
	}
	body := "> <@bot:local> 已接收，任务 #1\n\n取消任务 #1"
	adapter := matrix.NewChatAdapter(matrix.NewCommandService(client, "@bot:local", &matrix.BuildInfo{}), nil, config.MediaConfig{}, false, func(ctx context.Context, msg chat.Message, _ chat.Adapter) (agent.Result, error) {
		text, ok := matrix.GetReplyBody(ctx)
		if !ok || text != "取消任务 #1" || msg.Text != body {
			t.Fatalf("reply/control text lost: body=%q control=%q", msg.Text, text)
		}
		return agent.Result{}, nil
	})
	ctx := matrix.WithMessageRelations(matrix.WithEventID(context.Background(), "$current"), "$parent", "$thread")
	message := adapter.Message(ctx, "@user:local", "!room:local", body)
	if message.Text != body {
		t.Fatalf("quoted reply text lost: %q", message.Text)
	}
	if _, err := adapter.Receive(ctx, "@user:local", "!room:local", body); err != nil {
		t.Fatal(err)
	}
	if message.ID != "$current" || message.ReplyTo != "$parent" || message.Session.Thread != "$thread" {
		t.Fatalf("relations lost: %+v", message)
	}
	wrong := message.Session
	wrong.Account = "@another:local"
	if _, err := adapter.Send(ctx, chat.Reply{Session: wrong, Text: "forbidden"}); err == nil {
		t.Fatal("cross-account send accepted")
	}
	if err := adapter.Edit(ctx, "$out", chat.Reply{Session: message.Session, Text: "forbidden"}); err == nil {
		t.Fatal("editing disabled but accepted")
	}
}
