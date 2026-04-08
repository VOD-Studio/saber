//go:build goolm

package mcp

import (
	"testing"
)

func TestValidationError(t *testing.T) {
	tests := []struct {
		name     string
		field    string
		message  string
		expected string
	}{
		{
			name:     "basic error",
			field:    "url",
			message:  "is required",
			expected: "url: is required",
		},
		{
			name:     "empty field",
			field:    "",
			message:  "invalid format",
			expected: ": invalid format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &ValidationError{
				Field:   tt.field,
				Message: tt.message,
			}
			if err.Error() != tt.expected {
				t.Errorf("Error() = %q, want %q", err.Error(), tt.expected)
			}
		})
	}
}

func TestValidateToolInput(t *testing.T) {
	tests := []struct {
		name    string
		params  map[string]any
		schema  map[string]any
		wantErr bool
	}{
		{
			name:    "nil schema",
			params:  map[string]any{"url": "https://example.com"},
			schema:  nil,
			wantErr: false,
		},
		{
			name:    "empty schema",
			params:  map[string]any{"url": "https://example.com"},
			schema:  map[string]any{},
			wantErr: false,
		},
		{
			name: "所有必填字段存在",
			params: map[string]any{
				"url":    "https://example.com",
				"method": "GET",
			},
			schema: map[string]any{
				"required": []interface{}{"url", "method"},
			},
			wantErr: false,
		},
		{
			name: "缺少必填字段",
			params: map[string]any{
				"method": "GET",
			},
			schema: map[string]any{
				"required": []interface{}{"url", "method"},
			},
			wantErr: true,
		},
		{
			name: "Schema 无必填字段",
			params: map[string]any{
				"optional": "value",
			},
			schema: map[string]any{
				"type": "object",
			},
			wantErr: false,
		},
		{
			name: "必填字段 Schema 类型错误",
			params: map[string]any{
				"url": "https://example.com",
			},
			schema: map[string]any{
				"required": "not an array", // Invalid type
			},
			wantErr: false, // 无效 schema 不应报错
		},
		{
			name: "必填字段包含非字符串项",
			params: map[string]any{
				"url": "https://example.com",
			},
			schema: map[string]any{
				"required": []interface{}{123, true}, // Non-string items
			},
			wantErr: false, // 应跳过非字符串项
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateToolInput(tt.params, tt.schema)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateToolInput() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantErr && err != nil {
				// 验证返回的是 ValidationError
				if _, ok := err.(*ValidationError); !ok {
					t.Errorf("Expected ValidationError, got %T", err)
				}
			}
		})
	}
}

func TestValidationError_Field(t *testing.T) {
	err := &ValidationError{
		Field:   "api_key",
		Message: "required field is missing",
	}

	if err.Field != "api_key" {
		t.Errorf("Field = %q, want %q", err.Field, "api_key")
	}
	if err.Message != "required field is missing" {
		t.Errorf("Message = %q, want %q", err.Message, "required field is missing")
	}
}
