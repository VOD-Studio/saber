//go:build goolm

package mcp

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sashabaranov/go-openai"
)

func TestConvertToOpenAITools(t *testing.T) {
	tests := []struct {
		name          string
		tools         []mcp.Tool
		expectedCount int
	}{
		{
			name:          "空工具列表",
			tools:         []mcp.Tool{},
			expectedCount: 0,
		},
		{
			name: "单个工具",
			tools: []mcp.Tool{
				{
					Name:        "test_tool",
					Description: "A test tool",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"url": map[string]any{"type": "string"},
						},
					},
				},
			},
			expectedCount: 1,
		},
		{
			name: "多个工具",
			tools: []mcp.Tool{
				{
					Name:        "tool1",
					Description: "First tool",
					InputSchema: map[string]any{"type": "object"},
				},
				{
					Name:        "tool2",
					Description: "Second tool",
					InputSchema: map[string]any{"type": "object"},
				},
			},
			expectedCount: 2,
		},
		{
			name: "Schema 为空的工具",
			tools: []mcp.Tool{
				{
					Name:        "nil_schema_tool",
					Description: "Tool with nil schema",
					InputSchema: nil,
				},
			},
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := make(map[string]ToolMeta)
			result := ConvertToOpenAITools(tt.tools, meta)

			if len(result) != tt.expectedCount {
				t.Errorf("ConvertToOpenAITools() returned %d tools, want %d", len(result), tt.expectedCount)
			}

			for i, tool := range result {
				if tool.Type != openai.ToolTypeFunction {
					t.Errorf("Tool %d type = %v, want %v", i, tool.Type, openai.ToolTypeFunction)
				}
				if tool.Function == nil {
					t.Errorf("Tool %d Function is nil", i)
					continue
				}
				if tool.Function.Name != tt.tools[i].Name {
					t.Errorf("Tool %d name = %q, want %q", i, tool.Function.Name, tt.tools[i].Name)
				}
				if tool.Function.Description != tt.tools[i].Description {
					t.Errorf("Tool %d description = %q, want %q", i, tool.Function.Description, tt.tools[i].Description)
				}
			}

			if len(meta) != tt.expectedCount {
				t.Errorf("Meta map has %d entries, want %d", len(meta), tt.expectedCount)
			}
		})
	}
}

func TestConvertJSONSchema(t *testing.T) {
	tests := []struct {
		name         string
		schema       any
		expectObject bool
	}{
		{
			name:         "空 Schema",
			schema:       nil,
			expectObject: true,
		},
		{
			name: "简单对象 Schema",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string"},
				},
			},
			expectObject: false,
		},
		{
			name: "带必填字段的 Schema",
			schema: map[string]any{
				"type":     "object",
				"required": []string{"url"},
			},
			expectObject: false,
		},
		{
			name: "复杂嵌套 Schema",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"config": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"enabled": map[string]any{"type": "boolean"},
						},
					},
				},
			},
			expectObject: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertJSONSchema(tt.schema)

			if len(result) == 0 {
				t.Error("convertJSONSchema returned empty result")
			}

			if tt.expectObject {
				var parsed map[string]any
				if err := json.Unmarshal(result, &parsed); err != nil {
					t.Errorf("Failed to parse result as JSON: %v", err)
				}
				if parsed["type"] != "object" {
					t.Errorf("Expected type 'object', got %v", parsed["type"])
				}
			}
		})
	}
}

func TestToolMeta(t *testing.T) {
	meta := ToolMeta{
		ServerName: "test_server",
		ToolName:   "test_tool",
	}

	if meta.ServerName != "test_server" {
		t.Errorf("ServerName = %q, want %q", meta.ServerName, "test_server")
	}
	if meta.ToolName != "test_tool" {
		t.Errorf("ToolName = %q, want %q", meta.ToolName, "test_tool")
	}
}

func TestConvertToOpenAITools_MetaPopulation(t *testing.T) {
	tools := []mcp.Tool{
		{Name: "tool_a", Description: "Tool A", InputSchema: map[string]any{"type": "object"}},
		{Name: "tool_b", Description: "Tool B", InputSchema: map[string]any{"type": "object"}},
	}

	meta := make(map[string]ToolMeta)
	ConvertToOpenAITools(tools, meta)

	for _, tool := range tools {
		m, exists := meta[tool.Name]
		if !exists {
			t.Errorf("Meta for tool %q not found", tool.Name)
			continue
		}
		if m.ToolName != tool.Name {
			t.Errorf("Meta ToolName = %q, want %q", m.ToolName, tool.Name)
		}
	}
}
