package ai

import (
	"context"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/conversation"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/model"
)

// RunAgent 不连接聊天平台地运行一次 Agent，返回完整轨迹供调用方保存。
// 调用真实 MCP 工具时，ctx 须通过 chat.WithIdentity 携带接入端身份。
func (s *Service) RunAgent(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
	return s.runAgent(ctx, req, emit, nil)
}

func (s *Service) runAgent(ctx context.Context, req agent.Request, emit func(agent.Event), firstClient *Client) (agent.Result, error) {
	retry := &RetryConfigWrapper{MaxRetries: s.config.Agent.Retry.MaxRetries, InitialDelay: time.Duration(s.config.Agent.Retry.InitialDelayMs) * time.Millisecond, MaxDelay: time.Duration(s.config.Agent.Retry.MaxDelayMs) * time.Millisecond, BackoffFactor: s.config.Agent.Retry.BackoffFactor}
	if !s.config.Agent.Retry.Enabled {
		retry.MaxRetries = 0
	}
	if s.config.Agent.Retry.FallbackEnabled {
		retry.FallbackModels = s.config.Agent.Retry.FallbackModels
	}
	retry.CircuitBreaker = s.circuitBreaker
	getClient := func(model string) (*Client, error) {
		if firstClient != nil && model == req.Model {
			return firstClient, nil
		}
		return s.getClient(model)
	}
	runtime := agent.Runtime{
		Context: s.contextPolicy(),
		Limits:  agent.Limits{MaxRounds: s.config.Agent.MaxIterations, Timeout: time.Duration(s.config.Agent.TimeoutSeconds) * time.Second, MaxToolOutputBytes: s.config.Agent.MaxToolOutputBytes},
		Model:   model.AgentModel(getClient, retry),
		Execute: func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
			value, err := s.toolExecutor.ExecuteToolCall(ctx, name, args)
			output := agent.ToolOutput{Value: value}
			if result, ok := value.(*mcpsdk.CallToolResult); ok && result != nil {
				output.IsError = result.IsError
			}
			if result, ok := value.(execution.Result); ok {
				output.IsError = result.ExitCode != 0 || result.Error != ""
			}
			return output, err
		},
	}
	return runtime.Run(ctx, req, emit)
}

// runAgentReply 兼容预先构造请求的旧调用方，展示复用通用 Presenter。
func (s *Service) runAgentReply(ctx context.Context, req agent.Request, roomID id.RoomID, client *Client) (*ChatCompletionResponse, error) {
	timeout := time.Duration(s.config.Agent.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 600 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	adapter := matrix.NewChatAdapter(s.matrixService, s.mediaService, s.config.Matrix.Media, s.config.Matrix.StreamEdit.Enabled, nil)
	message := chat.Message{Session: adapter.Session(ctx, roomID), ID: string(matrix.GetEventID(ctx))}
	result, err := conversation.Deliver(ctx, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		return s.runAgent(ctx, req, emit, client)
	}, req, message, adapter, displayConfig(s.config.Matrix.StreamEdit), func(result agent.Result) {
		if s.contextManager != nil {
			s.contextManager.history.AddMessage(message.Session.Key(), RoleAssistant, result.Content, "")
		}
	})
	if err != nil {
		return nil, err
	}
	response := result.Rounds[len(result.Rounds)-1].Response
	response.Usage = result.Usage
	return &response, nil
}

func (s *Service) contextPolicy() agent.ContextPolicy {
	c := s.config.Agent.Context
	return agent.ContextPolicy{Enabled: c.Enabled, MaxMessages: c.MaxMessages, MaxInputTokens: c.MaxTokens}
}
