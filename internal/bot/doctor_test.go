package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"rua.plus/saber/internal/config"
)

func TestDoctor_DefaultConfigDoesNotCreateState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, config.GenerateExample(path))
	var output bytes.Buffer
	err := runDoctor(context.Background(), []string{"doctor", "-c", path, "--json"}, &output)
	require.NoError(t, err)
	var report struct {
		Checks []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
		} `json:"checks"`
		OK bool `json:"ok"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	require.True(t, report.OK)
	require.Contains(t, output.String(), "execution.disabled")
	// 只读检查不能创建 token、日志目录或数据库。
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "doctor must not create state files")
	require.Equal(t, "config.yaml", entries[0].Name())
}

func TestDoctor_MissingConfigReportsErrorAndExitCode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.yaml")
	var output bytes.Buffer
	err := runDoctor(context.Background(), []string{"doctor", "-c", path, "--json"}, &output)
	code, ok := IsExitCode(err)
	require.True(t, ok)
	require.Equal(t, 1, code)
	var report struct {
		Checks []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
		} `json:"checks"`
		OK bool `json:"ok"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	require.False(t, report.OK)
	require.Contains(t, output.String(), "config.load")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestDoctor_LocalExecutionDoesNotRequireDocker(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	require.NoError(t, os.Mkdir(work, 0700))
	cfg := config.DefaultConfig()
	cfg.Execution = config.ExecutionConfig{
		Enabled:        true,
		Backend:        "local",
		LogDir:         filepath.Join(dir, "logs"),
		TimeoutSeconds: 60,
		Workspaces:     map[string]config.WorkspaceConfig{"project": {Path: work}},
		Grants: []config.ExecutionGrant{{
			Platform: "terminal", Account: "saber", Room: "status", Users: []string{"0"}, Workspace: "project",
			Tools: []string{"read_file", "search_files", "process"},
		}},
	}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, data, 0600))
	var output bytes.Buffer
	err = runDoctor(context.Background(), []string{"doctor", "-c", configPath}, &output)
	require.NoError(t, err, output.String())
	require.Contains(t, output.String(), "execution.local.host_access")
	require.NotContains(t, output.String(), "execution.docker")
	_, statErr := os.Stat(filepath.Join(dir, "logs"))
	require.True(t, os.IsNotExist(statErr), "doctor must not create the execution log directory")
}

func TestDoctor_InvalidArgumentsExitTwo(t *testing.T) {
	var output bytes.Buffer
	err := runDoctor(context.Background(), []string{"doctor", "--unknown"}, &output)
	code, ok := IsExitCode(err)
	require.True(t, ok)
	require.Equal(t, 2, code)
}
