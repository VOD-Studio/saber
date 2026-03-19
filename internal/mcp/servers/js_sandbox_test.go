// Package servers 测试 JavaScript 沙箱服务器功能。
package servers

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"rua.plus/saber/internal/config"
)

func TestJSSandboxBasicExecution(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		wantErr  bool
		checkRes func(t *testing.T, output JSOutput)
	}{
		{
			name:    "simple arithmetic",
			code:    "1 + 1",
			wantErr: false,
			checkRes: func(t *testing.T, output JSOutput) {
				if output.Result != "2" {
					t.Errorf("expected 2, got %s", output.Result)
				}
			},
		},
		{
			name:    "string concatenation",
			code:    "'hello' + ' ' + 'world'",
			wantErr: false,
			checkRes: func(t *testing.T, output JSOutput) {
				if output.Result != "hello world" {
					t.Errorf("expected 'hello world', got %s", output.Result)
				}
			},
		},
		{
			name:    "array literal",
			code:    "[1, 2, 3]",
			wantErr: false,
			checkRes: func(t *testing.T, output JSOutput) {
				if output.Result != "[ 1, 2, 3 ]" {
					t.Errorf("expected '[ 1, 2, 3 ]', got %s", output.Result)
				}
			},
		},
		{
			name:    "object literal",
			code:    "({a: 1, b: 'test'})",
			wantErr: false,
			checkRes: func(t *testing.T, output JSOutput) {
				if output.Result == "" || output.Result == "undefined" {
					t.Errorf("expected non-empty object, got %s", output.Result)
				}
			},
		},
		{
			name:    "function definition and call",
			code:    "(function add(a, b) { return a + b; })(2, 3)",
			wantErr: false,
			checkRes: func(t *testing.T, output JSOutput) {
				if output.Result != "5" {
					t.Errorf("expected 5, got %s", output.Result)
				}
			},
		},
		{
			name:    "variable declaration",
			code:    "let x = 10; let y = 20; x + y",
			wantErr: false,
			checkRes: func(t *testing.T, output JSOutput) {
				if output.Result != "30" {
					t.Errorf("expected 30, got %s", output.Result)
				}
			},
		},
		{
			name:    "boolean result",
			code:    "true && false",
			wantErr: false,
			checkRes: func(t *testing.T, output JSOutput) {
				if output.Result != "false" {
					t.Errorf("expected false, got %s", output.Result)
				}
			},
		},
	}

	cfg := &config.JSSandboxConfig{
		Enabled:         true,
		TimeoutMs:       5000,
		MaxMemoryMB:     64,
		MaxOutputLength: 10000,
	}

	server := NewJSSandboxServerWithConfig(cfg)
	_, session, err := createTestClient(server)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer func() { _ = session.Close() }()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "run_js",
				Arguments: map[string]interface{}{
					"code": tt.code,
				},
			})

			if err != nil {
				t.Errorf("CallTool() unexpected error: %v", err)
				return
			}

			if result.StructuredContent == nil {
				t.Errorf("expected structured content in result")
				return
			}

			if tt.checkRes != nil {
				output, ok := result.StructuredContent.(map[string]interface{})
				if !ok {
					t.Errorf("expected map[string]interface{}, got %T", result.StructuredContent)
					return
				}
				jsOutput := JSOutput{
					Result: getString(output, "result"),
					Stdout: getString(output, "stdout"),
					Error:  getString(output, "error"),
				}
				tt.checkRes(t, jsOutput)
			}
		})
	}
}

func TestJSSandboxConsole(t *testing.T) {
	cfg := &config.JSSandboxConfig{
		Enabled:         true,
		TimeoutMs:       5000,
		MaxMemoryMB:     64,
		MaxOutputLength: 10000,
	}

	server := NewJSSandboxServerWithConfig(cfg)
	_, session, err := createTestClient(server)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer func() { _ = session.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "run_js",
		Arguments: map[string]interface{}{
			"code": `console.log("test output"); console.log(42);`,
		},
	})

	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}

	if result.StructuredContent == nil {
		t.Fatalf("expected structured content in result")
	}

	output, ok := result.StructuredContent.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result.StructuredContent)
	}

	stdout := getString(output, "stdout")
	if stdout == "" {
		t.Errorf("expected stdout output, got empty")
	}
	t.Logf("Stdout: %s", stdout)
}

