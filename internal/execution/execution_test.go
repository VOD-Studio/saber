package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

func fixture(t *testing.T) (config.ExecutionConfig, chat.Identity) {
	t.Helper()
	identity := chat.Identity{Session: chat.Session{Platform: "matrix", Account: "bot", Conversation: "room"}, SenderID: "alice"}
	cfg := config.ExecutionConfig{Enabled: true, LogDir: t.TempDir(), Workspaces: map[string]config.WorkspaceConfig{"project": {Path: t.TempDir(), Env: map[string]string{"PROJECT_FLAG": "configured"}}}, Grants: []config.ExecutionGrant{{Platform: "matrix", Account: "bot", Room: "room", Users: []string{"alice"}, Workspace: "project", Tools: []string{"exec", "read_file", "list_files", "write_file", "apply_patch"}}}}
	return cfg, identity
}

func TestPolicy_TrustedScopeAndSeparateCapabilities(t *testing.T) {
	cfg, identity := fixture(t)
	cfg.MCPRequirements = map[string][]string{"mcp:release:push": {"publish"}, "mcp:deploy:run": {"deploy"}, "mcp:files:read": {"cross_directory"}}
	cfg.Grants[0].Tools = append(cfg.Grants[0].Tools, "mcp:release:push", "mcp:deploy:run", "mcp:files:read")
	cfg.Grants[0].Capabilities = []string{"external", "publish"}
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	dir, err := e.Workspace(identity)
	require.NoError(t, err)
	ctx := WithTask(chat.WithIdentity(context.Background(), identity), 42, dir)
	require.NoError(t, e.Check(ctx, "exec"))
	require.Len(t, e.Tools(ctx), 5)
	require.NoError(t, e.CheckMCP(ctx, "release", "push"))
	require.Error(t, e.CheckMCP(ctx, "deploy", "run"))
	require.Error(t, e.CheckMCP(ctx, "files", "read"))
	require.Error(t, e.CheckMCP(ctx, "unknown", "tool"))
	require.Error(t, e.Check(context.Background(), "exec"))
	require.Error(t, e.Check(WithTask(ctx, 42, t.TempDir()), "exec"))
	other := identity
	other.SenderID = "bob"
	require.Error(t, e.Check(chat.WithIdentity(ctx, other), "exec"))
	other = identity
	other.Session.Account = "other"
	require.Error(t, e.Check(chat.WithIdentity(ctx, other), "exec"))
	// 修改原配置对象和工具内容不会改变编译后的权限。
	cfg.Grants[0].Capabilities[0] = "deploy"
	cfg.MCPRequirements["mcp:deploy:run"][0] = "publish"
	require.Error(t, e.CheckMCP(ctx, "deploy", "run"))
	_, err = e.Run(ctx, "exec", map[string]any{"command": "true", "workspace": "/", "capabilities": []string{"deploy"}})
	require.Error(t, err)
}

func TestPolicy_RejectsSecretsAndUnsafeRoots(t *testing.T) {
	for _, name := range []string{"protected file", "secret value", "reserved variable", "wildcard", "unknown capability", "unknown tool", "conflict"} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := fixture(t)
			var paths []string
			w := cfg.Workspaces["project"]
			switch name {
			case "protected file":
				paths = []string{filepath.Join(w.Path, "config.yaml")}
			case "secret value":
				w.Env["CUSTOM"] = "bot-secret"
			case "reserved variable":
				w.Env["MATRIX_ACCESS_TOKEN"] = "anything"
			case "wildcard":
				cfg.Grants[0].Users = []string{"*"}
			case "unknown capability":
				cfg.Grants[0].Capabilities = []string{"admin"}
			case "unknown tool":
				cfg.Grants[0].Tools = []string{"mcp:anything:execute"}
			case "conflict":
				cfg.Grants = append(cfg.Grants, cfg.Grants[0])
			}
			cfg.Workspaces["project"] = w
			_, err := New(cfg, paths, []string{"bot-secret"})
			require.Error(t, err)
		})
	}
	e, err := New(config.ExecutionConfig{}, nil, nil)
	require.NoError(t, err)
	_, err = e.Workspace(chat.Identity{})
	require.Error(t, err)
}

