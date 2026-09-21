package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/task"
)

func TestTaskLogs_FailedTimedOutCancelledAndInterrupted(t *testing.T) {
	var mu sync.Mutex
	var upload []byte
	files := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/upload") {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			upload = data
			mu.Unlock()
			if _, err = fmt.Fprint(w, `{"content_uri":"mxc://test/log"}`); err != nil {
				t.Error(err)
			}
			return
		}
		var content event.MessageEventContent
		if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
			t.Error(err)
			return
		}
		if content.MsgType == event.MsgFile {
			mu.Lock()
			data := append([]byte(nil), upload...)
			mu.Unlock()
			if err := content.File.DecryptInPlace(data); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			files[content.Body] = data
			mu.Unlock()
		}
		if _, err := fmt.Fprint(w, `{"event_id":"$logs"}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client, err := mautrix.NewClient(server.URL, "bot", "test")
	require.NoError(t, err)
	commands := matrix.NewCommandService(client, "bot", nil)
	cfg := *config.DefaultConfig()
	s, err := NewService(&cfg, commands, nil, nil)
	require.NoError(t, err)
	defer s.Stop()
	dir, logs := t.TempDir(), t.TempDir()
	s.executor, err = execution.New(config.ExecutionConfig{LogDir: logs}, nil, nil)
	require.NoError(t, err)
	started := make(chan struct{})
	path := filepath.Join(t.TempDir(), "tasks.db")
	s.tasks, err = task.Open(path, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		emit(agent.Event{Kind: agent.ToolStarted, Tool: agent.ToolRecord{Content: "full tool trace"}})
		if req.Model == "interrupted" {
			close(started)
			<-ctx.Done()
			return agent.Result{}, ctx.Err()
		}
		return agent.Result{Status: agent.Status(req.Model)}, errors.New("execution failed")
	}, func(context.Context, task.Task) (string, error) { return "$report", nil }, task.Options{Manage: func(i chat.Identity) bool { return i.SenderID == "admin" && i.Session.Conversation == "room" }})
	require.NoError(t, err)
	ctx := context.Background()
	session := chat.Session{Platform: "matrix", Account: "bot", Conversation: "room"}
	for index, status := range []string{"failed", "timed_out", "cancelled", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			logDir := filepath.Join(logs, fmt.Sprintf("task-%d", index+1))
			require.NoError(t, os.MkdirAll(logDir, 0700))
			body := strings.Repeat("完整日志\n", 3000)
			require.NoError(t, os.WriteFile(filepath.Join(logDir, "output-test.log"), []byte(body), 0600))
			source, err := s.tasks.Submit(ctx, chat.Message{ID: status, SenderID: "alice", Session: session, Text: "do work"}, dir, agent.Request{Model: status})
			require.NoError(t, err)
			if status == "interrupted" {
				<-started
				require.NoError(t, s.tasks.Close())
				s.tasks, err = task.Open(path, func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
					return agent.Result{}, errors.New("unexpected replay")
				}, func(context.Context, task.Task) (string, error) { return "$report", nil }, task.Options{Manage: func(i chat.Identity) bool { return i.SenderID == "admin" && i.Session.Conversation == "room" }})
				require.NoError(t, err)
			}
			require.Eventually(t, func() bool {
				got, err := s.tasks.Get(ctx, session, source.ID)
				return err == nil && got.Status != "queued" && got.Status != "running"
			}, time.Second, 10*time.Millisecond)
			identity := chat.Identity{Session: session, SenderID: "bob"}
			_, err = s.taskOperation(ctx, identity, "logs", source.ID)
			require.Error(t, err)
			identity.SenderID = "alice"
			reply, err := s.taskOperation(matrix.WithEventID(ctx, "$download-"+id.EventID(status)), identity, "logs", source.ID)
			require.NoError(t, err)
			require.Contains(t, reply, "1 份命令日志")
			mu.Lock()
			require.Equal(t, body, string(files[fmt.Sprintf("task-%d-output-test.log", source.ID)]))
			require.Contains(t, string(files[fmt.Sprintf("task-%d.json", source.ID)]), "full tool trace")
			mu.Unlock()
			identity.SenderID = "admin"
			_, err = s.taskOperation(ctx, identity, "logs", source.ID)
			require.NoError(t, err)
			identity.Session.Conversation = "other"
			_, err = s.taskOperation(ctx, identity, "logs", source.ID)
			require.Error(t, err)
		})
	}
}
