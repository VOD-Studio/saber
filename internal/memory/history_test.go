package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
)

func newTestMemory(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")
	svc, err := Open(dbPath, Config{
		GroupWriter: func(id chat.Identity) bool {
			return id.SenderID == "@admin:example.com"
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func testIdentity(platform, account, conversation, sender string, direct bool) chat.Identity {
	return chat.Identity{
		Session: chat.Session{
			Platform:     platform,
			Account:      account,
			Conversation: conversation,
		},
		SenderID: sender,
		Direct:   direct,
	}
}

// TestHistory_TrigramAndShortChinese 验证 >=3 字符 FTS5 全文召回以及 <3 字符短中文子串召回。
func TestHistory_TrigramAndShortChinese(t *testing.T) {
	svc := newTestMemory(t)
	ctx := context.Background()
	aliceID := testIdentity("matrix", "bot", "!room:example.com", "@alice:example.com", false)
	scope, ok := Space(aliceID)
	require.True(t, ok)

	// 插入测试投影
	records := []struct {
		taskID    int64
		user      string
		assistant string
	}{
		{1, "请问我们项目的发布流程是怎样的？", "发布流程是先打 git tag，随后 CI 自动编译并推送到容器镜像仓库。"},
		{2, "今天天气真不错，要不要出去走走？", "确实是个适合散步的好天气！"},
		{3, "如何配置 SQLite WAL 模式？", "通过 PRAGMA journal_mode=WAL; 设置。"},
	}

	for _, r := range records {
		_, err := svc.RecordProjection(ctx, ProjectionInput{
			TaskID:        r.taskID,
			Scope:         scope,
			Conversation:  "!room:example.com",
			SenderID:      "@alice:example.com",
			UserMessageID: fmt.Sprintf("msg_%d", r.taskID),
			UserText:      r.user,
			AssistantText: r.assistant,
			TaskStatus:    "completed",
			CreatedAt:     time.Now(),
		})
		require.NoError(t, err)
	}

	// 1. 三字及以上中文全文召回 (trigram)
	res, err := svc.SearchHistory(ctx, aliceID, HistorySearchOptions{Query: "发布流程"})
	require.NoError(t, err)
	require.Len(t, res.Records, 1)
	require.Equal(t, int64(1), res.Records[0].TaskID)

	res, err = svc.SearchHistory(ctx, aliceID, HistorySearchOptions{Query: "好天气"})
	require.NoError(t, err)
	require.Len(t, res.Records, 1)
	require.Equal(t, int64(2), res.Records[0].TaskID)

	// 英文检索 (>= 3 字符)
	res, err = svc.SearchHistory(ctx, aliceID, HistorySearchOptions{Query: "journal_mode"})
	require.NoError(t, err)
	require.Len(t, res.Records, 1)
	require.Equal(t, int64(3), res.Records[0].TaskID)

	// 2. 两字短中文词召回（验证 SQLite trigram 两字符限制下通过字面匹配正常召回）
	res, err = svc.SearchHistory(ctx, aliceID, HistorySearchOptions{Query: "发布"})
	require.NoError(t, err)
	require.Len(t, res.Records, 1)
	require.Equal(t, int64(1), res.Records[0].TaskID)

	res, err = svc.SearchHistory(ctx, aliceID, HistorySearchOptions{Query: "天气"})
	require.NoError(t, err)
	require.Len(t, res.Records, 1)
	require.Equal(t, int64(2), res.Records[0].TaskID)

	// 一字单字召回
	res, err = svc.SearchHistory(ctx, aliceID, HistorySearchOptions{Query: "散"})
	require.NoError(t, err)
	require.Len(t, res.Records, 1)
	require.Equal(t, int64(2), res.Records[0].TaskID)

	// 不匹配查询
	res, err = svc.SearchHistory(ctx, aliceID, HistorySearchOptions{Query: "不存在的内容"})
	require.NoError(t, err)
	require.Empty(t, res.Records)
}

// TestHistory_ScopeIsolation 验证跨用户私聊与跨群隔离，禁止越权搜索与读取。
func TestHistory_ScopeIsolation(t *testing.T) {
	svc := newTestMemory(t)
	ctx := context.Background()

	aliceDM := testIdentity("matrix", "bot", "!dm_alice:example.com", "@alice:example.com", true)
	bobDM := testIdentity("matrix", "bot", "!dm_bob:example.com", "@bob:example.com", true)
	roomA := testIdentity("matrix", "bot", "!room_a:example.com", "@alice:example.com", false)
	roomB := testIdentity("matrix", "bot", "!room_b:example.com", "@bob:example.com", false)

	aliceScope, _ := Space(aliceDM)
	bobScope, _ := Space(bobDM)
	roomAScope, _ := Space(roomA)

	// 插入 Alice 私聊
	recAlice, err := svc.RecordProjection(ctx, ProjectionInput{
		TaskID:        10,
		Scope:         aliceScope,
		Conversation:  "!dm_alice:example.com",
		SenderID:      "@alice:example.com",
		UserMessageID: "m10",
		UserText:      "Alice 的秘密偏好设置：以后请用日语回答",
		AssistantText: "好的，已了解您的偏好。",
		TaskStatus:    "completed",
	})
	require.NoError(t, err)

	// 插入 Bob 私聊
	recBob, err := svc.RecordProjection(ctx, ProjectionInput{
		TaskID:        20,
		Scope:         bobScope,
		Conversation:  "!dm_bob:example.com",
		SenderID:      "@bob:example.com",
		UserMessageID: "m20",
		UserText:      "Bob 的个人备忘录：服务器密码是 123456",
		AssistantText: "收到。",
		TaskStatus:    "completed",
	})
	require.NoError(t, err)

	// 插入 Room A 群记录
	recRoomA, err := svc.RecordProjection(ctx, ProjectionInput{
		TaskID:        30,
		Scope:         roomAScope,
		Conversation:  "!room_a:example.com",
		SenderID:      "@alice:example.com",
		UserMessageID: "m30",
		UserText:      "Room A 的公共发布流程",
		AssistantText: "本群发布流程已就绪。",
		TaskStatus:    "completed",
	})
	require.NoError(t, err)

	// 1. Bob 在自己私聊检索 Alice 的私聊内容：搜不到
	res, err := svc.SearchHistory(ctx, bobDM, HistorySearchOptions{Query: "秘密偏好"})
	require.NoError(t, err)
	require.Empty(t, res.Records)

	// 2. Alice 在私聊检索 Bob 的私聊内容：搜不到
	res, err = svc.SearchHistory(ctx, aliceDM, HistorySearchOptions{Query: "服务器密码"})
	require.NoError(t, err)
	require.Empty(t, res.Records)

	// 3. Room B 检索 Room A 的内容：搜不到
	res, err = svc.SearchHistory(ctx, roomB, HistorySearchOptions{Query: "公共发布流程"})
	require.NoError(t, err)
	require.Empty(t, res.Records)

	// 4. Bob 尝试读取 Alice 私聊记录的上下文详情：必须返回 ErrNotFound
	_, err = svc.ReadHistoryContext(ctx, bobDM, HistoryContextOptions{ID: recAlice.ID})
	require.ErrorIs(t, err, ErrNotFound)

	// 5. Room A 尝试读取 Alice 私聊记录的上下文详情：必须返回 ErrNotFound
	_, err = svc.ReadHistoryContext(ctx, roomA, HistoryContextOptions{ID: recAlice.ID})
	require.ErrorIs(t, err, ErrNotFound)

	// 6. 拥有者读取自己的上下文：成功
	ctxRes, err := svc.ReadHistoryContext(ctx, aliceDM, HistoryContextOptions{ID: recAlice.ID})
	require.NoError(t, err)
	require.Equal(t, recAlice.ID, ctxRes.Target.ID)
	require.Equal(t, recAlice.UserText, ctxRes.Target.UserText)

	// 7. Room A 读取自己的上下文：成功
	ctxRes, err = svc.ReadHistoryContext(ctx, roomA, HistoryContextOptions{ID: recRoomA.ID})
	require.NoError(t, err)
	require.Equal(t, recRoomA.ID, ctxRes.Target.ID)

	_ = recBob
}

// TestHistory_PaginationCursor 验证历史检索的分页游标与截断标志。
func TestHistory_PaginationCursor(t *testing.T) {
	svc := newTestMemory(t)
	ctx := context.Background()
	id := testIdentity("matrix", "bot", "!room_page:example.com", "@alice:example.com", false)
	scope, _ := Space(id)

	for i := 1; i <= 6; i++ {
		_, err := svc.RecordProjection(ctx, ProjectionInput{
			TaskID:        int64(i),
			Scope:         scope,
			Conversation:  "!room_page:example.com",
			SenderID:      "@alice:example.com",
			UserMessageID: fmt.Sprintf("msg_%d", i),
			UserText:      fmt.Sprintf("统一话题讨论第 %d 轮", i),
			AssistantText: fmt.Sprintf("这是第 %d 轮回复", i),
			TaskStatus:    "completed",
		})
		require.NoError(t, err)
	}

	// 查第 1 页，limit=2
	p1, err := svc.SearchHistory(ctx, id, HistorySearchOptions{Query: "统一话题", Limit: 2})
	require.NoError(t, err)
	require.Len(t, p1.Records, 2)
	require.True(t, p1.HasMore)
	require.Equal(t, int64(6), p1.Records[0].TaskID)
	require.Equal(t, int64(5), p1.Records[1].TaskID)
	require.Equal(t, p1.Records[1].ID, p1.NextCursor)

	// 查第 2 页，使用 Cursor
	p2, err := svc.SearchHistory(ctx, id, HistorySearchOptions{Query: "统一话题", Limit: 2, Cursor: p1.NextCursor})
	require.NoError(t, err)
	require.Len(t, p2.Records, 2)
	require.True(t, p2.HasMore)
	require.Equal(t, int64(4), p2.Records[0].TaskID)
	require.Equal(t, int64(3), p2.Records[1].TaskID)

	// 查第 3 页
	p3, err := svc.SearchHistory(ctx, id, HistorySearchOptions{Query: "统一话题", Limit: 2, Cursor: p2.NextCursor})
	require.NoError(t, err)
	require.Len(t, p3.Records, 2)
	require.False(t, p3.HasMore)
	require.Equal(t, int64(2), p3.Records[0].TaskID)
	require.Equal(t, int64(1), p3.Records[1].TaskID)
	require.Zero(t, p3.NextCursor)
}

// TestHistory_ContextBeforeAfter 验证指定记录的前后文读取与顺序。
func TestHistory_ContextBeforeAfter(t *testing.T) {
	svc := newTestMemory(t)
	ctx := context.Background()
	id := testIdentity("matrix", "bot", "!room_ctx:example.com", "@alice:example.com", false)
	scope, _ := Space(id)

	var recs []HistoryRecord
	for i := 1; i <= 5; i++ {
		r, err := svc.RecordProjection(ctx, ProjectionInput{
			TaskID:        int64(i),
			Scope:         scope,
			Conversation:  "!room_ctx:example.com",
			SenderID:      "@alice:example.com",
			UserMessageID: fmt.Sprintf("msg_%d", i),
			UserText:      fmt.Sprintf("对话消息 %d", i),
			AssistantText: fmt.Sprintf("对话回答 %d", i),
			TaskStatus:    "completed",
		})
		require.NoError(t, err)
		recs = append(recs, r)
	}

	target := recs[2] // ID=3, TaskID=3
	res, err := svc.ReadHistoryContext(ctx, id, HistoryContextOptions{
		ID:     target.ID,
		Before: 2,
		After:  2,
	})
	require.NoError(t, err)
	require.Equal(t, target.ID, res.Target.ID)

	// Before 必须是时间升序：recs[0], recs[1]
	require.Len(t, res.Before, 2)
	require.Equal(t, recs[0].ID, res.Before[0].ID)
	require.Equal(t, recs[1].ID, res.Before[1].ID)

	// After 必须是时间升序：recs[3], recs[4]
	require.Len(t, res.After, 2)
	require.Equal(t, recs[3].ID, res.After[0].ID)
	require.Equal(t, recs[4].ID, res.After[1].ID)
}

// TestHistory_IdempotencyAndUpdates 验证相同任务重复投递时幂等更新且 FTS 索引同步。
func TestHistory_IdempotencyAndUpdates(t *testing.T) {
	svc := newTestMemory(t)
	ctx := context.Background()
	id := testIdentity("matrix", "bot", "!room_idem:example.com", "@alice:example.com", false)
	scope, _ := Space(id)

	// 首次记录
	_, err := svc.RecordProjection(ctx, ProjectionInput{
		TaskID:        99,
		Scope:         scope,
		Conversation:  "!room_idem:example.com",
		SenderID:      "@alice:example.com",
		UserMessageID: "msg_99",
		UserText:      "原始输入文本",
		AssistantText: "旧的回答内容苹果树",
		TaskStatus:    "running",
	})
	require.NoError(t, err)

	res, err := svc.SearchHistory(ctx, id, HistorySearchOptions{Query: "苹果树"})
	require.NoError(t, err)
	require.Len(t, res.Records, 1)

	// 终态更新相同任务编号
	_, err = svc.RecordProjection(ctx, ProjectionInput{
		TaskID:        99,
		Scope:         scope,
		Conversation:  "!room_idem:example.com",
		SenderID:      "@alice:example.com",
		UserMessageID: "msg_99",
		UserText:      "原始输入文本",
		AssistantText: "最终回答内容香蕉船",
		TaskStatus:    "completed",
	})
	require.NoError(t, err)

	// 旧词应不再搜出
	resOld, err := svc.SearchHistory(ctx, id, HistorySearchOptions{Query: "苹果树"})
	require.NoError(t, err)
	require.Empty(t, resOld.Records)

	// 新词可搜出
	resNew, err := svc.SearchHistory(ctx, id, HistorySearchOptions{Query: "香蕉船"})
	require.NoError(t, err)
	require.Len(t, resNew.Records, 1)
	require.Equal(t, "completed", resNew.Records[0].TaskStatus)
}
