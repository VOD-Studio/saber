package model

import (
	"context"
	"time"

	"rua.plus/saber/internal/agent"
)

// AgentModel 将模型客户端、重试和增量响应适配为独立 Runtime 的模型接口。
func AgentModel(getClient func(string) (*Client, error), retry *RetryConfigWrapper) agent.ModelFunc {
	return func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Response, error) {
		if emit == nil {
			emit = func(agent.Event) {}
		}
		gate, gated := agent.AttemptGateFromContext(ctx)
		attempt := 0
		var response agent.Response
		begin := func(model string) (int, time.Time) {
			attempt++
			response = agent.Response{Model: model}
			return attempt, time.Now()
		}
		finish := func(model string, timer time.Time, requestErr error) (agent.Response, error) {
			if response.Model == "" {
				response.Model = model
			}
			record := agent.Attempt{Number: attempt, Response: response, Duration: time.Since(timer)}
			if requestErr != nil {
				record.Error = requestErr.Error()
			}
			emit(agent.Event{Kind: agent.AttemptFinished, Attempt: record})
			return response, requestErr
		}
		// 首个尝试先发事件：事件消费者在此取消时不得解析客户端或发起请求。
		number, timer := begin(req.Model)
		emit(agent.Event{Kind: agent.AttemptStarted, Attempt: agent.Attempt{Number: number, Response: response}})
		if err := ctx.Err(); err != nil {
			return finish(req.Model, timer, err)
		}
		firstClient, firstErr := getClient(req.Model)
		requestRetry := retry
		if firstClient != nil && firstClient.runtime != nil {
			requestRetry = &RetryConfigWrapper{}
		}
		if requestRetry == nil {
			requestRetry = &RetryConfigWrapper{}
		}
		fallback := FallbackModelHandler{MainModel: req.Model, RetryConfig: requestRetry}
		first := true
		_, err := fallback.TryWithFallback(ctx, func(model string) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if first {
				first = false
				timer = time.Now()
				response = agent.Response{Model: model}
			} else {
				number, timer = begin(model)
				emit(agent.Event{Kind: agent.AttemptStarted, Attempt: agent.Attempt{Number: number, Response: response}})
			}
			// 事件消费者可能在开始事件中取消，不能因此再发起一次请求。
			requestErr := ctx.Err()
			if requestErr == nil {
				client, clientErr := firstClient, firstErr
				if model != req.Model {
					client, clientErr = getClient(model)
				}
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
					admitted := false
					if gated && client.runtime == nil && effectiveAPI(client.config.Provider, client.config.API) != "google-gemini" {
						request, requestErr = gate.Admit(request)
						admitted = requestErr == nil
					}
					if requestErr == nil {
						collector := newAgentStreamHandler(emit)
						var result *ChatCompletionResponse
						if request.Stream {
							result, requestErr = client.completeRequest(ctx, request, collector)
						} else {
							result, requestErr = client.completeRequest(ctx, request, nil)
						}
						if result != nil {
							response = *result
						} else {
							response = collector.response()
						}
						if summary := collector.response().Thinking; response.Thinking == "" {
							response.Thinking = summary
						} else if summary == "" && response.Thinking != "" {
							emit(agent.Event{Kind: agent.ThinkingDelta, Text: response.Thinking})
						}
					}
					if admitted {
						gate.Record(request, response)
					}
				}
			}
			return finish(model, timer, requestErr)
		})
		return response, err
	}
}
