package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

func message(event, sender string) chat.Message {
	return chat.Message{ID: event, SenderID: sender, Text: sender + " input", Session: chat.Session{Platform: "matrix", Account: "bot", Conversation: "room", Thread: "topic"}}
}

func request(text string) agent.Request {
	return agent.Request{Model: "test", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: text}}}
}

func waitTask(t *testing.T, m *Manager, source Task, predicate func(Task) bool) Task {
	t.Helper()
	var got Task
	require.Eventually(t, func() bool {
		var err error
		got, err = m.Get(context.Background(), source.Message.Session, source.ID)
		return err == nil && predicate(got)
	}, 5*time.Second, 10*time.Millisecond)
	return got
}

func closeManager(t *testing.T, m *Manager) { t.Helper(); require.NoError(t, m.Close()) }

func TestManager_IsolationDedupAndDeliveryRetry(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var runs atomic.Int32
	var sends atomic.Int32
	var mu sync.Mutex
	seen := map[string]string{}
	m, err := Open(filepath.Join(dir, "tasks.db"), func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		runs.Add(1)
		identity, ok := chat.IdentityFromContext(ctx)
		if !ok {
			return agent.Result{}, errors.New("missing identity")
		}
		mu.Lock()
		seen[identity.SenderID] = req.Messages[0].Content
		mu.Unlock()
		emit(agent.Event{Kind: agent.ToolStarted, Tool: agent.ToolRecord{Call: openai.ToolCall{ID: "effect"}}})
		emit(agent.Event{Kind: agent.ToolFinished})
		return agent.Result{Status: agent.Completed, Content: identity.SenderID + " result"}, nil
	}, func(ctx context.Context, t Task) (string, error) {
		if sends.Add(1) == 1 {
			return "", errors.New("offline")
		}
		if t.Message.ID == "" || t.Message.Session.Thread != "topic" {
			return "", errors.New("lost source")
		}
		return fmt.Sprintf("reply-%d", t.ID), nil
	})
	require.NoError(t, err)
	defer closeManager(t, m)
	var wg sync.WaitGroup
	ids := make(chan int64, 10)
	for range 10 {
		wg.Go(func() {
			task, submitErr := m.Submit(ctx, message("event-a", "alice"), dir, request("alice only"))
			if submitErr != nil {
				t.Error(submitErr)
				return
			}
			ids <- task.ID
		})
	}
	wg.Wait()
	close(ids)
	var firstID int64
	for id := range ids {
		if firstID == 0 {
			firstID = id
		}
		require.Equal(t, firstID, id)
	}
	b, err := m.Submit(ctx, message("event-b", "bob"), dir, request("bob only"))
	require.NoError(t, err)
	a, err := m.Get(ctx, b.Message.Session, firstID)
	require.NoError(t, err)
	a = waitTask(t, m, a, func(t Task) bool { return t.Delivery == "sent" })
	waitTask(t, m, b, func(t Task) bool { return t.Delivery == "sent" })
	require.EqualValues(t, 2, runs.Load())
	require.EqualValues(t, 3, sends.Load())
	mu.Lock()
	require.Equal(t, map[string]string{"alice": "alice only", "bob": "bob only"}, seen)
	mu.Unlock()
	records, err := m.Events(ctx, a.Message.Session, a.ID)
	require.NoError(t, err)
	require.Len(t, records, 2)
	list, err := m.List(ctx, a.Message.Session)
	require.NoError(t, err)
	require.Len(t, list, 2)
	other := a.Message.Session
	other.Conversation = "elsewhere"
	_, err = m.Get(ctx, other, a.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = m.Events(ctx, other, a.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = m.Cancel(ctx, chat.Identity{Session: a.Message.Session, SenderID: "bob"}, a.ID)
	require.Error(t, err)
	_, err = m.Submit(ctx, message("", "alice"), dir, request("bad"))
	require.Error(t, err)
}

func TestManager_DirectorySerializationAndCancellation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	other := t.TempDir()
	started := make(chan string, 4)
	release := make(chan struct{})
	m, err := Open(filepath.Join(dir, "tasks.db"), func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		started <- WorkDir(ctx)
		select {
		case <-ctx.Done():
			<-release
			return agent.Result{Status: agent.Cancelled}, ctx.Err()
		case <-release:
			return agent.Result{Status: agent.Completed}, nil
		}
	}, func(context.Context, Task) (string, error) { return "reply", nil })
	require.NoError(t, err)
	defer closeManager(t, m)
	defer close(release)
	a, err := m.Submit(ctx, message("a", "alice"), dir, request("a"))
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first task did not start")
	}
	b, err := m.Submit(ctx, message("b", "bob"), filepath.Join(dir, "."), request("b"))
	require.NoError(t, err)
	c, err := m.Submit(ctx, message("c", "carol"), other, request("c"))
	require.NoError(t, err)
	select {
	case got := <-started:
		canonical, dirErr := canonicalDir(other)
		require.NoError(t, dirErr)
		require.Equal(t, canonical, got)
	case <-time.After(time.Second):
		t.Fatal("different directory blocked")
	}
	_, err = m.Cancel(ctx, chat.Identity{Session: a.Message.Session, SenderID: "alice"}, a.ID)
	require.NoError(t, err)
	// 已请求取消但工具尚未退出，后续写任务必须仍然排队。
	current, err := m.Get(ctx, b.Message.Session, b.ID)
	require.NoError(t, err)
	require.Equal(t, "queued", current.Status)
	current, err = m.Cancel(ctx, chat.Identity{Session: b.Message.Session, SenderID: "bob"}, b.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", current.Status)
	current, err = m.Get(ctx, c.Message.Session, c.ID)
	require.NoError(t, err)
	require.Equal(t, "running", current.Status)
}

