package ai

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/task"
)

// ErrOpenAIModelNotFound 表示请求模型不在受信注册表中。
var ErrOpenAIModelNotFound = errors.New("模型不存在")

// OpenAIMessage 保留已校验的角色、文本与模型可见图片，不接受伪造工具回复。
type OpenAIMessage struct {
	Role    string
	Content string
	Images  []string
}

// OpenAIChatRequest 是认证 API 的公开模型选项，不包含任务身份和执行后端字段。
type OpenAIChatRequest struct {
	Model        string
	Messages     []OpenAIMessage
	Stream       bool
	Temperature  *float64
	MaxTokens    int
	ToolNames    []string
	DisableTools bool
	ToolChoice   string
	// PreviousTaskID 由认证响应编号解析产生，不允许模型通过工具更换历史身份。
	PreviousTaskID int64
}

// ResolveOpenAIModel 只从配置注册表选择模型，不接受请求携带提供商 URL 或密钥。
func (s *Service) ResolveOpenAIModel(name string) (string, error) {
	registry := s.GetModelRegistry()
	if name == "" {
		name = registry.GetDefault()
	}
	if name == "" {
		return "", ErrOpenAIModelNotFound
	}
	if _, ok := registry.GetModelInfo(name); !ok {
		return "", fmt.Errorf("%w: %s", ErrOpenAIModelNotFound, name)
	}
	return name, nil
}