func TestJSSandboxErrorHandling(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{
			name: "syntax error",
			code: "function {",
		},
		{
			name: "runtime error",
			code: "throw new Error('test error')",
		},
		{
			name: "undefined variable",
			code: "undefinedVariable.foo",
		},
		{
			name: "type error",
			code: "null.foo",
		},
	}

	cfg := &config.JSSandboxConfig{
		Enabled:         true,
		TimeoutMs:       1000,
		MaxMemoryMB:     64,
		MaxOutputLength: 10000,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewJSSandboxServerWithConfig(cfg)
			_, session, err := createTestClient(server)
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			defer func() { _ = session.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "run_js",
				Arguments: map[string]interface{}{
					"code": tt.code,
				},
			})

			if err != nil {
				return
			}

			if !result.IsError {
				t.Errorf("expected IsError to be true for error case")
			}
		})
	}
}

func TestJSSandboxSecurity(t *testing.T) {
	tests := []struct {
		name        string
		code        string
		shouldBlock bool
	}{
		{
			name:        "eval should be blocked",
			code:        "eval('1+1')",
			shouldBlock: true,
		},
		{
			name:        "Function constructor should be blocked",
			code:        "new Function('return 1+1')()",
			shouldBlock: true,
		},
		{
			name:        "require should be blocked",
			code:        "require('fs')",
			shouldBlock: true,
		},
		{
			name:        "normal code should work",
			code:        "Math.max(1, 2, 3)",
			shouldBlock: false,
		},
		{
			name:        "Array methods should work",
			code:        "[1,2,3].map(x => x * 2)",
			shouldBlock: false,
		},
		{
			name:        "Object methods should work",
			code:        "Object.keys({a: 1, b: 2})",
			shouldBlock: false,
		},
	}

	cfg := &config.JSSandboxConfig{
		Enabled:         true,
		TimeoutMs:       5000,
		MaxMemoryMB:     64,
		MaxOutputLength: 10000,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewJSSandboxServerWithConfig(cfg)
			_, session, err := createTestClient(server)
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			defer func() { _ = session.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name: "run_js",
				Arguments: map[string]interface{}{
					"code": tt.code,
				},
			})

			if tt.shouldBlock {
				if err != nil {
					return
				}
				if !result.IsError {
					t.Errorf("expected IsError to be true for blocked operation")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestJSSandboxTimeout(t *testing.T) {
	cfg := &config.JSSandboxConfig{
		Enabled:         true,
		TimeoutMs:       100,
		MaxMemoryMB:     64,
		MaxOutputLength: 10000,
	}

	server := NewJSSandboxServerWithConfig(cfg)
	_, session, err := createTestClient(server)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer func() { _ = session.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "run_js",
		Arguments: map[string]interface{}{
			"code": "while(true) {}",
		},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Errorf("expected IsError to be true for timeout")
	}
}

func TestJSSandboxOutputLimit(t *testing.T) {
	cfg := &config.JSSandboxConfig{
		Enabled:         true,
		TimeoutMs:       5000,
		MaxMemoryMB:     64,
		MaxOutputLength: 100,
	}

	server := NewJSSandboxServerWithConfig(cfg)
	_, session, err := createTestClient(server)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer func() { _ = session.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	longStr := ""
	for i := 0; i < 200; i++ {
		longStr += "x"
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "run_js",
		Arguments: map[string]interface{}{
			"code": `console.log("` + longStr + `");`,
		},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.StructuredContent == nil {
		t.Fatalf("expected structured content in result")
	}

	output, ok := result.StructuredContent.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result.StructuredContent)
	}

	stdout := getString(output, "stdout")
	if len(stdout) > 150 {
		t.Errorf("output should be truncated, got length %d: %s", len(stdout), stdout)
	}
}

func TestJSSandboxEmptyCode(t *testing.T) {
	cfg := &config.JSSandboxConfig{
		Enabled:         true,
		TimeoutMs:       5000,
		MaxMemoryMB:     64,
		MaxOutputLength: 10000,
	}

	server := NewJSSandboxServerWithConfig(cfg)
	_, session, err := createTestClient(server)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer func() { _ = session.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "run_js",
		Arguments: map[string]interface{}{
			"code": "",
		},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsError {
		t.Errorf("expected IsError to be true for empty code")
	}
}

func createTestClient(server *mcp.Server) (*mcp.Client, *mcp.ClientSession, error) {
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	go func() {
		_ = server.Run(context.Background(), serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "test-client",
		Version: "1.0.0",
	}, nil)

	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		return nil, nil, err
	}

	return client, session, nil
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
