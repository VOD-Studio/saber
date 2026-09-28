package ai

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/chat"
	chatmemory "rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/memory"
)

func skillCommandMessage(sender, conversation string, direct bool) chat.Message {
	return chat.Message{
		Session:  chat.Session{Platform: "memory", Account: "bot", Conversation: conversation},
		ID:       "event",
		SenderID: sender,
		Direct:   direct,
		Text:     "/skill",
	}
}

func TestSkillCommand_PersonalRoundTrip(t *testing.T) {
	mem := newTestMemory(t, memory.Config{})
	svc := newMemoryAIService(t, mem, 0)
	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)
	ctx := context.Background()
	msg := skillCommandMessage("@alice:test", "dm", true)

	// 1. 初始列表为空
	require.NoError(t, svc.SkillCommand(ctx, msg, adapter, "list", ""))
	require.Contains(t, lastReply(t, adapter), "暂无可用技能")

	// 2. 添加技能
	require.NoError(t, svc.SkillCommand(ctx, msg, adapter, "add", "k8s-debug Pod排错步骤\n1. kubectl get\n2. kubectl logs"))
	require.Contains(t, lastReply(t, adapter), "已保存生效")
	require.Contains(t, lastReply(t, adapter), "k8s-debug")

	// 3. 查阅列表
	require.NoError(t, svc.SkillCommand(ctx, msg, adapter, "list", ""))
	require.Contains(t, lastReply(t, adapter), "k8s-debug (v1)")

	// 4. show 详细内容
	require.NoError(t, svc.SkillCommand(ctx, msg, adapter, "show", "k8s-debug"))
	require.Contains(t, lastReply(t, adapter), "【技能：k8s-debug (v1)】")
	require.Contains(t, lastReply(t, adapter), "kubectl logs")

	// 5. edit 修改正文
	require.NoError(t, svc.SkillCommand(ctx, msg, adapter, "edit", "k8s-debug 3. kubectl describe"))
	require.Contains(t, lastReply(t, adapter), "v2")

	// 6. delete 删除技能
	require.NoError(t, svc.SkillCommand(ctx, msg, adapter, "delete", "k8s-debug"))
	require.Contains(t, lastReply(t, adapter), "已成功删除")

	// 7. 再次 show 应报错
	require.NoError(t, svc.SkillCommand(ctx, msg, adapter, "show", "k8s-debug"))
	require.Contains(t, lastReply(t, adapter), "查看技能失败")
}

func TestSkillCommand_GroupSuggestionFlow(t *testing.T) {
	mem := newTestMemory(t, memory.Config{
		GroupWriter: func(identity chat.Identity) bool {
			return identity.SenderID == "@admin:test"
		},
	})
	svc := newMemoryAIService(t, mem, 0)
	adapter := chatmemory.New("bot", chat.Capabilities{}, nil)
	ctx := context.Background()

	member := skillCommandMessage("@member:test", "!group:test", false)
	admin := skillCommandMessage("@admin:test", "!group:test", false)

	// 1. 普通成员新增群技能，形成待确认建议
	require.NoError(t, svc.SkillCommand(ctx, member, adapter, "add", "deploy-flow 发布指南 make deploy"))
	require.Contains(t, lastReply(t, adapter), "待确认")

	// 2. 查看待确认建议
	require.NoError(t, svc.SkillCommand(ctx, member, adapter, "pending", ""))
	require.Contains(t, lastReply(t, adapter), "deploy-flow")

	// 3. 普通成员尝试审批 -> 失败
	require.NoError(t, svc.SkillCommand(ctx, member, adapter, "approve", "1"))
	require.Contains(t, lastReply(t, adapter), "确认失败")

	// 4. 管理员审批通过
	require.NoError(t, svc.SkillCommand(ctx, admin, adapter, "approve", "1"))
	require.Contains(t, lastReply(t, adapter), "已生效")

	// 5. 列表中已可见
	require.NoError(t, svc.SkillCommand(ctx, admin, adapter, "list", ""))
	require.Contains(t, lastReply(t, adapter), "deploy-flow (v1)")

	// 6. 普通成员再次提议新增，管理员予以拒绝
	require.NoError(t, svc.SkillCommand(ctx, member, adapter, "add", "bad-skill 垃圾流程 bad"))
	require.NoError(t, svc.SkillCommand(ctx, admin, adapter, "reject", "2"))
	require.Contains(t, lastReply(t, adapter), "已拒绝技能建议 #2")
}
