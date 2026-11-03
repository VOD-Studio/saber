package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	chatmemory "rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/memory"
)

func TestParseSkillInvocation(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantName string
		wantArgs string
		wantOK   bool
	}{
		{name: "无参数", text: "/deploy", wantName: "deploy", wantOK: true},
		{name: "带参数", text: "/deploy 生产环境", wantName: "deploy", wantArgs: "生产环境", wantOK: true},
		{name: "多余空白", text: "  /deploy   生产环境  ", wantName: "deploy", wantArgs: "生产环境", wantOK: true},
		{name: "大写名称归一", text: "/Deploy 参数", wantName: "deploy", wantArgs: "参数", wantOK: true},
		{name: "多行参数", text: "/deploy 第一行\n第二行", wantName: "deploy", wantArgs: "第一行\n第二行", wantOK: true},
		{name: "双斜杠转义", text: "//deploy 参数"},
		{name: "非命令正文", text: "请执行 deploy"},
		{name: "感叹号前缀", text: "!deploy"},
		{name: "空名称", text: "/ 参数"},
		{name: "非法名称", text: "/名前 参数"},
		{name: "名称包含斜杠", text: "/a/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, args, ok := parseSkillInvocation(tt.text)
			if ok != tt.wantOK || name != tt.wantName || args != tt.wantArgs {
				t.Fatalf("parseSkillInvocation(%q) = %q, %q, %v", tt.text, name, args, ok)
			}
		})
	}
}

func TestSkillMessageTextKeepsEscape(t *testing.T) {
	plain := chat.Message{Text: "/deploy 参数"}
	if text, ok := skillMessageText(plain); !ok || text != "/deploy 参数" {
		t.Fatalf("普通消息 = %q, %v", text, ok)
	}
	escaped := "/deploy 参数"
	if _, ok := skillMessageText(chat.Message{Text: escaped, ControlText: &escaped}); ok {
		t.Fatal("`//name` 转义后的正文不应识别为技能调用")
	}
	quoted := "/deploy 参数"
	quotedText := "> <@bot:x> 上一轮回复\n\n" + quoted
	if text, ok := skillMessageText(chat.Message{Text: quotedText, ControlText: &quoted}); !ok || text != quoted {
		t.Fatalf("引用回复应只识别用户本轮正文: %q, %v", text, ok)
	}
}

func TestSkillInvocationRuntimeInstructionBudget(t *testing.T) {
	inv := skillInvocation{
		entry: memory.SkillEntry{Name: "deploy", Description: "发布服务", Version: 2, Content: strings.Repeat("步骤", 4000)},
		args:  "参数",
	}
	got := inv.runtimeInstruction(256)
	if len(got) > 256 {
		t.Fatalf("注入长度 = %d, want <= 256", len(got))
	}
	if !strings.Contains(got, "【技能：deploy (v2)】") || !strings.Contains(got, "已截断") {
		t.Fatalf("注入内容缺少头部或截断提示: %q", got)
	}
	if strings.Contains(got, strings.Repeat("步骤", 4000)) {
		t.Fatal("超预算正文未被截断")
	}
	if full := inv.runtimeInstruction(0); !strings.Contains(full, strings.Repeat("步骤", 4000)) {
		t.Fatal("预算为 0 时应注入完整正文")
	}
}

func TestSkillInvocationUserText(t *testing.T) {
	inv := skillInvocation{entry: memory.SkillEntry{Description: "发布服务"}, args: "  "}
	if got := inv.userText(); got != "发布服务" {
		t.Fatalf("无参数 userText = %q, want 技能描述", got)
	}
	inv.args = "生产环境"
	if got := inv.userText(); got != "生产环境" {
		t.Fatalf("带参数 userText = %q", got)
	}
}

// skillModelServer 返回记录模型请求的测试服务。
func skillModelServer(t *testing.T) (*httptest.Server, func() []agent.Request) {
	t.Helper()
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
	t.Cleanup(server.Close)
	return server, func() []agent.Request {
		mu.Lock()
		defer mu.Unlock()
		return append([]agent.Request(nil), requests...)
	}
}

func newSkillTestService(t *testing.T, mem *memory.Service, baseURL string) *Service {
	t.Helper()
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: baseURL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.StreamEnabled = false
	svc, err := NewService(&cfg, WithMemory(mem))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	t.Cleanup(svc.Stop)
	return svc
}

