package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	chatmemory "rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/memory"
)

// profileModelServer 是画像与摘要测试共用的假模型，按调用序号返回预设内容并记录提示词。
type profileModelServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
	calls    int32
	contents []string
	rounds   int32
}

func newProfileModelServer(t *testing.T, contents ...string) *profileModelServer {
	t.Helper()
	fake := &profileModelServer{contents: contents}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []any `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fake.mu.Lock()
		index := int(atomic.AddInt32(&fake.calls, 1))
		if len(body.Messages) > 0 {
			fake.requests = append(fake.requests, body.Messages[len(body.Messages)-1].Content)
		}
		fake.mu.Unlock()

		// 复盘沙箱请求带工具；第一轮调用工具，第二轮结束。
		if len(body.Tools) > 0 {
			if atomic.AddInt32(&fake.rounds, 1) == 1 {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []any{map[string]any{
						"message": map[string]any{"role": "assistant", "tool_calls": []map[string]any{{
							"id":   "call_mem_add",
							"type": "function",
							"function": map[string]any{
								"name":      "saber_memory",
								"arguments": `{"action":"add","content":"用户偏好：回答精炼"}`,
							},
						}}},
						"finish_reason": "tool_calls",
					}},
					"usage": map[string]any{"total_tokens": 30},
				})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message":       map[string]any{"role": "assistant", "content": "复盘完成"},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"total_tokens": 10},
			})
			return
		}

		content := ""
		if len(fake.contents) > 0 {
			content = fake.contents[(index-1)%len(fake.contents)]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": content},
				"finish_reason": "stop",
			}},
		})
	}))
	t.Cleanup(fake.Close)
	return fake
}

// prompts 返回收到的模型输入副本。
func (f *profileModelServer) prompts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// newProfileTestService 以假模型构造启用记忆的 AI 服务。
func newProfileTestService(t *testing.T, serverURL string, writer func(chat.Identity) bool) (*Service, *memory.Service) {
	t.Helper()
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: serverURL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Memory.Enabled = true
	cfg.Memory.AutoLearn = true

	mem := newTestMemory(t, memory.Config{GroupWriter: writer})
	svc, err := NewService(&cfg, WithMemory(mem))
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	return svc, mem
}

// TestMemoryProfile_CommandViewRefreshAndInjection 验证画像查看、立即重建以及注入顺序。
func TestMemoryProfile_CommandViewRefreshAndInjection(t *testing.T) {
	fake := newProfileModelServer(t, "偏好：中文简洁回答，避免长篇大论")
	svc, mem := newProfileTestService(t, fake.URL, nil)
	ctx := context.Background()
	msg := memoryCommandMessage("@alice:test", "dm", true)
	identity := chat.Identity{Session: msg.Session, SenderID: msg.SenderID, Direct: true}
	_, err := mem.Add(ctx, identity, "回答尽量简洁，使用中文", memory.Source{}, true)
	require.NoError(t, err)

	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)
	require.NoError(t, svc.MemoryCommand(ctx, msg, adapter, "profile", ""))
	require.Contains(t, lastReply(t, adapter), "还没有画像")

	require.NoError(t, svc.MemoryCommand(ctx, msg, adapter, "profile", "refresh"))
	reply := lastReply(t, adapter)
	require.Contains(t, reply, "已重建当前空间画像")
	require.Contains(t, reply, "中文简洁回答")
	require.Equal(t, int32(1), atomic.LoadInt32(&fake.calls))

	require.NoError(t, svc.MemoryCommand(ctx, msg, adapter, "profile", ""))
	require.Contains(t, lastReply(t, adapter), "画像与当前记忆一致")

	// 注入顺序：画像在记忆条目之前。
	injectCtx := chat.WithIdentity(context.Background(), identity)
	got := svc.augmentMemory(injectCtx, agent.Request{})
	require.Len(t, got.RuntimeContext, 2)
	require.Contains(t, got.RuntimeContext[0].Content, "【用户画像")
	require.Contains(t, got.RuntimeContext[0].Content, "中文简洁回答")
	require.Contains(t, got.RuntimeContext[1].Content, "【长期记忆")
	require.Equal(t, int32(1), atomic.LoadInt32(&fake.calls), "画像新鲜时不应重复生成")

	// 非法子命令给出用法。
	require.NoError(t, svc.MemoryCommand(ctx, msg, adapter, "profile", "other"))
	require.Contains(t, lastReply(t, adapter), "用法：/memory profile [refresh]")
}

// TestMemoryProfile_StaleTriggersBackgroundRefresh 验证记忆条目变化后画像过期并在后台刷新。
func TestMemoryProfile_StaleTriggersBackgroundRefresh(t *testing.T) {
	fake := newProfileModelServer(t, "旧画像：偏好简洁", "新画像：偏好简洁且使用中文")
	svc, mem := newProfileTestService(t, fake.URL, nil)
	ctx := context.Background()
	msg := memoryCommandMessage("@alice:test", "dm", true)
	identity := chat.Identity{Session: msg.Session, SenderID: msg.SenderID, Direct: true}
	_, err := mem.Add(ctx, identity, "回答尽量简洁", memory.Source{}, true)
	require.NoError(t, err)

	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)
	require.NoError(t, svc.MemoryCommand(ctx, msg, adapter, "profile", "refresh"))
	require.Contains(t, lastReply(t, adapter), "旧画像")

	scope, ok := memory.Space(identity)
	require.True(t, ok)
	time.Sleep(2 * time.Millisecond)
	_, err = mem.Add(ctx, identity, "固定使用中文", memory.Source{}, true)
	require.NoError(t, err)

	stale, err := mem.Profile(ctx, identity)
	require.NoError(t, err)
	require.True(t, stale.Stale)

	// 本轮仍注入现有画像，同时触发一次后台刷新。
	got := svc.augmentMemory(chat.WithIdentity(context.Background(), identity), agent.Request{})
	require.Len(t, got.RuntimeContext, 2)
	require.Contains(t, got.RuntimeContext[0].Content, "旧画像")

	require.Eventually(t, func() bool {
		profile, err := mem.ProfileState(context.Background(), scope)
		return err == nil && profile.Content == "新画像：偏好简洁且使用中文" && !profile.Stale
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, int32(2), atomic.LoadInt32(&fake.calls))
}

// TestMemoryProfile_GroupIsolationAndPermission 验证群画像只使用本群条目，且普通成员不能重建。
func TestMemoryProfile_GroupIsolationAndPermission(t *testing.T) {
	fake := newProfileModelServer(t, "群约定：周五发布周报")
	admin := func(identity chat.Identity) bool { return identity.SenderID == "@admin:test" }
	svc, mem := newProfileTestService(t, fake.URL, admin)
	ctx := context.Background()

	adminMsg := memoryCommandMessage("@admin:test", "!room:test", false)
	adminIdentity := chat.Identity{Session: adminMsg.Session, SenderID: adminMsg.SenderID}
	_, err := mem.Add(chat.WithIdentity(ctx, adminIdentity), adminIdentity, "本群周五发布周报", memory.Source{}, true)
	require.NoError(t, err)
	personalMsg := memoryCommandMessage("@admin:test", "dm", true)
	personalID := chat.Identity{Session: personalMsg.Session, SenderID: personalMsg.SenderID, Direct: true}
	_, err = mem.Add(ctx, personalID, "个人偏好：只用中文", memory.Source{}, true)
	require.NoError(t, err)

	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)
	require.NoError(t, svc.MemoryCommand(ctx, adminMsg, adapter, "profile", "refresh"))
	require.Contains(t, lastReply(t, adapter), "群约定：周五发布周报")

	// 群画像输入只包含本群条目。
	prompts := fake.prompts()
	require.NotEmpty(t, prompts)
	require.Contains(t, prompts[0], "本群周五发布周报")
	require.NotContains(t, prompts[0], "个人偏好：只用中文")

	groupScope, ok := memory.Space(adminIdentity)
	require.True(t, ok)
	groupProfile, err := mem.ProfileState(ctx, groupScope)
	require.NoError(t, err)
	require.True(t, groupProfile.Exists)
	personalScope, ok := memory.Space(personalID)
	require.True(t, ok)
	personalProfile, err := mem.ProfileState(ctx, personalScope)
	require.NoError(t, err)
	require.False(t, personalProfile.Exists, "群画像不得写入个人空间")

	// 普通成员只能查看，不能重建。
	memberMsg := memoryCommandMessage("@bob:test", "!room:test", false)
	before := atomic.LoadInt32(&fake.calls)
	require.NoError(t, svc.MemoryCommand(ctx, memberMsg, adapter, "profile", "refresh"))
	require.Contains(t, lastReply(t, adapter), "只有本群记忆管理员")
	require.Equal(t, before, atomic.LoadInt32(&fake.calls), "无写权限时不得请求模型")
}

// TestMemoryProfile_ToolReadOnly 验证 saber_memory 的 profile 动作只读查看且不接受作用域参数。
func TestMemoryProfile_ToolReadOnly(t *testing.T) {
	fake := newProfileModelServer(t, "不会用到")
	svc, mem := newProfileTestService(t, fake.URL, nil)
	ctx := context.Background()
	alice := personalIdentity("@alice:example.com")
	ctx = chat.WithIdentity(ctx, alice)
	scope, ok := memory.Space(alice)
	require.True(t, ok)
	_, err := mem.SaveProfile(ctx, scope, "画像：偏好中文简洁", "")
	require.NoError(t, err)

	tool := memoryTool()
	require.NotNil(t, tool.Function)
	params, ok := tool.Function.Parameters.(map[string]any)
	require.True(t, ok)
	properties, ok := params["properties"].(map[string]any)
	require.True(t, ok)
	action, ok := properties["action"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, action["enum"], "profile")
	require.NotContains(t, properties, "scope", "工具不得接受作用域参数")

	out, err := svc.executeMemoryTool(ctx, map[string]any{"action": "profile", "scope": "other"})
	require.NoError(t, err)
	text, ok := out.(string)
	require.True(t, ok)
	require.Contains(t, text, "偏好中文简洁")
	require.Equal(t, int32(0), atomic.LoadInt32(&fake.calls), "只读查看不得请求模型")
}

// TestMemoryReview_TriggersProfileRefresh 验证后台复盘成功后触发一次画像刷新且不阻塞复盘。
func TestMemoryReview_TriggersProfileRefresh(t *testing.T) {
	fake := newProfileModelServer(t, "复盘后画像：用户偏好精炼回答")
	svc, mem := newProfileTestService(t, fake.URL, nil)
	ctx := context.Background()
	alice := personalIdentity("@alice:example.com")
	ctx = chat.WithIdentity(ctx, alice)
	scope, ok := memory.Space(alice)
	require.True(t, ok)
	_, err := mem.RecordProjection(ctx, memory.ProjectionInput{
		TaskID:        501,
		Scope:         scope,
		Conversation:  "dm",
		SenderID:      alice.SenderID,
		UserMessageID: "msg_501",
		UserText:      "以后回答尽量精炼",
		AssistantText: "好的，会尽量精炼。",
		TaskStatus:    "completed",
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)

	require.NoError(t, svc.ReviewScope(ctx, scope))
	require.Eventually(t, func() bool {
		profile, err := mem.ProfileState(context.Background(), scope)
		return err == nil && profile.Exists && strings.Contains(profile.Content, "复盘后画像")
	}, 5*time.Second, 20*time.Millisecond)
}
