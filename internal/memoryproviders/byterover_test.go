package memoryproviders

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

func TestByteRover_OfficialJSONEnvelopeBoundaries(t *testing.T) {
	query, err := byteRoverParse([]byte("ByteRover daemon started\n"+`{"command":"query","success":true,"data":{"status":"matches","matchedDocs":[{"path":"preferences/tea.html","rendered_md":"User prefers jasmine tea","score":0.9}]},"timestamp":"2026-11-09T00:00:00Z"}`+"\n"), "query")
	require.NoError(t, err)
	require.Len(t, query["matchedDocs"], 1)
	correction, err := byteRoverParse([]byte(`{"command":"curate","success":false,"data":{"ok":false,"status":"needs-llm-step","step":"correct-html","prompt":"Fix the validation error","sessionId":"f338d6bd-cbbc-4689-b8c1-752e344b0987"}}`), "curate")
	require.NoError(t, err)
	require.Equal(t, "correct-html", correction["step"])
	for _, data := range []string{`{"success":true}`, `{"command":"curate","success":true,"data":{"status":"done"}}`, `{"command":"query","success":false,"data":{"status":"error"}}`, `{"command":"query","data":{}}`} {
		_, err = byteRoverParse([]byte(data), "query")
		require.Error(t, err)
	}
}

func TestByteRover_CanceledOrDeniedNeverLaunchesCLI(t *testing.T) {
	launches := 0
	host := toolkit.Host{Workspace: func(context.Context) (string, error) { return t.TempDir(), nil }, Check: func(context.Context, string, bool) error { return errors.New("denied") }, StartProcess: func(context.Context, []string, map[string]string) (toolkit.Process, error) {
		launches++
		return nil, errors.New("must not launch")
	}}
	backend, err := NewByteRover(config.MemoryBackendConfig{}, host)
	require.NoError(t, err)
	defer func() { require.NoError(t, backend.Close()) }()
	_, err = backend.Call(nativeMemoryTestContext("alice"), "initialize", nil)
	require.ErrorContains(t, err, "denied")
	require.Zero(t, launches)
	ctx, cancel := context.WithCancel(nativeMemoryTestContext("alice"))
	cancel()
	_, err = backend.Call(ctx, "brv_query", nativeMemoryEnvelope(map[string]any{"query": "tea"}))
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, launches)
	_, err = backend.Call(nativeMemoryTestContext("alice"), "tool", nativeMemoryEnvelope(map[string]any{"tool_name": "brv_curate", "arguments": map[string]any{"content": "tea", "scope": "someone-else"}}))
	require.Error(t, err)
	require.Zero(t, launches)
}

type byteRoverSmokeProcess struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stdin  io.WriteCloser
	stderr bytes.Buffer
}

func (p *byteRoverSmokeProcess) Read(data []byte) (int, error)  { return p.stdout.Read(data) }
func (p *byteRoverSmokeProcess) Write(data []byte) (int, error) { return p.stdin.Write(data) }
func (p *byteRoverSmokeProcess) CloseWrite() error              { return p.stdin.Close() }
func (p *byteRoverSmokeProcess) Wait() error                    { return p.cmd.Wait() }
func (p *byteRoverSmokeProcess) Close() error {
	_ = p.stdin.Close()
	_ = p.stdout.Close()
	if p.cmd.Process != nil {
		err := p.cmd.Process.Kill()
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
	}
	return nil
}

// 此测试只调用安装好的真实 brv 和实际模型，不生成 echo 脚本或另写 vendor wrapper。
func TestByteRover_RealCLIContextAndSyncSmoke(t *testing.T) {
	if os.Getenv("SABER_NATIVE_MEMORY_SMOKE") != "1" {
		t.Skip("opt-in real brv smoke: set SABER_NATIVE_MEMORY_SMOKE=1 and install byterover-cli@3.16.1")
	}
	binary := os.Getenv("SABER_TEST_BRV_BINARY")
	if binary == "" {
		var err error
		binary, err = exec.LookPath("brv")
		if err != nil {
			t.Skip("real brv CLI is missing; install npm package byterover-cli@3.16.1")
		}
	}
	binary, err := filepath.Abs(binary)
	require.NoError(t, err)
	model := os.Getenv("SABER_TEST_BRV_MODEL")
	key := os.Getenv("OPENAI_API_KEY")
	if model == "" || key == "" {
		t.Skip("real curation needs SABER_TEST_BRV_MODEL and OPENAI_API_KEY; no synthetic model response is used")
	}
	clientConfig := openai.DefaultConfig(key)
	if endpoint := os.Getenv("OPENAI_BASE_URL"); endpoint != "" {
		clientConfig.BaseURL = endpoint
	}
	client := openai.NewClientWithConfig(clientConfig)
	workspace := t.TempDir()
	host := toolkit.Host{
		Workspace: func(context.Context) (string, error) { return workspace, nil },
		Check: func(_ context.Context, name string, external bool) error {
			if name != "exec" || !external {
				return errors.New("unexpected smoke permission")
			}
			return nil
		},
		StartProcess: func(ctx context.Context, argv []string, environment map[string]string) (toolkit.Process, error) {
			command := exec.CommandContext(ctx, argv[0], argv[1:]...)
			command.Dir = workspace
			command.Env = os.Environ()
			for key, value := range environment {
				command.Env = append(command.Env, key+"="+value)
			}
			process := &byteRoverSmokeProcess{cmd: command}
			stdout, err := command.StdoutPipe()
			if err != nil {
				return nil, err
			}
			process.stdout = stdout
			stdin, err := command.StdinPipe()
			if err != nil {
				return nil, err
			}
			process.stdin = stdin
			command.Stderr = &process.stderr
			if err = command.Start(); err != nil {
				return nil, err
			}
			return process, nil
		},
		Complete: func(ctx context.Context, model string, messages []openai.ChatCompletionMessage) (string, error) {
			response, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{Model: model, Messages: messages})
			if err != nil {
				return "", err
			}
			if len(response.Choices) == 0 {
				return "", errors.New("real model returned no choice")
			}
			return response.Choices[0].Message.Content, nil
		},
	}
	backend, err := NewByteRover(config.MemoryBackendConfig{Options: map[string]any{"binary": binary, "model": model}, TimeoutSeconds: 300}, host)
	require.NoError(t, err)
	defer func() { require.NoError(t, backend.Close()) }()
	ctx, cancel := context.WithTimeout(nativeMemoryTestContext("smoke-alice"), 5*time.Minute)
	defer cancel()
	status, err := backend.Call(ctx, "initialize", nil)
	require.NoError(t, err)
	require.NotEmpty(t, status.(map[string]any)["cliVersion"])
	_, err = backend.Call(ctx, "sync_turn", nativeMemoryEnvelope(map[string]any{"user_content": "I prefer jasmine tea when working on Go projects.", "assistant_content": "Your stable preference is jasmine tea.", "status": "completed"}))
	require.NoError(t, err)
	value, err := backend.Call(ctx, "context", nativeMemoryEnvelope(map[string]any{"query": "What tea does the user prefer when working on Go projects?"}))
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(value.(map[string]any)["context"].(string)), "jasmine")
	scope, err := ScopedIdentity(ctx)
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(workspace, ".saber-memory", "byterover", scope, ".brv", "context-tree"))
	require.NoError(t, err)
	require.True(t, info.IsDir())
}