func TestHandleChatSkillInvocation(t *testing.T) {
	ctx := context.Background()
	mem := newTestMemory(t, memory.Config{})
	identity := chat.Identity{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "dm"}, SenderID: "@alice:test", Direct: true}
	if _, err := mem.AddSkill(ctx, identity, "deploy", "发布服务并检查健康状态", "第一步：构建\n第二步：发布", memory.Source{}); err != nil {
		t.Fatalf("AddSkill() error = %v", err)
	}
	server, requests := skillModelServer(t)
	svc := newSkillTestService(t, mem, server.URL)
	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)
	session := chat.Session{Platform: "memory", Account: "bot", Conversation: "dm"}

	send := func(text string, control *string) {
		t.Helper()
		message := chat.Message{Session: session, ID: "$" + text, SenderID: "@alice:test", Direct: true, Text: text, ControlText: control}
		if _, err := svc.HandleChat(ctx, message, adapter); err != nil {
			t.Fatalf("HandleChat(%q) error = %v", text, err)
		}
	}
	send("/deploy 生产环境", nil)
	send("/deploy", nil)
	escaped := "/deploy 生产环境"
	send(escaped, &escaped)
	send("/missing 参数", nil)

	all := requests()
	if len(all) != 4 {
		t.Fatalf("模型请求数 = %d, want 4", len(all))
	}

	withArgs := all[0]
	if last := withArgs.Messages[len(withArgs.Messages)-1]; last.Role != "user" || last.Content != "生产环境" {
		t.Fatalf("带参数的 user 消息 = %+v", last)
	}
	joined := joinRequestContents(withArgs)
	if !strings.Contains(joined, "【技能：deploy (v1)】") || !strings.Contains(joined, "第一步：构建") {
		t.Fatalf("技能正文未注入运行时资料: %q", joined)
	}
	if strings.Contains(joined, "/deploy 生产环境") {
		t.Fatal("技能调用原文不应进入模型请求")
	}

	noArgs := all[1]
	if last := noArgs.Messages[len(noArgs.Messages)-1]; last.Content != "发布服务并检查健康状态" {
		t.Fatalf("无参数 user 消息 = %q, want 技能描述", last.Content)
	}

	escapedReq := all[2]
	if last := escapedReq.Messages[len(escapedReq.Messages)-1]; last.Content != "/deploy 生产环境" {
		t.Fatalf("转义正文的 user 消息 = %q", last.Content)
	}
	if strings.Contains(joinRequestContents(escapedReq), "【技能：deploy") {
		t.Fatal("`//name` 转义仍注入了技能正文")
	}

	missing := all[3]
	if last := missing.Messages[len(missing.Messages)-1]; last.Content != "/missing 参数" {
		t.Fatalf("未命中技能时的 user 消息 = %q", last.Content)
	}
	if strings.Contains(joinRequestContents(missing), "【技能：") {
		t.Fatal("未命中技能时不应注入技能正文")
	}
}

func TestHandleChatSkillInvocationSkippedWithoutAICommand(t *testing.T) {
	ctx := context.Background()
	mem := newTestMemory(t, memory.Config{})
	identity := chat.Identity{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "dm"}, SenderID: "@alice:test", Direct: true}
	if _, err := mem.AddSkill(ctx, identity, "deploy", "发布服务", "步骤", memory.Source{}); err != nil {
		t.Fatalf("AddSkill() error = %v", err)
	}
	server, requests := skillModelServer(t)
	svc := newSkillTestService(t, mem, server.URL)

	// `/ai` 明确要求把正文交给模型，不应再被解释为技能调用。
	message := chat.Message{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "dm"}, ID: "$ai", SenderID: "@alice:test", Direct: true, Text: "/deploy 参数"}
	if err := svc.ChatCommand(ctx, message, chatmemory.New("bot", chat.Capabilities{}, nil), "openai.local", "/deploy 参数"); err != nil {
		t.Fatalf("ChatCommand() error = %v", err)
	}
	all := requests()
	if len(all) != 1 {
		t.Fatalf("模型请求数 = %d", len(all))
	}
	if last := all[0].Messages[len(all[0].Messages)-1]; last.Content != "/deploy 参数" {
		t.Fatalf(`/ai 正文 = %q`, last.Content)
	}
	if strings.Contains(joinRequestContents(all[0]), "【技能：") {
		t.Fatal("/ai 请求不应注入技能正文")
	}
}

func TestHandleChatSkillInvocationScopeIsolation(t *testing.T) {
	ctx := context.Background()
	mem := newTestMemory(t, memory.Config{GroupWriter: func(chat.Identity) bool { return false }})
	private := chat.Identity{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "dm"}, SenderID: "@alice:test", Direct: true}
	if _, err := mem.AddSkill(ctx, private, "deploy", "发布服务", "步骤", memory.Source{}); err != nil {
		t.Fatalf("AddSkill() error = %v", err)
	}
	server, requests := skillModelServer(t)
	svc := newSkillTestService(t, mem, server.URL)

	message := chat.Message{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "!room:test"}, ID: "$room", SenderID: "@alice:test", Text: "/deploy 参数"}
	if _, err := svc.HandleChat(ctx, message, chatmemory.New("bot", chat.Capabilities{}, nil)); err != nil {
		t.Fatalf("HandleChat() error = %v", err)
	}
	all := requests()
	if len(all) != 1 {
		t.Fatalf("模型请求数 = %d", len(all))
	}
	if last := all[0].Messages[len(all[0].Messages)-1]; last.Content != "/deploy 参数" {
		t.Fatalf("群空间不应命中个人技能: user 消息 = %q", last.Content)
	}
	if strings.Contains(joinRequestContents(all[0]), "【技能：deploy") {
		t.Fatal("群空间使用了个人技能")
	}
}

// joinRequestContents 拼接请求中所有消息文本，便于断言注入内容。
func joinRequestContents(req agent.Request) string {
	var b strings.Builder
	for _, message := range req.Messages {
		b.WriteString(message.Content)
		b.WriteString("\n")
	}
	return b.String()
}
