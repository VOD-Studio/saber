package ai

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
)

// resolvedTempDir 返回已解析符号链接的临时目录，模拟 EnableTasks 的工作目录来源。
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

// TestDiscoverContextFiles_GitRoot 验证从工作目录向上只读到 git 根，按由近及远排序。
func TestDiscoverContextFiles_GitRoot(t *testing.T) {
	root := resolvedTempDir(t)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0700))
	sub := filepath.Join(root, "pkg")
	require.NoError(t, os.Mkdir(sub, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("rules"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("sub"), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(sub, ".github"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(sub, ".github", "copilot-instructions.md"), []byte("copilot"), 0600))

	files, err := discoverContextFiles(sub)
	require.NoError(t, err)
	require.Len(t, files, 4)
	require.Equal(t, "AGENTS.md", files[0].Path)
	require.Equal(t, "sub", files[0].Content)
	require.Equal(t, filepath.ToSlash(filepath.Join(".github", "copilot-instructions.md")), files[1].Path)
	require.Equal(t, "../AGENTS.md", files[2].Path)
	require.Equal(t, "../.cursorrules", files[3].Path)
	require.Contains(t, renderContextFiles(files), `<file path="AGENTS.md">`)
}

// TestDiscoverContextFiles_NoGitRoot 验证找不到 git 根时只读工作目录本身。
func TestDiscoverContextFiles_NoGitRoot(t *testing.T) {
	dir := resolvedTempDir(t)
	child := filepath.Join(dir, "nested")
	require.NoError(t, os.Mkdir(child, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("parent"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(child, "CLAUDE.md"), []byte("child"), 0600))

	files, err := discoverContextFiles(child)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, "CLAUDE.md", files[0].Path)
}

// TestDiscoverContextFiles_LimitsAndValidation 验证截断、UTF-8 校验与符号链接拒绝。
func TestDiscoverContextFiles_LimitsAndValidation(t *testing.T) {
	dir := resolvedTempDir(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0700))
	large := strings.Repeat("a", maxContextFileBytes+10)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(large), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte{0xff, 0xfe, 'x'}, 0600))
	secret := filepath.Join(t.TempDir(), "secret.md")
	require.NoError(t, os.WriteFile(secret, []byte("secret"), 0600))
	require.NoError(t, os.Symlink(secret, filepath.Join(dir, "CONTRIBUTING.md")))

	files, err := discoverContextFiles(dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.True(t, files[0].Truncated)
	require.Len(t, files[0].Content, maxContextFileBytes)
	require.Contains(t, renderContextFiles(files), "[内容已截断]")

	// 合计上限：第二个文件只能截断到剩余额度。
	require.NoError(t, os.Remove(filepath.Join(dir, "CLAUDE.md")))
	require.NoError(t, os.Remove(filepath.Join(dir, "CONTRIBUTING.md")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(strings.Repeat("b", maxContextFileBytes+4096)), 0600))
	files, err = discoverContextFiles(dir)
	require.NoError(t, err)
	require.Len(t, files, 2)
	require.True(t, files[1].Truncated)
	require.Equal(t, maxContextTotalBytes-len(files[0].Content), len(files[1].Content))
}

// TestAppendContextFiles_WithoutExecutor 验证没有执行器时保持请求不变。
func TestAppendContextFiles_WithoutExecutor(t *testing.T) {
	service := &Service{}
	got := service.appendContextFiles(context.Background(), agent.Request{})
	require.Empty(t, got.RuntimeContext)
}
