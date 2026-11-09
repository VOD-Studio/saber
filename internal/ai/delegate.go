package ai

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
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
			Description: "将目标明确且互不依赖的子任务交给子代理。每个子代理独立上下文，仅能使用父任务当前授权的工具子集；" +
				"有持久结果出口且工作副本不冲突时可后台运行，返回持久任务编号，否则同步执行。可用 worker 由管理员配置。",
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
					"worker":        map[string]any{"type": "string", "description": "管理员配置的 worker 路由名称，不接受 provider、密钥或 URL。"},
					"context":       map[string]any{"type": "string", "description": "子任务必需的背景和约束。"},
					"images":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 8},
					"output_schema": map[string]any{"type": "object", "description": "子任务最终 JSON 输出的 JSON Schema，只允许本地引用。"},
					"tasks":         map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "一组独立子目标；每项可含 goal、tools、worker、context、images、output_schema。"},
				},
				"anyOf": []any{map[string]any{"required": []string{"goal"}}, map[string]any{"required": []string{"tasks"}}},
			},
		},
	}
}

// exposeDelegate 在顶层运行且已有至少一个可用工具时追加 delegate。
// 没有任何工具时委派不能带来新能力，只会多一次模型运行；子运行的请求也不会经过这里。
func (te *ToolExecutor) exposeDelegate(ctx context.Context, tools []openai.Tool) []openai.Tool {
	if te.service == nil || te.service.config == nil {
		return tools
	}
	cfg := te.service.config.Agent.Subagents
	cfg.ApplyDefaults()
	if len(tools) == 0 || !te.service.subagentsEnabled() || agent.SubagentDepth(ctx) >= cfg.MaxDepth || isPublicThread(ctx) {
		return tools
	}
	tool := delegateTool()
	if len(cfg.Workers) > 0 {
		names := make([]string, 0, len(cfg.Workers))
		for name := range cfg.Workers {
			names = append(names, name)
		}
		sort.Strings(names)
		parameters := tool.Function.Parameters.(map[string]any)
		properties := parameters["properties"].(map[string]any)
		properties["worker"].(map[string]any)["enum"] = names
	}
	return append(tools, tool)
}

// subagentDelegator 为当前顶层运行组装委托器；未启用子代理或当前已是子代理时返回 nil。
// 子代理只能拿到父请求中仍然获准的工具，旧请求快照里已撤销的能力会被重新筛掉。
func (s *Service) subagentDelegator(ctx context.Context, req agent.Request, limits agent.Limits, modelFunc agent.ModelFunc, execute agent.ToolFunc) *agent.Delegator {
	cfg := s.config.Agent.Subagents
	cfg.ApplyDefaults()
	if !s.subagentsEnabled() || agent.SubagentDepth(ctx) >= cfg.MaxDepth || isPublicThread(ctx) {
		return nil
	}
	policy := agent.SubagentPolicy{
		MaxRounds:      cfg.MaxRounds,
		MaxTotalTokens: cfg.MaxTotalTokens,
		MaxParallel:    cfg.MaxParallel,
		MaxDepth:       cfg.MaxDepth,
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
	delegator.Route = s.routeSubagent
	delegator.Spawn = s.spawnSubagent
	delegator.RunChild = func(ctx context.Context, child agent.Request) (agent.Result, error) {
		child.RunLimits = &agent.Limits{MaxRounds: policy.MaxRounds, MaxTotalTokens: policy.MaxTotalTokens, Timeout: limits.Timeout, MaxToolOutputBytes: limits.MaxToolOutputBytes}
		if cfg.ChildTimeoutSeconds > 0 {
			child.RunLimits.Timeout = time.Duration(cfg.ChildTimeoutSeconds) * time.Second
		}
		return s.runAgent(ctx, child, nil, nil)
	}
	if cfg.WorktreeIsolation {
		delegator.Prepare = s.prepareSubagentContext
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
		Finish: func(ctx context.Context, id int64, status agent.Status, result string, usage openai.Usage) error {
			return s.tasks.RecordSubagentFinish(ctx, id, string(status), result, usage)
		},
	}
	return delegator
}

// subagentTools 按当前授权重新筛出父请求工具集，并去掉 delegate 与状态工具。
func (s *Service) subagentTools(ctx context.Context, tools []openai.Tool) []openai.Tool {
	filtered := make([]openai.Tool, 0, len(tools))
	current, _ := s.toolExecutor.PrepareTools(ctx)
	allowed := make(map[string]bool, len(current))
	for _, tool := range current {
		if tool.Function != nil {
			allowed[tool.Function.Name] = true
		}
	}
	for _, tool := range tools {
		if tool.Function == nil {
			continue
		}
		name := tool.Function.Name
		cfg := s.config.Agent.Subagents
		cfg.ApplyDefaults()
		if name == "" || name == "saber_task_state" || (name == agent.DelegateTool && agent.SubagentDepth(ctx)+1 >= cfg.MaxDepth) {
			continue
		}
		if !allowed[name] {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
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
