package bot

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/cli"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/platform"
	platformmatrix "rua.plus/saber/internal/platform/matrix"
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

// TestCommandCatalog_PlatformMediaCapabilities 验证命令目录只公开平台实际支持的媒体命令。
func TestCommandCatalog_PlatformMediaCapabilities(t *testing.T) {
	cfg := violetOnlyConfig()
	cfg.Matrix.Meme.Enabled = true
	cfg.Matrix.Meme.APIKey = "test"
	state := &appState{cfg: cfg, flags: &cli.Flags{ConfigPath: filepath.Join(t.TempDir(), "config.yaml")}, services: &services{}}
	defer state.shutdown(func() {})
	if err := state.initServices(); err != nil {
		t.Fatal(err)
	}
	client, err := mautrix.NewClient("http://unused.invalid", "@bot:test", "test")
	if err != nil {
		t.Fatal(err)
	}
	state.services.platforms.Register(platformmatrix.New(cfg, matrix.NewCommandService(client, "@bot:test", nil), nil))

	for _, tc := range []struct {
		platform string
		image    bool
		file     bool
	}{
		{"violet", true, false},
		{"matrix", true, true},
		{"terminal", false, false},
		{"unknown", false, false},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			image, file := false, false
			for _, command := range state.services.commandRegistry.Describe(state.commandCapabilities(tc.platform)) {
				image = image || command.ID == "meme"
				file = file || command.ID == "task.logs"
			}
			if image != tc.image || file != tc.file {
				t.Fatalf("目录媒体命令：meme=%t task.logs=%t，期望 %t %t", image, file, tc.image, tc.file)
			}
		})
	}
}
