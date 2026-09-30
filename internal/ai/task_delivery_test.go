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
	mu       sync.Mutex
	replies  map[string]chat.Reply
	updates  []chat.Reply
	sends    int
	attempts int
	sendErr  error
}

func (s *replyStateSink) Capabilities() chat.Capabilities {
	return chat.Capabilities{Edit: true, ReplyState: true}
}
func (s *replyStateSink) Send(_ context.Context, reply chat.Reply) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	if s.sendErr != nil {
		return "", s.sendErr
	}
	if reply.Status != "" && reply.Status != chat.ReplyPending {
		return "", errors.New("创建时只能设置 pending 状态")
	}
	if s.replies == nil {
		s.replies = make(map[string]chat.Reply)
	}
	if _, ok := s.replies[reply.TransactionID]; !ok {
		s.replies[reply.TransactionID] = reply
		s.sends++
	}
	return reply.TransactionID, nil
}

func TestTaskStream_FailedPendingUsesBackoff(t *testing.T) {
	sink := &replyStateSink{sendErr: errors.New("API 400")}
	p := &taskStream{
		task:    task.Task{ID: 1, Message: chat.Message{Session: chat.Session{Platform: "violet", Account: "bot", Conversation: "room"}, ID: "question"}},
		adapter: sink, display: chat.Display{EditInterval: time.Millisecond}, status: chat.ReplyPending,
		updates: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go p.run(context.Background())
	p.event(agent.Event{Kind: agent.ModelStarted})
	time.Sleep(50 * time.Millisecond)
	p.close()
	sink.mu.Lock()
	attempts := sink.attempts
	sink.mu.Unlock()
	require.Equal(t, 1, attempts, "占位消息失败后不应按编辑节拍持续重发")
}
func (s *replyStateSink) Edit(_ context.Context, id string, reply chat.Reply) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies[id] = reply
	s.updates = append(s.updates, reply)
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
			return reply.Status == chat.ReplyFailed && strings.Contains(reply.Text, "upstream failed") && strings.Contains(reply.Text, "部分正文") && reply.Thinking == "公开摘要" && reply.ErrorCode == "failed" && sink.sends == 1
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

func TestTaskStream_ReplyStateKeepsProgressAcrossRounds(t *testing.T) {
	sink := &replyStateSink{}
	message := chat.Message{Session: chat.Session{Platform: "violet", Account: "bot", Conversation: "room"}, ID: "question"}
	p := &taskStream{
		task: task.Task{ID: 1, Message: message}, adapter: sink,
		display: chat.Display{EditInterval: time.Millisecond}, status: chat.ReplyPending,
		updates: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go p.run(context.Background())
	defer p.close()
	matches := func(status chat.ReplyStatus, text, thinking string) bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		for _, reply := range sink.replies {
			return reply.Status == status && reply.Text == text && reply.Thinking == thinking
		}
		return false
	}
	p.event(agent.Event{Kind: agent.ModelStarted, Round: 1})
	p.event(agent.Event{Kind: agent.AttemptStarted, Round: 1})
	p.event(agent.Event{Kind: agent.ThinkingDelta, Round: 1, Text: "第一轮思考"})
	p.event(agent.Event{Kind: agent.TextDelta, Round: 1, Text: "第一轮草稿"})
	require.Eventually(t, func() bool {
		return matches(chat.ReplyStreaming, "第一轮草稿", "第一轮思考")
	}, time.Second, time.Millisecond)
	sink.mu.Lock()
	firstRoundUpdates := len(sink.updates)
	sink.mu.Unlock()

	p.event(agent.Event{Kind: agent.ModelStarted, Round: 2})
	p.event(agent.Event{Kind: agent.AttemptStarted, Round: 2})
	p.event(agent.Event{Kind: agent.ThinkingDelta, Round: 2, Text: "第二轮思考"})
	require.Eventually(t, func() bool {
		return matches(chat.ReplyThinking, "第一轮草稿", "第一轮思考\n\n第二轮思考")
	}, time.Second, time.Millisecond)

	p.event(agent.Event{Kind: agent.TextDelta, Round: 2, Text: "最终正文第一段"})
	require.Eventually(t, func() bool {
		return matches(chat.ReplyStreaming, "最终正文第一段", "第一轮思考\n\n第二轮思考")
	}, time.Second, time.Millisecond)
	p.event(agent.Event{Kind: agent.AttemptStarted, Round: 2})
	p.event(agent.Event{Kind: agent.ThinkingDelta, Round: 2, Text: "重试思考"})
	p.event(agent.Event{Kind: agent.TextDelta, Round: 2, Text: "重试正文第一段"})
	require.Eventually(t, func() bool {
		return matches(chat.ReplyStreaming, "重试正文第一段", "第一轮思考\n\n重试思考")
	}, time.Second, time.Millisecond)
	sink.mu.Lock()
	updates := append([]chat.Reply(nil), sink.updates[firstRoundUpdates:]...)
	sink.mu.Unlock()
	require.NotEmpty(t, updates)
	for _, reply := range updates {
		require.NotEmpty(t, reply.Text)
		require.Contains(t, reply.Thinking, "第一轮思考")
	}
}

// fileSink 是带文件发送能力的假平台 adapter，用来验证任务文件交付不依赖 Matrix。
type fileSink struct {
	replyStateSink
	uploads   []chat.FileUpload
	fileSends map[string]int
	sendErr   error
}

func (f *fileSink) UploadFile(_ context.Context, upload chat.FileUpload) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads = append(f.uploads, upload)
	return []byte(`{"name":"` + upload.Name + `"}`), nil
}

