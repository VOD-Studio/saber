//go:build goolm

package mcp

import (
	"testing"
	"time"

	"maunium.net/go/mautrix/id"
)

func TestSetDebugMode(t *testing.T) {
	originalMode := debugMode
	defer func() { debugMode = originalMode }()

	SetDebugMode(true)
	if !debugMode {
		t.Error("SetDebugMode(true) should set debugMode to true")
	}

	SetDebugMode(false)
	if debugMode {
		t.Error("SetDebugMode(false) should set debugMode to false")
	}
}

func TestLogToolCall(t *testing.T) {
	LogToolCall(
		"test_server",
		"test_tool",
		id.UserID("@user:example.com"),
		id.RoomID("!room:example.com"),
		map[string]any{"url": "https://example.com"},
	)
}

func TestLogToolCall_NilArgs(t *testing.T) {
	LogToolCall(
		"test_server",
		"test_tool",
		id.UserID("@user:example.com"),
		id.RoomID("!room:example.com"),
		nil,
	)
}

func TestLogToolResult_Success(t *testing.T) {
	LogToolResult("test_server", "test_tool", 100*time.Millisecond, nil)
}

func TestLogToolResult_Error(t *testing.T) {
	LogToolResult("test_server", "test_tool", 100*time.Millisecond, assertError{})
}

type assertError struct{}

func (assertError) Error() string { return "test error" }

func TestSanitizeArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]any
		expected map[string]any
	}{
		{
			name:     "空参数",
			args:     nil,
			expected: map[string]any{},
		},
		{
			name:     "空参数映射",
			args:     map[string]any{},
			expected: map[string]any{},
		},
		{
			name:     "无敏感字段",
			args:     map[string]any{"url": "https://example.com", "count": 10},
			expected: map[string]any{"url": "https://example.com", "count": 10},
		},
		{
			name:     "password 字段",
			args:     map[string]any{"password": "secret123"},
			expected: map[string]any{"password": "[REDACTED]"},
		},
		{
			name:     "token 字段",
			args:     map[string]any{"token": "abc123"},
			expected: map[string]any{"token": "[REDACTED]"},
		},
		{
			name:     "api_key 字段",
			args:     map[string]any{"api_key": "sk-123"},
			expected: map[string]any{"api_key": "[REDACTED]"},
		},
		{
			name:     "apikey 字段",
			args:     map[string]any{"apikey": "sk-456"},
			expected: map[string]any{"apikey": "[REDACTED]"},
		},
		{
			name:     "secret 字段",
			args:     map[string]any{"secret": "mysecret"},
			expected: map[string]any{"secret": "[REDACTED]"},
		},
		{
			name:     "authorization 字段",
			args:     map[string]any{"authorization": "Bearer token"},
			expected: map[string]any{"authorization": "[REDACTED]"},
		},
		{
			name:     "auth 字段",
			args:     map[string]any{"auth": "credentials"},
			expected: map[string]any{"auth": "[REDACTED]"},
		},
		{
			name:     "credential 字段",
			args:     map[string]any{"credential": "creds"},
			expected: map[string]any{"credential": "[REDACTED]"},
		},
		{
			name:     "private_key 字段",
			args:     map[string]any{"private_key": "key123"},
			expected: map[string]any{"private_key": "[REDACTED]"},
		},
		{
			name:     "privatekey 字段",
			args:     map[string]any{"privatekey": "key456"},
			expected: map[string]any{"privatekey": "[REDACTED]"},
		},
		{
			name:     "access_token 字段",
			args:     map[string]any{"access_token": "token123"},
			expected: map[string]any{"access_token": "[REDACTED]"},
		},
		{
			name:     "refresh_token 字段",
			args:     map[string]any{"refresh_token": "refresh123"},
			expected: map[string]any{"refresh_token": "[REDACTED]"},
		},
		{
			name: "混合敏感和非敏感字段",
			args: map[string]any{
				"url":      "https://example.com",
				"api_key":  "sk-123",
				"method":   "GET",
				"password": "secret",
			},
			expected: map[string]any{
				"url":      "https://example.com",
				"api_key":  "[REDACTED]",
				"method":   "GET",
				"password": "[REDACTED]",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeArgs(tt.args)

			if tt.args == nil {
				if len(result) != 0 {
					t.Errorf("sanitizeArgs(nil) = %v, want empty map", result)
				}
				return
			}

			for key, expectedVal := range tt.expected {
				actualVal, ok := result[key]
				if !ok {
					t.Errorf("Missing key %q in result", key)
					continue
				}
				if actualVal != expectedVal {
					t.Errorf("sanitizeArgs()[%q] = %v, want %v", key, actualVal, expectedVal)
				}
			}
		})
	}
}

func TestSanitizeArgs_PreservesOriginal(t *testing.T) {
	original := map[string]any{
		"password": "secret",
		"url":      "https://example.com",
	}

	result := sanitizeArgs(original)

	if original["password"] != "secret" {
		t.Error("sanitizeArgs should not modify original map")
	}
	if result["password"] != "[REDACTED]" {
		t.Error("Result should have redacted password")
	}
}
