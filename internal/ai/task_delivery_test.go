package ai

import (
	"context"
	"encoding/json"
	"errors"
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
	adapter := matrix.NewChatAdapter(commands, nil, config.DefaultMatrixConfig().Media, false, nil)
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
	require.Equal(t, 1, sends["done"])
}

type replyStateSink struct {
	mu      sync.Mutex
	replies map[string]chat.Reply
	sends   int
}

func (s *replyStateSink) Capabilities() chat.Capabilities {
	return chat.Capabilities{Edit: true, ReplyState: true}
}
func (s *replyStateSink) Send(_ context.Context, reply chat.Reply) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replies == nil {
		s.replies = make(map[string]chat.Reply)
	}
	if _, ok := s.replies[reply.TransactionID]; !ok {
		s.replies[reply.TransactionID] = reply
		s.sends++
	}
	return reply.TransactionID, nil
}
func (s *replyStateSink) Edit(_ context.Context, id string, reply chat.Reply) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies[id] = reply
	return nil
}
func (s *replyStateSink) SetTyping(context.Context, chat.Session, bool) error { return nil }

func TestTaskDelivery_ReplyStateKeepsPartialFailure(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseTask := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseTask()
	sink := &replyStateSink{}
	s := &Service{config: config.DefaultConfig(), taskDir: t.TempDir()}
	manager, err := task.Open(filepath.Join(t.TempDir(), "tasks.db"), func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		<-release
		partial := agent.Response{Content: "部分正文", Thinking: "公开摘要"}
		return agent.Result{Status: agent.Failed, Rounds: []agent.Round{{Response: partial, Attempts: []agent.Attempt{{Response: partial}}}}}, errors.New("upstream failed")
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	s.tasks = manager
	require.NoError(t, s.RegisterTaskDelivery("violet", sink))
	message := chat.Message{Session: chat.Session{Platform: "violet", Account: "bot", Conversation: "room"}, ID: "question", SenderID: "user", Text: "提问"}
	require.NoError(t, s.submitTask(context.Background(), message, agent.Request{}, sink))
	sink.mu.Lock()
	sends := sink.sends
	var pending chat.Reply
	for _, reply := range sink.replies {
		pending = reply
	}
	sink.mu.Unlock()
	require.Equal(t, 1, sends)
	require.Equal(t, chat.ReplyPending, pending.Status)
	require.Empty(t, pending.Text)
	releaseTask()
	require.Eventually(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		for _, reply := range sink.replies {
			return reply.Status == chat.ReplyFailed && reply.Text == "部分正文" && reply.Thinking == "公开摘要" && reply.ErrorCode == "failed" && sink.sends == 1
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
}

func TestTaskStream_ReplyStateUpdatesSameMessage(t *testing.T) {
	sink := &replyStateSink{}
	message := chat.Message{Session: chat.Session{Platform: "violet", Account: "bot", Conversation: "room"}, ID: "question"}
	p := &taskStream{
		task: task.Task{ID: 1, Message: message}, adapter: sink,
		display: chat.Display{EditInterval: time.Millisecond}, status: chat.ReplyPending,
		updates: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go p.run(context.Background())
	p.event(agent.Event{Kind: agent.ModelStarted})
	p.event(agent.Event{Kind: agent.ThinkingDelta, Text: "摘要"})
	require.Eventually(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		for _, reply := range sink.replies {
			return reply.Status == chat.ReplyThinking && reply.Thinking == "摘要"
		}
		return false
	}, time.Second, time.Millisecond)
	p.event(agent.Event{Kind: agent.TextDelta, Text: "正文"})
	require.Eventually(t, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		for _, reply := range sink.replies {
			return reply.Status == chat.ReplyStreaming && reply.Text == "正文" && reply.Thinking == "摘要"
		}
		return false
	}, time.Second, time.Millisecond)
	p.close()
	sink.mu.Lock()
	sends := sink.sends
	sink.mu.Unlock()
	require.Equal(t, 1, sends)
}
