package agent_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
)

func call(id, args string) openai.ToolCall {
	return openai.ToolCall{ID: id, Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "lookup", Arguments: args}}
}

func request() agent.Request {
	return agent.Request{Model: "test", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "find answer"}}, Tools: []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "lookup"}}}}
}

func tools(calls ...openai.ToolCall) agent.Response {
	return agent.Response{Content: "checking", ToolCalls: calls, FinishReason: "tool_calls"}
}

func final() agent.Response { return agent.Response{Content: "answer", FinishReason: "stop"} }

func TestRuntime_RunToolCorrection(t *testing.T) {
	var results []agent.Result
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "stream"}[stream], func(t *testing.T) {
			modelCalls, toolCalls, finished := 0, 0, 0
			req := request()
			req.Stream, req.MaxTokens, req.Temperature = stream, 42, 0.7
			runtime := agent.Runtime{
				Model: func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Response, error) {
					modelCalls++
					if req.MaxTokens != 42 || req.Temperature != 0.7 {
						t.Fatal("request settings lost")
					}
					var response agent.Response
					switch modelCalls {
					case 1:
						response = tools(call("first", `{"limit":-1}`))
					case 2:
						last := req.Messages[len(req.Messages)-1]
						if last.ToolCallID != "first" || !strings.Contains(last.Content, "limit must be positive") {
							t.Fatalf("model did not receive failure: %+v", last)
						}
						if len(req.Messages) != 3 {
							t.Fatalf("assistant history duplicated: %d", len(req.Messages))
						}
						response = tools(call("second", `{"limit":1}`))
					case 3:
						last := req.Messages[len(req.Messages)-1]
						if last.ToolCallID != "second" || last.Content != `{"found":"answer"}` {
							t.Fatalf("missing result: %+v", last)
						}
						response = final()
					default:
						t.Fatal("unexpected model call")
					}
					if stream {
						emit(agent.Event{Kind: agent.TextDelta, Text: response.Content})
					}
					return response, nil
				},
				Execute: func(ctx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
					toolCalls++
					if args["limit"].(float64) < 0 {
						return agent.ToolOutput{}, errors.New("limit must be positive")
					}
					return agent.ToolOutput{Value: map[string]string{"found": "answer"}}, nil
				},
			}
			result, err := runtime.Run(context.Background(), req, func(e agent.Event) {
				if e.Kind == agent.RunFinished {
					finished++
					if e.Status != agent.Completed {
						t.Errorf("terminal: %+v", e)
					}
				}
			})
			if err != nil || result.Status != agent.Completed || result.Content != "answer" || modelCalls != 3 || toolCalls != 2 || finished != 1 {
				t.Fatalf("result=%+v err=%v model=%d tool=%d terminal=%d", result, err, modelCalls, toolCalls, finished)
			}
			if result.Rounds[0].Tools[0].ErrorCode != "tool_failed" || result.Rounds[1].Tools[0].Call.Function.Arguments != `{"limit":1}` {
				t.Fatal("incomplete trace")
			}
			if len(req.Messages) != 1 {
				t.Fatal("input mutated")
			}
			results = append(results, result)
		})
	}
	for i := range results {
		results[i].Duration = 0
		for j := range results[i].Rounds {
			for k := range results[i].Rounds[j].Tools {
				results[i].Rounds[j].Tools[k].Duration = 0
			}
		}
	}
	if !reflect.DeepEqual(results[0], results[1]) {
		t.Fatal("stream/direct semantics differ")
	}
}

