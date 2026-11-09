package devtools

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SyntaxResult 区分真实解析错误、实际成功和没有可用解析器，不能用空成功冒充检查。
type SyntaxResult struct {
	Status string `json:"status"`
	Output string `json:"output"`
	Parser string `json:"parser,omitempty"`
}

// Syntax 对 Go/JSON/YAML/XML 使用纯解析器，其他已知语言通过授权任务后端的只解析命令。
func (m *Manager) Syntax(ctx context.Context, root, path string, data []byte) SyntaxResult {
	if !m.cfg.Enabled {
		return SyntaxResult{Status: "skipped", Output: "developer tools disabled"}
	}
	if err := ctx.Err(); err != nil {
		return SyntaxResult{Status: "unavailable", Output: err.Error()}
	}
	ext := strings.ToLower(filepath.Ext(path))
	result := SyntaxResult{Status: "ok"}
	var err error
	switch ext {
	case ".go":
		result.Parser = "go/parser"
		_, err = parser.ParseFile(token.NewFileSet(), path, data, parser.AllErrors)
	case ".json", ".ipynb":
		result.Parser = "encoding/json"
		decoder := json.NewDecoder(bytes.NewReader(data))
		var value any
		err = decoder.Decode(&value)
		if err == nil {
			var extra any
			if e := decoder.Decode(&extra); e != io.EOF {
				if e == nil {
					result.Status, result.Output = "error", "multiple JSON values"
					return result
				}
				err = e
			}
		}
	case ".yaml", ".yml":
		result.Parser = "yaml.v3"
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var node yaml.Node
			e := decoder.Decode(&node)
			if e == io.EOF {
				break
			}
			if e != nil {
				err = e
				break
			}
		}
	case ".xml", ".svg", ".xsd":
		result.Parser = "encoding/xml"
		decoder := xml.NewDecoder(bytes.NewReader(data))
		for {
			_, e := decoder.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				err = e
				break
			}
		}
	default:
		var command []string
		switch ext {
		case ".py", ".pyi":
			command = []string{"python3", "-c", "import ast,sys; ast.parse(sys.stdin.read(), filename=sys.argv[1])", path}
		case ".toml":
			command = []string{"python3", "-c", "import tomllib,sys; tomllib.loads(sys.stdin.read())"}
		case ".js", ".mjs", ".cjs":
			command = []string{"node", "--check"}
		case ".sh", ".bash":
			command = []string{"bash", "-n"}
		case ".zsh":
			command = []string{"zsh", "-n"}
		case ".rb":
			command = []string{"ruby", "-c"}
		case ".php":
			command = []string{"php", "-l"}
		default:
			return SyntaxResult{Status: "skipped", Output: "no file-local syntax parser; language server supplies semantic and syntax diagnostics"}
		}
		if !m.trusted(root) {
			return SyntaxResult{Status: "skipped", Output: "external syntax parser requires explicitly trusted workspace"}
		}
		result.Parser = command[0]
		timeout, stop := context.WithTimeout(ctx, time.Duration(m.cfg.WaitTimeoutSeconds)*time.Second)
		defer stop()
		output, e := m.runProcess(timeout, root, command, data, 64<<10)
		result.Output = strings.TrimSpace(string(output))
		if e != nil {
			result.Status = "error"
			var processErr *processRunError
			if !errors.As(e, &processErr) || processErr.stage != "exit" {
				result.Status = "unavailable"
			}
			if result.Output == "" {
				result.Output = e.Error()
			}
		}
		return result
	}
	if err != nil {
		result.Status, result.Output = "error", err.Error()
	}
	return result
}

// Preflight 拒绝已确认语法错误的结构化配置，必须在文件工具任何写入之前调用。
// 没有解析器时不冒充成功，调用方的编辑后 lint 仍会说明不可用原因。
func (m *Manager) Preflight(ctx context.Context, root, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json", ".yaml", ".yml", ".toml":
		result := m.Syntax(ctx, root, path, data)
		if result.Status == "error" {
			return fmt.Errorf("invalid structured file %s: %s; file was not modified", path, result.Output)
		}
	}
	return ctx.Err()
}
