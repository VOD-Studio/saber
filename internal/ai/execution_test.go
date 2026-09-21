package ai

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

func TestTaskAutonomousDockerWorkflow(t *testing.T) {
	image := os.Getenv("SABER_EXECUTION_TEST_IMAGE")
	if image == "" {
		t.Skip("设置 SABER_EXECUTION_TEST_IMAGE 运行真实容器与模拟群聊验收")
	}
	var rounds atomic.Int32
	var uploads atomic.Int32
	var failFile atomic.Bool
	failFile.Store(true)
	var mu sync.Mutex
	var sentFiles []event.MessageEventContent
	var filePaths []string
	var encryptedUploads [][]byte
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		n := rounds.Add(1)
		var name string
		var args map[string]any
		switch n {
		case 1:
			if len(req.Tools) != 6 {
				t.Errorf("expected authorized local tools and task tool, got %d", len(req.Tools))
			}
			name = "exec"
			args = map[string]any{"command": "cat not-created-yet.txt"}
		case 2:
			last := req.Messages[len(req.Messages)-1].Content
			if !strings.Contains(last, "tool_failed") || !strings.Contains(last, "exit_code") || !strings.Contains(last, "log_path") {
				t.Errorf("lost execution failure details: %s", last)
			}
			name = "exec"
			args = map[string]any{"command": "printf draft > answer.txt"}
		case 3:
			name = "apply_patch"
			args = map[string]any{"path": "answer.txt", "edits": []any{map[string]any{"old": "draft", "new": "final artifact"}}}
		case 4:
			name = "read_file"
			args = map[string]any{"path": "answer.txt", "deliver": true}
		case 6:
			name = "exec"
			args = map[string]any{"command": "sleep 60 & wait"}
		default:
			if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "文件已完成"}, "finish_reason": "stop"}}}); err != nil {
				t.Error(err)
			}
			return
		}
		data, err := json.Marshal(args)
		if err != nil {
			t.Error(err)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []openai.ToolCall{{ID: fmt.Sprint(n), Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: name, Arguments: string(data)}}}}, "finish_reason": "tool_calls"}}}); err != nil {
			t.Error(err)
		}
	}))
	defer model.Close()
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/upload") {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			encryptedUploads = append(encryptedUploads, data)
			mu.Unlock()
			uploads.Add(1)
			if _, err := fmt.Fprint(w, `{"content_uri":"mxc://test/artifact"}`); err != nil {
				t.Error(err)
			}
			return
		}
		if strings.Contains(r.URL.Path, "/send/") {
			var content event.MessageEventContent
			if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
				t.Error(err)
				return
			}
			if content.MsgType == event.MsgFile {
				mu.Lock()
				sentFiles = append(sentFiles, content)
				filePaths = append(filePaths, r.URL.Path)
				mu.Unlock()
				if failFile.Swap(false) {
					w.WriteHeader(503)
					if _, err := fmt.Fprint(w, `{"errcode":"M_UNKNOWN","error":"offline"}`); err != nil {
						t.Error(err)
					}
					return
				}
			}
			if _, err := fmt.Fprint(w, `{"event_id":"$reply"}`); err != nil {
				t.Error(err)
			}
			return
		}
		if _, err := fmt.Fprint(w, `{"event_id":"$source","sender":"@alice:test","type":"m.room.message","content":{"msgtype":"m.text","body":"work"}}`); err != nil {
			t.Error(err)
		}
	}))
	defer homeserver.Close()
	client, err := mautrix.NewClient(homeserver.URL, "@bot:test", "test")
	require.NoError(t, err)
	client.DefaultHTTPRetries = 0
	commands := matrix.NewCommandService(client, "@bot:test", nil)
	cfg := config.DefaultAIConfig()
	cfg.Enabled = true
	cfg.Provider = "openai"
	cfg.BaseURL = model.URL
	cfg.APIKey = "test"
	cfg.DefaultModel = "local"
	cfg.StreamEnabled = false
	cfg.ToolCalling.MaxIterations = 6
	cfg.ToolCalling.TimeoutSeconds = 30
	service, err := NewService(&cfg, commands, nil, nil)
	require.NoError(t, err)
	defer service.Stop()
	workdir, logs := t.TempDir(), t.TempDir()
	execCfg := config.ExecutionConfig{Enabled: true, Image: image, LogDir: logs, Workspaces: map[string]config.WorkspaceConfig{"project": {Path: workdir}}, Grants: []config.ExecutionGrant{{Platform: "matrix", Account: "@bot:test", Room: "!room:test", Users: []string{"@alice:test"}, Workspace: "project", Tools: []string{"exec", "read_file", "list_files", "write_file", "apply_patch"}}}}
	require.NoError(t, service.ConfigureExecution(execCfg, nil, []string{"test"}))
	require.NoError(t, service.EnableTasks(filepath.Join(t.TempDir(), "tasks.db")))
	ctx := matrix.WithMessageRelations(matrix.WithEventID(context.Background(), "$source"), "", "$thread")
	// 一个群聊请求驱动多轮操作，第一次命令失败后继续修正并交付。
	require.NoError(t, service.handleAICommand(ctx, "@alice:test", "!room:test", service.GetModelRegistry().GetDefault(), []string{"生成一个文件并交付"}))
	session := chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test", Thread: "$thread"}
	require.Eventually(t, func() bool {
		tasks, err := service.tasks.List(context.Background(), session)
		return err == nil && len(tasks) == 1 && tasks[0].Status == "completed" && tasks[0].Delivery == "sent"
	}, 15*time.Second, 20*time.Millisecond)
	require.EqualValues(t, 5, rounds.Load())
	require.EqualValues(t, 2, uploads.Load())
	mu.Lock()
	require.Len(t, sentFiles, 2)
	require.Equal(t, filePaths[0], filePaths[1])
	require.Equal(t, "answer.txt", sentFiles[1].Body)
	require.Equal(t, "$source", string(sentFiles[1].RelatesTo.GetReplyTo()))
	require.Equal(t, "$thread", string(sentFiles[1].RelatesTo.GetThreadParent()))
	plaintext := append([]byte(nil), encryptedUploads[1]...)
	err = sentFiles[1].File.DecryptInPlace(plaintext)
	require.NoError(t, err)
	require.Equal(t, "final artifact", string(plaintext))
	mu.Unlock()
	// 未授权成员不能通过伪造工具参数获得本地执行能力。
	_, err = service.executor.Workspace(chat.Identity{Session: session, SenderID: "@mallory:test"})
	require.Error(t, err)
	// !task cancel 终止正在执行的容器，而不只是停止聊天端等待。
	cancelCtx := matrix.WithEventID(context.Background(), "$cancel-source")
	require.NoError(t, service.handleAICommand(cancelCtx, "@alice:test", "!room:test", service.GetModelRegistry().GetDefault(), []string{"取消测试"}))
	canonicalLogs, err := filepath.EvalSymlinks(logs)
	require.NoError(t, err)
	owner := fmt.Sprintf("%x", sha256.Sum256([]byte(canonicalLogs)))
	containers := func() string {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=saber.executor="+owner).Output()
		if err != nil {
			return "docker-error"
		}
		return strings.TrimSpace(string(out))
	}
	require.Eventually(t, func() bool { return containers() != "" }, 5*time.Second, 20*time.Millisecond)
	tasks, err := service.tasks.List(context.Background(), session)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	command, ok := commands.GetCommand("task")
	require.True(t, ok)
	require.NoError(t, command.Handler.Handle(context.Background(), "@alice:test", "!room:test", []string{"cancel", fmt.Sprint(tasks[0].ID)}))
	require.Eventually(t, func() bool {
		task, err := service.tasks.Get(context.Background(), session, tasks[0].ID)
		return err == nil && task.Status == "cancelled" && containers() == ""
	}, 10*time.Second, 20*time.Millisecond)
	require.EqualValues(t, 6, rounds.Load())
}