func TestRuntime_RunCancellation(t *testing.T) {
	for _, where := range []string{"before_run", "model", "tool_started", "during_tool", "tool_finished"} {
		t.Run(where, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if where == "before_run" {
				cancel()
			}
			modelCalls, toolCalls, finished := 0, 0, 0
			runtime := agent.Runtime{
				Model: func(context.Context, agent.Request, func(agent.Event)) (agent.Response, error) {
					modelCalls++
					if where == "model" {
						cancel()
					}
					return tools(call("one", `{}`), call("two", `{}`)), nil
				},
				Execute: func(ctx context.Context, _ string, _ map[string]any) (agent.ToolOutput, error) {
					toolCalls++
					if where == "during_tool" {
						cancel()
						<-ctx.Done()
						return agent.ToolOutput{}, ctx.Err()
					}
					return agent.ToolOutput{Value: "ok"}, nil
				},
			}
			result, err := runtime.Run(ctx, request(), func(e agent.Event) {
				if where == "tool_started" && e.Kind == agent.ToolStarted || where == "tool_finished" && e.Kind == agent.ToolFinished {
					cancel()
				}
				if e.Kind == agent.RunFinished {
					finished++
				}
			})
			wantTools := 0
			if where == "during_tool" || where == "tool_finished" {
				wantTools = 1
			}
			wantModels := 1
			if where == "before_run" {
				wantModels = 0
			}
			if !errors.Is(err, context.Canceled) || result.Status != agent.Cancelled || toolCalls != wantTools || modelCalls != wantModels || finished != 1 {
				t.Fatalf("result=%+v err=%v tools=%d models=%d terminal=%d", result, err, toolCalls, modelCalls, finished)
			}
		})
	}
}

