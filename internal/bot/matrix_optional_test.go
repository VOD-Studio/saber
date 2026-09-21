package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/cli"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// TestRun_MatrixOptional 覆盖真实启动分支，关闭入口时不应尝试创建 Matrix 客户端。
func TestRun_MatrixOptional(t *testing.T) {
	for _, body := range []string{"{}", "matrix:\n  enabled: false\n  homeserver: invalid\n  enable_e2ee: true\n  e2ee_session_path: ''\n"} {
		t.Run(body, func(t *testing.T) {
			args, flags := os.Args, flag.CommandLine
			t.Cleanup(func() { os.Args, flag.CommandLine = args, flags })
			path := createTestConfigFile(t, body)
			os.Args = []string{"saber", "-c", path}
			flag.CommandLine = flag.NewFlagSet("saber", flag.ContinueOnError)
			require.NoError(t, Run(matrix.BuildInfo{Version: "test"}))
		})
	}
}

func TestTerminal_SharedServiceAndHistory(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses", r.URL.Path)
		var req struct {
			Input []map[string]any `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		requests++
		if requests == 1 {
			require.Len(t, req.Input, 1)
		} else {
			require.Len(t, req.Input, 3)
			require.Equal(t, "first", req.Input[0]["content"])
			require.Equal(t, "remembered", req.Input[1]["content"])
			require.Equal(t, "second", req.Input[2]["content"])
		}
		_, err := fmt.Fprint(w, `{"status":"completed","model":"test","output":[{"type":"message","content":[{"type":"output_text","text":"remembered"}]}]}`)
		require.NoError(t, err)
	}))
	defer server.Close()
	cfg := config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.StreamEnabled = false
	cfg.AI.DefaultModel = "test.model"
	cfg.AI.Providers = map[string]config.ProviderConfig{"test": {API: "openai-responses", BaseURL: server.URL + "/v1", Models: map[string]config.ModelConfig{"model": {Model: "test"}}}}
	cfg.MCP.Enabled = false
	state := &appState{cfg: cfg, flags: &cli.Flags{ConfigPath: filepath.Join(t.TempDir(), "config.yaml")}, services: &services{}}
	defer state.shutdown(func() {})
	require.NoError(t, state.initServices())
	require.Nil(t, state.services.client)
	require.Nil(t, state.services.mediaService)
	require.Nil(t, state.services.commandService)
	require.Nil(t, state.services.proactiveManager)
	var out bytes.Buffer
	require.NoError(t, cli.RunTerminal(context.Background(), io.NopCloser(strings.NewReader("first\nsecond\n/exit\n")), &out, state.services.aiService.HandleChat, cfg.AI.DefaultModel))
	require.Equal(t, 2, requests)
	require.Equal(t, 2, strings.Count(out.String(), "Saber> remembered"))
}

func TestExampleConfig_DefaultsToTerminal(t *testing.T) {
	path := createTestConfigFile(t, config.ExampleConfig())
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.False(t, cfg.Matrix.Enabled)
	require.Empty(t, cfg.Matrix.UserID)
	require.Empty(t, cfg.Matrix.AccessToken)
	require.Equal(t, 8192, cfg.AI.MaxTokens)
}
