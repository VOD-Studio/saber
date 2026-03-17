package mcp

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sashabaranov/go-openai"
)

type ToolMeta struct {
	ServerName string
	ToolName   string
}

func ConvertToOpenAITools(tools []mcp.Tool, meta map[string]ToolMeta) []openai.Tool {
	result := make([]openai.Tool, 0, len(tools))
	for _, tool := range tools {
		result = append(result, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  convertJSONSchema(tool.InputSchema),
			},
		})
		meta[tool.Name] = ToolMeta{ToolName: tool.Name}
	}
	return result
}

func convertJSONSchema(schema any) json.RawMessage {
	if schema == nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	return data
}