func TestDockerExecution(t *testing.T) {
	image := os.Getenv("SABER_EXECUTION_TEST_IMAGE")
	if image == "" {
		t.Skip("设置 SABER_EXECUTION_TEST_IMAGE 后运行真实 Docker 隔离验收")
	}
	cfg, identity := fixture(t)
	cfg.Image = image
	cfg.TimeoutSeconds = 2
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	require.NoError(t, e.Recover(context.Background()))
	dir, err := e.Workspace(identity)
	require.NoError(t, err)
	ctx := WithTask(chat.WithIdentity(context.Background(), identity), 42, dir)
	t.Setenv("MATRIX_ACCESS_TOKEN", "must-not-leak")
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	run := func(tool string, args map[string]any) Result {
		t.Helper()
		r, err := e.Run(ctx, tool, args)
		require.NoError(t, err)
		require.FileExists(t, r.LogPath)
		return r
	}
	r := run("exec", map[string]any{"command": "test -z \"$MATRIX_ACCESS_TOKEN$OPENAI_API_KEY\" && test ! -S /var/run/docker.sock && test \"$PROJECT_FLAG\" = configured && printf isolated"})
	require.Zero(t, r.ExitCode, r.Summary)
	require.Equal(t, "isolated", r.Summary)
	r = run("exec", map[string]any{"command": "printf failure; exit 7"})
	require.Equal(t, 7, r.ExitCode)
	require.Equal(t, "failure", r.Summary)
	r = run("write_file", map[string]any{"path": "answer.txt", "content": "first\n"})
	require.Zero(t, r.ExitCode)
	r = run("apply_patch", map[string]any{"path": "answer.txt", "edits": []any{map[string]any{"old": "absent", "new": "second"}}})
	require.NotZero(t, r.ExitCode)
	r = run("apply_patch", map[string]any{"path": "answer.txt", "edits": []any{map[string]any{"old": "first", "new": "second"}}})
	require.Zero(t, r.ExitCode)
	r = run("read_file", map[string]any{"path": "answer.txt", "deliver": true})
	require.Equal(t, "second\n", r.Summary)
	require.FileExists(t, r.Artifact)
	artifact := r.Artifact
	r = run("write_file", map[string]any{"path": "answer.txt", "content": "changed later"})
	require.Zero(t, r.ExitCode)
	data, err := os.ReadFile(artifact)
	require.NoError(t, err)
	require.Equal(t, "second\n", string(data))
	artifacts, err := e.Artifacts(42)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	require.Equal(t, "answer.txt", artifacts[0].Name)
	r = run("list_files", map[string]any{})
	require.Contains(t, r.Summary, "answer.txt")
	r = run("exec", map[string]any{"command": "ln -s /etc/passwd escape; ln -s /etc outside"})
	require.Zero(t, r.ExitCode)
	for _, path := range []string{"escape", "outside/passwd"} {
		r = run("read_file", map[string]any{"path": path})
		require.NotZero(t, r.ExitCode)
	}
	_, err = e.Run(ctx, "read_file", map[string]any{"path": "../config.yaml"})
	require.Error(t, err)
	// 文件工具与命令都不能越过挂载边界读取宿主私有目录。
	secretPath := filepath.Join(t.TempDir(), "host-secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("host-secret"), 0600))
	r = run("exec", map[string]any{"command": "cat '" + secretPath + "'"})
	require.NotZero(t, r.ExitCode)
	// 超时终止整个容器，包括 shell 派生出的后台进程。
	started := time.Now()
	r, err = e.Run(ctx, "exec", map[string]any{"command": "sleep 60 & wait"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), 15*time.Second)
	cancelCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := e.Run(cancelCtx, "exec", map[string]any{"command": "sleep 60 & wait"}); done <- err }()
	require.Eventually(t, func() bool {
		out, err := docker(context.Background(), "ps", "-q", "--filter", "label=saber.executor="+e.owner).Output()
		return err == nil && strings.TrimSpace(string(out)) != ""
	}, 5*time.Second, 50*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	out, err := docker(context.Background(), "ps", "-aq", "--filter", "label=saber.executor="+e.owner).Output()
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(string(out)))
}

func TestValidateArgs(t *testing.T) {
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"exec", map[string]any{"command": "echo ok", "env": map[string]any{"TOKEN": "secret"}}},
		{"read_file", map[string]any{"path": "/etc/passwd"}},
		{"write_file", map[string]any{"path": "a/../b", "content": "bad"}},
		{"apply_patch", map[string]any{"path": "a", "edits": []any{map[string]any{"old": "", "new": "x"}}}},
	} {
		require.Error(t, validateArgs(tc.tool, tc.args))
	}
}