func TestManager_RestartRecoversQueueWithoutReplayingExecution(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.db")
	s, err := openStore(path)
	require.NoError(t, err)
	interrupted, err := s.submit(ctx, message("running", "alice"), dir, request("must not replay"))
	require.NoError(t, err)
	_, err = s.claim(ctx)
	require.NoError(t, err)
	require.NoError(t, s.record(ctx, interrupted.ID, agent.Event{Kind: agent.ToolStarted, Text: "external effect"}))
	queued, err := s.submit(ctx, message("queued", "bob"), dir, request("resume queued"))
	require.NoError(t, err)
	completed, err := s.submit(ctx, message("complete", "carol"), t.TempDir(), request("must not replay success"))
	require.NoError(t, err)
	_, err = s.claim(ctx)
	require.NoError(t, err)
	require.NoError(t, s.finish(ctx, completed, agent.Result{Status: agent.Completed, Content: "saved result"}, nil, false))
	require.NoError(t, s.db.Close())
	var runs atomic.Int32
	m, err := Open(path, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		runs.Add(1)
		if req.Messages[0].Content != "resume queued" {
			return agent.Result{}, errors.New("replayed completed or interrupted task")
		}
		return agent.Result{Status: agent.Completed, Content: "done"}, nil
	}, func(context.Context, Task) (string, error) { return "delivered", nil })
	require.NoError(t, err)
	defer closeManager(t, m)
	got := waitTask(t, m, interrupted, func(t Task) bool { return t.Delivery == "sent" })
	require.Equal(t, "interrupted", got.Status)
	got = waitTask(t, m, completed, func(t Task) bool { return t.Delivery == "sent" })
	require.Equal(t, "saved result", got.Result.Content)
	got = waitTask(t, m, queued, func(t Task) bool { return t.Delivery == "sent" })
	require.Equal(t, "completed", got.Status)
	require.EqualValues(t, 1, runs.Load())
	records, err := m.Events(ctx, interrupted.Message.Session, interrupted.ID)
	require.NoError(t, err)
	require.Contains(t, records[0], "external effect")
	duplicate, err := m.Submit(ctx, message("running", "alice"), dir, request("different"))
	require.NoError(t, err)
	require.Equal(t, interrupted.ID, duplicate.ID)
}

func TestManager_CloseLeavesQueueAndInterruptsRunning(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.db")
	started := make(chan struct{})
	m, err := Open(path, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		close(started)
		<-ctx.Done()
		return agent.Result{Status: agent.Cancelled}, ctx.Err()
	}, func(context.Context, Task) (string, error) { return "", nil })
	require.NoError(t, err)
	a, err := m.Submit(ctx, message("a", "alice"), dir, request("a"))
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	b, err := m.Submit(ctx, message("b", "bob"), dir, request("b"))
	require.NoError(t, err)
	require.NoError(t, m.Close())
	require.NoError(t, m.Close())
	_, err = m.Submit(ctx, message("c", "carol"), dir, request("c"))
	require.Error(t, err)
	s, err := openStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.db.Close()) }()
	a, err = s.get(ctx, a.Message.Session, a.ID)
	require.NoError(t, err)
	require.Equal(t, "interrupted", a.Status)
	b, err = s.get(ctx, b.Message.Session, b.ID)
	require.NoError(t, err)
	require.Equal(t, "queued", b.Status)
}

func TestManager_JournalFailureStopsBeforeTool(t *testing.T) {
	dir := t.TempDir()
	var effects atomic.Int32
	runtime := agent.Runtime{Model: func(context.Context, agent.Request, func(agent.Event)) (agent.Response, error) {
		return agent.Response{FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{{ID: "1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "write", Arguments: "{}"}}}}, nil
	}, Execute: func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
		effects.Add(1)
		return agent.ToolOutput{}, nil
	}}
	m, err := Open(filepath.Join(dir, "tasks.db"), runtime.Run, func(context.Context, Task) (string, error) { return "id", nil })
	require.NoError(t, err)
	defer closeManager(t, m)
	_, err = m.store.db.Exec(`CREATE TRIGGER fail_tool_record BEFORE INSERT ON task_events WHEN CAST(NEW.record AS TEXT) LIKE '%tool_started%' BEGIN SELECT RAISE(FAIL,'disk failure'); END`)
	require.NoError(t, err)
	req := request("write")
	req.Tools = []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "write"}}}
	a, err := m.Submit(context.Background(), message("a", "alice"), dir, req)
	require.NoError(t, err)
	a = waitTask(t, m, a, func(t Task) bool { return t.Status != "queued" && t.Status != "running" })
	require.Zero(t, effects.Load())
	require.True(t, strings.Contains(a.Error, "disk failure"))
}
