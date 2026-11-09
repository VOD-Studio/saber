package acp

import (
	"encoding/json"
	"strings"

	"rua.plus/saber/internal/agent"
)

func toolContent(record agent.ToolRecord) []any {
	content := []any{map[string]any{"type": "content", "content": contentBlock{Type: "text", Text: record.Content}}}
	for _, image := range record.Images {
		if strings.HasPrefix(image, "data:image/") {
			meta, data, ok := strings.Cut(image, ",")
			if ok && strings.HasSuffix(meta, ";base64") {
				mime := strings.TrimSuffix(strings.TrimPrefix(meta, "data:"), ";base64")
				content = append(content, map[string]any{"type": "content", "content": contentBlock{Type: "image", MIMEType: mime, Data: data}})
			}
		} else {
			content = append(content, map[string]any{"type": "content", "content": map[string]any{"type": "resource_link", "uri": image, "name": "Tool image"}})
		}
	}
	var result struct {
		Edits []struct {
			Path      string `json:"path"`
			OldText   string `json:"old_text"`
			NewText   string `json:"new_text"`
			Truncated bool   `json:"truncated"`
		} `json:"edits"`
	}
	if record.ErrorCode == "" && json.Unmarshal([]byte(record.Content), &result) == nil {
		for _, diff := range result.Edits {
			if diff.Path != "" && !diff.Truncated {
				content = append(content, map[string]any{"type": "diff", "path": diff.Path, "oldText": diff.OldText, "newText": diff.NewText})
			}
		}
	}
	return content
}
