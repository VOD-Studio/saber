package ai

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/chat"
	chatmemory "rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/memory"
)

func memoryCommandMessage(sender, conversation string, direct bool) chat.Message {
	return chat.Message{
		Session:  chat.Session{Platform: "memory", Account: "bot", Conversation: conversation},
		ID:       "event",
		SenderID: sender,
		Direct:   direct,
		Text:     "/memory",
	}
}

func lastReply(t *testing.T, adapter *chatmemory.Adapter) string {
	t.Helper()
	replies := adapter.Replies()
	require.NotEmpty(t, replies)
	return replies[len(replies)-1].Text
}

func TestMemoryCommand_PersonalRoundTrip(t *testing.T) {
	mem := newTestMemory(t, memory.Config{})
	svc := newMemoryAIService(t, mem, 0)
	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)
	ctx := context.Background()
	msg := memoryCommandMessage("@alice:test", "dm", true)

	require.NoError(t, svc.MemoryCommand(ctx, msg, adapter, "add", "以后用中文简洁回答"))
	require.Contains(t, lastReply(t, adapter), "已保存")

	require.NoError(t, svc.MemoryCommand(ctx, msg, adapter, "list", ""))
	require.Contains(t, lastReply(t, adapter), "以后用中文简洁回答")

	require.NoError(t, svc.MemoryCommand(ctx, msg, adapter, "edit", "1 改用中文回复"))
	require.Contains(t, lastReply(t, adapter), "已保存")

	require.NoError(t, svc.MemoryCommand(ctx, msg, adapter, "status", ""))
	require.Contains(t, lastReply(t, adapter), "个人空间")

	require.NoError(t, svc.MemoryCommand(ctx, msg, adapter, "forget", "1"))
	require.Contains(t, lastReply(t, adapter), "已保存")

	require.NoError(t, svc.MemoryCommand(ctx, msg, adapter, "list", ""))
	require.Contains(t, lastReply(t, adapter), "暂无记忆条目")
}

func TestMemoryCommand_GroupSuggestionFlow(t *testing.T) {
	mem := newTestMemory(t, memory.Config{GroupWriter: func(identity chat.Identity) bool { return identity.SenderID == "@admin:test" }})
	svc := newMemoryAIService(t, mem, 0)
	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)
	ctx := context.Background()
	member := memoryCommandMessage("@member:test", "!room", false)
	admin := memoryCommandMessage("@admin:test", "!room", false)

	require.NoError(t, svc.MemoryCommand(ctx, member, adapter, "add", "本群周五发布"))
	require.Contains(t, lastReply(t, adapter), "待确认")

	require.NoError(t, svc.MemoryCommand(ctx, member, adapter, "pending", ""))
	require.Contains(t, lastReply(t, adapter), "本群周五发布")

	// 普通成员没有写权限，不能确认建议。
	require.NoError(t, svc.MemoryCommand(ctx, member, adapter, "approve", "1"))
	require.Contains(t, lastReply(t, adapter), "只有本群记忆管理员")

	require.NoError(t, svc.MemoryCommand(ctx, admin, adapter, "approve", "1"))
	require.Contains(t, lastReply(t, adapter), "已确认建议")

	require.NoError(t, svc.MemoryCommand(ctx, admin, adapter, "list", ""))
	require.Contains(t, lastReply(t, adapter), "本群周五发布")

	// 拒绝路径。
	require.NoError(t, svc.MemoryCommand(ctx, member, adapter, "add", "临时想法"))
	require.NoError(t, svc.MemoryCommand(ctx, admin, adapter, "reject", "2"))
	require.Contains(t, lastReply(t, adapter), "已拒绝建议")
}

func TestMemoryCommand_Disabled(t *testing.T) {
	svc := newMemoryAIService(t, nil, 0)
	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)
	require.NoError(t, svc.MemoryCommand(context.Background(), memoryCommandMessage("@a:test", "dm", true), adapter, "list", ""))
	require.Contains(t, lastReply(t, adapter), "长期记忆未启用")
}

func TestMemoryCommand_UsageErrors(t *testing.T) {
	mem := newTestMemory(t, memory.Config{})
	svc := newMemoryAIService(t, mem, 0)
	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)
	msg := memoryCommandMessage("@a:test", "dm", true)
	for _, tc := range []struct{ action, raw string }{
		{"add", "   "},
		{"edit", "1"},
		{"forget", "x"},
		{"list", "extra"},
		{"unknown", ""},
	} {
		t.Run(tc.action, func(t *testing.T) {
			require.NoError(t, svc.MemoryCommand(context.Background(), msg, adapter, tc.action, tc.raw))
			require.Contains(t, lastReply(t, adapter), "用法")
		})
	}
}

// 确保 memory 工具的 list 文案与命令的 list 文案都不泄露其他作用域。
func TestMemoryCommand_DoesNotLeakOtherScope(t *testing.T) {
	mem := newTestMemory(t, memory.Config{GroupWriter: func(chat.Identity) bool { return true }})
	svc := newMemoryAIService(t, mem, 0)
	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)
	ctx := context.Background()
	require.NoError(t, svc.MemoryCommand(ctx, memoryCommandMessage("@alice:test", "dm", true), adapter, "add", "私聊秘密"))
	require.NoError(t, svc.MemoryCommand(ctx, memoryCommandMessage("@alice:test", "!room", false), adapter, "list", ""))
	require.False(t, strings.Contains(lastReply(t, adapter), "私聊秘密"))
}

func TestMemoryCommand_PauseResumeAndStatus(t *testing.T) {
	mem := newTestMemory(t, memory.Config{GroupWriter: func(i chat.Identity) bool { return i.SenderID == "@admin:test" }})
	svc := newMemoryAIService(t, mem, 0)
	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)
	ctx := context.Background()

	// 1. 个人空间 pause 与 resume
	userMsg := memoryCommandMessage("@alice:test", "dm", true)
	require.NoError(t, svc.MemoryCommand(ctx, userMsg, adapter, "status", ""))
	require.Contains(t, lastReply(t, adapter), "自动学习：开启")

	require.NoError(t, svc.MemoryCommand(ctx, userMsg, adapter, "pause", ""))
	require.Contains(t, lastReply(t, adapter), "已暂停当前空间的自动记忆学习")

	require.NoError(t, svc.MemoryCommand(ctx, userMsg, adapter, "status", ""))
	require.Contains(t, lastReply(t, adapter), "自动学习：已暂停")

	require.NoError(t, svc.MemoryCommand(ctx, userMsg, adapter, "resume", ""))
	require.Contains(t, lastReply(t, adapter), "已恢复当前空间的自动记忆学习")

	// 2. 群空间权限测试
	memberMsg := memoryCommandMessage("@member:test", "!room", false)
	adminMsg := memoryCommandMessage("@admin:test", "!room", false)

	require.NoError(t, svc.MemoryCommand(ctx, memberMsg, adapter, "pause", ""))
	require.Contains(t, lastReply(t, adapter), "只有本群记忆管理员")

	require.NoError(t, svc.MemoryCommand(ctx, adminMsg, adapter, "pause", ""))
	require.Contains(t, lastReply(t, adapter), "已暂停当前空间的自动记忆学习")

	require.NoError(t, svc.MemoryCommand(ctx, adminMsg, adapter, "resume", ""))
	require.Contains(t, lastReply(t, adapter), "已恢复当前空间的自动记忆学习")
}
