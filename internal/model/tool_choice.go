package model

import (
	"encoding/json"
	"errors"
)

func modelToolChoice(raw any) (mode, name string, err error) {
	if raw == nil {
		return "", "", nil
	}
	switch value := raw.(type) {
	case string:
		switch value {
		case "none", "auto", "required":
			return value, "", nil
		default:
			return "", "", errors.New("tool_choice string must be none, auto or required")
		}
	case map[string]any:
		if value["type"] != "function" {
			return "", "", errors.New("tool_choice object requires type=function")
		}
		if function, ok := value["function"].(map[string]any); ok {
			name, _ = function["name"].(string)
		} else {
			name, _ = value["name"].(string)
		}
		if name == "" {
			return "", "", errors.New("named tool_choice requires a function name")
		}
		return "named", name, nil
	default:
		data, err := json.Marshal(raw)
		if err != nil {
			return "", "", err
		}
		var normalized any
		if err = json.Unmarshal(data, &normalized); err != nil {
			return "", "", err
		}
		if normalized == nil {
			return "", "", nil
		}
		if _, ok := normalized.(map[string]any); !ok {
			if _, ok := normalized.(string); !ok {
				return "", "", errors.New("invalid tool_choice JSON")
			}
		}
		return modelToolChoice(normalized)
	}
}

func completionToolChoice(raw any) (any, error) {
	mode, name, err := modelToolChoice(raw)
	if err != nil {
		return nil, err
	}
	if mode == "" {
		return nil, nil
	}
	if mode == "named" {
		return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
	}
	return mode, nil
}

func responsesToolChoice(raw any) (any, error) {
	mode, name, err := modelToolChoice(raw)
	if err != nil {
		return nil, err
	}
	if mode == "" {
		return nil, nil
	}
	if mode == "named" {
		return map[string]any{"type": "function", "name": name}, nil
	}
	return mode, nil
}