func (f *fileSink) SendUploadedFile(_ context.Context, reply chat.Reply, payload []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return "", f.sendErr
	}
	if f.fileSends == nil {
		f.fileSends = map[string]int{}
	}
	f.fileSends[string(payload)]++
	return reply.TransactionID, nil
}

// newArtifactTask 准备带两个产物的 violet 任务环境，返回服务、任务源和会话。
func newArtifactTask(t *testing.T, adapter chat.Adapter) (*task.Manager, chat.Message, string) {
	t.Helper()
	dir, logs := t.TempDir(), t.TempDir()
	cfg := config.ExecutionConfig{Enabled: true, LogDir: logs, Workspaces: map[string]config.WorkspaceConfig{"w": {Path: dir}}, Grants: []config.ExecutionGrant{{Platform: "violet", Account: "bot", Room: "room", Users: []string{"user"}, Workspace: "w", Tools: []string{"read_file"}}}}
	executor, err := execution.New(cfg, nil, nil)
	require.NoError(t, err)
	artifactDir := filepath.Join(logs, "task-1", "artifacts")
	require.NoError(t, os.MkdirAll(artifactDir, 0700))
	for i, name := range []string{"first.txt", "second.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(artifactDir, fmt.Sprintf("%032d-%s", i, name)), []byte(name), 0600))
	}
	s := &Service{config: config.DefaultConfig(), executor: executor, taskDir: t.TempDir()}
	manager, err := task.Open(filepath.Join(t.TempDir(), "tasks.db"), func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{Status: agent.Completed, Content: "done"}, nil
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	s.tasks = manager
	require.NoError(t, s.RegisterTaskDelivery("violet", adapter))
	msg := chat.Message{ID: "question", SenderID: "user", Text: "files", Session: chat.Session{Platform: "violet", Account: "bot", Conversation: "room"}}
	return manager, msg, dir
}

func waitDelivered(t *testing.T, manager *task.Manager, msg chat.Message, dir string) task.Task {
	t.Helper()
	source, err := manager.Submit(context.Background(), msg, dir, agent.Request{})
	require.NoError(t, err)
	require.Equal(t, int64(1), source.ID)
	var got task.Task
	require.Eventually(t, func() bool {
		got, err = manager.Get(context.Background(), msg.Session, source.ID)
		return err == nil && got.Delivery == "sent"
	}, 10*time.Second, 25*time.Millisecond)
	require.Empty(t, got.DeliveryError)
	return got
}

// TestTaskDelivery_NonMatrixPlatformDeliversFiles 非 Matrix 平台只要实现 chat.FileAdapter，
// 产物就经通用接口交付，且不依赖 matrixService。
func TestTaskDelivery_NonMatrixPlatformDeliversFiles(t *testing.T) {
	sink := &fileSink{}
	manager, msg, dir := newArtifactTask(t, sink)
	waitDelivered(t, manager, msg, dir)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.uploads, 2)
	names := []string{sink.uploads[0].Name, sink.uploads[1].Name}
	require.ElementsMatch(t, []string{"first.txt", "second.txt"}, names)
	for _, upload := range sink.uploads {
		require.Equal(t, msg.Session, upload.Session)
		require.Equal(t, "question", upload.ReplyTo)
		require.Equal(t, upload.Name, string(upload.Data))
	}
	require.Len(t, sink.fileSends, 2)
	for _, n := range sink.fileSends {
		require.Equal(t, 1, n)
	}
	var texts []string
	for _, reply := range sink.replies {
		texts = append(texts, reply.Text)
	}
	require.Equal(t, []string{"done"}, texts, "有文件发送能力时不应出现降级提示")
}

