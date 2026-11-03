package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHistorySummary_CacheTTLAndScopeIsolation 验证摘要缓存命中、记录集合变化、TTL 过期与跨作用域隔离。
func TestHistorySummary_CacheTTLAndScopeIsolation(t *testing.T) {
	svc := newTestMemory(t)
	ctx := context.Background()
	roomA := testIdentity("matrix", "bot", "!room-a:example.com", "@alice:example.com", false)
	roomB := testIdentity("matrix", "bot", "!room-b:example.com", "@alice:example.com", false)
	scopeA, ok := Space(roomA)
	require.True(t, ok)
	scopeB, ok := Space(roomB)
	require.True(t, ok)
	require.NotEqual(t, scopeA.Key(), scopeB.Key())

	hash := HashHistoryQuery(scopeA, "发布流程", 5, 0)
	require.NotEqual(t, hash, HashHistoryQuery(scopeA, "发布流程", 10, 0), "条数参与指纹")
	require.NotEqual(t, hash, HashHistoryQuery(scopeA, "发布流程", 5, 100), "游标参与指纹")
	require.NotEqual(t, hash, HashHistoryQuery(scopeA, "上线流程", 5, 0), "检索词参与指纹")
	require.NotEqual(t, hash, HashHistoryQuery(scopeB, "发布流程", 5, 0), "作用域参与指纹")

	// 未写入时未命中。
	_, found, err := svc.CachedHistorySummary(ctx, scopeA, hash, 42)
	require.NoError(t, err)
	require.False(t, found)

	require.NoError(t, svc.SaveHistorySummary(ctx, scopeA, hash, 42, " 周五发布，待补回归清单 "))
	cached, found, err := svc.CachedHistorySummary(ctx, scopeA, hash, 42)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "周五发布，待补回归清单", cached.Summary)
	require.Equal(t, int64(42), cached.LastRecordID)

	// 记录集合变化（最大记录 ID 不同）不复用。
	_, found, err = svc.CachedHistorySummary(ctx, scopeA, hash, 43)
	require.NoError(t, err)
	require.False(t, found, "命中记录集合变化时必须重新生成")

	// 其他作用域不得读取本作用域的缓存。
	_, found, err = svc.CachedHistorySummary(ctx, scopeB, hash, 42)
	require.NoError(t, err)
	require.False(t, found, "不得跨作用域读取摘要缓存")
	_, found, err = svc.CachedHistorySummary(ctx, scopeB, HashHistoryQuery(scopeB, "发布流程", 5, 0), 42)
	require.NoError(t, err)
	require.False(t, found)

	// 覆盖同一键时刷新内容与时间。
	require.NoError(t, svc.SaveHistorySummary(ctx, scopeA, hash, 42, "周五发布，回归清单已更新"))
	cached, found, err = svc.CachedHistorySummary(ctx, scopeA, hash, 42)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "周五发布，回归清单已更新", cached.Summary)

	// TTL 过期后不再命中。
	expired := time.Now().Add(-HistorySummaryTTL - time.Minute).UnixMilli()
	_, err = svc.store.db.ExecContext(ctx, `UPDATE history_summaries SET created_at=?`, expired)
	require.NoError(t, err)
	_, found, err = svc.CachedHistorySummary(ctx, scopeA, hash, 42)
	require.NoError(t, err)
	require.False(t, found, "超过 TTL 的缓存必须失效")
}

// TestHistorySummary_InvalidInput 验证缓存写入拒绝无效键与空摘要。
func TestHistorySummary_InvalidInput(t *testing.T) {
	svc := newTestMemory(t)
	ctx := context.Background()
	identity := testIdentity("matrix", "bot", "!room:example.com", "@alice:example.com", false)
	scope, ok := Space(identity)
	require.True(t, ok)

	require.Error(t, svc.SaveHistorySummary(ctx, scope, "", 1, "摘要"))
	require.Error(t, svc.SaveHistorySummary(ctx, scope, "hash", 0, "摘要"))
	require.Error(t, svc.SaveHistorySummary(ctx, scope, "hash", 1, "   "))
	require.Error(t, svc.SaveHistorySummary(ctx, Scope{}, "hash", 1, "摘要"))

	_, found, err := svc.CachedHistorySummary(ctx, scope, "", 1)
	require.NoError(t, err)
	require.False(t, found)
}

// TestSearchHistory_ReturnsQueryHash 验证检索结果携带与作用域、参数一致的查询指纹。
func TestSearchHistory_ReturnsQueryHash(t *testing.T) {
	svc := newTestMemory(t)
	ctx := context.Background()
	identity := testIdentity("matrix", "bot", "!room:example.com", "@alice:example.com", false)
	scope, ok := Space(identity)
	require.True(t, ok)
	require.NoError(t, insertProjection(svc, ctx, scope, 701, "周五发布流程确认"))
	require.NoError(t, insertProjection(svc, ctx, scope, 702, "发布流程回归清单补充"))

	first, err := svc.SearchHistory(ctx, identity, HistorySearchOptions{Query: "发布流程", Limit: 1})
	require.NoError(t, err)
	require.Len(t, first.Records, 1)
	require.True(t, first.HasMore)
	require.Equal(t, HashHistoryQuery(scope, "发布流程", 1, 0), first.QueryHash)

	second, err := svc.SearchHistory(ctx, identity, HistorySearchOptions{Query: "发布流程", Limit: 1, Cursor: first.NextCursor})
	require.NoError(t, err)
	require.NotEqual(t, first.QueryHash, second.QueryHash, "游标不同的检索使用不同指纹")
}

// insertProjection 写入一条测试用历史投影。
func insertProjection(svc *Service, ctx context.Context, scope Scope, taskID int64, userText string) error {
	_, err := svc.RecordProjection(ctx, ProjectionInput{
		TaskID:        taskID,
		Scope:         scope,
		Conversation:  scope.ID,
		SenderID:      "@alice:example.com",
		UserMessageID: "msg",
		UserText:      userText,
		AssistantText: "已记录",
		TaskStatus:    "completed",
		CreatedAt:     time.UnixMilli(int64(1700000000000 + taskID)),
	})
	return err
}
