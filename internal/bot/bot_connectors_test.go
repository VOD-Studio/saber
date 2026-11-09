package bot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/cli"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/platform"
)

// stubDeliveryAdapter 是只满足 chat.Adapter 最小面值的测试替身，用于证明
// 任务投递注册的唯一性约束。
type stubDeliveryAdapter struct{}

func (stubDeliveryAdapter) Capabilities() chat.Capabilities                     { return chat.Capabilities{} }
func (stubDeliveryAdapter) Send(context.Context, chat.Reply) (string, error)    { return "", nil }
func (stubDeliveryAdapter) Edit(context.Context, string, chat.Reply) error      { return nil }
func (stubDeliveryAdapter) SetTyping(context.Context, chat.Session, bool) error { return nil }

// lineConnectorConfig 返回一个可离线运行的 line 连接器配置：回调监听本地端口，
// 凭据与允许用户固定，签名校验使用 secret。
func lineConnectorConfig(t *testing.T) config.ConnectorConfig {
	t.Helper()
	return config.ConnectorConfig{
		Enabled:      true,
		Account:      "account",
		Tenant:       "tenant",
		Token:        "token",
		Secret:       "secret",
		AllowedUsers: []string{"user"},
		Listen:       freeAddr(t),
	}
}

// TestInitServices_RegistersConnectorPlatforms 验证 AI 关闭分支也会把显式启用的
// 连接器登记进平台注册表（只登记连接，不注册任务投递）。
func TestInitServices_RegistersConnectorPlatforms(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AI.Enabled = false
	cfg.Platforms.Terminal.Enabled = false
	cfg.Platforms.Connectors["line"] = lineConnectorConfig(t)
	state := newStateForPlatforms(t, cfg)
	defer state.shutdown(func() {})

	require.NoError(t, state.initServices())
	_, ok := state.services.platforms.Lookup("line")
	require.True(t, ok, "启用的连接器应注册进平台注册表")
	enabled := state.services.platforms.Enabled(cfg)
	require.Len(t, enabled, 1)
	require.Equal(t, "line", enabled[0].Name())
}

// TestConnectorPlatform_DeliversInboundToSharedHandler 端到端验证入站链路：
// 启动连接器监听后投递带合法签名的 LINE 回调，共享 chat Handler 应收到
// 带可信身份的规范化消息。
func TestConnectorPlatform_DeliversInboundToSharedHandler(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AI.Enabled = false
	cfg.Platforms.Terminal.Enabled = false
	cfg.Platforms.Connectors["line"] = lineConnectorConfig(t)
	state := newStateForPlatforms(t, cfg)
	defer state.shutdown(func() {})

	require.NoError(t, state.initServices())
	p, ok := state.services.platforms.Lookup("line")
	require.True(t, ok)
	connector, ok := p.(platform.Platform)
	require.True(t, ok)

	messages := make(chan chat.Message, 1)
	handler := func(ctx context.Context, msg chat.Message, adapter chat.Adapter) (agent.Result, error) {
		select {
		case messages <- msg:
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
		return agent.Result{}, nil
	}
	require.NoError(t, connector.Start(context.Background(), handler))
	defer connector.Stop()

	body := []byte(`{"events":[{"type":"message","source":{"type":"user","userId":"user"},"message":{"id":"m1","type":"text","text":"hello"}}]}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write(body)
	request, err := http.NewRequest(http.MethodPost, "http://"+cfg.Platforms.Connectors["line"].Listen+"/events", bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("X-Line-Signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)

	select {
	case msg := <-messages:
		require.Equal(t, "user", msg.SenderID)
		require.Equal(t, "line", msg.Session.Platform)
		require.Equal(t, "account", msg.Session.Account)
		require.True(t, msg.Direct)
		require.Equal(t, "hello", msg.Text)
	case <-time.After(2 * time.Second):
		t.Fatal("2 秒内共享 Handler 未收到入站消息")
	}
}

// TestRegisterConnectorPlatforms_RegistersTaskDelivery 验证 AI 启用路径的投递接线：
// 连接器登记后，同平台重复注册任务投递必须报错，即证明 helper 已注册真实 adapter。
func TestRegisterConnectorPlatforms_RegistersTaskDelivery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}})
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: server.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.StreamEnabled = false
	cfg.Platforms.Terminal.Enabled = false
	cfg.Platforms.Connectors["line"] = lineConnectorConfig(t)

	aiSvc, err := ai.NewService(cfg)
	require.NoError(t, err)
	defer aiSvc.Stop()
	require.NoError(t, aiSvc.EnableTasks(filepath.Join(t.TempDir(), "tasks.db")))

	state := &appState{
		cfg:      cfg,
		flags:    &cli.Flags{ConfigPath: filepath.Join(t.TempDir(), "config.yaml")},
		services: &services{platforms: platform.NewRegistry(), aiService: aiSvc},
	}
	require.NoError(t, state.registerConnectorPlatforms(aiSvc))

	p, ok := state.services.platforms.Lookup("line")
	require.True(t, ok)
	require.Equal(t, "line", p.Name())
	err = aiSvc.RegisterTaskDelivery("line", stubDeliveryAdapter{})
	require.Error(t, err, "同平台重复注册被拒绝即证明投递已登记")
}
