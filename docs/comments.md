# Golang 注释规范

本文档定义了 `rua.plus/saber` 项目的注释规范，遵循 Go 官方最佳实践。

## 目录

- [基本原则](#基本原则)
- [包注释](#包注释)
- [函数/方法注释](#函数方法注释)
- [类型注释](#类型注释)
- [常量与变量注释](#常量与变量注释)
- [特殊注释](#特殊注释)
- [godoc 格式](#godoc-格式)
- [注释检查工具](#注释检查工具)

---

## 基本原则

1. **注释代码意图，而非实现** — 解释"为什么"，不是"是什么"
2. **保持同步** — 代码变更时必须更新相关注释
3. **避免冗余** — 好的命名胜过长注释
4. **使用完整句子** — 以被注释对象名称开头
5. **为导出标识符注释** — 所有 exported 名称必须有注释

---

## 包注释

### 格式

```go
// Package <name> <one-line summary>.
//
// <detailed description paragraph(s)>
//
// Example (optional):
//
//	<code example>
package <name>
```

### 示例

```go
// Package main provides the entry point for the saber application.
//
// Saber is a command-line tool for [brief description of what saber does].
//
// Usage:
//
//	saber [flags] [arguments]
//
// Run "saber --help" for more information.
package main
```

### 规则

- 以 `// Package <name>` 开头
- 写在 `package` 语句之前，紧邻无空行
- 第一句应是完整句子，概括包的功能（会出现在 godoc 索引中）
- 包注释由同一文件中的多个注释块组成时，以空行分隔

---

## 函数/方法注释

### 格式

```go
// <FunctionName> <one-line summary>.
//
// <detailed description (optional)>
//
// Parameters:
//   - <param1>: <description>
//   - <param2>: <description>
//
// Returns:
//   - <return1>: <description>
//
// Errors (if applicable):
//   - <error condition 1>
//   - <error condition 2>
//
// Example (optional):
//
//	<code example>
func <FunctionName>(...) ...
```

### 示例

#### 简单函数

```go
// Add returns the sum of two integers.
func Add(a, b int) int {
    return a + b
}
```

#### 复杂函数

```go
// ParseConfig reads and validates the configuration from the given file path.
//
// The function supports both YAML and JSON formats, determined by file extension.
//
// Parameters:
//   - path: Absolute or relative path to the configuration file
//
// Returns:
//   - *Config: Parsed configuration object
//   - error: Non-nil if file not found, parse fails, or validation fails
//
// Errors:
//   - os.ErrNotExist: File does not exist
//   - ErrInvalidConfig: Configuration validation failed
//   - ErrUnknownFormat: Unsupported file format
//
// Example:
//
//	cfg, err := ParseConfig("config.yaml")
//	if err != nil {
//	    log.Fatal(err)
//	}
func ParseConfig(path string) (*Config, error) {
    // ...
}
```

#### 方法

```go
// String returns the string representation of the Error.
//
// It formats the error with its code and message, suitable for logging
// or displaying to users.
func (e *Error) String() string {
    return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}
```

### 规则

- 以函数名开头（`// <FunctionName> ...`）
- 描述函数**做什么**，而非**怎么做**
- 简单函数（如 getter/setter）可以只写一行注释
- 复杂函数应说明：
  - 参数含义和约束
  - 返回值含义
  - 可能的错误条件
  - 使用示例

---

## 类型注释

### 结构体

```go
// Config holds the configuration for the saber application.
//
// A zero value Config is valid and uses default values for all fields.
// Use LoadConfig to populate from a configuration file.
type Config struct {
    // Name is the application instance name (default: "saber")
    Name string
    
    // LogLevel controls the verbosity of logging (default: "info")
    // Valid values: "debug", "info", "warn", "error"
    LogLevel string
    
    // Timeout specifies the maximum duration for operations.
    // A zero value means no timeout.
    Timeout time.Duration
}
```

### 接口

```go
// Storage defines the interface for data persistence backends.
//
// Implementations must be safe for concurrent use.
type Storage interface {
    // Get retrieves the value for the given key.
    // Returns ErrNotFound if the key does not exist.
    Get(key string) ([]byte, error)
    
    // Set stores the value with the given key.
    // Overwrites any existing value.
    Set(key string, value []byte) error
    
    // Delete removes the value for the given key.
    // No error is returned if the key does not exist.
    Delete(key string) error
}
```

### 其他类型

```go
// Option is a functional option for configuring the Client.
type Option func(*Client) error

// ErrorCode represents a categorization of errors.
type ErrorCode int
```

### 规则

- 描述类型的用途和约束
- 导出字段必须有独立注释
- 注释应说明零值是否有效
- 接口方法应各自有注释

---

## 常量与变量注释

### 常量

```go
const (
    // DefaultPort is the default server port.
    DefaultPort = 8080
    
    // MaxRetries controls the maximum number of retry attempts.
    MaxRetries = 3
    
    // Timeout is the default operation timeout.
    Timeout = 30 * time.Second
)
```

### 变量

```go
var (
    // ErrNotFound indicates that the requested resource was not found.
    ErrNotFound = errors.New("resource not found")
    
    // ErrInvalidInput indicates that the input validation failed.
    ErrInvalidInput = errors.New("invalid input")
)
```

### 规则

- 常量/变量注释应说明用途和含义
- 错误变量以 `Err` 前缀命名，注释说明触发条件
- 分组声明时，注释写在每个常量/变量上方

---

## 特殊注释

### TODO / FIXME

```go
// TODO(username): Optimize for large inputs (>10MB)
// TODO(author): Add support for custom delimiters

// FIXME: This doesn't handle nil case properly
// FIXME(author): Race condition in concurrent access
```

**格式**: `// TODO(username): description` 或 `// FIXME: description`

**用途**:
- `TODO`: 计划改进或添加的功能
- `FIXME`: 已知问题需要修复

### 弃用警告

```go
// OldFunc is deprecated: Use NewFunc instead.
//
// OldFunc has performance issues with large inputs.
// Migrate to NewFunc before v2.0.0 release.
func OldFunc() {}

// Deprecated: Use Config.Timeout instead.
const DefaultTimeout = 30 * time.Second
```

**格式**: `// <name> is deprecated: Use <alternative> instead.`

### 条件编译注释

```go
//go:build linux

// +build darwin freebsd netbsd
```

---

## godoc 格式

godoc 会解析注释并生成文档，遵循以下规则：

### 段落格式

```go
// Package example demonstrates godoc formatting.
//
// The first sentence appears in package listings.
// It should be a complete, self-contained summary.
//
// Second paragraph provides more details.
// Consecutive lines in the same paragraph are wrapped.
//
// A blank line starts a new paragraph.
//
// Indented lines are formatted as preformatted text:
//
//	if err := doSomething(); err != nil {
//	    return err
//	}
package example
```

### 列表格式

```go
// List format is supported:
//
//   - Item 1
//   - Item 2
//   - Item 3
//
// Numbered lists:
//
//  1. First item
//  2. Second item
//  3. Third item
```

### 链接

```go
// See https://pkg.go.dev/fmt for formatting rules.
// Related: [encoding/json] for JSON operations.
```

---

## 注释检查工具

### golint (已弃用)

使用 `staticcheck` 或 `revive` 替代。

### revive

```bash
# 安装
go install github.com/mgechev/revive@latest

# 检查注释
revive -config revive.toml ./...
```

配置 `revive.toml`:

```toml
[rule.exported]
  severity = "warning"
  arguments = [["checkPrivateReceivers"]]
```

### staticcheck

```bash
# 安装
go install honnef.co/go/tools/cmd/staticcheck@latest

# 检查
staticcheck ./...
```

### go doc

```bash
# 查看包文档
go doc -all ./...

# 查看特定标识符
go doc <package>.<Type>
go doc <package>.<Function>
```

---

## 快速参考

| 元素 | 格式 | 示例 |
|------|------|------|
| 包 | `// Package <name> ...` | `// Package main provides ...` |
| 函数 | `// <Name> ...` | `// Add returns the sum...` |
| 类型 | `// <Type> ...` | `// Config holds...` |
| 常量 | `// <Name> ...` | `// DefaultPort is...` |
| 变量 | `// <Name> ...` | `// ErrNotFound indicates...` |
| TODO | `// TODO(user): ...` | `// TODO(john): optimize` |
| Deprecated | `// <name> is deprecated: ...` | `// OldFunc is deprecated: Use NewFunc` |

---

## 参考资源

- [Effective Go - Comments](https://go.dev/doc/effective_go#commentary)
- [Go Code Review Comments - Doc Comments](https://github.com/golang/go/wiki/CodeReviewComments#doc-comments)
- [Go Doc Comments](https://go.dev/doc/comment)
- [godoc documentation](https://pkg.go.dev/golang.org/x/tools/cmd/godoc)