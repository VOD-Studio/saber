package task

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

func TestAuthorizeOutput_ScopesAndAncestry(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m, err := Open(filepath.Join(dir, "tasks.db"), func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{Status: agent.Completed, Content: "ok"}, nil
	}, nil, Options{Context: agent.ContextPolicy{Enabled: true, MaxMessages: 50, MaxInputTokens: 32768}})
	require.NoError(t, err)
	defer closeManager(t, m)
	root, err := m.Submit(ctx, message("root", "alice"), dir, request("root"))
	require.NoError(t, err)
	root = waitTask(t, m, root, func(t Task) bool { return t.Status == "completed" })
	child, err := m.store.submit(ctx, message("child", "alice"), dir, request("child"), root.ID)
	require.NoError(t, err)
	unrelated, err := m.store.submit(ctx, message("other", "alice"), dir, request("other"))
	require.NoError(t, err)

	alice := chat.Identity{Session: message("x", "alice").Session, SenderID: "alice"}
	bob := chat.Identity{Session: alice.Session, SenderID: "bob"}
	require.NoError(t, m.AuthorizeOutput(ctx, alice, child.ID, root.ID))
	require.NoError(t, m.AuthorizeOutput(ctx, alice, child.ID, child.ID))
	require.Error(t, m.AuthorizeOutput(ctx, bob, child.ID, root.ID), "同群其他成员不能猜句柄读发起人的日志")
	require.Error(t, m.AuthorizeOutput(ctx, alice, child.ID, unrelated.ID), "无关任务不构成祖先关系")
	otherRoom := alice
	otherRoom.Session.Conversation = "elsewhere"
	require.Error(t, m.AuthorizeOutput(ctx, otherRoom, child.ID, root.ID))
	require.Error(t, m.AuthorizeOutput(ctx, alice, 0, root.ID))
}
