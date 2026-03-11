# AGENTS.md — Saber Matrix Bot Development Guide

## Project Overview

**Saber** is a Matrix bot built with Go 1.26.1 using the mautrix SDK (`maunium.net/go/mautrix`).
Module path: `rua.plus/saber`

Architecture:
- `main.go` — Entry point, delegates to `internal/bot`
- `internal/bot/` — Bot initialization, lifecycle, shutdown
- `internal/matrix/` — Matrix client wrapper, session management, event handlers
- `internal/config/` — YAML configuration loading and validation
- `internal/cli/` — Command-line flag parsing

---

## Build, Test, and Lint Commands

### Makefile Targets

```bash
make build     # Build binary to bin/saber
make run       # Run: go run main.go
make test      # Run all tests: go test -v ./...
make fmt       # Format: go fmt ./...
make lint      # Lint: golangci-lint run ./...
make clean     # Remove build artifacts
```

### Running Single Tests

```bash
go test -v ./internal/package -run TestFunctionName  # Specific test
go test -cover ./internal/package                     # With coverage
go test -race ./...                                   # Race detector
```

---

## Code Style Guidelines

### Formatting

- **Indentation**: Tabs (`\t`), 4 spaces visual width (`.editorconfig`)
- **Line endings**: LF (`\n`), UTF-8, final newline required
- Run `make fmt` before committing — `gofmt` is authoritative

### Imports

```go
import (
    "context"
    "fmt"

    "gopkg.in/yaml.v3"
    "maunium.net/go/mautrix"

    "rua.plus/saber/internal/config"
)
```

Order: Standard library → External → Internal (blank lines between groups).

### Naming Conventions

- **Packages**: Lowercase single word (`bot`, `matrix`, `config`)
- **Types**: PascalCase (`MatrixClient`, `EventHandler`)
- **Functions**: PascalCase exported, camelCase unexported (`Run`, `setupLogging`)
- **Variables**: camelCase, descriptive (`cfg`, `userID`)
- **Errors**: Prefix with `Err` (`ErrNotFound`, `ErrInvalidConfig`)

### Error Handling

- **Never ignore errors** — use `_` only with explicit justification
- **Wrap errors** with context: `fmt.Errorf("failed to load: %w", err)`
- **Check immediately** after the call
- **Return up the stack**; handle at boundaries

```go
if err != nil {
    return fmt.Errorf("failed to load config: %w", err)
}
```

### Logging

- Use `log/slog` (standard library)
- Structured: `slog.Info("Loaded", "path", cfgPath, "user", userID)`
- **Never log sensitive data** (tokens, passwords)

### Comments and Documentation

**所有注释必须使用中文**（除专有名词、API 名称外）。详见 [`docs/comments.md`](docs/comments.md)。

#### 基本规则

- **所有导出标识符必须有注释**（linter 强制检查）
- 注释以标识符名称开头：`// Run 初始化并运行机器人`
- 解释**为什么**，而非**是什么**
- 使用 godoc 格式：空行分隔段落
- 非导出标识符可根据需要添加注释

#### 包注释

```go
// Package bot 封装了所有机器人初始化和运行逻辑。
package bot
```

- 写在 `package` 语句之前，紧邻无空行
- 以 `// Package <包名>` 开头
- 第一句完整句子概括包的功能

#### 函数/方法注释

```go
// Run 初始化并运行机器人。
//
// 它处理 CLI 标志、配置加载、Matrix 客户端设置和优雅关闭。
//
// 参数:
//   - version: 版本号，用于日志和版本信息
func Run(version string) {
    // ...
}
```

- 简单函数可只写一行注释
- 复杂函数应说明：参数含义、返回值、可能的错误条件

#### 类型注释

```go
// Config 存储应用程序配置。
//
// 零值 Config 是有效的，所有字段使用默认值。
type Config struct {
    // Name 是应用程序实例名称（默认："saber"）
    Name string
}
```

#### 特殊注释

```go
// TODO(用户名): 优化大输入处理（>10MB）
// FIXME: 此处未正确处理 nil 情况
// Deprecated: 请使用 NewFunc 替代。
```

#### godoc 格式

```go
// 段落之间使用空行分隔。
//
// 缩进代码行会格式化为预格式化文本：
//
//	if err := doSomething(); err != nil {
//	    return err
//	}
//
// 列表格式：
//   - 项目 1
//   - 项目 2
//   - 项目 3
```

### Context and Concurrency

- Use `context.Context` for all I/O and long-running operations
- Pass context first: `func(ctx context.Context, ...)`
- Protect shared state with `sync.Mutex` or channels

---

## Testing Guidelines

- File naming: `<name>_test.go`
- Function naming: `Test<FunctionName>`
- Use table-driven tests for multiple cases
- Test success and failure paths

```go
func TestLoadConfig(t *testing.T) {
    tests := []struct {
        name    string
        path    string
        wantErr bool
    }{
        {"valid", "testdata/valid.yaml", false},
        {"missing", "nonexistent.yaml", true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // test logic
        })
    }
}
```

---

## Linting

Project uses `golangci-lint` with default configuration.

```bash
make lint                     # Run linter
golangci-lint run --fix ./... # Auto-fix issues
```

### Common Lint Errors

- **exported function missing comment**: Add `// FunctionName does...`
- **error not checked**: Add `if err != nil` check
- **shadow declaration**: Rename variable

---

## Sensitive Data and Security

### Never Commit

- `config.yaml` (gitignored)
- `*.session` files (access tokens)
- `*.credentials.yaml`, `*.db` files

### Session Files

- Generated on password login, used for passwordless startups
- Permissions: `0600` (owner read/write only)
- **Never** commit to version control

### Auth Methods

- **Token auth** (recommended): Use `access_token` in config
- **Password auth**: Use `password` for initial login, then save session

---

## Development Workflow

1. **Create branch**: `git checkout -b feature/description`
2. **Make changes** following code style
3. **Run linter**: `make lint` (must pass)
4. **Run tests**: `make test` (all must pass)
5. **Format**: `make fmt`
6. **Commit** with descriptive message

### Commit Message Format

```
<type>(<scope>): <subject>

<body (optional)>
```

Types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`

---

## Troubleshooting

```bash
# Clean rebuild
make clean && make build

# Tidy dependencies
go mod tidy

# Check Go version (must be 1.26.1+)
go version
```

---

## External Resources

- [Effective Go](https://go.dev/doc/effective_go)
- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- [mautrix-go Documentation](https://docs.mau.fi/mautrix-go/)
