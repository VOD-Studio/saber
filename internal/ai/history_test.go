package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	chatmemory "rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/memory"
)

// TestHistoryTool_PrepareAndExecute 验证 saber_history 工具注册、检索与上下文阅读。
func TestHistoryTool_PrepareAndExecute(t *testing.T) {
	mem := newTestMemory(t, memory.Config{})
	svc := newMemoryAIService(t, mem, 0)

	aliceDM := personalIdentity("@alice:example.com")
	ctx := chat.WithIdentity(context.Background(), aliceDM)

	// 1. 验证 PrepareTools 包含 saber_history 与 saber_memory
	tools, ok := svc.toolExecutor.PrepareTools(ctx)
	require.True(t, ok)
	var foundHistory, foundMemory bool
	for _, tool := range tools {
		if tool.Function.Name == "saber_history" {
			foundHistory = true
		}
		if tool.Function.Name == "saber_memory" {
			foundMemory = true
		}
	}
	require.True(t, foundHistory, "应注册 saber_history 工具")
	require.True(t, foundMemory, "应注册 saber_memory 工具")

	// 2. 插入测试历史投影
	scope, ok := memory.Space(aliceDM)
	require.True(t, ok)
	rec1, err := mem.RecordProjection(ctx, memory.ProjectionInput{
		TaskID:        101,
		Scope:         scope,
		Conversation:  "dm",
		SenderID:      "@alice:example.com",
		UserMessageID: "msg_101",
		UserText:      "我想要学习 Go 语言的并发模型，有什么推荐的资料吗？",
		AssistantText: "推荐阅读 Go 官方文档中的 Effective Go 以及 Go Memory Model。",
		TaskStatus:    "completed",
		CreatedAt:     time.Now().Add(-10 * time.Minute),
	})
	require.NoError(t, err)

	rec2, err := mem.RecordProjection(ctx, memory.ProjectionInput{
		TaskID:        102,
		Scope:         scope,
		Conversation:  "dm",
		SenderID:      "@alice:example.com",
		UserMessageID: "msg_102",
		UserText:      "发布流程是怎样的？",
		AssistantText: "先打 git tag，随后 CI 自动编译交付。",
		TaskStatus:    "completed",
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)

	// 3. 执行 search 操作 (>= 3 字符，FTS5 trigram 召回)
	out, err := svc.executeHistoryTool(ctx, map[string]any{
		"action": "search",
		"query":  "并发模型",
	})
	require.NoError(t, err)
	text, ok := out.(string)
	require.True(t, ok)
	require.Contains(t, text, "并发模型")
	require.Contains(t, text, "Effective Go")
	require.Contains(t, text, "#101")

	// 4. 执行 search 操作 (< 3 字符，短中文字面匹配)
	out, err = svc.executeHistoryTool(ctx, map[string]any{
		"action": "search",
		"query":  "发布",
	})
	require.NoError(t, err)
	text, ok = out.(string)
	require.True(t, ok)
	require.Contains(t, text, "发布流程")
	require.Contains(t, text, "#102")

	// 5. 执行 context 操作
	out, err = svc.executeHistoryTool(ctx, map[string]any{
		"action": "context",
		"id":     float64(rec2.ID),
		"before": float64(1),
		"after":  float64(0),
	})
	require.NoError(t, err)
	text, ok = out.(string)
	require.True(t, ok)
	require.Contains(t, text, "目标记录")
	require.Contains(t, text, "发布流程是怎样的")
	require.Contains(t, text, "前文（1 条）")
	require.Contains(t, text, "Go 语言的并发模型")

	// 6. 跨空间 context 访问被拒绝，不泄露内容
	bobDM := personalIdentity("@bob:example.com")
	bobCtx := chat.WithIdentity(context.Background(), bobDM)
	out, err = svc.executeHistoryTool(bobCtx, map[string]any{
		"action": "context",
		"id":     float64(rec1.ID),
	})
	require.NoError(t, err)
	text, ok = out.(string)
	require.True(t, ok)
	require.Contains(t, text, "未在当前空间找到记录")
	require.NotContains(t, text, "Effective Go")

	// 7. 参数验证错误
	_, err = svc.executeHistoryTool(ctx, map[string]any{"action": "unknown"})
	require.Error(t, err)

	_, err = svc.executeHistoryTool(ctx, map[string]any{"action": "search"})
	require.Error(t, err)

	_, err = svc.executeHistoryTool(ctx, map[string]any{"action": "context"})
	require.Error(t, err)
}

// TestHistory_SyncPipelineAndBackfill 验证持久化任务落盘后自动投影到历史搜索及回填功能。
func TestHistory_SyncPipelineAndBackfill(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{
						"role":    "assistant",
						"content": "模型回答：发布流程需审查 PR 并合并到 master",
					},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer modelServer.Close()

	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: modelServer.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.StreamEnabled = false

	mem := newTestMemory(t, memory.Config{})
	service, err := NewService(&cfg, WithMemory(mem))
	require.NoError(t, err)
	defer service.Stop()

	tasksDB := filepath.Join(t.TempDir(), "tasks.db")
	require.NoError(t, service.EnableTasks(tasksDB))

	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)
	msg := chat.Message{
		ID:       "msg_pipe_1",
		SenderID: "@charlie:test",
		Text:     "请问项目的部署和发布流程是怎样的？",
		Session: chat.Session{
			Platform:     "memory",
			Account:      "bot",
			Conversation: "!group_pipe:test",
		},
		Direct: false,
	}

	// 提交任务执行
	_, err = service.HandleChat(context.Background(), msg, adapter)
	require.NoError(t, err)

	// 等待任务执行与投影完成
	identity := chat.Identity{Session: msg.Session, SenderID: msg.SenderID, Direct: msg.Direct}
	ctx := chat.WithIdentity(context.Background(), identity)

	var res memory.HistorySearchResult
	require.Eventually(t, func() bool {
		_ = service.SyncHistory(ctx)
		res, err = mem.SearchHistory(ctx, identity, memory.HistorySearchOptions{Query: "发布流程"})
		return err == nil && len(res.Records) > 0
	}, 5*time.Second, 50*time.Millisecond)

	require.Len(t, res.Records, 1)
	require.Contains(t, res.Records[0].UserText, "部署和发布流程")
	require.Contains(t, res.Records[0].AssistantText, "合并到 master")

	// 验证回填方法
	count, err := service.BackfillHistory(ctx, 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, count, 1)
}

