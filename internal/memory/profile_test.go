package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
)

// newTestProfileService 打开一个容量充足、带群管理员判定的记忆服务，用于画像边界测试。
func newTestProfileService(t *testing.T) *Service {
	t.Helper()
	svc, err := Open(filepath.Join(t.TempDir(), "memory.db"), Config{
		MaxChars: 200000,
		GroupWriter: func(id chat.Identity) bool {
			return id.SenderID == "@admin:example.com"
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// TestProfileSource_ExplicitFirstAndCaps 验证画像输入优先使用明确保存的条目，并受条目数与字节预算限制。
func TestProfileSource_ExplicitFirstAndCaps(t *testing.T) {
	t.Run("明确保存优先", func(t *testing.T) {
		svc := newTestProfileService(t)
		ctx := context.Background()
		identity := testIdentity("matrix", "bot", "dm", "@alice:example.com", true)
		_, err := svc.Add(ctx, identity, "自动提炼的偏好", Source{}, false)
		require.NoError(t, err)
		time.Sleep(2 * time.Millisecond)
		_, err = svc.Add(ctx, identity, "用户明确要求：回答用中文", Source{}, true)
		require.NoError(t, err)

		scope, ok := Space(identity)
		require.True(t, ok)
		src, err := svc.ProfileSource(ctx, scope)
		require.NoError(t, err)
		require.Len(t, src.Entries, 2)
		require.True(t, src.Entries[0].Explicit, "明确保存的条目应排在最前")
		require.Contains(t, src.Text, "回答用中文")
		require.Zero(t, src.Truncated)
		require.Equal(t, scope.Key(), src.Scope.Key())
	})

	t.Run("条目数上限", func(t *testing.T) {
		svc := newTestProfileService(t)
		ctx := context.Background()
		identity := testIdentity("matrix", "bot", "dm", "@alice:example.com", true)
		for i := range ProfileSourceEntries + 5 {
			_, err := svc.Add(ctx, identity, fmt.Sprintf("条目 %02d", i), Source{}, i%2 == 0)
			require.NoError(t, err)
		}
		scope, ok := Space(identity)
		require.True(t, ok)
		src, err := svc.ProfileSource(ctx, scope)
		require.NoError(t, err)
		require.Len(t, src.Entries, ProfileSourceEntries)
		require.Equal(t, 5, src.Truncated)
	})

	t.Run("字节上限", func(t *testing.T) {
		svc := newTestProfileService(t)
		ctx := context.Background()
		identity := testIdentity("matrix", "bot", "dm", "@alice:example.com", true)
		for i := range 30 {
			_, err := svc.Add(ctx, identity, fmt.Sprintf("%02d-%s", i, strings.Repeat("记忆", 100)), Source{}, false)
			require.NoError(t, err)
		}
		scope, ok := Space(identity)
		require.True(t, ok)
		src, err := svc.ProfileSource(ctx, scope)
		require.NoError(t, err)
		require.LessOrEqual(t, len(src.Text), ProfileSourceBytes, "输入文本不得超过 8 KiB")
		require.Greater(t, src.Truncated, 0)
		require.NotEmpty(t, src.Entries)
	})
}

// TestProfile_StateStaleAndIsolation 验证画像缺失、生成、过期判定以及群/私聊作用域隔离。
func TestProfile_StateStaleAndIsolation(t *testing.T) {
	svc := newTestProfileService(t)
	ctx := context.Background()
	alice := testIdentity("matrix", "bot", "dm", "@alice:example.com", true)
	room := testIdentity("matrix", "bot", "!room:example.com", "@alice:example.com", false)

	// 空空间没有画像，版本为 "0"，无需刷新。
	empty, err := svc.Profile(ctx, alice)
	require.NoError(t, err)
	require.False(t, empty.Exists)
	require.True(t, empty.Stale)
	require.Equal(t, "0", empty.Version)

	_, err = svc.Add(ctx, alice, "回答尽量简洁", Source{}, true)
	require.NoError(t, err)
	state, err := svc.Profile(ctx, alice)
	require.NoError(t, err)
	require.False(t, state.Exists)
	require.True(t, state.Stale)
	require.NotEqual(t, "0", state.Version)

	src, err := svc.ProfileSource(ctx, state.Scope)
	require.NoError(t, err)
	saved, err := svc.SaveProfile(ctx, state.Scope, "偏好：回答简洁", src.Version)
	require.NoError(t, err)
	require.True(t, saved.Exists)
	require.False(t, saved.Stale)
	require.Equal(t, "偏好：回答简洁", saved.Content)

	// 条目更新后画像过期。
	time.Sleep(2 * time.Millisecond)
	_, err = svc.Add(ctx, alice, "固定使用中文", Source{}, true)
	require.NoError(t, err)
	stale, err := svc.Profile(ctx, alice)
	require.NoError(t, err)
	require.True(t, stale.Exists)
	require.True(t, stale.Stale, "记忆条目变化后画像必须视为过期")
	require.Equal(t, "偏好：回答简洁", stale.Content)

	// 群空间看不到个人画像，个人空间也看不到群画像。
	groupProfile, err := svc.Profile(ctx, room)
	require.NoError(t, err)
	require.False(t, groupProfile.Exists, "群空间不得读取个人画像")

	groupSrc, err := svc.ProfileSource(ctx, groupProfile.Scope)
	require.NoError(t, err)
	require.Empty(t, groupSrc.Entries, "群画像只使用本群条目")
	_, err = svc.SaveProfile(ctx, groupProfile.Scope, "本群约定：周五发布", groupSrc.Version)
	require.NoError(t, err)

	reloaded, err := svc.Profile(ctx, room)
	require.NoError(t, err)
	require.Equal(t, "本群约定：周五发布", reloaded.Content)
	personal, err := svc.Profile(ctx, alice)
	require.NoError(t, err)
	require.Equal(t, "偏好：回答简洁", personal.Content, "个人画像不受群画像影响")
}

// TestProfile_SaveTruncatesAndValidates 验证画像正文截断与空内容拒绝。
func TestProfile_SaveTruncatesAndValidates(t *testing.T) {
	svc := newTestProfileService(t)
	ctx := context.Background()
	identity := testIdentity("matrix", "bot", "dm", "@alice:example.com", true)
	scope, ok := Space(identity)
	require.True(t, ok)

	_, err := svc.SaveProfile(ctx, scope, "   ", "")
	require.Error(t, err)

	long := strings.Repeat("偏好", MaxProfileRunes)
	saved, err := svc.SaveProfile(ctx, scope, long, "")
	require.NoError(t, err)
	require.LessOrEqual(t, len([]rune(saved.Content)), MaxProfileRunes)
	require.NotEmpty(t, saved.Content)
	require.False(t, saved.Stale, "空空间写入画像后与当前版本一致")
}

// TestProfile_CanWriteProfile 验证个人空间允许本人重建，群空间只允许记忆管理员重建。
func TestProfile_CanWriteProfile(t *testing.T) {
	svc := newTestProfileService(t)
	ctx := context.Background()
	personal := testIdentity("matrix", "bot", "dm", "@alice:example.com", true)
	member := testIdentity("matrix", "bot", "!room:example.com", "@bob:example.com", false)
	admin := testIdentity("matrix", "bot", "!room:example.com", "@admin:example.com", false)

	allowed, err := svc.CanWriteProfile(ctx, personal)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = svc.CanWriteProfile(ctx, member)
	require.NoError(t, err)
	require.False(t, allowed, "普通群成员不得重建群画像")
	allowed, err = svc.CanWriteProfile(ctx, admin)
	require.NoError(t, err)
	require.True(t, allowed)
}
