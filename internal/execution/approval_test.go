package execution

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// approvalCodePattern 从工具错误中提取审批码，模拟用户按提示执行 !approve。
var approvalCodePattern = regexp.MustCompile(`!approve ([0-9a-f]+)`)

func approvalCode(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	match := approvalCodePattern.FindStringSubmatch(err.Error())
	require.Len(t, match, 2, "错误必须包含审批码：%v", err)
	return match[1]
}

// askFixture 构造 ask 模式执行器，并返回当前任务上下文。
func askFixture(t *testing.T) (*Executor, chat.Identity, context.Context, string) {
	t.Helper()
	cfg, identity := fixture(t)
	cfg.CommandGuard = "ask"
	cfg.Grants[0].Tools = append(cfg.Grants[0].Tools, "process", "read_output")
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	dir, err := e.Workspace(identity)
	require.NoError(t, err)
	return e, identity, WithTask(chat.WithIdentity(context.Background(), identity), 21, dir), dir
}

func TestApproval_AskRequiresApprovalThenExecutesOnce(t *testing.T) {
	e, identity, ctx, dir := askFixture(t)
	command := "echo SECRET_MARKER --privileged >> ran.txt"

	_, err := e.Run(ctx, "exec", map[string]any{"command": command})
	code := approvalCode(t, err)
	require.NotContains(t, err.Error(), "SECRET_MARKER", "审批要求不回显命令正文")
	require.Contains(t, err.Error(), "需要本群授权成员执行 !approve")
	// 批准前不创建环境、不执行命令。
	_, statErr := os.Stat(filepath.Join(dir, "ran.txt"))
	require.True(t, os.IsNotExist(statErr))

	receipt, err := e.ResolveApproval(identity, code, true)
	require.NoError(t, err)
	require.Contains(t, receipt, "echo SECRET_MARKER --privileged", "批准回执向审批者展示被批准的命令")
	require.Contains(t, receipt, "生效一次")

	// 批准后同一命令执行一次；文件只追加一行。
	result, err := e.Run(ctx, "exec", map[string]any{"command": command})
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)
	raw, err := os.ReadFile(filepath.Join(dir, "ran.txt"))
	require.NoError(t, err)
	require.Equal(t, "SECRET_MARKER --privileged\n", string(raw))

	// 二次使用同一批准必须重新审批，不能重放。
	_, err = e.Run(ctx, "exec", map[string]any{"command": command})
	approvalCode(t, err)
	raw, err = os.ReadFile(filepath.Join(dir, "ran.txt"))
	require.NoError(t, err)
	require.Equal(t, "SECRET_MARKER --privileged\n", string(raw))
}

func TestApproval_RejectsForeignIdentity(t *testing.T) {
	e, identity, ctx, _ := askFixture(t)
	_, err := e.Run(ctx, "exec", map[string]any{"command": "rm -rf /"})
	code := approvalCode(t, err)

	other := identity
	other.SenderID = "mallory"
	_, err = e.ResolveApproval(other, code, true)
	require.ErrorContains(t, err, "只有该工作区的授权成员或任务管理员可以审批")

	// 身份不符的调用不会消耗审批码，原作者仍可正常批准。
	_, err = e.ResolveApproval(identity, code, true)
	require.NoError(t, err)
}

func TestApproval_RejectsAnotherAccount(t *testing.T) {
	e, identity, ctx, _ := askFixture(t)
	_, err := e.Run(ctx, "exec", map[string]any{"command": "rm -rf /"})
	code := approvalCode(t, err)

	other := identity
	other.Session.Account = "another-bot"
	_, err = e.ResolveApproval(other, code, true)
	require.Error(t, err)
}

// TestApproval_CommandMismatchCreatesNewRequest 验证批准只绑定完全相同的命令。
func TestApproval_CommandMismatchCreatesNewRequest(t *testing.T) {
	e, _, ctx, _ := askFixture(t)
	_, err := e.Run(ctx, "exec", map[string]any{"command": "rm -rf /"})
	first := approvalCode(t, err)

	_, err = e.Run(ctx, "exec", map[string]any{"command": "cat /etc/shadow"})
	second := approvalCode(t, err)
	require.NotEqual(t, first, second, "不同命令必须生成不同审批码")
}

// TestApproval_DirectoryMismatchCreatesNewRequest 验证审批绑定工作区目录。
func TestApproval_DirectoryMismatchCreatesNewRequest(t *testing.T) {
	e, identity, ctx, dir := askFixture(t)
	if err := e.gateCommand(ctx, "exec", map[string]any{"command": "rm -rf /"}); err != nil {
		approvalCode(t, err)
	}
	otherDir := filepath.Join(dir, "..", "other-workspace")
	otherCtx := WithTask(chat.WithIdentity(context.Background(), identity), 22, otherDir)
	err := e.gateCommand(otherCtx, "exec", map[string]any{"command": "rm -rf /"})
	code := approvalCode(t, err)

	e.approvalMu.Lock()
	request := e.approvals[code]
	e.approvalMu.Unlock()
	require.NotNil(t, request)
	require.Equal(t, otherDir, request.dir)
}

