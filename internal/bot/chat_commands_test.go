package bot

import (
	"context"
	"strings"
	"testing"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/platform"
)

func TestBasicCommandsWorkWithoutAI(t *testing.T) {
	state := &appState{cfg: config.DefaultConfig(), info: matrix.BuildInfo{Version: "test"}, services: &services{platforms: platform.NewRegistry()}}
	if err := state.buildCommands(); err != nil {
		t.Fatal(err)
	}
	adapter := memory.New("bot", chat.Capabilities{}, state.handleChat)
	for _, body := range []string{"/ping", "!help", "/version", "/task list", "//ping"} {
		_, err := adapter.Receive(context.Background(), chat.Message{Session: chat.Session{Conversation: "room"}, ID: body, SenderID: "user", Text: body})
		if err != nil {
			t.Fatal(err)
		}
	}
	replies := adapter.Replies()
	if len(replies) != 4 || replies[0].Text != "🏓 Pong!" || !strings.Contains(replies[1].Text, "/ping") || !strings.Contains(replies[2].Text, "test") || !strings.Contains(replies[3].Text, "未知命令") {
		t.Fatalf("基础命令回执 = %+v", replies)
	}
}
