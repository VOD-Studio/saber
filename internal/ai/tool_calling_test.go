// Package ai_test contains tool calling integration tests.
package ai

import (
	"testing"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/mcp"
)

func TestNewService_WithMCPManager(t *testing.T) {
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "test-key"}}
	cfg.AI.DefaultModel = "openai.gpt-4"

	mcpCfg := &config.MCPConfig{
		Enabled: false,
		Servers: make(map[string]config.ServerConfig),
	}
	mcpManager := mcp.NewManager(mcpCfg)

	service, err := NewService(&cfg, WithMCP(mcpManager))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if service == nil {
		t.Fatal("service is nil")
		return
	}
	if service.mcpManager != mcpManager {
		t.Error("mcpManager not stored correctly")
	}
}
