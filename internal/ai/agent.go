package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/automation"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/model"
	"rua.plus/saber/internal/task"
)

// RunAgent 不连接聊天平台地运行一次 Agent，返回完整轨迹供调用方保存。
// 调用真实 MCP 工具时，ctx 须通过 chat.WithIdentity 携带接入端身份。
func (s *Service) RunAgent(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
	return s.runAgent(ctx, req, emit, nil)
}

func (s *Service) runAgent(ctx context.Context, req agent.Request, emit func(agent.Event), firstClient *Client) (result agent.Result, runErr error) {
	if req.SubagentDepth > 0 {
		ctx = agent.WithSubagentDepth(ctx, req.SubagentDepth)
	}
	if req.ToolScope != nil || req.SubagentDepth > 0 {
		ctx = agent.WithToolScope(ctx, req.ToolScope)
	}
	req.SubagentDepth = agent.SubagentDepth(ctx)
	if req.ParentAttemptGate != nil {
		ctx = agent.WithAttemptGate(ctx, req.ParentAttemptGate)
	}
	public := isPublicThread(ctx)
	if !public && s.automationService != nil {
		_, script := automation.ScriptRequest(req)
		_, command := automation.CommandRequest(req)
		delivery := automation.DeliveryRequest(req)
		if script || command || delivery {
			var err error
			req, err = s.automationService.BeforeTurn(ctx, req)
			if err != nil {
				return agent.Result{Status: agent.Failed}, err
			}
			if script {
				result, runErr = s.automationService.RunScript(ctx, req, emit)
			} else if command {
				result, runErr = s.automationService.RunCommand(ctx, req, emit)
			} else {
				result, runErr = s.automationService.RunDelivery(ctx, req, emit)
			}
			return result, errors.Join(runErr, s.finishRuntimeTurn(ctx, req, result, runErr))
		}
	}
	lease, _ := ctx.Value(runtimeLeaseKey{}).(*agent.WorkspaceLease)
	if lease == nil {
		lease = &agent.WorkspaceLease{}
		ctx = context.WithValue(ctx, runtimeLeaseKey{}, lease)
	}
	ctx = withAvatarImages(ctx, req.Messages)
	retry := s.agentRetry()
	budget := s.config.Agent.Budget
	limits := agent.Limits{MaxRounds: s.config.Agent.MaxIterations, Timeout: time.Duration(s.config.Agent.TimeoutSeconds) * time.Second, MaxToolOutputBytes: s.config.Agent.MaxToolOutputBytes, MaxTotalTokens: budget.MaxTotalTokens}
	if req.RunLimits != nil {
		limits = *req.RunLimits
	}
	if budget.Enabled && req.SubagentDepth == 0 {
		limits.InitialRounds, limits.ExtensionRounds = budget.InitialRounds, budget.ExtensionRounds
	}
	getClient := func(model string) (*Client, error) {
		if firstClient != nil && model == req.Model {
			return firstClient, nil
		}
		return s.getClient(model)
	}
	baseModel := model.AgentModel(getClient, retry)
	modelFunc := func(ctx context.Context, request agent.Request, emit func(agent.Event)) (agent.Response, error) {
		if !public && s.automationService != nil {
			var err error
			request, err = s.automationService.BeforeModel(ctx, request)
			if err != nil {
				return agent.Response{}, err
			}
		}
		response, err := baseModel(ctx, request, emit)
		if !public && s.automationService != nil {
			err = errors.Join(err, s.automationService.AfterModel(ctx, response))
		}
		return response, err
	}
	execute := func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
		key := subagentWorkspace(ctx)
		if dir, ok := ctx.Value(runtimeWorkspaceKey{}).(string); ok {
			key = dir
		}
		unlock := lease.Lock(key)
		defer unlock()
		value, err := s.toolExecutor.ExecuteToolCall(ctx, name, args)
		return runtimeToolOutput(value), err
	}
	runtime := agent.Runtime{Context: s.contextPolicy(), Summarize: s.runtimeSummarizer(req.Model), Limits: limits, Model: modelFunc, Execute: execute}
	if !public {
		runtime.AfterRun = s.finishRuntimeTurn
		runtime.Notifications = s.runtimeNotifications
		if s.automationService != nil {
			runtime.BeforeRun = func(ctx context.Context, request agent.Request) (agent.Request, error) {
				if automation.ProcessWaitRequest(request) {
					var err error
					request, err = s.automationService.AwaitProcess(ctx, request)
					if err != nil {
						return request, err
					}
				}
				return s.automationService.BeforeTurn(ctx, request)
			}
		}
		if s.executor != nil && s.executor.Check(ctx, "read_file") == nil {
			runtime.OnCompaction = s.executor.InvalidateTaskReads
		}
	}
	if delegator := s.subagentDelegator(ctx, req, limits, modelFunc, execute); delegator != nil {
		runtime.ParallelTool = func(name string) int {
			if name == agent.DelegateTool {
				return delegator.Policy.MaxParallel
			}
			return 1
		}
		runtime.Execute = func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
			if name != agent.DelegateTool {
				return execute(ctx, name, args)
			}
			output, err := delegator.Delegate(ctx, args)
			if err == nil && !public {
				data, marshalErr := json.Marshal(output.Value)
				if marshalErr != nil {
					return output, marshalErr
				}
				goal, _ := args["goal"].(string)
				err = s.ExternalMemoryDelegation(ctx, goal, string(data), "")
			}
			return output, err
		}
	}
	toolFunc := runtime.Execute
	runtime.Execute = func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
		if !agent.ToolAllowed(ctx, name) {
			return agent.ToolOutput{}, fmt.Errorf("子任务工具范围不允许 %q", name)
		}
		if !public && s.automationService != nil {
			var err error
			args, err = s.automationService.BeforeTool(ctx, name, args)
			if err != nil {
				return agent.ToolOutput{}, err
			}
		}
		output, err := toolFunc(ctx, name, args)
		if !public && s.automationService != nil {
			if hookErr := s.automationService.AfterTool(ctx, name, args, output.Value, err); hookErr != nil {
				slog.Error("工具结束通知失败", "tool", name, "error", hookErr)
			}
		}
		return output, err
	}
	if !public && task.ID(ctx) > 0 && s.tasks != nil {
		runtime.ChildResults = s.consumeChildResults
		runtime.Completion = func(ctx context.Context) (string, bool, error) {
			identity, ok := chat.IdentityFromContext(ctx)
			if !ok {
				return "", false, errors.New("任务完成需要可信身份")
			}
			return s.tasks.RequestedCompletion(ctx, identity, task.ID(ctx))
		}
	}
	if budget.Enabled && req.SubagentDepth == 0 {
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
