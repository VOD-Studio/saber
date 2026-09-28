package ai

import (
	"context"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/memory"
)

func TestSkillTool_PrepareAndExecute(t *testing.T) {
	mem := newTestMemory(t, memory.Config{})
	svc := newMemoryAIService(t, mem, 0)

	alice := personalIdentity("@alice:example.com")
	ctx := chat.WithIdentity(context.Background(), alice)

	// 1. 验证 PrepareTools 包含 saber_skill
	tools, ok := svc.toolExecutor.PrepareTools(ctx)
	require.True(t, ok)
	var foundSkill bool
	for _, tool := range tools {
		if tool.Function.Name == "saber_skill" {
			foundSkill = true
			require.Equal(t, "saber_skill", tool.Function.Name)
			require.Contains(t, tool.Function.Description, "程序性经验")
		}
	}
	require.True(t, foundSkill, "应在工具列表中注册 saber_skill")

	// 2. catalog：初始无技能
	res, err := svc.executeSkillTool(ctx, map[string]any{"action": "catalog"})
	require.NoError(t, err)
	require.Contains(t, res.(string), "暂无可用技能")

	// 3. add：添加新技能
	addRes, err := svc.executeSkillTool(ctx, map[string]any{
		"action":      "add",
		"name":        "deploy-k8s",
		"description": "K8s 服务滚动部署与健康检查流程",
		"content":     "## 步骤\n1. kubectl apply\n2. kubectl rollout status",
	})
	require.NoError(t, err)
	require.Contains(t, addRes.(string), "已保存生效")
	require.Contains(t, addRes.(string), "deploy-k8s")
	require.Contains(t, addRes.(string), "v1")

	// 4. read：读取该技能详情
	readRes, err := svc.executeSkillTool(ctx, map[string]any{
		"action": "read",
		"name":   "deploy-k8s",
	})
	require.NoError(t, err)
	readText := readRes.(string)
	require.Contains(t, readText, "【技能：deploy-k8s (v1)】")
	require.Contains(t, readText, "适用场景：K8s 服务滚动部署与健康检查流程")
	require.Contains(t, readText, "kubectl rollout status")

	// 5. catalog：再次列出应包含新技能
	catRes, err := svc.executeSkillTool(ctx, map[string]any{"action": "catalog"})
	require.NoError(t, err)
	require.Contains(t, catRes.(string), "deploy-k8s (v1)")

	// 6. replace：更新技能正文 (CAS 预期版本 1)
	repRes, err := svc.executeSkillTool(ctx, map[string]any{
		"action":           "replace",
		"name":             "deploy-k8s",
		"expected_version": float64(1),
		"description":      "K8s 服务滚动部署与回滚流程",
		"content":          "## 步骤\n1. kubectl apply\n2. 检查失败则 kubectl rollout undo",
	})
	require.NoError(t, err)
	require.Contains(t, repRes.(string), "v2")

	// 7. replace 冲突：再次以版本 1 尝试更新
	_, err = svc.executeSkillTool(ctx, map[string]any{
		"action":           "replace",
		"name":             "deploy-k8s",
		"expected_version": float64(1),
		"description":      "旧版本尝试覆盖",
		"content":          "fail",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, memory.ErrSkillConflict)

	// 8. remove：删除技能 (预期版本 2)
	remRes, err := svc.executeSkillTool(ctx, map[string]any{
		"action":           "remove",
		"name":             "deploy-k8s",
		"expected_version": float64(2),
	})
	require.NoError(t, err)
	require.Contains(t, remRes.(string), "成功删除")

	// 9. 确认已删除
	_, err = svc.executeSkillTool(ctx, map[string]any{
		"action": "read",
		"name":   "deploy-k8s",
	})
	require.Error(t, err)
	require.ErrorIs(t, err, memory.ErrSkillNotFound)
}

func TestAugmentMemory_InjectsSkillCatalog(t *testing.T) {
	ctx := chat.WithIdentity(context.Background(), personalIdentity("@alice:x"))
	mem := newTestMemory(t, memory.Config{})

	// 添加一条技能
	_, err := mem.AddSkill(ctx, personalIdentity("@alice:x"), "rebase-flow", "Git 变基与排错流程", "详细 Markdown 步骤", memory.Source{})
	require.NoError(t, err)

	svc := newMemoryAIService(t, mem, 0)
	req := agent.Request{Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: "base prompt"}}}

	// 调用 augmentMemory
	got := svc.augmentMemory(ctx, req)

	// 原请求不应被修改
	require.Len(t, req.Messages, 1)

	// 增强后的请求应注入技能目录
	require.Len(t, got.Messages, 2)
	snapContent := got.Messages[1].Content
	require.Contains(t, snapContent, "【可用技能目录")
	require.Contains(t, snapContent, "- rebase-flow: Git 变基与排错流程 (v1)")
	require.Contains(t, snapContent, "【技能目录结束】")
	// 完整正文不应进入运行时目录快照（渐进加载）
	require.NotContains(t, snapContent, "详细 Markdown 步骤")
}