func TestApproval_DenyInvalidates(t *testing.T) {
	e, identity, ctx, dir := askFixture(t)
	command := "echo DENIED_MARKER --privileged >> denied.txt"
	_, err := e.Run(ctx, "exec", map[string]any{"command": command})
	code := approvalCode(t, err)

	receipt, err := e.ResolveApproval(identity, code, false)
	require.NoError(t, err)
	require.Contains(t, receipt, "已拒绝审批码")

	// 拒绝后再次执行必须重新申请，而不是复用旧码。
	_, err = e.Run(ctx, "exec", map[string]any{"command": command})
	require.NotEqual(t, code, approvalCode(t, err))
	_, err = e.ResolveApproval(identity, code, true)
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(dir, "denied.txt"))
	require.True(t, os.IsNotExist(statErr))
}

func TestApproval_TTLExpires(t *testing.T) {
	e, identity, ctx, _ := askFixture(t)
	_, err := e.Run(ctx, "exec", map[string]any{"command": "rm -rf /"})
	code := approvalCode(t, err)

	e.approvalMu.Lock()
	fingerprint := e.approvals[code].fingerprint
	e.approvals[code].expires = time.Now().Add(-time.Second)
	e.approvalMu.Unlock()

	_, err = e.ResolveApproval(identity, code, true)
	require.ErrorContains(t, err, "不存在、已过期或已被使用")

	e.approvalMu.Lock()
	_, exists := e.approvals[code]
	_, fingerprintExists := e.approvalFingerprints[fingerprint]
	e.approvalMu.Unlock()
	require.False(t, exists)
	require.False(t, fingerprintExists)
}

func TestApproval_PendingLimit(t *testing.T) {
	cfg, identity := fixture(t)
	cfg.CommandGuard = "ask"
	cfg.ApprovalMaxPending = 2
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	dir, err := e.Workspace(identity)
	require.NoError(t, err)
	ctx := WithTask(chat.WithIdentity(context.Background(), identity), 23, dir)

	for _, command := range []string{"cat /etc/shadow", "cat /etc/sudoers"} {
		_, err = e.Run(ctx, "exec", map[string]any{"command": command})
		approvalCode(t, err)
	}
	_, err = e.Run(ctx, "exec", map[string]any{"command": "cat ~/.netrc"})
	require.ErrorContains(t, err, "待审批命令已达上限 2")
}

func TestApproval_ProcessStartRequiresApproval(t *testing.T) {
	e, _, ctx, _ := askFixture(t)
	_, err := e.Run(ctx, "process", map[string]any{"action": "start", "command": "echo --privileged"})
	approvalCode(t, err)
	require.Zero(t, e.ProcessCount(21), "审批前不得创建后台进程")
}

// TestApproval_BlockModeKeepsDenying 验证 block 模式不产生审批码，行为不回归。
func TestApproval_BlockModeKeepsDenying(t *testing.T) {
	cfg, identity := fixture(t)
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	dir, err := e.Workspace(identity)
	require.NoError(t, err)
	ctx := WithTask(chat.WithIdentity(context.Background(), identity), 24, dir)

	_, err = e.Run(ctx, "exec", map[string]any{"command": "rm -rf /"})
	require.ErrorContains(t, err, "命令被安全策略拒绝")
	require.NotContains(t, err.Error(), "!approve")
	e.approvalMu.Lock()
	require.Empty(t, e.approvals)
	e.approvalMu.Unlock()
}

func TestValidateConfig_Approval(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*config.ExecutionConfig)
		wantErr string
	}{
		{"ask 模式", func(cfg *config.ExecutionConfig) { cfg.CommandGuard = "ask" }, ""},
		{"未知模式", func(cfg *config.ExecutionConfig) { cfg.CommandGuard = "confirm" }, "must be block, ask or off"},
		{"TTL 过短", func(cfg *config.ExecutionConfig) { cfg.ApprovalTTLSeconds = 59 }, "approval_ttl_seconds"},
		{"TTL 过长", func(cfg *config.ExecutionConfig) { cfg.ApprovalTTLSeconds = 3601 }, "approval_ttl_seconds"},
		{"pending 过大", func(cfg *config.ExecutionConfig) { cfg.ApprovalMaxPending = 257 }, "approval_max_pending"},
		{"pending 为零使用默认", func(cfg *config.ExecutionConfig) { cfg.CommandGuard = "ask" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := fixture(t)
			tc.mutate(&cfg)
			got, err := ValidateConfig(cfg, nil, nil)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.GreaterOrEqual(t, got.ApprovalTTLSeconds, 60)
			require.GreaterOrEqual(t, got.ApprovalMaxPending, 1)
			require.LessOrEqual(t, got.ApprovalMaxPending, 256)
			require.Equal(t, "ask", got.CommandGuard)
		})
	}
	// 零值归一化为文档默认值。
	cfg, _ := fixture(t)
	cfg.CommandGuard = "ask"
	got, err := ValidateConfig(cfg, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 600, got.ApprovalTTLSeconds)
	require.Equal(t, 32, got.ApprovalMaxPending)
	require.True(t, strings.HasPrefix(got.CommandGuard, "ask"))
}
