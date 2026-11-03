package ai

import (
	"context"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/model"
	"rua.plus/saber/internal/task"
)

// RunAgent 不连接聊天平台地运行一次 Agent，返回完整轨迹供调用方保存。
// 调用真实 MCP 工具时，ctx 须通过 chat.WithIdentity 携带接入端身份。
func (s *Service) RunAgent(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
	return s.runAgent(ctx, req, emit, nil)
}

func (s *Service) runAgent(ctx context.Context, req agent.Request, emit func(agent.Event), firstClient *Client) (agent.Result, error) {
	retry := s.agentRetry()
	budget := s.config.Agent.Budget
	limits := agent.Limits{
		MaxRounds:          s.config.Agent.MaxIterations,
		Timeout:            time.Duration(s.config.Agent.TimeoutSeconds) * time.Second,
		MaxToolOutputBytes: s.config.Agent.MaxToolOutputBytes,
		MaxTotalTokens:     budget.MaxTotalTokens,
	}
	if budget.Enabled {
		limits.InitialRounds = budget.InitialRounds
		limits.ExtensionRounds = budget.ExtensionRounds
	}
	getClient := func(model string) (*Client, error) {
		if firstClient != nil && model == req.Model {
			return firstClient, nil
		}
		return s.getClient(model)
	}
	modelFunc := model.AgentModel(getClient, retry)
	execute := func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
		value, err := s.toolExecutor.ExecuteToolCall(ctx, name, args)
		output := agent.ToolOutput{Value: value}
		if result, ok := value.(*mcpsdk.CallToolResult); ok && result != nil {
			output.IsError = result.IsError
		}
		if result, ok := value.(execution.Result); ok {
			output.IsError = result.ExitCode != 0 || result.Error != ""
			output.OutputID = result.OutputID
			output.Structured = true
		}
		return output, err
	}
	runtime := agent.Runtime{
		Context:   s.contextPolicy(),
		Summarize: s.summarizer(req.Model),
		Limits:    limits,
		Model:     modelFunc,
		Execute:   execute,
	}
	// 子代理只挂在顶层运行：子运行的 Execute 是父 execute 的租约包装，
	// 工具集也已去掉 delegate，因此不可能递归委派。
	if delegator := s.subagentDelegator(ctx, req, limits, modelFunc, execute); delegator != nil {
		runtime.ParallelTool = func(name string) int {
			if name == agent.DelegateTool {
				return delegator.Policy.MaxParallel
			}
			return 1
		}
		runtime.Execute = func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
			if name == agent.DelegateTool {
				return delegator.Delegate(ctx, args)
			}
			return execute(ctx, name, args)
		}
	}
	if budget.Enabled {
		runtime.Evaluate = s.budgetEvaluator(req.Model)
	}
	if s.executor != nil {
		runtime.RunningProcesses = func() int { return s.executor.ProcessCount(task.ID(ctx)) }
	}
	return runtime.Run(ctx, req, emit)
}

func (s *Service) contextPolicy() agent.ContextPolicy {
	c := s.config.Agent.Context
	return agent.ContextPolicy{
		Enabled:        c.Enabled,
		MaxMessages:    c.MaxMessages,
		MaxInputTokens: c.MaxTokens,
		Compaction: agent.CompactionPolicy{
			Enabled:      c.Compaction.Enabled,
			TriggerRatio: c.Compaction.TriggerRatio,
			TargetRatio:  c.Compaction.TargetRatio,
		},
	}
}
