package ai

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/task"
)

// subagentsEnabled 报告是否允许顶层运行调用 delegate；零值配置等于默认开启。
func (s *Service) subagentsEnabled() bool {
	return s.config != nil && s.config.Agent.Subagents.IsEnabled()
}

// delegateTool 是内置子代理工具的协议定义。
// 描述里明确「独立子目标」和「不能再次委派」，降低模型把串行依赖拆给子代理的概率。
func delegateTool() openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name: agent.DelegateTool,
			Description: "把一个目标明确、与当前步骤无串行依赖的子任务交给子代理并行完成。" +
				"子代理有独立上下文，看不到本次对话历史和你的工具结果，只能看到 goal；" +
				"它使用当前模型和本次已授权的工具子集，不能再次委派。" +
				"同一轮可以多次调用以并行处理互不依赖的子目标；有先后依赖或需要你判断结果时，请等待返回后再调用。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"goal": map[string]any{
						"type":        "string",
						"description": "子代理要独立完成的完整目标，含必要背景、约束和期望产出，最多 8192 字节。",
					},
					"tools": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "可选：子代理可用的工具名子集，必须来自本次可用工具；省略表示沿用父请求的全部工具。",
					},
				},
				"required": []string{"goal"},
			},
		},
	}
}

// exposeDelegate 在顶层运行且已有至少一个可用工具时追加 delegate。
// 没有任何工具时委派不能带来新能力，只会多一次模型运行；子运行的请求也不会经过这里。
func (te *ToolExecutor) exposeDelegate(ctx context.Context, tools []openai.Tool) []openai.Tool {
	if len(tools) == 0 || !te.service.subagentsEnabled() || agent.SubagentDepth(ctx) > 0 {
		return tools
	}
	return append(tools, delegateTool())
}

// subagentDelegator 为当前顶层运行组装委托器；未启用子代理或当前已是子代理时返回 nil。
// 子代理只能拿到父请求中仍然获准的工具，旧请求快照里已撤销的能力会被重新筛掉。
func (s *Service) subagentDelegator(ctx context.Context, req agent.Request, limits agent.Limits, modelFunc agent.ModelFunc, execute agent.ToolFunc) *agent.Delegator {
	if !s.subagentsEnabled() || agent.SubagentDepth(ctx) > 0 {
		return nil
	}
	cfg := s.config.Agent.Subagents
	policy := agent.SubagentPolicy{
		MaxRounds:      cfg.MaxRounds,
		MaxTotalTokens: cfg.MaxTotalTokens,
		MaxParallel:    cfg.MaxParallel,
	}.Normalize()
	delegator := &agent.Delegator{
		Policy:    policy,
		Model:     modelFunc,
		Execute:   execute,
		Parent:    req,
		Tools:     s.subagentTools(ctx, req.Tools),
		Context:   s.contextPolicy(),
		Summarize: s.summarizer(req.Model),
		Limits:    limits,
		Lease:     &agent.WorkspaceLease{},
		LeaseKey:  subagentWorkspace(ctx),
	}
	taskID := task.ID(ctx)
	if taskID <= 0 || s.tasks == nil {
		return delegator
	}
	// 生命周期写入父任务的持久化记录；这些写操作已由委托器切换到不可取消的 ctx。
	delegator.Hooks = agent.SubagentHooks{
		Start: func(ctx context.Context, goal string) (int64, error) {
			return s.tasks.RecordSubagentStart(ctx, taskID, goal)
		},
		Finish: func(ctx context.Context, id int64, status agent.Status, result string, usage openai.Usage) {
			if err := s.tasks.RecordSubagentFinish(ctx, id, string(status), result, usage); err != nil {
				slog.Error("记录子代理终态失败", "error", err, "task_id", taskID, "subagent_id", id)
			}
		},
	}
	return delegator
}

// subagentTools 按当前授权重新筛出父请求工具集，并去掉 delegate 与状态工具。
func (s *Service) subagentTools(ctx context.Context, tools []openai.Tool) []openai.Tool {
	filtered := make([]openai.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool.Function == nil {
			continue
		}
		name := tool.Function.Name
		if name == "" || name == agent.DelegateTool || name == "saber_task_state" {
			continue
		}
		if !s.subagentToolAllowed(ctx, name) {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

// subagentToolAllowed 对本地执行工具与 MCP 工具重新做程序层授权；
// 内部工具没有额外授权层，能进入父请求即视为已授权。
func (s *Service) subagentToolAllowed(ctx context.Context, name string) bool {
	if execution.LocalTool(name) {
		return s.executor != nil && s.executor.Check(ctx, name) == nil
	}
	if s.mcpManager != nil {
		if server := s.mcpManager.GetServerForTool(name); server != "" {
			return s.executor != nil && s.executor.CheckMCP(ctx, server, name) == nil
		}
	}
	return true
}

// subagentWorkspace 返回工作区租约键：优先使用任务目录，其次任务编号。
func subagentWorkspace(ctx context.Context) string {
	if dir := task.WorkDir(ctx); dir != "" {
		return dir
	}
	if id := task.ID(ctx); id > 0 {
		return fmt.Sprintf("task:%d", id)
	}
	return ""
}
