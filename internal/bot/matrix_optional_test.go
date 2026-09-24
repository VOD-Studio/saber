package bot

import (
	"context"
	"net"
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
	for _, body := range []string{"server:\n  listen: 127.0.0.1:0\n", "server:\n  listen: 127.0.0.1:0\nplatforms:\n  matrix:\n    enabled: false\nmatrix:\n  homeserver: invalid\n"} {
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
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.MCP.Enabled = false
	state := &appState{cfg: cfg, flags: &cli.Flags{ConfigPath: filepath.Join(t.TempDir(), "config.yaml")}, services: &services{}}
	defer state.shutdown(func() {})
	require.NoError(t, state.initServices())
	require.Nil(t, state.services.client)
	require.Nil(t, state.services.commandService)
	require.NotNil(t, state.services.aiService.Tasks())
}

// TestInitServices_PlatformRegistryWithoutAI 验证关闭 AI 时平台注册表仍就绪：
// run() 会在 initServices 后直接遍历 Enabled()，注册表为 nil 时会 panic。
func TestInitServices_PlatformRegistryWithoutAI(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AI.Enabled = false
	cfg.Platforms.Matrix.Enabled = false
	state := &appState{cfg: cfg, flags: &cli.Flags{ConfigPath: filepath.Join(t.TempDir(), "config.yaml")}, services: &services{}}

	require.NoError(t, state.initServices())
	require.NotNil(t, state.services.platforms)
	require.Empty(t, state.services.platforms.Enabled(cfg))
	state.shutdown(func() {})
}

func TestExampleConfig_DefaultsToServer(t *testing.T) {
	path := createTestConfigFile(t, config.ExampleConfig())
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.False(t, cfg.Platforms.Matrix.Enabled)
	require.Empty(t, cfg.Matrix.UserID)
	require.Empty(t, cfg.Matrix.AccessToken)
	require.Equal(t, 8192, cfg.AI.MaxTokens)
}

func TestRun_PortConflictBeforeServiceInitialization(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { require.NoError(t, listener.Close()) }()
	args := os.Args
	t.Cleanup(func() { os.Args = args })
	path := createTestConfigFile(t, "server:\n  listen: "+listener.Addr().String()+"\nai:\n  enabled: true\n")
	os.Args = []string{"saber", "-c", path}
	require.ErrorContains(t, run(context.Background(), matrix.BuildInfo{}), "无法启动服务")
	_, err = os.Stat(filepath.Join(filepath.Dir(path), "tasks.db"))
	require.True(t, os.IsNotExist(err))
}

// TestInitServices_ProtectedPathsFollowMatrix 确认 E2EE 会话与密钥只在 Matrix 启用时
// 进入受保护清单。默认 matrix.e2ee_session_path 是相对当前目录的路径，工作区就是当前目录
// 时它必然落在工作区内；关掉 Matrix 不应再因此拒绝装配执行权限。
func TestInitServices_ProtectedPathsFollowMatrix(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)

	tests := []struct {
		name    string
		enabled bool
		wantErr string
	}{
		{"启用 Matrix 时保护 E2EE 默认路径", true, "执行权限初始化失败"},
		{"关闭 Matrix 时不再检查 E2EE 路径", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.AI.Enabled = true
			cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test"}}
			cfg.AI.DefaultModel = "openai.local"
			cfg.MCP.Enabled = false
			cfg.Platforms.Matrix.Enabled = tt.enabled
			cfg.Execution = config.ExecutionConfig{
				Enabled:    true,
				LogDir:     t.TempDir(),
				Workspaces: map[string]config.WorkspaceConfig{"repo": {Path: wd}},
				Grants: []config.ExecutionGrant{{
					Platform: "matrix", Account: "@bot:test", Room: "!room:test",
					Users: []string{"@user:test"}, Workspace: "repo", Tools: []string{"exec"},
				}},
			}
			path := createTestConfigFile(t, "server:\n  listen: 127.0.0.1:0\n")
			state := &appState{cfg: cfg, flags: &cli.Flags{ConfigPath: path}, services: &services{}}
			defer state.shutdown(func() {})

			err := state.initServices()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
