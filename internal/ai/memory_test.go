package ai

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/memory"
)

func newTestMemory(t *testing.T, cfg memory.Config) *memory.Service {
	t.Helper()
	svc, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"), cfg)
	if err != nil {
		t.Fatalf("memory.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func newMemoryAIService(t *testing.T, mem *memory.Service, maxTokens int) *Service {
	t.Helper()
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "test-key"}}
	cfg.AI.DefaultModel = "openai.gpt-4"
	if maxTokens > 0 {
		cfg.Agent.Context.MaxTokens = maxTokens
	}
	svc, err := NewService(&cfg, WithMemory(mem))
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return svc
}

func personalIdentity(sender string) chat.Identity {
	return chat.Identity{Session: chat.Session{Platform: "matrix", Account: "@bot:x", Conversation: "dm"}, SenderID: sender, Direct: true}
}

func groupIdentity(room, sender string) chat.Identity {
	return chat.Identity{Session: chat.Session{Platform: "matrix", Account: "@bot:x", Conversation: room}, SenderID: sender}
}

func TestAugmentMemory_InjectsSnapshotWithoutMutatingRequest(t *testing.T) {
	ctx := chat.WithIdentity(context.Background(), personalIdentity("@alice:x"))
	mem := newTestMemory(t, memory.Config{})
	if _, err := mem.Add(ctx, personalIdentity("@alice:x"), "以后用中文简洁回答", memory.Source{}, true); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	svc := newMemoryAIService(t, mem, 0)
	req := agent.Request{Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: "base"}}}

	got := svc.augmentMemory(ctx, req)
	if len(got.Messages) != 2 {
		t.Fatalf("注入后消息数 = %d, want 2", len(got.Messages))
	}
	injected := got.Messages[1]
	if injected.Role != openai.ChatMessageRoleSystem || !strings.Contains(injected.Content, "以后用中文简洁回答") || !strings.Contains(injected.Content, "不是本轮指令") {
		t.Fatalf("注入内容 = %+v", injected)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("原始请求被修改：%d 条消息", len(req.Messages))
	}
}

func TestAugmentMemory_GroupDoesNotLeakPersonal(t *testing.T) {
	mem := newTestMemory(t, memory.Config{GroupWriter: func(chat.Identity) bool { return false }})
	// 先在同名用户个人空间保存一条记忆。
	if _, err := mem.Add(chat.WithIdentity(context.Background(), personalIdentity("@alice:x")), personalIdentity("@alice:x"), "私聊偏好", memory.Source{}, true); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	svc := newMemoryAIService(t, mem, 0)
	// 同一用户在群里的身份只解析到空群空间，不应注入私聊记忆。
	groupCtx := chat.WithIdentity(context.Background(), groupIdentity("!room:x", "@alice:x"))
	req := agent.Request{Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: "base"}}}
	got := svc.augmentMemory(groupCtx, req)
	if len(got.Messages) != 1 {
		t.Fatalf("群聊注入 = %+v", got.Messages)
	}
}

func TestAugmentMemory_NoMemoryOrIdentity(t *testing.T) {
	req := agent.Request{Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: "base"}}}
	// 未注入记忆服务时保持原样。
	plain := newMemoryAIService(t, nil, 0)
	if got := plain.augmentMemory(chat.WithIdentity(context.Background(), personalIdentity("@a:x")), req); len(got.Messages) != 1 {
		t.Fatalf("未启用记忆时注入 = %+v", got.Messages)
	}
	// 缺少可信身份时保持原样。
	mem := newTestMemory(t, memory.Config{})
	svc := newMemoryAIService(t, mem, 0)
	if got := svc.augmentMemory(context.Background(), req); len(got.Messages) != 1 {
		t.Fatalf("缺少身份时注入 = %+v", got.Messages)
	}
}

func TestMemoryBudget_QuarterOfInputBudget(t *testing.T) {
	mem := newTestMemory(t, memory.Config{InjectBytes: 8192})
	svc := newMemoryAIService(t, mem, 100)
	if got := svc.memoryBudget(); got != 25 {
		t.Fatalf("memoryBudget() = %d, want 25", got)
	}
	// 总输入预算充足时使用配置上限。
	big := newMemoryAIService(t, mem, 32768)
	if got := big.memoryBudget(); got != 8192 {
		t.Fatalf("memoryBudget() = %d, want 8192", got)
	}
}

