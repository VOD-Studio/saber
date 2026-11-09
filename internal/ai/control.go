package ai

import (
	"context"
	"errors"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

const graphControlTool = "saber_task_graph"
const questionControlTool = "clarify"

func (s *Service) isControlTool(name string) bool {
	return name == graphControlTool || name == questionControlTool
}

func (s *Service) controlDefinitions(ctx context.Context) []openai.Tool {
	if s.tasks == nil || s.config == nil || !s.config.Control.Enabled || isPublicThread(ctx) {
		return nil
	}
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return nil
	}
	return []openai.Tool{
		{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
			Name:        graphControlTool,
			Description: "在唯一持久任务图中列举/查看真实任务、创建有依赖的子任务、防环链接、记录阻塞、请求审查、评论、显式可信跨平台交接或读取后台结果。运行代理只能控制自己的任务树；审查批准、解除阻塞和重启恢复必须由真实用户入口执行。依赖必须完成才能派发。block/request_review将停止当前工作并等待人类，不能代替报告未完成事实。create使用当前模型与权限，不接受用户身份或凭据。",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"action":  map[string]any{"type": "string", "enum": []string{"list", "show", "create", "link", "unlink", "complete", "block", "request_review", "comment", "handoff", "drain", "stop_children"}},
				"task_id": map[string]any{"type": "integer"}, "parent_id": map[string]any{"type": "integer"},
				"dependency_id": map[string]any{"type": "integer"}, "dependencies": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"goal": map[string]any{"type": "string"}, "work_dir": map[string]any{"type": "string"},
				"kind":   map[string]any{"type": "string", "enum": []string{"dependency", "needs_input", "capability", "transient"}},
				"reason": map[string]any{"type": "string"}, "summary": map[string]any{"type": "string"},
				"metadata": map[string]any{"type": "object"}, "text": map[string]any{"type": "string"}, "route": map[string]any{"type": "string"},
			}, "required": []string{"action"}, "additionalProperties": false},
		}},
		{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
			Name:        questionControlTool,
			Description: "向任务发起人提出1到5个独立决策问题并等待真实答案。每题最多4个选项，推荐项放第一；省略choices为自由文本，multi_select允许多选及其他文字。问题与答案持久保存，只有发起人/授权管理员能回答。不能用来代替执行工具的危险操作授权。",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"questions": map[string]any{"type": "array", "minItems": 1, "maxItems": 5, "items": map[string]any{"type": "object", "properties": map[string]any{
					"question": map[string]any{"type": "string"}, "choices": map[string]any{"type": "array", "maxItems": 4, "items": map[string]any{"type": "string"}}, "multi_select": map[string]any{"type": "boolean"},
				}, "required": []string{"question"}, "additionalProperties": false}},
			}, "required": []string{"questions"}, "additionalProperties": false},
		}},
	}
}

func (s *Service) executeControlTool(ctx context.Context, name string, args map[string]any) (any, error) {
	if s.tasks == nil || s.config == nil || !s.config.Control.Enabled {
		return nil, errors.New("task control is disabled or task manager is unavailable")
	}
	if isPublicThread(ctx) {
		return nil, errors.New("公开互动回合不允许任务控制")
	}
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return nil, errors.New("task control requires trusted identity")
	}
	switch name {
	case graphControlTool:
		return s.tasks.GraphTool(ctx, args)
	case questionControlTool:
		if task.ID(ctx) <= 0 {
			return nil, errors.New("澄清工具必须绑定真实持久任务")
		}
		questions := map[string]any{"questions": args["questions"], "action": "ask", "wait": true}
		return s.tasks.QuestionTool(ctx, questions)
	default:
		return nil, errors.New("unknown control tool")
	}
}
