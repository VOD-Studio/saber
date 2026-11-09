package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func toolObject(value any) (map[string]any, error) {
	var encoded []byte
	var err error
	if raw, ok := value.(string); ok {
		encoded = []byte(raw)
	} else {
		encoded, err = json.Marshal(value)
	}
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err = json.Unmarshal(encoded, &out); err != nil {
		return nil, err
	}
	if inner, ok := out["data"].(map[string]any); ok {
		if _, hasSummary := out["summary"]; !hasSummary {
			for k, v := range inner {
				out[k] = v
			}
		}
	}
	return out, nil
}
func (s *Service) toolText(ctx context.Context, value any, full bool, maxBytes int) (string, error) {
	object, err := toolObject(value)
	if err != nil {
		return "", err
	}
	output := ""
	for _, key := range []string{"summary", "output", "stdout"} {
		if text, ok := object[key].(string); ok {
			output = text
		}
	}
	id, _ := object["output_id"].(string)
	if !full || id == "" {
		if len(output) > maxBytes {
			return "", fmt.Errorf("tool output exceeds %d bytes", maxBytes)
		}
		return output, nil
	}
	var collected strings.Builder
	offset := int64(0)
	for {
		page, err := s.host.Call(ctx, "read_output", map[string]any{"output_id": id, "offset": offset, "limit": 8192})
		if err != nil {
			return "", err
		}
		data, err := toolObject(page)
		if err != nil {
			return "", err
		}
		if inner, ok := data["data"].(map[string]any); ok {
			data = inner
		}
		text, ok := data["text"].(string)
		if !ok {
			return "", errors.New("output archive missing text")
		}
		if collected.Len()+len(text) > maxBytes {
			return "", fmt.Errorf("output archive %s exceeds %d bytes; preserved in task logs", id, maxBytes)
		}
		collected.WriteString(text)
		eof, _ := data["eof"].(bool)
		if eof {
			return collected.String(), nil
		}
		next := number(data, "next_offset")
		if next <= offset {
			return "", errors.New("output archive did not advance")
		}
		offset = next
	}
}

func reportedToolError(value any) error {
	if value == nil {
		return errors.New("delivery tool returned no acknowledgement")
	}
	object, err := toolObject(value)
	if err != nil {
		if _, ok := value.(string); ok {
			return nil
		}
		return err
	}
	for _, key := range []string{"exit_code", "code"} {
		if code, ok := object[key].(float64); ok && code != 0 {
			return fmt.Errorf("delivery tool returned %s=%v", key, code)
		}
	}
	if success, ok := object["success"].(bool); ok && !success {
		return errors.New("delivery tool reported success=false")
	}
	if failed, ok := object["is_error"].(bool); ok && failed {
		return errors.New("delivery tool reported is_error=true")
	}
	if failure := object["error"]; failure != nil && failure != "" {
		return fmt.Errorf("delivery tool error: %v", failure)
	}
	if inner, ok := object["data"].(map[string]any); ok {
		return reportedToolError(inner)
	}
	return nil
}
