package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
)

func newTestMemoryService(t *testing.T, groupWriter func(chat.Identity) bool) *Service {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "memory.db")
	svc, err := Open(dbPath, Config{
		MaxChars:    1000,
		InjectBytes: 2000,
		GroupWriter: groupWriter,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func TestReview_PauseAndResume(t *testing.T) {
	svc := newTestMemoryService(t, func(i chat.Identity) bool {
		return i.SenderID == "@admin:test"
	})
	ctx := context.Background()

	userMsg := chat.Identity{
		Session:  chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test"},
		SenderID: "@user:test",
		Direct:   true,
	}
	userScope, _ := Space(userMsg)

	// 1. 默认未暂停
	paused, err := svc.IsPaused(ctx, userScope)
	require.NoError(t, err)
	require.False(t, paused)

	// 2. 个人空间用户可暂停
	err = svc.Pause(ctx, userMsg)
	require.NoError(t, err)
	paused, err = svc.IsPaused(ctx, userScope)
	require.NoError(t, err)
	require.True(t, paused)

	// 3. 个人空间用户可恢复
	err = svc.Resume(ctx, userMsg)
	require.NoError(t, err)
	paused, err = svc.IsPaused(ctx, userScope)
	require.NoError(t, err)
	require.False(t, paused)

	// 4. 群空间普通成员不可控制暂停
	groupMember := chat.Identity{
		Session:  chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test"},
		SenderID: "@member:test",
		Direct:   false,
	}
	groupAdmin := chat.Identity{
		Session:  chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test"},
		SenderID: "@admin:test",
		Direct:   false,
	}
	groupScope, _ := Space(groupMember)

	err = svc.Pause(ctx, groupMember)
	require.Error(t, err)
	require.Contains(t, err.Error(), "只有本群记忆管理员可以控制自动学习")

	// 5. 群管理员可以暂停与恢复
	err = svc.Pause(ctx, groupAdmin)
	require.NoError(t, err)
	paused, err = svc.IsPaused(ctx, groupScope)
	require.NoError(t, err)
	require.True(t, paused)

	err = svc.Resume(ctx, groupAdmin)
	require.NoError(t, err)
	paused, err = svc.IsPaused(ctx, groupScope)
	require.NoError(t, err)
	require.False(t, paused)
}

func TestReview_SourceExclusionOnRemoveAndReject(t *testing.T) {
	svc := newTestMemoryService(t, func(i chat.Identity) bool {
		return i.SenderID == "@admin:test"
	})
	ctx := context.Background()

	userMsg := chat.Identity{
		Session:  chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test"},
		SenderID: "@user:test",
		Direct:   true,
	}
	userScope, _ := Space(userMsg)

	// 1. 添加一条带 SourceTask 的个人记忆
	mut, err := svc.Add(ctx, userMsg, "偏好黑暗模式", Source{Task: 101}, true)
	require.NoError(t, err)
	require.Equal(t, int64(101), mut.Entry.SourceTask)

	// 任务 101 当前未被排除
	excluded, err := svc.IsSourceExcluded(ctx, userScope, 101)
	require.NoError(t, err)
	require.False(t, excluded)

	// 2. 删除该记忆后，来源任务 101 自动被排除
	_, err = svc.Remove(ctx, userMsg, mut.Entry.ID, mut.Entry.Version, Source{})
	require.NoError(t, err)

	excluded, err = svc.IsSourceExcluded(ctx, userScope, 101)
	require.NoError(t, err)
	require.True(t, excluded, "删除条目后其来源任务应自动被排除")

	// 3. 群聊中提出建议并拒绝
	groupMember := chat.Identity{
		Session:  chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!group:test"},
		SenderID: "@member:test",
		Direct:   false,
	}
	groupAdmin := chat.Identity{
		Session:  chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!group:test"},
		SenderID: "@admin:test",
		Direct:   false,
	}
	groupScope, _ := Space(groupMember)

	propMut, err := svc.Add(ctx, groupMember, "群内建议周五上线", Source{Task: 202}, false)
	require.NoError(t, err)
	require.True(t, propMut.Suggested)
	require.Equal(t, int64(202), propMut.Change.SourceTask)

	// 管理员拒绝建议
	_, err = svc.Reject(ctx, groupAdmin, propMut.Change.ID)
	require.NoError(t, err)

	excluded, err = svc.IsSourceExcluded(ctx, groupScope, 202)
	require.NoError(t, err)
	require.True(t, excluded, "拒绝建议后其来源任务应自动被排除")
}

func TestReview_UnreviewedProjectionsAndMarking(t *testing.T) {
	svc := newTestMemoryService(t, nil)
	ctx := context.Background()

	scope := Scope{Kind: ScopeUser, Platform: "matrix", Account: "@bot:test", ID: "@alice:test"}

	// 写入 3 条投影记录
	for i := int64(1); i <= 3; i++ {
		_, err := svc.RecordProjection(ctx, ProjectionInput{
			TaskID:        i,
			Scope:         scope,
			Conversation:  "dm",
			SenderID:      "@alice:test",
			UserMessageID: "msg",
			UserText:      "问题",
			AssistantText: "回答",
			TaskStatus:    "completed",
			CreatedAt:     time.Now().Add(time.Duration(i) * time.Minute),
		})
		require.NoError(t, err)
	}

	// 排除任务 2
	require.NoError(t, svc.ExcludeSource(ctx, scope, 2, "测试排除"))

	// 查询未复盘记录，应返回 1 和 3（2 已被排除）
	records, err := svc.UnreviewedProjections(ctx, scope, 10)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, int64(1), records[0].TaskID)
	require.Equal(t, int64(3), records[1].TaskID)

	// 标记任务 1 已复盘
	require.NoError(t, svc.MarkTasksReviewed(ctx, scope, []int64{1}))

	// 再次查询未复盘记录，只剩 3
	records, err = svc.UnreviewedProjections(ctx, scope, 10)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, int64(3), records[0].TaskID)

	// 验证 ScopeSettings 记录了 last_reviewed_task_id
	settings, err := svc.ScopeSettings(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, int64(1), settings.LastReviewedTaskID)
}

