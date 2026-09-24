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
	"rua.plus/saber/internal/platform"
)

// violetOnlyConfig 构造「关掉 Matrix、只开 Violet」的配置：
// 这正是本次装配修复要证明的场景——Matrix 不启用时平台注册表依然有内容。
func violetOnlyConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.MCP.Enabled = false
	cfg.Platforms.Matrix.Enabled = false
	cfg.Platforms.Violet.Enabled = true
	cfg.Platforms.Violet.Endpoint = "http://127.0.0.1:1"
	cfg.Platforms.Violet.BotToken = "violet_bot_test"
	return cfg
}

// TestInitServices_RegistersVioletWithoutMatrix 验证 Matrix 关闭时 violet 仍能注册并被启用。
func TestInitServices_RegistersVioletWithoutMatrix(t *testing.T) {
	cfg := violetOnlyConfig()
	state := &appState{cfg: cfg, flags: &cli.Flags{ConfigPath: filepath.Join(t.TempDir(), "config.yaml")}, services: &services{}}
	defer state.shutdown(func() {})

	require.NoError(t, state.initServices())
	require.Nil(t, state.services.client, "Matrix 未启用时不应有客户端")
	require.NotNil(t, state.services.aiService.Tasks(), "任务服务与平台入口无关，仍须就绪")

	violet, ok := state.services.platforms.Lookup("violet")
	require.True(t, ok, "violet 平台应已注册")
	delivery, ok := violet.(platform.TaskDelivery)
	require.True(t, ok, "violet 应实现任务投递端口")
	require.NotNil(t, delivery.DeliveryAdapter(), "violet 的投递 adapter 不能为空")
	_, ok = state.services.platforms.Lookup("matrix")
	require.False(t, ok, "Matrix 关闭时不应注册其平台接入端")

	enabled := state.services.platforms.Enabled(cfg)
	require.Len(t, enabled, 1)
	require.Equal(t, "violet", enabled[0].Name())
}

// TestInitServices_RejectsInvalidVioletConfig 验证配置问题在启动阶段就报出来，
// 而不是等平台 Start 失败只留下一条 warning。
func TestInitServices_RejectsInvalidVioletConfig(t *testing.T) {
	cfg := violetOnlyConfig()
	cfg.Platforms.Violet.BotToken = ""
	state := &appState{cfg: cfg, flags: &cli.Flags{ConfigPath: filepath.Join(t.TempDir(), "config.yaml")}, services: &services{}}
	defer state.shutdown(func() {})

	require.ErrorContains(t, state.initServices(), "violet 平台配置无效")
}

// TestRun_VioletOnlyStillServes 验证只开 violet 的常驻服务能起来并干净退出：
// 站点不可达只影响平台自己的重连，不该拖走 HTTP 服务。
func TestRun_VioletOnlyStillServes(t *testing.T) {
	args := os.Args
	t.Cleanup(func() { os.Args = args })
	path := createTestConfigFile(t, "server:\n  listen: 127.0.0.1:0\nai:\n  enabled: true\n  providers:\n    openai:\n      type: openai\n      base_url: \"http://127.0.0.1:1/v1\"\n      api_key: test\n  default_model: openai.local\nplatforms:\n  violet:\n    enabled: true\n    endpoint: \"http://127.0.0.1:1\"\n    bot_token: violet_bot_test\n")
	os.Args = []string{"saber", "-c", path}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, matrix.BuildInfo{Version: "test"}) }()

	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(filepath.Dir(path), ".saber-token"))
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("violet 重连循环挡住了优雅退出")
	}
}
