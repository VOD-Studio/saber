package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/config"
	appcontext "rua.plus/saber/internal/context"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/mcp"
)

func TestAgentModel_ToolCorrectionAndRetry(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			var requests atomic.Int32
			client, _ := setupMockServer(t, func(w http.ResponseWriter, r *http.Request) {
				var req ChatCompletionRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					http.Error(w, "bad json", 400)
					return
				}
				n := requests.Add(1)
				if req.MaxTokens != 42 || req.Temperature != 0.75 || req.Stream != stream {
					t.Errorf("request settings changed: %+v", req)
				}
				if n == 2 {
					http.Error(w, `{"error":{"message":"retry","type":"server_error"}}`, http.StatusServiceUnavailable)
					return
				}
				var calls []openai.ToolCall
				reason, content := "tool_calls", "checking"
				switch n {
				case 1:
					calls = []openai.ToolCall{{ID: "one", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "lookup", Arguments: `{"limit":-1}`}}}
				case 3:
					if len(req.Messages) != 3 || !strings.Contains(req.Messages[2].Content, "limit must be positive") {
						t.Errorf("missing failure/history duplicated: %+v", req.Messages)
					}
					calls = []openai.ToolCall{{ID: "two", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "lookup", Arguments: `{"limit":1}`}}}
				default:
					if n != 4 || len(req.Messages) != 5 || req.Messages[4].Content != `"answer"` {
						t.Errorf("wrong final history/request: n=%d %+v", n, req.Messages)
					}
					reason, content = "stop", "answer"
				}
				if !stream {
					if err := json.NewEncoder(w).Encode(map[string]any{"model": "gpt-4", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content, "tool_calls": calls}, "finish_reason": reason}}}); err != nil {
						t.Error(err)
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				write := func(delta any, finish any) {
					data, err := json.Marshal(map[string]any{"model": "gpt-4", "choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
					if err != nil {
						t.Error(err)
						return
					}
					if _, err = fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
						t.Error(err)
					}
				}
				write(map[string]any{"content": content}, nil)
				for _, call := range calls {
					args := call.Function.Arguments
					write(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": call.ID, "type": "function", "function": map[string]any{"name": call.Function.Name, "arguments": args[:5]}}}}, nil)
					write(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": args[5:]}}}}, nil)
				}
				write(map[string]any{}, reason)
				if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
					t.Error(err)
				}
			})
			toolCalls := 0
			runtime := agent.Runtime{
				Model: agentModel(func(string) (*Client, error) { return client, nil }, &RetryConfigWrapper{MaxRetries: 1}),
				Execute: func(_ context.Context, _ string, args map[string]any) (agent.ToolOutput, error) {
					toolCalls++
					if args["limit"].(float64) < 0 {
						return agent.ToolOutput{}, errors.New("limit must be positive")
					}
					return agent.ToolOutput{Value: "answer"}, nil
				},
			}
			result, err := runtime.Run(context.Background(), agent.Request{Model: "test", Stream: stream, MaxTokens: 42, Temperature: 0.75, Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "lookup"}}, Tools: []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "lookup"}}}}, nil)
			if err != nil || result.Status != agent.Completed || result.Content != "answer" || toolCalls != 2 || requests.Load() != 4 {
				t.Fatalf("result=%+v err=%v tools=%d requests=%d", result, err, toolCalls, requests.Load())
			}
			if len(result.Rounds[1].Attempts) != 2 || result.Rounds[1].Attempts[0].Error == "" {
				t.Fatal("retry trace missing")
			}
		})
	}
}

