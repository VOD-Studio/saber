package task

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

func TestManager_ContinuationWaitsAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path, dir := filepath.Join(t.TempDir(), "tasks.db"), t.TempDir()
	started := make(chan struct{})
	m, err := Open(path, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		close(started)
		<-ctx.Done()
		return agent.Result{Status: agent.Cancelled, Rounds: []agent.Round{{Response: agent.Response{ToolCalls: []openai.ToolCall{{ID: "call", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "exec", Arguments: `{"command":"touch done"}`}}}}, Tools: []agent.ToolRecord{{Call: openai.ToolCall{ID: "call"}, Content: "created done"}}}}}, ctx.Err()
	}, func(context.Context, Task) (string, error) { return "", nil })
	require.NoError(t, err)
	parent, err := m.Submit(ctx, message("original", "alice"), dir, request("create file; never publish"))
	require.NoError(t, err)
	<-started
	require.NoError(t, m.RememberMessage(ctx, parent.ID, "receipt"))
	follow := message("follow", "alice")
	follow.ReplyTo = "receipt"
	child, err := m.Continue(ctx, follow, dir, request("check it"))
	require.NoError(t, err)
	require.Equal(t, "queued", child.Status)
	closeManager(t, m)
	seen := make(chan agent.Request, 1)
	m, err = Open(path, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		seen <- req
		return agent.Result{Status: agent.Completed}, nil
	}, func(context.Context, Task) (string, error) { return "", nil })
	require.NoError(t, err)
	defer closeManager(t, m)
	select {
	case req := <-seen:
		require.Equal(t, "create file; never publish", req.Messages[0].Content)
		require.Equal(t, "call", req.Messages[1].ToolCalls[0].ID)
		require.Equal(t, "created done", req.Messages[2].Content)
		require.Equal(t, "check it", req.Messages[len(req.Messages)-1].Content)
	case <-time.After(3 * time.Second):
		t.Fatal("continuation did not resume")
	}
	stored := waitTask(t, m, child, func(t Task) bool { return t.Status == "completed" })
	require.Greater(t, len(stored.Request.Messages), 1)
	other := follow
	other.ID = "other"
	other.Session.Conversation = "other-room"
	_, err = m.Continue(ctx, other, dir, request("steal"))
	require.Error(t, err)
	wrong := follow
	wrong.ID = "wrong-dir"
	_, err = m.Continue(ctx, wrong, t.TempDir(), request("steal"))
	require.Error(t, err)
}

func TestManager_ContinuationThroughCancelledQueuedRound(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := openStore(filepath.Join(t.TempDir(), "tasks.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, s.db.Close()) }()
	m := &Manager{store: s, ctx: ctx}
	parent, err := m.Submit(ctx, message("source", "alice"), dir, request("original goal and constraints"))
	require.NoError(t, err)
	follow := message("follow", "alice")
	follow.ReplyTo = "source"
	child, err := m.Continue(ctx, follow, dir, request("additional constraint"))
	require.NoError(t, err)
	_, err = m.Cancel(ctx, chat.Identity{Session: follow.Session, SenderID: "alice"}, child.ID)
	require.NoError(t, err)
	latest := message("latest", "alice")
	latest.ReplyTo = "source"
	grandchild, err := m.Continue(ctx, latest, dir, request("continue"))
	require.NoError(t, err)
	req, err := m.continuationRequest(ctx, grandchild)
	require.NoError(t, err)
	require.Equal(t, "original goal and constraints", req.Messages[0].Content)
	require.Contains(t, fmt.Sprint(req.Messages), "additional constraint")
	stored, err := m.Get(ctx, latest.Session, grandchild.ID)
	require.NoError(t, err)
	again, err := m.continuationRequest(ctx, stored)
	require.NoError(t, err)
	require.Equal(t, req, again)
	duplicate, err := m.Continue(ctx, follow, dir, request("changed"))
	require.NoError(t, err)
	require.Equal(t, child.ID, duplicate.ID)
	var parentID int64
	require.NoError(t, s.db.QueryRow(`SELECT parent_id FROM task_links WHERE task_id=?`, child.ID).Scan(&parentID))
	require.Equal(t, parent.ID, parentID)
}
