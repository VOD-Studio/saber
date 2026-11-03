package execution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

func TestValidateConfig_BackendExplicitnessAndOverrides(t *testing.T) {
	cfg, _ := fixture(t)
	cfg.Backend = ""
	_, err := ValidateConfig(cfg, nil, nil)
	require.ErrorContains(t, err, "execution backend must be explicit")

	cfg.Backend = "local"
	validated, err := ValidateConfig(cfg, nil, nil)
	require.NoError(t, err)
	backend, err := validated.BackendFor("project")
	require.NoError(t, err)
	require.Equal(t, "local", backend)

	// 工作区显式值优先于全局值。
	cfg.Backend = "docker"
	project := cfg.Workspaces["project"]
	project.Backend = "local"
	cfg.Workspaces["project"] = project
	validated, err = ValidateConfig(cfg, nil, nil)
	require.NoError(t, err)
	backend, err = validated.BackendFor("project")
	require.NoError(t, err)
	require.Equal(t, "local", backend)

	project.Backend = ""
	cfg.Workspaces["project"] = project
	cfg.Backend = "bogus"
	_, err = ValidateConfig(cfg, nil, nil)
	require.ErrorContains(t, err, "unknown execution backend")

	// 关闭执行时允许省略后端，纯聊天不受影响。
	disabled, _ := fixture(t)
	disabled.Enabled, disabled.Backend = false, ""
	_, err = ValidateConfig(disabled, nil, nil)
	require.NoError(t, err)

	// local 不能声称断网隔离。
	disabledNetwork := false
	localOnly, _ := fixture(t)
	localOnly.Network = &disabledNetwork
	_, err = ValidateConfig(localOnly, nil, nil)
	require.ErrorContains(t, err, "local backend cannot enforce network isolation")

	// 校验结果克隆 workspace 环境，不修改调用者配置。
	original, _ := fixture(t)
	original.Backend = "local"
	validated, err = ValidateConfig(original, nil, nil)
	require.NoError(t, err)
	validated.Workspaces["project"].Env["PROJECT_FLAG"] = "changed"
	require.Equal(t, "configured", original.Workspaces["project"].Env["PROJECT_FLAG"])
	require.Equal(t, "configured", cfg.Workspaces["project"].Env["PROJECT_FLAG"])
}

func TestEnvironment_ActiveLocalRecordBlocksWorkspaceOnRestart(t *testing.T) {
	cfg, identity := fixture(t)
	cfg.Grants[0].Tools = append(cfg.Grants[0].Tools, "read_output", "process")
	first, err := New(cfg, nil, nil)
	require.NoError(t, err)
	dir, err := first.Workspace(identity)
	require.NoError(t, err)
	ctx := WithTask(chat.WithIdentity(context.Background(), identity), 11, dir)
	_, err = first.Run(ctx, "exec", map[string]any{"command": "true"})
	require.NoError(t, err)
	// 模拟进程在清理前崩溃：不调用 CloseTask，直接用同一日志目录重建执行器。
	second, err := New(cfg, nil, nil)
	require.NoError(t, err)
	require.Contains(t, second.BlockedDirectories(), dir)
	_, err = second.Run(ctx, "exec", map[string]any{"command": "true"})
	require.Error(t, err)
}

func TestBackendFor_DefaultsToLocal(t *testing.T) {
	cfg := config.ExecutionConfig{Workspaces: map[string]config.WorkspaceConfig{"w": {Path: filepath.Join(t.TempDir())}}}
	backend, err := cfg.BackendFor("w")
	require.NoError(t, err)
	require.Equal(t, "local", backend)
	_, err = cfg.BackendFor("missing")
	require.NoError(t, err)
}

func TestRemoveEnvironmentRecordOnClose(t *testing.T) {
	e, ctx, _ := fileFixture(t)
	_, err := e.Run(ctx, "exec", map[string]any{"command": "true"})
	require.NoError(t, err)
	before, err := e.readEnvironmentRecords()
	require.NoError(t, err)
	require.Len(t, before, 1)
	require.Equal(t, envActive, before[0].State)
	require.NoError(t, e.CloseTask(ctx, 7))
	after, err := e.readEnvironmentRecords()
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, envClosed, after[0].State)
	require.Empty(t, e.BlockedDirectories())
}
