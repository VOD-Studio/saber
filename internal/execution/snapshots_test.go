package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
)

// snapshotEnv 构造一个已授权任务的执行上下文。
func snapshotEnv(t *testing.T) (*Executor, context.Context, scope, string) {
	t.Helper()
	cfg, identity := fixture(t)
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	dir, err := e.Workspace(identity)
	require.NoError(t, err)
	ctx := WithTask(chat.WithIdentity(context.Background(), identity), 9, dir)
	run, ok := ctx.Value(scopeKey{}).(scope)
	require.True(t, ok)
	return e, ctx, run, dir
}

// listSnapshotRecords 通过工具读取当前任务的快照摘要。
func listSnapshotRecords(t *testing.T, e *Executor, ctx context.Context) []snapshotSummary {
	t.Helper()
	result, err := e.Run(ctx, "snapshot", map[string]any{"action": "list"})
	require.NoError(t, err)
	summaries, ok := result.Data.(map[string]any)["snapshots"].([]snapshotSummary)
	require.True(t, ok)
	return summaries
}

// TestSnapshot_RollbackRestoresContent 验证 write_file 修改前自动快照并可回滚恢复。
func TestSnapshot_RollbackRestoresContent(t *testing.T) {
	e, ctx, _, dir := snapshotEnv(t)
	target := filepath.Join(dir, "notes.txt")
	require.NoError(t, os.WriteFile(target, []byte("first\n"), 0600))

	_, err := e.Run(ctx, "write_file", map[string]any{"path": "notes.txt", "content": "second\n"})
	require.NoError(t, err)
	summaries := listSnapshotRecords(t, e, ctx)
	require.Len(t, summaries, 1)
	require.Equal(t, "notes.txt", summaries[0].Path)
	require.False(t, summaries[0].Absent)

	_, err = e.Run(ctx, "write_file", map[string]any{"path": "notes.txt", "content": "third\n"})
	require.NoError(t, err)

	result, err := e.Run(ctx, "snapshot", map[string]any{"action": "rollback", "snapshot_id": summaries[0].ID})
	require.NoError(t, err)
	data := result.Data.(map[string]any)
	require.Equal(t, true, data["restored"])
	pre, ok := data["pre_rollback_snapshot_id"].(string)
	require.True(t, ok, "覆盖不同内容时必须返回 pre_rollback_snapshot_id")
	require.NotEmpty(t, pre)

	content, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "first\n", string(content))

	// 回滚前保存的当前内容仍可再回滚。
	_, err = e.Run(ctx, "snapshot", map[string]any{"action": "rollback", "snapshot_id": pre})
	require.NoError(t, err)
	content, err = os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "third\n", string(content))

	// 快照文件权限为 0600，list 不包含正文。
	entries, err := os.ReadDir(filepath.Join(e.snapshotDir(9)))
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, entry := range entries {
		info, err := entry.Info()
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
}

// TestSnapshot_RollbackDeletesCreatedFile 验证新建文件的 absent 快照回滚即删除。
func TestSnapshot_RollbackDeletesCreatedFile(t *testing.T) {
	e, ctx, _, dir := snapshotEnv(t)
	_, err := e.Run(ctx, "write_file", map[string]any{"path": "created.txt", "content": "body"})
	require.NoError(t, err)
	summaries := listSnapshotRecords(t, e, ctx)
	require.Len(t, summaries, 1)
	require.True(t, summaries[0].Absent)

	result, err := e.Run(ctx, "snapshot", map[string]any{"action": "rollback", "path": "created.txt"})
	require.NoError(t, err)
	require.Equal(t, true, result.Data.(map[string]any)["deleted"])
	require.NoFileExists(t, filepath.Join(dir, "created.txt"))
}

// TestSnapshot_ApplyPatchRollback 验证 apply_patch 的编辑前快照。
func TestSnapshot_ApplyPatchRollback(t *testing.T) {
	e, ctx, _, dir := snapshotEnv(t)
	target := filepath.Join(dir, "app.txt")
	require.NoError(t, os.WriteFile(target, []byte("alpha beta\n"), 0600))

	_, err := e.Run(ctx, "apply_patch", map[string]any{"path": "app.txt", "edits": []any{map[string]any{"old": "alpha", "new": "gamma"}}})
	require.NoError(t, err)
	_, err = e.Run(ctx, "snapshot", map[string]any{"action": "rollback", "path": "app.txt"})
	require.NoError(t, err)
	content, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "alpha beta\n", string(content))
}

// TestSnapshot_RejectsTraversalAndInvalidArgs 验证越界路径与非法参数被拒绝。
func TestSnapshot_RejectsTraversalAndInvalidArgs(t *testing.T) {
	e, ctx, _, _ := snapshotEnv(t)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"向上穿越", map[string]any{"action": "rollback", "path": "../escape.txt"}},
		{"绝对路径", map[string]any{"action": "rollback", "path": "/etc/passwd"}},
		{"未知快照", map[string]any{"action": "rollback", "snapshot_id": "0123456789abcdef0123456789abcdef"}},
		{"list 不接受其他参数", map[string]any{"action": "list", "path": "notes.txt"}},
		{"未知动作", map[string]any{"action": "undo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.Run(ctx, "snapshot", tc.args)
			require.Error(t, err)
		})
	}
	require.NoError(t, validateArgs("snapshot", map[string]any{"action": "list"}))
	require.Error(t, validateArgs("snapshot", map[string]any{"action": "list", "unknown": true}))
	require.Error(t, validateArgs("snapshot", map[string]any{}))
}

// TestSnapshot_ToolRequiresWriteAuthorization 验证快照工具只随写入能力暴露与调用。
func TestSnapshot_ToolRequiresWriteAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tools []string
		want  bool
	}{
		{"仅读取", []string{"read_file", "list_files"}, false},
		{"可写文件", []string{"read_file", "write_file"}, true},
		{"可打补丁", []string{"read_file", "apply_patch"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, identity := fixture(t)
			cfg.Grants[0].Tools = tc.tools
			e, err := New(cfg, nil, nil)
			require.NoError(t, err)
			dir, err := e.Workspace(identity)
			require.NoError(t, err)
			ctx := WithTask(chat.WithIdentity(context.Background(), identity), 3, dir)
			names := make(map[string]bool)
			for _, tool := range e.Tools(ctx) {
				names[tool.Function.Name] = true
			}
			require.Equal(t, tc.want, names["snapshot"])
			_, err = e.Run(ctx, "snapshot", map[string]any{"action": "list"})
			if tc.want {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestSnapshot_RequiresPersistentTask 验证快照工具不能脱离任务调用。
func TestSnapshot_RequiresPersistentTask(t *testing.T) {
	e, _, _, _ := snapshotEnv(t)
	noTask := chat.WithIdentity(context.Background(), chat.Identity{})
	require.Error(t, e.Check(noTask, "snapshot"))
	require.False(t, e.snapshotAllowed(noTask))
}
