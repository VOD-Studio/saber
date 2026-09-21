package model

import (
	"context"
	"time"

	"rua.plus/saber/internal/agent"
)

// AgentModel 将模型客户端、重试和增量响应适配为独立 Runtime 的模型接口。
func AgentModel(getClient func(string) (*Client, error), retry *RetryConfigWrapper) agent.ModelFunc {
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
					request := req
					request.Model = model
					if model != req.Model {
						request.MaxTokens = client.config.MaxTokens
						if client.config.Temperature != nil {
							request.Temperature = *client.config.Temperature
						}
					}
					if client.usesResponses() {
						collector := newAgentStreamHandler(emit)
						var result *ChatCompletionResponse
						result, requestErr = client.createResponse(ctx, request, collector)
						if result != nil {
							response = *result
						} else {
							response = collector.response()
						}
					} else if req.Stream {
						collector := newAgentStreamHandler(emit)
						requestErr = client.CreateStreamingChatCompletionWithTools(ctx, request, collector)
						response = collector.response()
					} else {
						var result *ChatCompletionResponse
						result, requestErr = client.CreateChatCompletion(ctx, request)
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
