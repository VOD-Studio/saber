package ai

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/execution"
)

// toolDef 构造测试用工具定义。
func toolDef(name string) openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: name, Parameters: map[string]any{"type": "object"}}}
}

// toolNames 提取工具名。
func toolNames(tools []openai.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Function != nil {
			names = append(names, tool.Function.Name)
		}
	}
	return names
}

// testSubagentService 构造只用于工具装配的服务；不发起真实模型请求。
func testSubagentService(t *testing.T, mutate func(*config.Config)) *Service {
	t.Helper()
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "test-key"}}
	cfg.AI.DefaultModel = "openai.gpt-4"
	if mutate != nil {
		mutate(&cfg)
	}
	service, err := NewService(&cfg)
	require.NoError(t, err)
	return service
}

// authorizedWorkspace 给服务装上只含 read_file 权限的执行器，返回带身份与任务作用域的 ctx。
func authorizedWorkspace(t *testing.T, service *Service) context.Context {
	t.Helper()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	executor, err := execution.New(config.ExecutionConfig{
		Enabled:    true,
		Backend:    "local",
		LogDir:     t.TempDir(),
		Workspaces: map[string]config.WorkspaceConfig{"w": {Path: workspace}},
		Grants: []config.ExecutionGrant{{
			Platform: "matrix", Account: "bot", Room: "!room", Users: []string{"@alice"},
			Workspace: "w", Tools: []string{"read_file"},
		}},
	}, nil, nil)
	require.NoError(t, err)
	service.executor = executor
	ctx := chat.WithIdentity(context.Background(), chat.Identity{
		Session:  chat.Session{Platform: "matrix", Account: "bot", Conversation: "!room"},
		SenderID: "@alice",
	})
	return execution.WithTask(ctx, 1, workspace)
}

// TestPrepareTools_SubagentExposure 验证 delegate 只在顶层、启用且已有可用工具时暴露。
func TestPrepareTools_SubagentExposure(t *testing.T) {
	service := testSubagentService(t, nil)
	ctx := authorizedWorkspace(t, service)
	executor := NewToolExecutor(service)

	tools, ok := executor.PrepareTools(ctx)
	require.True(t, ok)
	require.Contains(t, toolNames(tools), agent.DelegateTool)
	require.Contains(t, toolNames(tools), "read_file")

	tools, ok = executor.PrepareTools(agent.WithSubagentDepth(ctx, 1))
	require.True(t, ok)
	require.NotContains(t, toolNames(tools), agent.DelegateTool)

	// 没有任何已授权工具时不暴露：单独 delegate 不带来新能力。
	empty, ok := executor.PrepareTools(context.Background())
	require.False(t, ok)
	require.Empty(t, empty)

	disabled := false
	service = testSubagentService(t, func(cfg *config.Config) { cfg.Agent.Subagents.Enabled = &disabled })
	disabledCtx := authorizedWorkspace(t, service)
	tools, ok = NewToolExecutor(service).PrepareTools(disabledCtx)
	require.True(t, ok)
	require.NotContains(t, toolNames(tools), agent.DelegateTool)

}

// TestSubagentDelegatorPolicy 验证配置到子运行策略的映射与默认值。
func TestSubagentDelegatorPolicy(t *testing.T) {
	service := testSubagentService(t, func(cfg *config.Config) {
		cfg.Agent.Subagents.MaxRounds = 0
		cfg.Agent.Subagents.MaxParallel = 0
	})
	req := agent.Request{Model: "openai.gpt-4"}
	limits := agent.Limits{Timeout: time.Second, MaxToolOutputBytes: 1024}
	delegator := service.subagentDelegator(context.Background(), req, limits, nil, nil)
	require.NotNil(t, delegator)
	require.Equal(t, agent.DefaultSubagentMaxRounds, delegator.Policy.MaxRounds)
	require.Equal(t, agent.DefaultSubagentMaxParallel, delegator.Policy.MaxParallel)
	require.Zero(t, delegator.Policy.MaxTotalTokens)
	require.Equal(t, req.Model, delegator.Parent.Model)
	require.Equal(t, limits.Timeout, delegator.Limits.Timeout)

	// 子代理内部不再组装委托器，保证深度 1。
	require.Nil(t, service.subagentDelegator(agent.WithSubagentDepth(context.Background(), 1), req, limits, nil, nil))

	disabled := false
	service = testSubagentService(t, func(cfg *config.Config) { cfg.Agent.Subagents.Enabled = &disabled })
	require.Nil(t, service.subagentDelegator(context.Background(), req, limits, nil, nil))
}

// TestSubagentToolsRecheckAuthorization 验证子代理工具集去掉 delegate/状态工具并重新授权。
func TestSubagentToolsRecheckAuthorization(t *testing.T) {
	service := testSubagentService(t, nil)
	parent := []openai.Tool{
		toolDef(agent.DelegateTool), toolDef("saber_task_state"), toolDef("exec"), toolDef("read_file"),
	}
	ctx := authorizedWorkspace(t, service)
	filtered := service.subagentTools(ctx, parent)
	require.Equal(t, []string{"read_file"}, toolNames(filtered))

	// 显式子集由委托器再校验，delegate 永远不可请求。
	req := agent.Request{Model: "openai.gpt-4", Tools: parent}
	delegator := service.subagentDelegator(context.Background(), req, agent.Limits{}, nil, nil)
	require.NotNil(t, delegator)
	_, err := delegator.Delegate(context.Background(), map[string]any{"goal": "递归", "tools": []any{agent.DelegateTool}})
	require.Error(t, err)
}