func TestRuntime_RunLimitsAndFailures(t *testing.T) {
	for _, where := range []string{"model", "tool"} {
		t.Run("timeout_"+where, func(t *testing.T) {
			runtime := agent.Runtime{Limits: agent.Limits{Timeout: 10 * time.Millisecond},
				Model: func(ctx context.Context, _ agent.Request, _ func(agent.Event)) (agent.Response, error) {
					if where == "model" {
						<-ctx.Done()
						return agent.Response{}, ctx.Err()
					}
					return tools(call("one", `{}`)), nil
				},
				Execute: func(ctx context.Context, _ string, _ map[string]any) (agent.ToolOutput, error) {
					<-ctx.Done()
					return agent.ToolOutput{}, ctx.Err()
				},
			}
			result, err := runtime.Run(context.Background(), request(), nil)
			if result.Status != agent.TimedOut || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
	t.Run("round_budget", func(t *testing.T) {
		n := 0
		runtime := agent.Runtime{Limits: agent.Limits{MaxRounds: 2}, Model: func(context.Context, agent.Request, func(agent.Event)) (agent.Response, error) {
			return tools(call("one", `{}`)), nil
		}, Execute: func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
			n++
			return agent.ToolOutput{}, nil
		}}
		result, err := runtime.Run(context.Background(), request(), nil)
		if result.Status != agent.BudgetExhausted || !errors.Is(err, agent.ErrBudgetExhausted) || n != 1 || len(result.Rounds) != 2 {
			t.Fatalf("%+v %v tools=%d", result, err, n)
		}
	})
	for _, response := range []agent.Response{{Content: "partial"}, {Content: "cut", FinishReason: "length"}, tools(call("duplicate", `{}`), call("duplicate", `{}`)), {ToolCalls: []openai.ToolCall{call("one", `{}`)}, FinishReason: "stop"}} {
		t.Run("incomplete_response", func(t *testing.T) {
			runtime := agent.Runtime{Model: func(context.Context, agent.Request, func(agent.Event)) (agent.Response, error) { return response, nil }, Execute: func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
				t.Fatal("invalid response executed tools")
				return agent.ToolOutput{}, nil
			}}
			result, err := runtime.Run(context.Background(), request(), nil)
			if result.Status != agent.Failed || err == nil || len(result.Rounds) != 1 {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
	t.Run("model_failure", func(t *testing.T) {
		failure := errors.New("upstream unavailable")
		runtime := agent.Runtime{Model: func(context.Context, agent.Request, func(agent.Event)) (agent.Response, error) {
			return agent.Response{Content: "partial"}, failure
		}}
		result, err := runtime.Run(context.Background(), request(), nil)
		if !errors.Is(err, failure) || result.Status != agent.Failed || result.Rounds[0].Response.Content != "partial" || result.Rounds[0].Error == "" {
			t.Fatalf("%+v %v", result, err)
		}
	})
	for _, limits := range []agent.Limits{{MaxRounds: -1}, {Timeout: -1}, {MaxToolOutputBytes: 1}} {
		t.Run("invalid_limits", func(t *testing.T) {
			result, err := (agent.Runtime{Limits: limits}).Run(context.Background(), request(), nil)
			if err == nil || result.Status != agent.Failed {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
	t.Run("missing_model", func(t *testing.T) {
		result, err := (agent.Runtime{}).Run(context.Background(), request(), nil)
		if err == nil || result.Status != agent.Failed {
			t.Fatalf("%+v %v", result, err)
		}
	})
}

func TestRuntime_RunToolFailuresAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name, args, code string
		output           agent.ToolOutput
		missing          bool
	}{
		{name: "arguments", args: `{broken`, code: "invalid_arguments"},
		{name: "null", args: `null`, code: "invalid_arguments"},
		{name: "protocol_error", args: `{}`, code: "tool_failed", output: agent.ToolOutput{Value: "not found", IsError: true}},
		{name: "serialization", args: `{}`, code: "serialization_failed", output: agent.ToolOutput{Value: make(chan int)}},
		{name: "missing", args: `{}`, code: "tool_unavailable", missing: true},
		{name: "unknown", args: `{}`, code: "unknown_tool"},
		{name: "truncated", args: `{}`, output: agent.ToolOutput{Value: strings.Repeat("中文", 1000)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			var modelContent string
			runtime := agent.Runtime{Limits: agent.Limits{MaxToolOutputBytes: 128}, Model: func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Response, error) {
				n++
				if n == 1 {
					c := call("one", tc.args)
					if tc.name == "unknown" {
						c.Function.Name = "other"
					}
					return tools(c), nil
				}
				modelContent = req.Messages[len(req.Messages)-1].Content
				return final(), nil
			}}
			if !tc.missing {
				runtime.Execute = func(context.Context, string, map[string]any) (agent.ToolOutput, error) { return tc.output, nil }
			}
			result, err := runtime.Run(context.Background(), request(), nil)
			if err != nil {
				t.Fatal(err)
			}
			record := result.Rounds[0].Tools[0]
			if record.ErrorCode != tc.code || record.Content != modelContent || len(record.Content) > 128 || !utf8.ValidString(record.Content) {
				t.Fatalf("%+v", record)
			}
			if tc.name == "truncated" && (!record.Truncated || record.OriginalBytes < 1000 || !strings.Contains(record.Content, "truncated")) {
				t.Fatalf("%+v", record)
			}
		})
	}
}

func TestRuntime_RunAttemptRecords(t *testing.T) {
	runtime := agent.Runtime{Model: func(_ context.Context, _ agent.Request, emit func(agent.Event)) (agent.Response, error) {
		emit(agent.Event{Kind: agent.AttemptFinished, Attempt: agent.Attempt{Number: 1, Error: "503", Response: agent.Response{Usage: openai.Usage{TotalTokens: 2}}}})
		emit(agent.Event{Kind: agent.AttemptFinished, Attempt: agent.Attempt{Number: 2, Response: agent.Response{Usage: openai.Usage{TotalTokens: 3}}}})
		emit(agent.Event{Kind: agent.RunFinished})
		return final(), nil
	}}
	finished := 0
	result, err := runtime.Run(context.Background(), request(), func(e agent.Event) {
		if e.Kind == agent.RunFinished {
			finished++
		}
	})
	if err != nil || len(result.Rounds[0].Attempts) != 2 || result.Usage.TotalTokens != 5 || finished != 1 {
		t.Fatalf("%+v %v terminal=%d", result, err, finished)
	}
}
