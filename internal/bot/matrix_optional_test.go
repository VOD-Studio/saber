package bot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/cli"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// TestRun_MatrixOptional 覆盖无 Matrix 的常驻服务启动与正常取消。
func TestRun_MatrixOptional(t *testing.T) {
	for _, body := range []string{"server:\n  listen: 127.0.0.1:0\n", "server:\n  listen: 127.0.0.1:0\nmatrix:\n  enabled: false\n  homeserver: invalid\n"} {
		t.Run(body, func(t *testing.T) {
			args := os.Args
			t.Cleanup(func() { os.Args = args })
			path := createTestConfigFile(t, body)
			os.Args = []string{"saber", "-c", path}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- run(ctx, matrix.BuildInfo{Version: "test"}) }()
			require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(filepath.Dir(path), ".saber-token")); return err == nil }, time.Second, 10*time.Millisecond)
			select {
			case err := <-done:
				t.Fatalf("服务提前退出: %v", err)
			default:
			}
			cancel()
			require.NoError(t, <-done)
		})
	}
}

func TestServer_SharedServicesWithoutMatrix(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AI.Enabled, cfg.AI.Provider, cfg.AI.BaseURL, cfg.AI.APIKey, cfg.AI.DefaultModel = true, "openai", "http://127.0.0.1:1/v1", "test", "local"
	cfg.MCP.Enabled = false
	state := &appState{cfg: cfg, flags: &cli.Flags{ConfigPath: filepath.Join(t.TempDir(), "config.yaml")}, services: &services{}}
	defer state.shutdown(func() {})
	require.NoError(t, state.initServices())
	require.Nil(t, state.services.client)
	require.Nil(t, state.services.commandService)
	require.NotNil(t, state.services.aiService.Tasks())
}

func TestExampleConfig_DefaultsToServer(t *testing.T) {
	path := createTestConfigFile(t, config.ExampleConfig())
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.False(t, cfg.Matrix.Enabled)
	require.Empty(t, cfg.Matrix.UserID)
	require.Empty(t, cfg.Matrix.AccessToken)
	require.Equal(t, 8192, cfg.AI.MaxTokens)
}