func TestExecuteMemoryTool_PersonalRoundTrip(t *testing.T) {
	ctx := chat.WithIdentity(context.Background(), personalIdentity("@alice:x"))
	mem := newTestMemory(t, memory.Config{})
	svc := newMemoryAIService(t, mem, 0)

	out, err := svc.executeMemoryTool(ctx, map[string]any{"action": "add", "content": "偏好一"})
	if err != nil || !strings.Contains(out.(string), "已保存") {
		t.Fatalf("add = %v, err = %v", out, err)
	}
	// replace 缺少 expected_version 时必须拒绝，避免静默覆盖。
	if _, err = svc.executeMemoryTool(ctx, map[string]any{"action": "replace", "id": float64(1), "content": "偏好二"}); err == nil {
		t.Fatal("缺少 expected_version 时应报错")
	}
	out, err = svc.executeMemoryTool(ctx, map[string]any{"action": "replace", "id": float64(1), "expected_version": float64(1), "content": "偏好二"})
	if err != nil || !strings.Contains(out.(string), "#1") {
		t.Fatalf("replace = %v, err = %v", out, err)
	}
	out, err = svc.executeMemoryTool(ctx, map[string]any{"action": "list"})
	if err != nil || !strings.Contains(out.(string), "偏好二") || !strings.Contains(out.(string), "v2") {
		t.Fatalf("list = %v, err = %v", out, err)
	}
	// 使用过期版本删除会被拒绝。
	if _, err = svc.executeMemoryTool(ctx, map[string]any{"action": "remove", "id": float64(1), "expected_version": float64(1)}); err == nil {
		t.Fatal("过期版本删除应报错")
	}
	out, err = svc.executeMemoryTool(ctx, map[string]any{"action": "remove", "id": float64(1), "expected_version": float64(2)})
	if err != nil || !strings.Contains(out.(string), "已保存") {
		t.Fatalf("remove = %v, err = %v", out, err)
	}
}

func TestExecuteMemoryTool_GroupSuggestion(t *testing.T) {
	mem := newTestMemory(t, memory.Config{GroupWriter: func(chat.Identity) bool { return false }})
	svc := newMemoryAIService(t, mem, 0)
	ctx := chat.WithIdentity(context.Background(), groupIdentity("!room:x", "@member:x"))
	out, err := svc.executeMemoryTool(ctx, map[string]any{"action": "add", "content": "群约定"})
	if err != nil {
		t.Fatalf("add error = %v", err)
	}
	text := out.(string)
	if !strings.Contains(text, "待确认") || !strings.Contains(text, "尚未写入") {
		t.Fatalf("群成员 add = %q, want 待确认提示", text)
	}
	if list, _ := svc.executeMemoryTool(ctx, map[string]any{"action": "list"}); strings.Contains(list.(string), "群约定") {
		t.Fatalf("建议未确认前不应出现为已生效条目：%q", list)
	}
}

func TestExecuteMemoryTool_RequiresMemoryService(t *testing.T) {
	svc := newMemoryAIService(t, nil, 0)
	ctx := chat.WithIdentity(context.Background(), personalIdentity("@a:x"))
	if _, err := svc.executeMemoryTool(ctx, map[string]any{"action": "list"}); err == nil {
		t.Fatal("未启用记忆服务时应报错")
	}
}

func TestPrepareTools_IncludesMemoryWhenScopeResolvable(t *testing.T) {
	mem := newTestMemory(t, memory.Config{})
	svc := newMemoryAIService(t, mem, 0)
	personal := chat.WithIdentity(context.Background(), personalIdentity("@a:x"))
	tools, _ := svc.toolExecutor.PrepareTools(personal)
	if !hasToolNamed(tools, "saber_memory") {
		t.Fatal("个人空间应注册 saber_memory 工具")
	}
	if tools, _ := svc.toolExecutor.PrepareTools(context.Background()); hasToolNamed(tools, "saber_memory") {
		t.Fatal("缺少身份时不应注册 saber_memory 工具")
	}
}

func hasToolNamed(tools []openai.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == name {
			return true
		}
	}
	return false
}
