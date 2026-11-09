package discovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

func discoveryContext() context.Context {
	return chat.WithIdentity(context.Background(), chat.Identity{Session: chat.Session{Platform: "test", Account: "account", Conversation: "room"}, SenderID: "user", Direct: true})
}
func writeDefinition() toolkit.Definition {
	return toolkit.Definition{Tool: openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "write_document", Description: "Write an authorized document", Parameters: map[string]any{"type": "object", "properties": map[string]any{"content": map[string]any{"type": "string"}}}}}, Permission: "write_document", Requires: []string{"write_file"}}
}

func TestDiscoveryRechecksRevokedGrantBeforeRealWrite(t *testing.T) {
	allowed := true
	path := filepath.Join(t.TempDir(), "document.txt")
	calls := 0
	host := toolkit.Host{Check: func(context.Context, string, bool) error {
		if !allowed {
			return errors.New("revoked")
		}
		return nil
	}, Call: func(ctx context.Context, name string, args map[string]any) (any, error) {
		calls++
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, os.WriteFile(path, []byte(args["content"].(string)), 0600)
	}}
	service, err := New(config.DefaultAgentToolsConfig(), host, func(context.Context) []toolkit.Definition { return []toolkit.Definition{writeDefinition()} })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(discoveryContext(), "tool_search", map[string]any{"query": "document"}); err != nil {
		t.Fatal(err)
	}
	allowed = false
	if _, err := service.Execute(discoveryContext(), "tool_call", map[string]any{"name": "write_document", "arguments": map[string]any{"content": "denied"}}); err == nil || calls != 0 {
		t.Fatal("revoked tool was dispatched")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("denied operation wrote file")
	}
	allowed = true
	if _, err := service.Execute(discoveryContext(), "tool_call", map[string]any{"name": "write_document", "arguments": map[string]any{"content": "approved"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "approved" {
		t.Fatalf("real authorized dispatch failed: %s %v", raw, err)
	}
}

func TestDiscoveryChildBridgeCannotExpandScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.txt")
	host := toolkit.Host{Check: func(context.Context, string, bool) error { return nil }, Call: func(context.Context, string, map[string]any) (any, error) {
		return nil, os.WriteFile(path, []byte("escaped"), 0600)
	}}
	service, err := New(config.DefaultAgentToolsConfig(), host, func(context.Context) []toolkit.Definition { return []toolkit.Definition{writeDefinition()} })
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.WithToolScope(discoveryContext(), []string{"tool_call", "tool_search"})
	if _, err := service.Execute(ctx, "tool_call", map[string]any{"name": "write_document", "arguments": map[string]any{}}); err == nil {
		t.Fatal("bridge expanded child scope")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("scope denied write occurred")
	}
}

func TestDiscoveryCancellationAndRecursiveCallAreRejected(t *testing.T) {
	calls := 0
	service, err := New(config.DefaultAgentToolsConfig(), toolkit.Host{Call: func(context.Context, string, map[string]any) (any, error) { calls++; return nil, nil }}, func(context.Context) []toolkit.Definition { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(discoveryContext())
	cancel()
	if _, err := service.Execute(ctx, "tool_call", map[string]any{"name": "write_document", "arguments": map[string]any{}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := service.Execute(discoveryContext(), "tool_call", map[string]any{"name": "tool_search", "arguments": map[string]any{}}); err == nil || calls != 0 {
		t.Fatal("recursive bridge executed")
	}
}
