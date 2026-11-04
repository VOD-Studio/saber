package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecutor_WriteArtifactKeepsPrivateNamingContract(t *testing.T) {
	cfg, _ := fixture(t)
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	path, err := e.WriteArtifact(7, "trace-7.jsonl", []byte("{\"seq\":1}\n"))
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dirInfo, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
	require.Equal(t, e.ArtifactDir(7), filepath.Dir(path))
	// 32 字节随机前缀加分隔符必须能被 Artifacts 还原成展示名，否则交付端会发错文件名。
	name := filepath.Base(path)
	require.Len(t, strings.Split(name, "-")[0], 32)
	artifacts, err := e.Artifacts(7)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	require.Equal(t, "trace-7.jsonl", artifacts[0].Name)
}

func TestExecutor_WriteArtifactRejectsEscapingNames(t *testing.T) {
	cfg, _ := fixture(t)
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	for _, tc := range []struct {
		name  string
		task  int64
		label string
	}{
		{"no task", 0, "trace.jsonl"},
		{"parent", 3, "../trace.jsonl"},
		{"nested", 3, "sub/trace.jsonl"},
		{"empty", 3, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.WriteArtifact(tc.task, tc.label, []byte("x"))
			require.Error(t, err)
		})
	}
}
