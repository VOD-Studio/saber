package bot

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/cli"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// freeAddr 取一个空闲的回环地址用于测试监听。
func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())
	return addr
}

// newStateForPlatforms 构造只含配置与 CLI 标志的装配状态。
func newStateForPlatforms(t *testing.T, cfg *config.Config) *appState {
	t.Helper()
	return &appState{
		cfg:      cfg,
		flags:    &cli.Flags{ConfigPath: filepath.Join(t.TempDir(), "config.yaml")},
		services: &services{},
	}
}

// TestInitServices_TerminalDisabled 验证关闭终端开关后既不注册终端平台，也不生成令牌。
func TestInitServices_TerminalDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AI.Enabled = false
	cfg.Platforms.Terminal.Enabled = false
	cfg.Server.Listen = "127.0.0.1:0"
	state := newStateForPlatforms(t, cfg)
	defer state.shutdown(func() {})

	require.NoError(t, state.initServices())
	_, ok := state.services.platforms.Lookup(terminalPlatformName)
	require.False(t, ok, "关闭开关后不应注册终端平台")
	require.Empty(t, state.services.platforms.Enabled(cfg))

	tokenPath := cfg.Server.TokenPath(state.flags.ConfigPath)
	_, err := os.Stat(tokenPath)
	require.True(t, os.IsNotExist(err), "未启动时不应生成令牌文件")
}

// TestInitServices_PlatformRegistrationOrder 验证注册顺序决定启动顺序：终端最先。
func TestInitServices_PlatformRegistrationOrder(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AI.Enabled = false
	cfg.Platforms.Matrix.Enabled = true
	cfg.Matrix = config.MatrixConfig{Homeserver: "http://localhost:1", UserID: "@bot:local", AccessToken: "token"}
	state := newStateForPlatforms(t, cfg)
	client, err := matrix.NewMatrixClient(&cfg.Matrix)
	require.NoError(t, err)
	state.services.client = client
	defer state.shutdown(func() {})

	require.NoError(t, state.initServices())
	enabled := state.services.platforms.Enabled(cfg)
	require.Len(t, enabled, 2)
	require.Equal(t, terminalPlatformName, enabled[0].Name())
	require.Equal(t, "matrix", enabled[1].Name())
}

// TestInitServices_MatrixSwitchControlsStartup 验证 Matrix 开关只影响是否启动，不影响注册。
func TestInitServices_MatrixSwitchControlsStartup(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AI.Enabled = false
	cfg.Matrix = config.MatrixConfig{Homeserver: "http://localhost:1", UserID: "@bot:local", AccessToken: "token"}
	state := newStateForPlatforms(t, cfg)
	client, err := matrix.NewMatrixClient(&cfg.Matrix)
	require.NoError(t, err)
	state.services.client = client
	defer state.shutdown(func() {})

	cfg.Platforms.Matrix.Enabled = false
	require.NoError(t, state.initServices())
	_, registered := state.services.platforms.Lookup("matrix")
	require.True(t, registered, "有 Matrix 客户端时应注册平台，由开关决定是否启动")
	for _, p := range state.services.platforms.Enabled(cfg) {
		require.NotEqual(t, "matrix", p.Name())
	}

	cfg.Platforms.Matrix.Enabled = true
	found := false
	for _, p := range state.services.platforms.Enabled(cfg) {
		if p.Name() == "matrix" {
			found = true
		}
	}
	require.True(t, found, "打开开关后 Matrix 平台应进入启动列表")
}

// TestRun_TerminalDisabledDoesNotListen 验证关闭终端时进程不监听端口、不生成令牌。
func TestRun_TerminalDisabledDoesNotListen(t *testing.T) {
	addr := freeAddr(t)
	path := createTestConfigFile(t, "server:\n  listen: "+addr+"\nplatforms:\n  terminal:\n    enabled: false\n")
	args := os.Args
	t.Cleanup(func() { os.Args = args })
	os.Args = []string{"saber", "-c", path}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, matrix.BuildInfo{Version: "test"}) }()

	// 给装配留出时间后确认端口仍空闲、且没有生成令牌。
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("服务提前退出: %v", err)
	default:
	}
	listener, err := net.Listen("tcp", addr)
	require.NoError(t, err, "关闭 terminal 后不应监听端口")
	require.NoError(t, listener.Close())

	tokenPath := filepath.Join(filepath.Dir(path), ".saber-token")
	_, err = os.Stat(tokenPath)
	require.True(t, os.IsNotExist(err), "关闭 terminal 后不应生成令牌")

	cancel()
	require.NoError(t, <-done)
}

// TestRun_TerminalServesLocalAPI 验证终端平台接管 HTTP 入口：令牌由平台创建，接口可用。
func TestRun_TerminalServesLocalAPI(t *testing.T) {
	addr := freeAddr(t)
	path := createTestConfigFile(t, "server:\n  listen: "+addr+"\n")
	args := os.Args
	t.Cleanup(func() { os.Args = args })
	os.Args = []string{"saber", "-c", path}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, matrix.BuildInfo{Version: "test"}) }()

	tokenPath := filepath.Join(filepath.Dir(path), ".saber-token")
	require.Eventually(t, func() bool {
		_, err := os.Stat(tokenPath)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond, "终端平台启动后应创建令牌")

	token, err := os.ReadFile(tokenPath)
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/info", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, response.Body.Close())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(body))

	cancel()
	require.NoError(t, <-done)
}
