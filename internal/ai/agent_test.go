package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/config"
	appcontext "rua.plus/saber/internal/context"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/mcp"
)

func TestService_RunAgentWithoutMatrix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fmt.Fprint(w, `{"model":"local","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: server.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.Context.Enabled = false
	service, err := NewService(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunAgent(context.Background(), agent.Request{Model: cfg.AI.DefaultModel}, nil)
	if err != nil || result.Status != agent.Completed || result.Content != "hello" || result.Rounds[0].Response.Model == "" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestService_RunAgentMCPFailure(t *testing.T) {
	var executed atomic.Int32
	sdkServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	sdkServer.AddTool(&mcpsdk.Tool{Name: "lookup", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		executed.Add(1)
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
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: modelServer.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.Context.Enabled = false
	service, err := NewService(&cfg, WithMCP(manager))
	if err != nil {
		t.Fatal(err)
	}
	taskCtx := appcontext.WithUserContext(ctx, "@test:local", "!test:local")
	// 工具默认隐藏，直接调用 Manager 也不能绕过授权。
	if available, _ := service.toolExecutor.PrepareTools(taskCtx); len(available) != 0 {
		t.Fatal("ungranted tools exposed")
	}
	if _, err := manager.CallTool(taskCtx, "test", "lookup", nil); err == nil {
		t.Fatal("ungranted MCP call accepted")
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service.executor, err = execution.New(config.ExecutionConfig{Enabled: true, LogDir: t.TempDir(), Workspaces: map[string]config.WorkspaceConfig{"test": {Path: workspace}}, Grants: []config.ExecutionGrant{{Platform: "matrix", Account: "legacy", Room: "!test:local", Users: []string{"@test:local"}, Workspace: "test", Tools: []string{"mcp:test:lookup"}, Capabilities: []string{"external"}}}, MCPRequirements: map[string][]string{"mcp:test:lookup": {}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetAuthorizer(service.executor.CheckMCP)
	taskCtx = execution.WithTask(taskCtx, 1, workspace)
	available, _ := service.toolExecutor.PrepareTools(taskCtx)
	result, err := service.RunAgent(taskCtx, agent.Request{Model: cfg.AI.DefaultModel, Tools: available}, nil)
	if err != nil || result.Status != agent.Completed || result.Rounds[0].Tools[0].ErrorCode != "tool_failed" {
		t.Fatalf("%+v %v", result, err)
	}
	identity, _ := chat.IdentityFromContext(taskCtx)
	identity.SenderID = "@mallory:local"
	forgedCtx := chat.WithIdentity(taskCtx, identity)
	if _, err := manager.CallTool(forgedCtx, "test", "lookup", map[string]any{"sender_id": "@test:local", "capabilities": []string{"external"}}); err == nil {
		t.Fatal("forged parameters bypassed MCP permission")
	}
	if executed.Load() != 1 {
		t.Fatal("denied MCP call reached external server")
	}
}

func setupMockServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(func() { server.Close() })

	cfg := &config.ModelConfig{
		Model:    "gpt-4",
		Provider: "openai",
		BaseURL:  server.URL,
		APIKey:   "test-key",
	}

	client, err := NewClientWithModel(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	return client, server
}

func TestService_HandleChat_MemoryWithoutMatrix(t *testing.T) {
	var n atomic.Int32
	_, server := setupMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		count := n.Add(1)
		want := int(count) * 2
		if len(req.Messages) != want || req.Messages[0].Content != "base prompt" {
			t.Errorf("wrong shared pipeline history: %+v", req.Messages)
		}
		if _, err := fmt.Fprint(w, `{"model":"local","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`); err != nil {
			t.Error(err)
		}
	})
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: server.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.StreamEnabled = false
	cfg.AI.SystemPrompt = "base prompt"
	service, err := NewService(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	adapter := memory.New("account", chat.Capabilities{}, service.HandleChat)
	for _, text := range []string{"first", "second"} {
		if result, err := adapter.Receive(context.Background(), chat.Message{Session: chat.Session{Conversation: "room"}, SenderID: "42", Text: text}); err != nil || result.Status != agent.Completed {
			t.Fatalf("%+v %v", result, err)
		}
	}
	if len(adapter.Replies()) != 2 {
		t.Fatal("reply missing")
	}
}
