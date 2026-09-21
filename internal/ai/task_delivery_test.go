package ai

import (
	"context"
	"encoding/json"
	"fmt"
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
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/task"
)

func TestTaskDelivery_SlowMultipleFilesResumeWithoutReupload(t *testing.T) {
	var mu sync.Mutex
	uploads := 0
	sends := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			if _, err := fmt.Fprint(w, `{"event_id":"$source","sender":"@alice:test","type":"m.room.message","content":{"msgtype":"m.text","body":"files"}}`); err != nil {
				t.Error(err)
			}
			return
		}
		if strings.Contains(r.URL.Path, "/upload") {
			select {
			case <-time.After(6 * time.Second):
			case <-r.Context().Done():
				return
			}
			mu.Lock()
			uploads++
			mu.Unlock()
			_, err := fmt.Fprint(w, `{"content_uri":"mxc://test/file"}`)
			if err != nil {
				t.Error(err)
			}
			return
		}
		var content event.MessageEventContent
		if err := json.NewDecoder(r.Body).Decode(&content); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		sends[content.Body]++
		n := sends[content.Body]
		mu.Unlock()
		if content.Body == "second.txt" && n == 1 {
			w.WriteHeader(503)
			_, err := fmt.Fprint(w, `{"errcode":"M_UNKNOWN","error":"offline"}`)
			if err != nil {
				t.Error(err)
			}
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]string{"event_id": "$" + content.Body}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client, err := mautrix.NewClient(server.URL, "@bot:test", "test")
	require.NoError(t, err)
	client.DefaultHTTPRetries = 0
	commands := matrix.NewCommandService(client, "@bot:test", nil)
	dir, logs := t.TempDir(), t.TempDir()
	cfg := config.ExecutionConfig{Enabled: true, LogDir: logs, Workspaces: map[string]config.WorkspaceConfig{"w": {Path: dir}}, Grants: []config.ExecutionGrant{{Platform: "matrix", Account: "@bot:test", Room: "!room:test", Users: []string{"@alice:test"}, Workspace: "w", Tools: []string{"read_file"}}}}
	executor, err := execution.New(cfg, nil, nil)
	require.NoError(t, err)
	artifactDir := filepath.Join(logs, "task-1", "artifacts")
	require.NoError(t, os.MkdirAll(artifactDir, 0700))
	for i, name := range []string{"first.txt", "second.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(artifactDir, fmt.Sprintf("%032d-%s", i, name)), []byte(name), 0600))
	}
	s := &Service{executor: executor, matrixService: commands}
	adapter := matrix.NewChatAdapter(commands, nil, config.DefaultAIConfig().Media, false, nil)
	manager, err := task.Open(filepath.Join(t.TempDir(), "tasks.db"), func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{Status: agent.Completed, Content: "done"}, nil
	}, func(ctx context.Context, t task.Task) (string, error) { return s.deliverTask(ctx, adapter, t) })
	require.NoError(t, err)
	defer func() { require.NoError(t, manager.Close()) }()
	s.tasks = manager
	msg := chat.Message{ID: "$source", SenderID: "@alice:test", Text: "files", Session: chat.Session{Platform: "matrix", Account: "@bot:test", Conversation: "!room:test"}}
	source, err := manager.Submit(context.Background(), msg, dir, agent.Request{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err := manager.Get(context.Background(), msg.Session, source.ID)
		return err == nil && got.Delivery == "sent"
	}, 20*time.Second, 25*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, uploads)
	require.Equal(t, 1, sends["first.txt"])
	require.Equal(t, 2, sends["second.txt"])
	require.Equal(t, 1, sends["任务 #1：completed\ndone"])
}
