package execution

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

func TestCommandGuard_Rules(t *testing.T) {
	guard := newCommandGuard(config.ExecutionConfig{CommandGuard: "block"})
	for _, tc := range []struct {
		name    string
		command string
		rule    string
	}{
		{"plain ls", "ls -la", ""},
		{"go test", "go test ./...", ""},
		{"relative delete", "rm -rf ./build", ""},
		{"subdirectory delete", "rm -rf /tmp/x", ""},
		{"git status", "git status", ""},
		{"docker socket", "docker run -v /var/run/docker.sock:/var/run/docker.sock alpine", "/var/run/docker.sock"},
		{"privileged", "docker run --privileged alpine", "--privileged"},
		{"cap add", "docker run --cap-add=NET_ADMIN alpine", "--cap-add"},
		{"pid host", "docker run --pid=host alpine", "--pid=host"},
		{"net host", "docker run --net=host alpine", "--net=host"},
		{"userns host", "docker run --userns=host alpine", "--userns=host"},
		{"security opt", "docker run --security-opt seccomp=unconfined alpine", "--security-opt"},
		{"shadow", "cat /etc/shadow", "/etc/shadow"},
		{"sudoers", "cat /etc/sudoers", "/etc/sudoers"},
		{"ssh key", "cat ~/.ssh/id_ed25519", "/.ssh/"},
		{"aws credentials", "cat ~/.aws/credentials", "/.aws/"},
		{"git credentials", "cat .git-credentials", ".git-credentials"},
		{"netrc", "cat .netrc", ".netrc"},
		{"fork bomb", ":(){ :|:& };:", ":(){"},
		{"uppercase rule", "DOCKER RUN --PRIVILEGED alpine", "--privileged"},
		{"root delete", "rm -rf /", rmRootLabel},
		{"root wildcard delete", "rm -fr /*", rmRootLabel},
		{"root delete flags reversed", "rm -r -f /", rmRootLabel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule, blocked := guard.check(tc.command)
			require.Equal(t, tc.rule != "", blocked)
			require.Equal(t, tc.rule, rule)
		})
	}
}

func TestCommandGuard_OffAllowsEverything(t *testing.T) {
	guard := newCommandGuard(config.ExecutionConfig{CommandGuard: "off", CommandGuardDeny: []string{"terraform destroy"}})
	require.Nil(t, guard)
	_, blocked := guard.check("rm -rf /")
	require.False(t, blocked)
}

func TestCommandGuard_CustomRule(t *testing.T) {
	guard := newCommandGuard(config.ExecutionConfig{CommandGuard: "block", CommandGuardDeny: []string{"Terraform Destroy"}})
	rule, blocked := guard.check("terraform destroy -auto-approve")
	require.True(t, blocked)
	require.Equal(t, "terraform destroy", rule)
}

func TestValidateConfig_CommandGuard(t *testing.T) {
	cfg, _ := fixture(t)
	validated, err := ValidateConfig(cfg, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "block", validated.CommandGuard, "省略时默认拦截")

	cfg.CommandGuard = "warn"
	_, err = ValidateConfig(cfg, nil, nil)
	require.ErrorContains(t, err, "command_guard must be block or off")

	cfg.CommandGuard = "block"
	cfg.CommandGuardDeny = make([]string, maxGuardRules+1)
	for i := range cfg.CommandGuardDeny {
		cfg.CommandGuardDeny[i] = "rule"
	}
	_, err = ValidateConfig(cfg, nil, nil)
	require.ErrorContains(t, err, "at most 64 rules")

	cfg.CommandGuardDeny = []string{""}
	_, err = ValidateConfig(cfg, nil, nil)
	require.ErrorContains(t, err, "1..256 bytes")

	cfg.CommandGuardDeny = []string{strings.Repeat("x", maxGuardRuleLength+1)}
	_, err = ValidateConfig(cfg, nil, nil)
	require.ErrorContains(t, err, "1..256 bytes")

	cfg.CommandGuardDeny = []string{"terraform destroy"}
	validated, err = ValidateConfig(cfg, nil, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"terraform destroy"}, validated.CommandGuardDeny)
}

// TestCommandGuard_RunDenies 验证前台 exec 在打开环境前拒绝危险命令，且不回显命令正文。
func TestCommandGuard_RunDenies(t *testing.T) {
	cfg, identity := fixture(t)
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	dir, err := e.Workspace(identity)
	require.NoError(t, err)
	ctx := WithTask(chat.WithIdentity(context.Background(), identity), 11, dir)

	_, err = e.Run(ctx, "exec", map[string]any{"command": "cat /etc/shadow # SECRET-TOKEN"})
	require.ErrorContains(t, err, `命中规则 "/etc/shadow"`)
	require.NotContains(t, err.Error(), "SECRET-TOKEN", "拒绝信息不得回显命令正文")

	_, err = e.Run(ctx, "exec", map[string]any{"command": "rm -rf /"})
	require.ErrorContains(t, err, rmRootLabel)

	result, err := e.Run(ctx, "exec", map[string]any{"command": "echo allowed"})
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)
}

// TestCommandGuard_StartProcessDenies 验证后台作业入口使用同一份规则。
func TestCommandGuard_StartProcessDenies(t *testing.T) {
	cfg, identity := fixture(t)
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	dir, err := e.Workspace(identity)
	require.NoError(t, err)
	ctx := WithTask(chat.WithIdentity(context.Background(), identity), 12, dir)
	run := ctx.Value(scopeKey{}).(scope)

	_, err = e.startProcess(ctx, map[string]any{"command": "docker run --privileged alpine # SECRET-TOKEN"}, run)
	require.ErrorContains(t, err, `命中规则 "--privileged"`)
	require.NotContains(t, err.Error(), "SECRET-TOKEN")
	require.Zero(t, e.ProcessCount(run.id), "被拒绝的命令不得留下后台作业")
}

// TestCommandGuard_OffExecutes 验证 off 模式完全跳过内置规则。
func TestCommandGuard_OffExecutes(t *testing.T) {
	cfg, identity := fixture(t)
	cfg.CommandGuard = "off"
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	dir, err := e.Workspace(identity)
	require.NoError(t, err)
	ctx := WithTask(chat.WithIdentity(context.Background(), identity), 13, dir)

	result, err := e.Run(ctx, "exec", map[string]any{"command": "echo --privileged"})
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)
	require.Contains(t, result.Summary, "--privileged")
}

func TestCommandGuard_CustomRuleDeniesRun(t *testing.T) {
	cfg, identity := fixture(t)
	cfg.CommandGuardDeny = []string{"terraform destroy"}
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	dir, err := e.Workspace(identity)
	require.NoError(t, err)
	ctx := WithTask(chat.WithIdentity(context.Background(), identity), 14, dir)

	_, err = e.Run(ctx, "exec", map[string]any{"command": "terraform destroy -auto-approve"})
	require.ErrorContains(t, err, `命中规则 "terraform destroy"`)
	require.NotContains(t, err.Error(), "-auto-approve")
}
