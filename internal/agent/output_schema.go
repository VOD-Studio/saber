package agent

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// ResolveOutputSchema 校验子任务输出契约；不配置 Loader，远程引用无法发起网络请求。
func ResolveOutputSchema(schema map[string]any) (*jsonschema.Resolved, error) {
	if schema == nil {
		return nil, nil
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	if len(data) > 64*1024 {
		return nil, fmt.Errorf("output_schema 超过 64 KiB")
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	resolved, err := parsed.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("output_schema 无效: %w", err)
	}
	return resolved, nil
}

func validateOutput(schema *jsonschema.Resolved, text string) error {
	if schema == nil {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return fmt.Errorf("最终输出不是 JSON: %w", err)
	}
	return schema.Validate(value)
}
