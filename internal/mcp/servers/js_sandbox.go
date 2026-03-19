// Package servers 提供内置 MCP 服务器实现。
package servers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rua.plus/saber/internal/config"
)

// JSInput 定义 run_js 工具的输入参数。
type JSInput struct {
	Code string `json:"code" jsonschema:"required,要执行的 JavaScript 代码"`
}

// JSOutput 定义 run_js 工具的输出结果。
type JSOutput struct {
	Result string `json:"result" jsonschema:"执行结果（JSON 格式）"`
	Stdout string `json:"stdout" jsonschema:"控制台输出"`
	Error  string `json:"error,omitempty" jsonschema:"错误信息（如果有）"`
}

// JSSandbox 封装 JavaScript 沙箱执行环境。
//
// 它提供安全的代码执行能力，包括超时控制、输出限制和危险 API 禁用。
type JSSandbox struct {
	cfg    *config.JSSandboxConfig
	stdout *strings.Builder
	mu     sync.Mutex
}

// NewJSSandboxServerWithConfig 使用指定配置创建 JavaScript 沙箱 MCP 服务器。
func NewJSSandboxServerWithConfig(cfg *config.JSSandboxConfig) *mcp.Server {
	if cfg == nil {
		cfg = &config.JSSandboxConfig{
			Enabled:         true,
			TimeoutMs:       5000,
			MaxMemoryMB:     64,
			MaxOutputLength: 10000,
		}
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "js_sandbox",
		Version: "1.0.0",
	}, nil)

	sandbox := &JSSandbox{cfg: cfg}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "run_js",
		Description: "在安全沙箱中执行 JavaScript 代码",
	}, sandbox.handleRunJS)

	return server
}

func (s *JSSandbox) handleRunJS(ctx context.Context, _ *mcp.CallToolRequest, input JSInput) (*mcp.CallToolResult, JSOutput, error) {
	if input.Code == "" {
		return nil, JSOutput{
			Error: "code 参数不能为空",
		}, fmt.Errorf("code 参数不能为空")
	}

	s.mu.Lock()
	s.stdout = &strings.Builder{}
	s.mu.Unlock()

	vm := goja.New()

	vm.SetMaxCallStackSize(100)
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))

	_ = vm.Set("console", map[string]interface{}{
		"log": func(call goja.FunctionCall) goja.Value {
			s.mu.Lock()
			defer s.mu.Unlock()
			for i, arg := range call.Arguments {
				if i > 0 {
					s.stdout.WriteString(" ")
				}
				fmt.Fprintf(s.stdout, "%v", arg.Export())
			}
			s.stdout.WriteString("\n")
			return goja.Undefined()
		},
	})

	if err := s.disableDangerousAPIs(vm); err != nil {
		return nil, JSOutput{}, fmt.Errorf("禁用危险 API 失败: %w", err)
	}

	var result interface{}
	var execErr error

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				execErr = fmt.Errorf("执行崩溃: %v", r)
			}
		}()

		value, err := vm.RunString(input.Code)
		if err != nil {
			execErr = err
			return
		}
		result = value.Export()
	}()

	select {
	case <-done:
		s.mu.Lock()
		stdout := s.stdout.String()
		s.mu.Unlock()

		if len(stdout) > s.cfg.MaxOutputLength {
			stdout = stdout[:s.cfg.MaxOutputLength] + "...(截断)"
		}

		output := JSOutput{
			Stdout: stdout,
		}

		if execErr != nil {
			output.Error = execErr.Error()
			return nil, output, execErr
		}

		output.Result = s.formatResult(result)
		return nil, output, nil

	case <-ctx.Done():
		vm.Interrupt("context cancelled")
		return nil, JSOutput{}, fmt.Errorf("执行被取消")

	case <-time.After(time.Duration(s.cfg.TimeoutMs) * time.Millisecond):
		vm.Interrupt("execution timeout")
		<-done
		return nil, JSOutput{
			Error: fmt.Sprintf("执行超时（%d 毫秒）", s.cfg.TimeoutMs),
		}, fmt.Errorf("执行超时（%d 毫秒）", s.cfg.TimeoutMs)
	}
}

func (s *JSSandbox) disableDangerousAPIs(vm *goja.Runtime) error {
	var err error

	_, err = vm.RunString(`
		delete this.eval;
		delete this.Function;
		delete this.GeneratorFunction;
		delete this.AsyncFunction;
		delete this.WebAssembly;
	`)
	if err != nil {
		return err
	}

	_ = vm.Set("require", func() {
		panic(newError("require is disabled in sandbox"))
	})

	_ = vm.Set("import", func() {
		panic(newError("import is disabled in sandbox"))
	})

	return nil
}

func newError(msg string) error {
	return fmt.Errorf("%s", msg)
}

func (s *JSSandbox) formatResult(result interface{}) string {
	if result == nil {
		return "undefined"
	}

	switch v := result.(type) {
	case string:
		return v
	case float64:
		if float64(int64(v)) == v {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%v", v)
	case int, int64, int32:
		return fmt.Sprintf("%d", v)
	case bool:
		return fmt.Sprintf("%t", v)
	case map[string]interface{}:
		return formatMap(v)
	case []interface{}:
		return formatArray(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func formatMap(m map[string]interface{}) string {
	if len(m) == 0 {
		return "{}"
	}
	var sb strings.Builder
	sb.WriteString("{ ")
	first := true
	for k, v := range m {
		if !first {
			sb.WriteString(", ")
		}
		first = false
		fmt.Fprintf(&sb, "%s: %v", k, formatValue(v))
	}
	sb.WriteString(" }")
	return sb.String()
}

func formatArray(a []interface{}) string {
	if len(a) == 0 {
		return "[]"
	}
	var sb strings.Builder
	sb.WriteString("[ ")
	for i, v := range a {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(formatValue(v))
	}
	sb.WriteString(" ]")
	return sb.String()
}

func formatValue(v interface{}) string {
	if v == nil {
		return "null"
	}
	switch val := v.(type) {
	case string:
		return fmt.Sprintf("\"%s\"", val)
	case map[string]interface{}:
		return formatMap(val)
	case []interface{}:
		return formatArray(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}
