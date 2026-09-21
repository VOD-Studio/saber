package ai

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/matrix"
)

// RunAgent 不连接聊天平台地运行一次 Agent，返回完整轨迹供调用方保存。
// 调用真实 MCP 工具时，ctx 仍须携带现有 MCP 身份上下文。
func (s *Service) RunAgent(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
	return s.runAgent(ctx, req, emit, nil)
}

func (s *Service) runAgent(ctx context.Context, req agent.Request, emit func(agent.Event), firstClient *Client) (agent.Result, error) {
	cfg := s.core.GetConfig()
	retry := &RetryConfigWrapper{MaxRetries: cfg.Retry.MaxRetries, InitialDelay: time.Duration(cfg.Retry.InitialDelayMs) * time.Millisecond, MaxDelay: time.Duration(cfg.Retry.MaxDelayMs) * time.Millisecond, BackoffFactor: cfg.Retry.BackoffFactor}
	if cfg.Retry.FallbackEnabled {
		retry.FallbackModels = cfg.Retry.FallbackModels
	}
	if cfg.CircuitBreaker.Enabled {
		retry.CircuitBreaker = NewCircuitBreaker(cfg.CircuitBreaker.FailureThreshold, time.Duration(cfg.CircuitBreaker.ResetTimeout)*time.Second)
	}
	getClient := func(model string) (*Client, error) {
		if firstClient != nil && model == req.Model {
			return firstClient, nil
		}
		return s.getClient(model)
	}
	runtime := agent.Runtime{
		Limits: agent.Limits{MaxRounds: cfg.ToolCalling.MaxIterations, Timeout: time.Duration(cfg.ToolCalling.TimeoutSeconds) * time.Second, MaxToolOutputBytes: cfg.ToolCalling.MaxToolOutputBytes},
		Model:  agentModel(getClient, retry),
		Execute: func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
			value, err := s.toolExecutor.ExecuteToolCall(ctx, name, args)
			output := agent.ToolOutput{Value: value}
			if result, ok := value.(*mcpsdk.CallToolResult); ok && result != nil {
				output.IsError = result.IsError
			}
			return output, err
		},
	}
	return runtime.Run(ctx, req, emit)
}

func agentModel(getClient func(string) (*Client, error), retry *RetryConfigWrapper) agent.ModelFunc {
	return func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Response, error) {
		fallback := FallbackModelHandler{MainModel: req.Model, RetryConfig: retry}
		attempt := 0
		var response agent.Response
		_, err := fallback.TryWithFallback(ctx, func(model string) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			attempt++
			started := time.Now()
			response = agent.Response{Model: model}
			emit(agent.Event{Kind: agent.AttemptStarted, Attempt: agent.Attempt{Number: attempt, Response: response}})
			// 事件消费者可能在开始事件中取消，不能因此再发起一次请求。
			requestErr := ctx.Err()
			if requestErr == nil {
				client, clientErr := getClient(model)
				requestErr = clientErr
				if requestErr == nil {
					req.Model = model
					if req.Stream {
						collector := newAgentStreamHandler(emit)
						requestErr = client.CreateStreamingChatCompletionWithTools(ctx, req, collector)
						response = collector.response()
					} else {
						var result *ChatCompletionResponse
						result, requestErr = client.CreateChatCompletion(ctx, req)
						if result != nil {
							response = *result
						}
					}
				}
			}
			if response.Model == "" {
				response.Model = model
			}
			record := agent.Attempt{Number: attempt, Response: response, Duration: time.Since(started)}
			if requestErr != nil {
				record.Error = requestErr.Error()
			}
			emit(agent.Event{Kind: agent.AttemptFinished, Attempt: record})
			return response, requestErr
		})
		return response, err
	}
}

// runAgentReply 是当前 Matrix 展示 adapter；发送失败不会重新执行 Agent。
func (s *Service) runAgentReply(ctx context.Context, req agent.Request, roomID id.RoomID, client *Client) (*ChatCompletionResponse, error) {
	// 展示回调执行网络操作，也必须受总运行时长约束。
	timeout := time.Duration(s.core.GetConfig().ToolCalling.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := s.matrixService.StartTyping(ctx, roomID, 30000); err != nil {
		slog.Warn("无法启动 typing indicator", "error", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := s.matrixService.StopTyping(cleanup, roomID); err != nil {
			slog.Warn("无法停止 typing indicator", "error", err)
		}
	}()
	var emit func(agent.Event)
	var editor *StreamEditor
	var displayErr error
	if req.Stream {
		cfg := s.core.GetConfig().StreamEdit
		editor = NewStreamEditor(s.matrixService, roomID, "", cfg, matrix.GetEventID(ctx))
		defer editor.Stop()
		var content strings.Builder
		started := false
		attemptStart := time.Now()
		emit = func(e agent.Event) {
			switch e.Kind {
			case agent.AttemptStarted:
				content.Reset()
				attemptStart = time.Now()
			case agent.TextDelta:
				content.WriteString(e.Text)
				if displayErr != nil {
					return
				}
				if !started && (content.Len() >= cfg.CharThreshold || time.Since(attemptStart) >= time.Duration(cfg.TimeThresholdMs)*time.Millisecond) {
					displayErr = editor.Start(ctx)
					started = displayErr == nil
				}
				if started {
					displayErr = editor.Update(ctx, content.String())
				}
			case agent.RunFinished:
				if e.Status != agent.Completed {
					return
				}
				// 临时编辑失败不妨碍最后一次交付，也不触发模型或工具重跑。
				if !started {
					displayErr = editor.Start(ctx)
					if displayErr != nil {
						return
					}
				}
				displayErr = editor.SendFinal(ctx, e.Text)
			}
		}
	}
	result, err := s.runAgent(ctx, req, emit, client)
	if err != nil {
		return nil, err
	}
	// 先保存生成结果，展示端断线不会丢失本次模型回答。
	if s.contextManager != nil {
		s.contextManager.AddMessage(roomID, RoleAssistant, result.Content, s.matrixService.BotID())
	}
	if !req.Stream {
		displayErr = s.respHandler.SendResponse(ctx, roomID, result.Content)
	}
	if displayErr != nil {
		return nil, fmt.Errorf("发送响应失败：%w", displayErr)
	}
	response := result.Rounds[len(result.Rounds)-1].Response
	response.Usage = result.Usage
	return &response, nil
}