func TestReview_AutoAddReplaceRemoveRules(t *testing.T) {
	svc := newTestMemoryService(t, nil)
	ctx := context.Background()

	// 1. 个人空间 AutoAdd 直接生效
	userMsg := chat.Identity{
		Session:  chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test"},
		SenderID: "@user:test",
		Direct:   true,
	}
	mut, err := svc.AutoAdd(ctx, userMsg, "自动提炼的偏好：喜欢简洁", Source{Task: 301})
	require.NoError(t, err)
	require.False(t, mut.Suggested, "个人空间 AutoAdd 应直接生效")
	require.False(t, mut.Entry.Explicit, "自动提炼条目 Explicit 应为 false")
	require.Equal(t, "自动提炼的偏好：喜欢简洁", mut.Entry.Content)

	// 重复内容不报错，返回 Duplicate
	dupMut, err := svc.AutoAdd(ctx, userMsg, "自动提炼的偏好：喜欢简洁", Source{Task: 302})
	require.NoError(t, err)
	require.True(t, dupMut.Duplicate)

	// 2. 个人空间 AutoReplace 必须形成待确认建议！
	repMut, err := svc.AutoReplace(ctx, userMsg, mut.Entry.ID, mut.Entry.Version, "更新后的偏好：喜欢极简", Source{Task: 303})
	require.NoError(t, err)
	require.True(t, repMut.Suggested, "个人空间后台 AutoReplace 必须形成建议，不能直接覆盖")
	require.Equal(t, ActionReplace, repMut.Change.Action)

	// 3. 个人空间 AutoRemove 必须形成待确认建议！
	remMut, err := svc.AutoRemove(ctx, userMsg, mut.Entry.ID, mut.Entry.Version, Source{Task: 304})
	require.NoError(t, err)
	require.True(t, remMut.Suggested, "个人空间后台 AutoRemove 必须形成建议，不能直接删除")
	require.Equal(t, ActionRemove, remMut.Change.Action)

	// 4. 群空间 AutoAdd 也必须形成建议！
	groupMsg := chat.Identity{
		Session:  chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!group:test"},
		SenderID: "@member:test",
		Direct:   false,
	}
	gMut, err := svc.AutoAdd(ctx, groupMsg, "群共识：每周三代码评审", Source{Task: 401})
	require.NoError(t, err)
	require.True(t, gMut.Suggested, "群聊空间后台 AutoAdd 必须形成建议")
	require.Equal(t, ActionAdd, gMut.Change.Action)

	// 5. 暂停状态下 AutoAdd / Replace / Remove 均返回 ErrPaused
	require.NoError(t, svc.Pause(ctx, userMsg))
	_, err = svc.AutoAdd(ctx, userMsg, "暂停时提炼", Source{Task: 501})
	require.ErrorIs(t, err, ErrPaused)
	_, err = svc.AutoReplace(ctx, userMsg, mut.Entry.ID, mut.Entry.Version, "暂停时替换", Source{Task: 502})
	require.ErrorIs(t, err, ErrPaused)
	_, err = svc.AutoRemove(ctx, userMsg, mut.Entry.ID, mut.Entry.Version, Source{Task: 503})
	require.ErrorIs(t, err, ErrPaused)
}

func TestReview_UsageAndStatus(t *testing.T) {
	svc := newTestMemoryService(t, nil)
	ctx := context.Background()

	userMsg := chat.Identity{
		Session:  chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test"},
		SenderID: "@user:test",
		Direct:   true,
	}
	userScope, _ := Space(userMsg)

	// 添加一条记忆与一条待确认变更
	_, err := svc.Add(ctx, userMsg, "偏好一", Source{}, true)
	require.NoError(t, err)
	_, err = svc.AutoReplace(ctx, userMsg, 1, 1, "偏好一替换建议", Source{})
	require.NoError(t, err)

	// 记录复盘使用量
	require.NoError(t, svc.RecordReviewUsage(ctx, userScope, 150))
	require.NoError(t, svc.RecordReviewUsage(ctx, userScope, 200))

	// 查询状态
	status, err := svc.Status(ctx, userMsg)
	require.NoError(t, err)
	require.Equal(t, 1, status.Usage.Count)
	require.Equal(t, 2, status.ReviewCount)
	require.Equal(t, 350, status.TokenCount)
	require.Equal(t, 1, status.PendingCount)
	require.False(t, status.Paused)

	// 全局统计
	globalReviews, globalTokens, err := svc.GlobalUsage(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, globalReviews)
	require.Equal(t, 350, globalTokens)
}
