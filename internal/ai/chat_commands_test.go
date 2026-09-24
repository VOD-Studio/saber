package ai

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/task"
)

func TestAICommand_ModelAndUsageReceipts(t *testing.T) {
	cfg := createTestMultiProviderAIConfig()
	svc, err := NewService(cfg)
	require.NoError(t, err)
	defer svc.Stop()
	adapter := memory.New("bot", chat.Capabilities{}, nil)
	message := chat.Message{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "room"}, ID: "event", SenderID: "user", Text: "/ai models"}
	for _, tc := range []struct {
		action, raw, contains string
	}{
		{"models", "", "可用模型"},
		{"current", "", "全局默认模型"},
		{"switch", "", "用法"},
		{"switch", "openai.gpt-4o", "全局默认模型已从"},
		{"current", "", "openai.gpt-4o"},
		{"models", "extra", "用法"},
	} {
		require.NoError(t, svc.AICommand(context.Background(), message, adapter, tc.action, tc.raw))
		replies := adapter.Replies()
		require.True(t, strings.Contains(replies[len(replies)-1].Text, tc.contains), "%s: %q", tc.action, replies[len(replies)-1].Text)
	}
}

func TestCommandField_PreservesScheduleGoal(t *testing.T) {
	when, rest := commandField(" 1h\tAsia/Shanghai  \"quoted goal\"\nnext")
	zone, goal := commandField(rest)
	require.Equal(t, "1h", when)
	require.Equal(t, "Asia/Shanghai", zone)
	require.Equal(t, " \"quoted goal\"\nnext", goal)
}

func TestChatCommand_ExplicitQuestionBypassesNaturalTaskControl(t *testing.T) {
	cfg := createTestMultiProviderAIConfig()
	svc, err := NewService(cfg)
	require.NoError(t, err)
	dir := t.TempDir()
	mgr, err := task.Open(filepath.Join(dir, "tasks.db"), func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{Status: agent.Completed}, nil
	}, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, mgr.Close()) }()
	svc.tasks, svc.taskDir = mgr, dir
	adapter := memory.New("bot", chat.Capabilities{}, nil)
	message := chat.Message{Session: chat.Session{Platform: "memory", Account: "bot", Conversation: "room"}, ID: "event", SenderID: "user", Text: "/ai 取消任务 123"}
	require.NoError(t, svc.ChatCommand(context.Background(), message, adapter, cfg.AI.DefaultModel, "取消任务 123"))
	tasks, err := mgr.List(context.Background(), message.Session)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "取消任务 123", tasks[0].Message.Text)
	require.NoError(t, svc.AICommand(context.Background(), message, adapter, "clear", ""))
	generation, err := mgr.ContextGeneration(context.Background(), message.Session)
	require.NoError(t, err)
	require.EqualValues(t, 1, generation)
	follow := message
	follow.ID, follow.ReplyTo = "follow", message.ID
	_, err = mgr.Continue(context.Background(), follow, dir, agent.Request{Model: cfg.AI.DefaultModel})
	require.ErrorIs(t, err, sql.ErrNoRows)
}
