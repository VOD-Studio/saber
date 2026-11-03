package ai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// ErrOpenAIModelNotFound 表示 OpenAI 兼容接口请求的模型不在注册表中。
var ErrOpenAIModelNotFound = errors.New("模型不存在")

// OpenAIMessage 是 OpenAI 兼容接口接受的一条文本消息。
// 该接口不读取聊天历史，也不启用工具调用，因此只保留角色和文本内容。
type OpenAIMessage struct {
	Role    string
	Content string
}

// OpenAIChatRequest 描述一次非任务、不持久化的模型请求。
// Model 为空时使用当前默认模型；Temperature 为 nil 时沿用模型配置。
type OpenAIChatRequest struct {
	Model       string
	Messages    []OpenAIMessage
	Stream      bool
	Temperature *float64
	MaxTokens   int
}

// OpenAIChatResult 是一次运行对 HTTP 层可见的结果。
// Status 为 agent.Completed 时 Content 才是完整回答。
type OpenAIChatResult struct {
	Content string
	Usage   openai.Usage
	Model   string
	Status  agent.Status
}

// ResolveOpenAIModel 校验并返回实际使用的模型标识。
// 空名称使用注册表默认模型；不在注册表中的名称返回 ErrOpenAIModelNotFound。
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

// RunOpenAIChat 构建一次 Agent 请求并同步运行，不写入任务数据库也不读取历史。
// emit 可为空；非空时必须是快速返回的回调，运行事件由调用方自行排队。
func (s *Service) RunOpenAIChat(ctx context.Context, req OpenAIChatRequest, emit func(agent.Event)) (OpenAIChatResult, error) {
	if !s.IsEnabled() {
		return OpenAIChatResult{}, errors.New("AI功能未启用")
	}
	modelName, err := s.ResolveOpenAIModel(req.Model)
	if err != nil {
		return OpenAIChatResult{}, err
	}
	if len(req.Messages) == 0 {
		return OpenAIChatResult{}, errors.New("messages 不能为空")
	}
	messages := make([]openai.ChatCompletionMessage, len(req.Messages))
	for i, message := range req.Messages {
		if !openAIRoleAllowed(message.Role) {
			return OpenAIChatResult{}, fmt.Errorf("不支持的消息角色 %q", message.Role)
		}
		messages[i] = openai.ChatCompletionMessage{Role: message.Role, Content: message.Content}
	}

	temperature := 0.0
	cfg, _ := s.core.GetConfig().GetModelConfig(modelName)
	if req.Temperature != nil {
		temperature = *req.Temperature
	} else if cfg.Temperature != nil {
		temperature = *cfg.Temperature
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = cfg.MaxTokens
	}

	agentRequest := agent.Request{
		Model:       modelName,
		Messages:    messages,
		Stream:      req.Stream,
		Temperature: temperature,
		MaxTokens:   maxTokens,
	}
	// 身份只用于内部上下文，接口本身不访问聊天历史或外部工具。
	identity := chat.Identity{
		Session:  chat.Session{Platform: "terminal", Account: "saber", Conversation: "api"},
		SenderID: strconv.Itoa(os.Getuid()),
	}
	result, err := s.RunAgent(chat.WithIdentity(ctx, identity), agentRequest, emit)
	if err != nil {
		return OpenAIChatResult{Content: result.Content, Usage: openAIUsage(result), Model: modelName, Status: result.Status}, err
	}
	return OpenAIChatResult{Content: result.Content, Usage: openAIUsage(result), Model: modelName, Status: result.Status}, nil
}

// openAIRoleAllowed 限定 OpenAI 兼容入口接受的文本角色。
func openAIRoleAllowed(role string) bool {
	switch role {
	case "system", "user", "assistant":
		return true
	default:
		return false
	}
}

// openAIUsage 返回本次运行的主请求用量；未启用账本时按已完成的轮次汇总。
func openAIUsage(result agent.Result) openai.Usage {
	usage := result.Usage
	if usage.TotalTokens > 0 {
		return usage
	}
	for _, round := range result.Rounds {
		usage.PromptTokens += round.Response.Usage.PromptTokens
		usage.CompletionTokens += round.Response.Usage.CompletionTokens
		usage.TotalTokens += round.Response.Usage.TotalTokens
	}
	return usage
}