// TestTaskDelivery_NoFileCapabilityDegrades 平台没有文件发送能力（也没有 Matrix）时，
// 不 panic、不返回错误，文字结果照常送达并附一条幂等提示。
func TestTaskDelivery_NoFileCapabilityDegrades(t *testing.T) {
	sink := &replyStateSink{}
	manager, msg, dir := newArtifactTask(t, sink)
	waitDelivered(t, manager, msg, dir)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var texts []string
	for _, reply := range sink.replies {
		texts = append(texts, reply.Text)
	}
	require.Len(t, texts, 2, "文字结果和文件不可发送提示各一条")
	require.Contains(t, texts, "done")
	require.Contains(t, texts, "任务产生了 2 个文件，当前平台无法发送")
}

// TestTaskDelivery_FileSendFailureKeepsPending 文件发送失败属于可重试错误，任务保持 pending。
func TestTaskDelivery_FileSendFailureKeepsPending(t *testing.T) {
	sink := &fileSink{sendErr: errors.New("offline")}
	manager, msg, dir := newArtifactTask(t, sink)
	source, err := manager.Submit(context.Background(), msg, dir, agent.Request{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err := manager.Get(context.Background(), msg.Session, source.ID)
		return err == nil && got.DeliveryAttempts >= 1 && got.Delivery == "pending" && strings.Contains(got.DeliveryError, "offline")
	}, 10*time.Second, 25*time.Millisecond)
}

// noticeFlakySink 让降级提示的第一次发送失败，用来验证重试不会重复发送文字结果或提示。
type noticeFlakySink struct {
	replyStateSink
	noticeAttempts int
	noticeOK       int
	resultSends    int
	noticeTxns     []string
}

func (n *noticeFlakySink) Send(ctx context.Context, reply chat.Reply) (string, error) {
	if strings.Contains(reply.Text, "当前平台无法发送") {
		n.mu.Lock()
		n.noticeAttempts++
		n.noticeTxns = append(n.noticeTxns, reply.TransactionID)
		fail := n.noticeAttempts == 1
		if !fail {
			n.noticeOK++
		}
		n.mu.Unlock()
		if fail {
			return "", errors.New("offline")
		}
		return reply.TransactionID, nil
	}
	n.mu.Lock()
	n.resultSends++
	n.mu.Unlock()
	return n.replyStateSink.Send(ctx, reply)
}

// TestTaskDelivery_DegradeNoticeRetryIsIdempotent 降级提示发送失败后任务保持 pending，
// 重试只补发提示，不重复发送文字结果，也不重复发送已成功的提示。
func TestTaskDelivery_DegradeNoticeRetryIsIdempotent(t *testing.T) {
	sink := &noticeFlakySink{}
	manager, msg, dir := newArtifactTask(t, sink)
	source, err := manager.Submit(context.Background(), msg, dir, agent.Request{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err := manager.Get(context.Background(), msg.Session, source.ID)
		return err == nil && got.Delivery == "pending" && got.DeliveryAttempts == 1 && strings.Contains(got.DeliveryError, "offline")
	}, 10*time.Second, 25*time.Millisecond)
	var got task.Task
	require.Eventually(t, func() bool {
		got, err = manager.Get(context.Background(), msg.Session, source.ID)
		return err == nil && got.Delivery == "sent"
	}, 10*time.Second, 25*time.Millisecond)
	require.Equal(t, 2, got.DeliveryAttempts)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Equal(t, 2, sink.noticeAttempts)
	require.Equal(t, 1, sink.noticeOK, "提示只成功发送一次")
	require.Equal(t, 1, sink.resultSends, "文字结果已持久化为已发送，重试不应再发")
	require.Len(t, sink.noticeTxns, 2)
	require.Equal(t, sink.noticeTxns[0], sink.noticeTxns[1], "重试使用同一幂等事务 ID")
}

func TestService_DeliverTaskFile_NilAdapter(t *testing.T) {
	s := &Service{}
	_, err := s.deliverTaskFile(context.Background(), nil, task.Task{ID: 1}, "artifact:x", "x.txt", func() ([]byte, error) {
		t.Error("没有文件发送能力时不应读取文件")
		return nil, nil
	})
	require.EqualError(t, err, "当前平台不支持文件交付")
}

func TestService_TaskFileAdapter(t *testing.T) {
	registered := &fileSink{}
	s := &Service{}
	s.taskFiles.Store("violet", chat.FileAdapter(registered))
	require.Nil(t, s.taskFileAdapter("matrix", nil))
	require.Nil(t, s.taskFileAdapter("matrix", &replyStateSink{}), "投递 adapter 未实现文件能力且平台未登记")
	require.Equal(t, chat.FileAdapter(registered), s.taskFileAdapter("violet", nil))
	other := &fileSink{}
	require.Equal(t, chat.FileAdapter(other), s.taskFileAdapter("violet", other), "优先使用当前投递 adapter")
}