// SubmitOpenAIChatTask 冻结原始请求并进入同一任务队列，工具权限在派发和调用时重新验证。
func (s *Service) SubmitOpenAIChatTask(ctx context.Context, message chat.Message, input OpenAIChatRequest) (task.Task, error) {
	if !s.IsEnabled() || s.tasks == nil {
		return task.Task{}, errors.New("持久 Agent 任务服务未启用")
	}
	identity := chat.Identity{Session: message.Session, SenderID: message.SenderID, Direct: message.Direct}
	if identity.SenderID == "" || identity.Session.Validate() != nil {
		return task.Task{}, errors.New("API 缺少可信身份")
	}
	if supplied, ok := chat.IdentityFromContext(ctx); ok && (supplied.Session != identity.Session || supplied.SenderID != identity.SenderID) {
		return task.Task{}, errors.New("API identity mismatch")
	}
	ctx = chat.WithIdentity(ctx, identity)
	if err := message.Validate(); err != nil {
		return task.Task{}, err
	}
	modelName, err := s.ResolveOpenAIModel(input.Model)
	if err != nil {
		return task.Task{}, err
	}
	req, err := s.taskRequest(message, modelName)
	if err != nil {
		return task.Task{}, err
	}
	if input.Temperature != nil {
		v := *input.Temperature
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 2 {
			return task.Task{}, errors.New("temperature 必须在 0..2")
		}
		req.Temperature = v
	}
	if input.MaxTokens < 0 {
		return task.Task{}, errors.New("max_tokens 不能为负数")
	}
	if input.MaxTokens > 0 {
		req.MaxTokens = input.MaxTokens
	}
	if len(input.Messages) == 0 {
		return task.Task{}, errors.New("messages 不能为空")
	}
	for _, m := range input.Messages {
		switch m.Role {
		case "user", "assistant", "system", "developer":
		default:
			return task.Task{}, fmt.Errorf("不允许客户端消息角色 %q", m.Role)
		}
		msg := openai.ChatCompletionMessage{Role: m.Role, Content: m.Content}
		if len(m.Images) > 0 {
			validated := chat.Message{Session: message.Session, SenderID: message.SenderID, Text: m.Content}
			for _, image := range m.Images {
				validated.Attachments = append(validated.Attachments, chat.Attachment{Kind: "image", URL: image})
			}
			if err := validated.Validate(); err != nil {
				return task.Task{}, err
			}
			msg = taskInput(validated)
			msg.Role = m.Role
		}
		req.Messages = append(req.Messages, msg)
	}
	dir := s.taskDir
	if s.executor != nil {
		if workspace, e := s.executor.Workspace(identity); e == nil {
			dir = workspace
		}
	}
	toolCtx := execution.WithTask(ctx, 0, dir)
	known := make(map[string]openai.Tool)
	for _, definition := range s.combinedDefinitions(toolCtx) {
		if definition.Tool.Function != nil {
			known[definition.Tool.Function.Name] = definition.Tool
		}
	}
	if s.toolExecutor != nil {
		visible, _ := s.toolExecutor.PrepareTools(toolCtx)
		for _, tool := range visible {
			if tool.Function != nil {
				known[tool.Function.Name] = tool
			}
		}
	}
	if input.DisableTools || input.ToolChoice == "none" {
		if input.ToolChoice != "" && input.ToolChoice != "none" {
			return task.Task{}, errors.New("禁用工具与 tool_choice 冲突")
		}
		req.ToolScope = []string{"__saber_no_tools__"}
		req.Tools = nil
		req.ToolChoice = "none"
	} else {
		if input.ToolNames != nil {
			seen := map[string]bool{}
			for _, name := range input.ToolNames {
				tool, ok := known[name]
				if !ok {
					return task.Task{}, fmt.Errorf("工具 %q 未配置或当前身份无权调用", name)
				}
				if seen[name] {
					return task.Task{}, errors.New("重复工具名称")
				}
				seen[name] = true
				req.ToolScope = append(req.ToolScope, name)
				req.Tools = append(req.Tools, tool)
			}
		}
		choice := input.ToolChoice
		switch choice {
		case "", "auto":
			if choice != "" {
				req.ToolChoice = "auto"
			}
		case "required":
			if len(known) == 0 || input.ToolNames != nil && len(input.ToolNames) == 0 {
				return task.Task{}, errors.New("required 没有可用工具")
			}
			req.ToolChoice = "required"
		default:
			tool, ok := known[choice]
			if !ok {
				return task.Task{}, fmt.Errorf("指定工具 %q 不可用", choice)
			}
			if req.ToolScope != nil {
				found := false
				for _, name := range req.ToolScope {
					if name == choice {
						found = true
					}
				}
				if !found {
					return task.Task{}, errors.New("tool_choice 不在请求工具范围")
				}
			} else {
				req.ToolScope = []string{choice}
				req.Tools = []openai.Tool{tool}
			}
			req.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": choice}}
		}
	}
	if input.PreviousTaskID > 0 {
		req, err = s.tasks.RestoreRequest(ctx, identity, input.PreviousTaskID, req)
		if err != nil {
			return task.Task{}, err
		}
	}
	return s.tasks.SubmitTurn(ctx, message, dir, req)
}

// PrepareOpenAIChoice 保持显式选择的真实工具可见，不让延迟发现隐藏调用方已经指定的工具。
func (s *Service) PrepareOpenAIChoice(ctx context.Context, req agent.Request) (agent.Request, error) {
	if len(req.ToolScope) == 0 {
		return req, nil
	}
	allowed := make(map[string]bool, len(req.ToolScope))
	for _, name := range req.ToolScope {
		allowed[name] = true
	}
	tools := req.Tools
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool.Function != nil {
			seen[tool.Function.Name] = true
		}
	}
	for _, definition := range s.combinedDefinitions(ctx) {
		name := definition.Tool.Function.Name
		if allowed[name] && !seen[name] {
			tools = append(tools, definition.Tool)
			seen[name] = true
		}
	}
	req.Tools = tools
	if req.ToolChoice == "none" {
		req.Tools = nil
		return req, nil
	}
	if choice, ok := req.ToolChoice.(map[string]any); ok {
		function, ok := choice["function"].(map[string]any)
		if !ok {
			return req, errors.New("无效持久 tool_choice")
		}
		name, _ := function["name"].(string)
		if !seen[name] {
			return req, errors.New("指定工具在任务派发时已经失去授权")
		}
	}
	return req, nil
}