func TestAgentModel_StreamOrderAndIncompleteResponse(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprintf("complete_%t", complete), func(t *testing.T) {
			var requests atomic.Int32
			client, _ := setupMockServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				var body string
				if requests.Add(1) == 1 {
					body = "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"second","function":{"name":"lookup","arguments":"{}"}},{"index":0,"id":"first","function":{"name":"lookup","arguments":"{}"}}]}}]}` + "\n\n"
					if complete {
						body += "data: " + `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"
					}
				} else {
					body = "data: " + `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}` + "\n\n"
				}
				body += "data: [DONE]\n\n"
				if _, err := fmt.Fprint(w, body); err != nil {
					t.Error(err)
				}
			})
			var ids []string
			runtime := agent.Runtime{Model: agentModel(func(string) (*Client, error) { return client, nil }, &RetryConfigWrapper{}), Execute: func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
				return agent.ToolOutput{}, nil
			}}
			result, err := runtime.Run(context.Background(), agent.Request{Model: "test", Stream: true, Tools: []openai.Tool{{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "lookup"}}}}, func(e agent.Event) {
				if e.Kind == agent.ToolFinished {
					ids = append(ids, e.Tool.Call.ID)
				}
			})
			if complete {
				if err != nil || !reflect.DeepEqual(ids, []string{"first", "second"}) {
					t.Fatalf("%+v %v ids=%v", result, err, ids)
				}
			} else if err == nil || result.Status != agent.Failed || len(ids) != 0 || requests.Load() != 1 {
				t.Fatalf("incomplete stream executed: %+v %v ids=%v", result, err, ids)
			}
		})
	}
}

func TestAgentModel_CancelRetryAndFallback(t *testing.T) {
	for _, stop := range []string{"attempt_start", "retry_wait", "timeout", "fallback"} {
		t.Run(stop, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var requests atomic.Int32
			client, _ := setupMockServer(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.Error(w, "retry", http.StatusServiceUnavailable)
			})
			getCalls := 0
			runtime := agent.Runtime{Model: agentModel(func(string) (*Client, error) { getCalls++; return client, nil }, &RetryConfigWrapper{MaxRetries: 2, InitialDelay: time.Second, MaxDelay: time.Second, BackoffFactor: 1, FallbackModels: []string{"backup"}})}
			if stop == "timeout" {
				runtime.Limits.Timeout = 20 * time.Millisecond
			}
			if stop == "fallback" {
				runtime.Model = agentModel(func(string) (*Client, error) { getCalls++; return client, nil }, &RetryConfigWrapper{FallbackModels: []string{"backup"}})
			}
			result, err := runtime.Run(ctx, agent.Request{Model: "test"}, func(e agent.Event) {
				if stop == "attempt_start" && e.Kind == agent.AttemptStarted || (stop == "retry_wait" || stop == "fallback") && e.Kind == agent.AttemptFinished {
					cancel()
				}
			})
			want := int32(1)
			if stop == "attempt_start" {
				want = 0
			}
			status := agent.Cancelled
			if stop == "timeout" {
				status = agent.TimedOut
			}
			if err == nil || result.Status != status || requests.Load() != want || getCalls != int(want) {
				t.Fatalf("%+v %v requests=%d client=%d", result, err, requests.Load(), getCalls)
			}
		})
	}
}

func TestService_RunAgentWithoutMatrix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fmt.Fprint(w, `{"model":"local","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	cfg := config.DefaultAIConfig()
	cfg.Enabled = true
	cfg.Provider = "openai"
	cfg.BaseURL = server.URL
	cfg.APIKey = "test"
	cfg.DefaultModel = "local"
	cfg.Context.Enabled = false
	service, err := NewService(&cfg, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunAgent(context.Background(), agent.Request{Model: cfg.DefaultModel}, nil)
	if err != nil || result.Status != agent.Completed || result.Content != "hello" || result.Rounds[0].Response.Model == "" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestService_RunAgentMCPFailure(t *testing.T) {
	sdkServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	sdkServer.AddTool(&mcpsdk.Tool{Name: "lookup", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "lookup denied"}}}, nil
	})
	endpoint := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return sdkServer }, nil))
	defer endpoint.Close()
	manager := mcp.NewManager(&config.MCPConfig{Enabled: true, Servers: map[string]config.ServerConfig{"test": {Enabled: true, Type: "http", URL: endpoint.URL, Token: "test"}}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	}()
	var n atomic.Int32
	_, modelServer := setupMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req agent.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var body string
		if n.Add(1) == 1 {
			body = `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"one","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
		} else {
			last := req.Messages[len(req.Messages)-1]
			if !strings.Contains(last.Content, "lookup denied") || !strings.Contains(last.Content, "tool_failed") {
				t.Errorf("MCP failure lost: %+v", last)
			}
			body = `{"choices":[{"message":{"role":"assistant","content":"permission denied"},"finish_reason":"stop"}]}`
		}
		if _, err := fmt.Fprint(w, body); err != nil {
			t.Error(err)
		}
	})
	cfg := config.DefaultAIConfig()
	cfg.Enabled = true
	cfg.Provider = "openai"
	cfg.BaseURL = modelServer.URL
	cfg.APIKey = "test"
	cfg.DefaultModel = "local"
	cfg.Context.Enabled = false
	service, err := NewService(&cfg, nil, manager, nil)
	if err != nil {
		t.Fatal(err)
	}
	available, _ := service.toolExecutor.PrepareTools()
	result, err := service.RunAgent(appcontext.WithUserContext(ctx, "@test:local", "!test:local"), agent.Request{Model: cfg.DefaultModel, Tools: available}, nil)
	if err != nil || result.Status != agent.Completed || result.Rounds[0].Tools[0].ErrorCode != "tool_failed" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestService_RunAgentReply(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, failureMode := range []string{"none", "all", "preview"} {
			if failureMode == "preview" && !stream {
				continue
			}
			sendFails := failureMode == "all"
			t.Run(fmt.Sprintf("stream_%t_failure_%s", stream, failureMode), func(t *testing.T) {
				var modelRequests atomic.Int32
				model, _ := setupMockServer(t, func(w http.ResponseWriter, r *http.Request) {
					modelRequests.Add(1)
					var body string
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						body = "data: " + `{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
					} else {
						body = `{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
					}
					if _, err := fmt.Fprint(w, body); err != nil {
						t.Error(err)
					}
				})
				var sent atomic.Int32
				matrixServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.URL.Path, "/send/") {
						sent.Add(1)
						if sendFails || failureMode == "preview" && sent.Load() == 1 {
							w.WriteHeader(http.StatusForbidden)
							if _, err := fmt.Fprint(w, `{"errcode":"M_FORBIDDEN","error":"denied"}`); err != nil {
								t.Error(err)
							}
							return
						}
					}
					if _, err := fmt.Fprint(w, `{"event_id":"$test"}`); err != nil {
						t.Error(err)
					}
				}))
				defer matrixServer.Close()
				client, err := mautrix.NewClient(matrixServer.URL, "@bot:local", "test")
				if err != nil {
					t.Fatal(err)
				}
				cfg := config.DefaultAIConfig()
				cfg.Enabled = true
				cfg.Provider = "openai"
				cfg.BaseURL = "http://unused.invalid"
				cfg.APIKey = "test"
				cfg.DefaultModel = "local"
				cfg.StreamEdit.CharThreshold = 1
				service, err := NewService(&cfg, matrix.NewCommandService(client, id.UserID("@bot:local"), &matrix.BuildInfo{}), nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer service.Stop()
				response, err := service.runAgentReply(context.Background(), agent.Request{Model: cfg.DefaultModel, Stream: stream}, "!test:local", model)
				if (err != nil) != sendFails || modelRequests.Load() != 1 || sent.Load() == 0 {
					t.Fatalf("response=%+v err=%v requests=%d sent=%d", response, err, modelRequests.Load(), sent.Load())
				}
				history := service.contextManager.GetContext("!test:local")
				if len(history) != 1 || history[0].Content != "hello" {
					t.Fatalf("answer not saved exactly once: %+v", history)
				}
			})
		}
	}
}