// newHistorySummaryService 构造启用记忆与假模型的 AI 服务，返回调用计数指针。
func newHistorySummaryService(t *testing.T, content string, fail bool) (*Service, *memory.Service, *int32) {
	t.Helper()
	var calls int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if fail {
			http.Error(w, "model unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": content},
				"finish_reason": "stop",
			}},
		})
	}))
	t.Cleanup(modelServer.Close)

	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: modelServer.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Memory.Enabled = true

	mem := newTestMemory(t, memory.Config{})
	svc, err := NewService(&cfg, WithMemory(mem))
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	return svc, mem, &calls
}

// recordPublishedHistory 写入一条与「发布流程」相关的历史投影。
func recordPublishedHistory(t *testing.T, mem *memory.Service, ctx context.Context, identity chat.Identity, taskID int64, text string) {
	t.Helper()
	scope, ok := memory.Space(identity)
	require.True(t, ok)
	_, err := mem.RecordProjection(ctx, memory.ProjectionInput{
		TaskID:        taskID,
		Scope:         scope,
		Conversation:  identity.Session.Conversation,
		SenderID:      identity.SenderID,
		UserMessageID: "msg",
		UserText:      text,
		AssistantText: "已确认发布流程",
		TaskStatus:    "completed",
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)
}

// TestHistoryTool_SummaryCacheAndScopeIsolation 验证摘要随原文返回、相同查询复用缓存且不跨作用域共享。
func TestHistoryTool_SummaryCacheAndScopeIsolation(t *testing.T) {
	const summary = "摘要：周五发布，先打标签再由 CI 交付，回归清单仍待补。"
	svc, mem, calls := newHistorySummaryService(t, summary, false)
	alice := personalIdentity("@alice:example.com")
	ctx := chat.WithIdentity(context.Background(), alice)
	recordPublishedHistory(t, mem, ctx, alice, 201, "发布流程是打标签后由 CI 交付")
	recordPublishedHistory(t, mem, ctx, alice, 202, "发布流程还要补回归清单")

	out, err := svc.executeHistoryTool(ctx, map[string]any{"action": "search", "query": "发布流程"})
	require.NoError(t, err)
	text := out.(string)
	require.Contains(t, text, "打标签后由 CI 交付", "原文片段必须保留")
	require.Contains(t, text, "【摘要】")
	require.Contains(t, text, summary)
	require.Equal(t, int32(1), atomic.LoadInt32(calls))

	// 相同作用域、相同查询与记录集合命中缓存，不再请求模型。
	_, err = svc.executeHistoryTool(ctx, map[string]any{"action": "search", "query": "发布流程"})
	require.NoError(t, err)
	require.Equal(t, int32(1), atomic.LoadInt32(calls), "缓存命中时不得重复请求模型")

	// 查询变化需要新的摘要。
	_, err = svc.executeHistoryTool(ctx, map[string]any{"action": "search", "query": "回归清单"})
	require.NoError(t, err)
	require.Equal(t, int32(2), atomic.LoadInt32(calls))

	// 其他用户的作用域不共享缓存。
	bob := personalIdentity("@bob:example.com")
	bobCtx := chat.WithIdentity(context.Background(), bob)
	recordPublishedHistory(t, mem, bobCtx, bob, 203, "发布流程由值班同学执行")
	out, err = svc.executeHistoryTool(bobCtx, map[string]any{"action": "search", "query": "发布流程"})
	require.NoError(t, err)
	require.Contains(t, out.(string), "值班同学")
	require.Equal(t, int32(3), atomic.LoadInt32(calls), "不同作用域必须重新生成摘要")
}

// TestHistoryTool_SummaryDegradesOnModelFailure 验证模型失败时只返回原文，不影响检索结果。
func TestHistoryTool_SummaryDegradesOnModelFailure(t *testing.T) {
	svc, mem, calls := newHistorySummaryService(t, "", true)
	alice := personalIdentity("@alice:example.com")
	ctx := chat.WithIdentity(context.Background(), alice)
	recordPublishedHistory(t, mem, ctx, alice, 301, "发布流程需要两人复核")

	out, err := svc.executeHistoryTool(ctx, map[string]any{"action": "search", "query": "发布流程"})
	require.NoError(t, err)
	text := out.(string)
	require.Contains(t, text, "两人复核", "模型失败时检索结果不受影响")
	require.NotContains(t, text, "【摘要】")
	require.Equal(t, int32(1), atomic.LoadInt32(calls))
}

// TestHistoryTool_SummaryTruncated 验证摘要长度被限制在 MaxHistorySummaryRunes 以内。
func TestHistoryTool_SummaryTruncated(t *testing.T) {
	svc, mem, _ := newHistorySummaryService(t, strings.Repeat("摘", 700), false)
	alice := personalIdentity("@alice:example.com")
	ctx := chat.WithIdentity(context.Background(), alice)
	recordPublishedHistory(t, mem, ctx, alice, 401, "发布流程由发布同学负责")

	out, err := svc.executeHistoryTool(ctx, map[string]any{"action": "search", "query": "发布流程"})
	require.NoError(t, err)
	text := out.(string)
	idx := strings.Index(text, "【摘要】")
	require.GreaterOrEqual(t, idx, 0)
	require.LessOrEqual(t, utf8.RuneCountInString(text[idx+len("【摘要】"):]), memory.MaxHistorySummaryRunes)
}
