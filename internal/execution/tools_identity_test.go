package execution

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
)

// TestValidateArgs_RejectsIdentityKeys 锁定工具参数不得携带身份字段。
//
// 身份只能来自可信上下文（chat.WithIdentity + WithTask），模型参数不能自报
// user_id、room_id、sender、owner、platform、account 或 session；工具清单取自
// Tools(ctx) 的实际定义，新增本地工具若没有对应样例会让本测试失败。
func TestValidateArgs_RejectsIdentityKeys(t *testing.T) {
	cfg, identity := fixture(t)
	cfg.Grants[0].Tools = []string{"exec", "read_output", "read_file", "list_files", "write_file", "apply_patch", "search_files", "glob_files", "process"}
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	dir, err := e.Workspace(identity)
	require.NoError(t, err)

	valid := map[string]map[string]any{
		"exec":         {"command": "true"},
		"read_output":  {"output_id": "t1-0123456789abcdef0123456789abcdef"},
		"read_file":    {"path": "notes.txt"},
		"list_files":   {},
		"write_file":   {"path": "notes.txt", "content": "hello"},
		"apply_patch":  {"path": "notes.txt", "edits": []any{map[string]any{"old": "a", "new": "b"}}},
		"search_files": {"pattern": "TODO"},
		"glob_files":   {"pattern": "**/*.go"},
		"process":      {"action": "list"},
	}
	identityKeys := []string{"user_id", "room_id", "sender", "owner", "platform", "account", "session"}
	tools := e.Tools(WithTask(chat.WithIdentity(context.Background(), identity), 1, dir))
	require.NotEmpty(t, tools)
	for _, tool := range tools {
		name := tool.Function.Name
		require.True(t, LocalTool(name), "%s 必须仍是本地工具", name)
		base, ok := valid[name]
		require.True(t, ok, "缺少本地工具 %s 的参数样例", name)
		require.NoError(t, validateArgs(name, cloneArgs(base)), "%s 的基准参数应通过校验", name)
		for _, key := range identityKeys {
			t.Run(name+"/"+key, func(t *testing.T) {
				args := cloneArgs(base)
				args[key] = "attacker-controlled"
				err := validateArgs(name, args)
				require.Error(t, err, "工具 %s 接受了身份字段 %q", name, key)
				require.Contains(t, err.Error(), "unexpected tool argument")
			})
		}
	}
	require.Len(t, valid, len(tools), "参数样例必须覆盖当前全部本地工具")
}

// cloneArgs 复制一层参数，避免 list_files 之类会补默认值的工具污染样例。
func cloneArgs(args map[string]any) map[string]any {
	cloned := make(map[string]any, len(args)+1)
	for key, value := range args {
		cloned[key] = value
	}
	return cloned
}
