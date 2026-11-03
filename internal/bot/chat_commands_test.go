package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/cli"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
	memstore "rua.plus/saber/internal/memory"
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

// TestCommandFallbackInvokesSkill 验证未注册的 `/词` 只在命中当前空间技能时交给 AI，
// 未命中仍回复「未知命令」，已注册命令不受 fallback 影响。
func TestCommandFallbackInvokesSkill(t *testing.T) {
	var mu sync.Mutex
	var requests []agent.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: server.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.StreamEnabled = false

	mem, err := memstore.Open(filepath.Join(t.TempDir(), "memory.db"), memstore.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	identity := chat.Identity{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "dm"}, SenderID: "user", Direct: true}
	if _, err := mem.AddSkill(context.Background(), identity, "deploy", "发布服务", "第一步：构建", memstore.Source{}); err != nil {
		t.Fatal(err)
	}
	aiSvc, err := ai.NewService(cfg, ai.WithMemory(mem))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(aiSvc.Stop)

	state := &appState{cfg: cfg, info: matrix.BuildInfo{Version: "test"}, services: &services{platforms: platform.NewRegistry(), aiService: aiSvc, memoryService: mem}}
	if err := state.buildCommands(); err != nil {
		t.Fatal(err)
	}
	adapter := memory.New("bot", chat.Capabilities{}, nil)
	send := func(text string) {
		t.Helper()
		message := chat.Message{Session: identity.Session, ID: "$" + text, SenderID: identity.SenderID, Direct: true, Text: text}
		if _, err := state.handleChat(context.Background(), message, adapter); err != nil {
			t.Fatalf("handleChat(%q) error = %v", text, err)
		}
	}

	send("/deploy 生产环境")
	mu.Lock()
	got := append([]agent.Request(nil), requests...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("技能调用模型请求数 = %d, want 1", len(got))
	}
	if last := got[0].Messages[len(got[0].Messages)-1]; last.Content != "生产环境" {
		t.Fatalf("技能调用 user 消息 = %q", last.Content)
	}
	joined := ""
	for _, message := range got[0].Messages {
		joined += message.Content + "\n"
	}
	if !strings.Contains(joined, "【技能：deploy (v1)】") {
		t.Fatalf("技能正文未注入: %q", joined)
	}

	// 未命中技能时 fallback 放行，注册表仍明确回复未知命令。
	send("/absent 参数")
	replies := adapter.Replies()
	if len(replies) == 0 || !strings.Contains(replies[len(replies)-1].Text, "未知命令") {
		t.Fatalf("未命中技能应回复未知命令: %+v", replies)
	}
	// 已注册命令优先于 fallback。
	send("/ping")
	replies = adapter.Replies()
	if last := replies[len(replies)-1].Text; last != "🏓 Pong!" {
		t.Fatalf("已注册命令回复 = %q", last)
	}
}
