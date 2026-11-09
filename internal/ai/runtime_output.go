package ai

import (
	"encoding/base64"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/toolkit"
)

func runtimeToolOutput(value any) agent.ToolOutput {
	output := agent.ToolOutput{Value: value}
	switch result := value.(type) {
	case toolkit.Result:
		output = featureToolOutput(result)
	case *toolkit.Result:
		if result != nil {
			output = featureToolOutput(*result)
		}
	case execution.Result:
		output.IsError, output.OutputID, output.Structured = result.ExitCode != 0 || result.Error != "", result.OutputID, true
	case *mcpsdk.CallToolResult:
		if result == nil {
			break
		}
		output.IsError = result.IsError
		content := make([]mcpsdk.Content, 0, len(result.Content))
		for _, item := range result.Content {
			if image, ok := item.(*mcpsdk.ImageContent); ok && image != nil && strings.HasPrefix(image.MIMEType, "image/") {
				output.Images = append(output.Images, "data:"+image.MIMEType+";base64,"+base64.StdEncoding.EncodeToString(image.Data))
			} else {
				content = append(content, item)
			}
		}
		output.Value = map[string]any{"content": content, "structuredContent": result.StructuredContent}
	}
	output.Value, _ = modelMediaValue(output.Value)
	return output
}

func featureToolOutput(result toolkit.Result) agent.ToolOutput {
	data, _ := modelMediaValue(result.Data)
	return agent.ToolOutput{Value: struct {
		Data     any      `json:"data,omitempty"`
		Files    []string `json:"files,omitempty"`
		OutputID string   `json:"output_id,omitempty"`
	}{data, result.Files, result.OutputID}, Images: result.Images, Files: result.Files, IsError: result.IsError, OutputID: result.OutputID}
}
