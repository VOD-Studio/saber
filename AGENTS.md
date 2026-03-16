# AGENTS.md — Saber Matrix Bot Development Guide

## Project Overview

**Saber** 是一个集成 AI 功能的 Matrix 机器人，使用 Go 1.26.1 和 mautrix SDK 构建。
Module path: `rua.plus/saber`

**架构**: `main.go` → `internal/bot` → `internal/{matrix,ai,config,cli}`

**关键依赖**: `maunium.net/go/mautrix` (Matrix), `github.com/sashabaranov/go-openai` (AI), `log/slog` (日志)

---

## Build, Test, Lint Commands

**Build Tag (E2EE 必需)**: `-tags goolm`

```bash
# 构建
make build                              # → bin/saber
go build -tags goolm -ldflags="..." .   # 手动构建

# 测试
make test                               # 全部测试
go test -v -tags goolm ./internal/ai -run TestService  # 单个测试
go test -cover -race -tags goolm ./...  # 覆盖率 + 竞态检测

# 代码质量
make fmt                                # goimports 格式化
make lint                               # golangci-lint
golangci-lint run --fix --build-tags goolm ./...
```

---

## Code Style Guidelines

### 格式化与导入

- **缩进**: Tab，4 空格视觉宽度 (`.editorconfig`)
- **换行**: LF，UTF-8，文件末尾空行
- **导入顺序**: 标准库 → 外部库 → 内部包（组间空行分隔）

```go
import (
    "context"
    "fmt"

    "maunium.net/go/mautrix"

    "rua.plus/saber/internal/config"
)
```

### 命名规范

| 元素 | 规则 | 示例 |
|------|------|------|
| 包名 | 小写单词 | `bot`, `matrix`, `ai` |
| 导出类型/函数 | PascalCase | `MatrixClient`, `NewService` |
| 非导出函数 | camelCase | `setupLogging`, `handleMessage` |
| 错误变量 | `Err` 前缀 | `ErrNotFound`, `ErrInvalidConfig` |

### 错误处理

```go
// ✅ 正确：立即检查，包装上下文
if err != nil {
    return fmt.Errorf("failed to load config: %w", err)
}

// ❌ 禁止：忽略错误
result, _ := doSomething()  // 仅在明确安全时使用
```

### 日志规范

- 使用 `log/slog` 结构化日志
- 格式: `slog.Info("消息", "key1", value1, "key2", value2)`
- **禁止记录敏感数据** (tokens, passwords, API keys)

### 注释规范

**所有注释必须使用中文**（除专有名词、API 名称外）。详细规范见 [`docs/comments.md`](docs/comments.md)。

```go
// Package bot 封装所有机器人初始化和运行逻辑。
package bot

// Run 初始化并运行机器人。
//
// 它处理 CLI 标志、配置加载、Matrix 客户端设置和优雅关闭。
func Run(version, gitMsg string) { ... }

// Config 存储应用程序配置。
type Config struct {
    // Name 是应用程序实例名称（默认："saber"）
    Name string
}
```

**规则**: 所有导出标识符必须有注释 → 注释以标识符名称开头 → 解释"为什么"而非"是什么"

### Context 与并发

- 所有 I/O 和长时间操作必须使用 `context.Context`
- 参数顺序: `func(ctx context.Context, ...)`
- 共享状态使用 `sync.Mutex` 或 channel 保护

---

## Testing Guidelines

```go
// 文件命名: <name>_test.go | 函数命名: Test<FunctionName>
func TestValidate(t *testing.T) {
    tests := []struct {
        name    string
        config  Config
        wantErr bool
    }{
        {"valid", Config{URL: "https://example.com"}, false},
        {"missing URL", Config{}, true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := tt.config.Validate()
            if (err != nil) != tt.wantErr {
                t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
            }
        })
    }
}
```

**要求**: 成功和失败路径都要覆盖 | 使用 `t.TempDir()` 创建临时文件

---

## Linting

项目使用 `golangci-lint` 默认配置。常见错误修复:

| 错误 | 修复方法 |
|------|----------|
| exported function missing comment | 添加 `// FunctionName ...` 注释 |
| error not checked | 添加 `if err != nil` 检查 |
| shadow declaration | 重命名变量避免遮蔽 |

---

## 敏感数据与安全

**禁止提交**: `config.yaml` | `*.session` | `*.credentials.yaml` | `*.db` | `*.key`

**认证方式优先级**: access_token (推荐) > password (仅首次登录后保存 session)

**Session 文件权限**: `0600`

---

## 开发工作流

```bash
git checkout -b feature/description
# ... 编码 ...
make lint && make test && make fmt
git commit -m "feat(scope): 描述"
```

**Commit 格式**: `<type>(<scope>): <subject>` (feat/fix/docs/style/refactor/test/chore)

---

## 常见问题

```bash
make clean && make build   # 干净重建
go mod tidy                # 整理依赖
go version                 # 确认 >= 1.26.1
```

---

## 参考资源

- [Effective Go](https://go.dev/doc/effective_go)
- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- [mautrix-go Documentation](https://docs.mau.fi/mautrix-go/)
