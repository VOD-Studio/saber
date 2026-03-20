# AGENTS.md — Saber Matrix Bot Development Guide

## Project Overview

**Saber** 是一个集成 AI 功能的 Matrix 机器人，使用 Go 1.26.1 和 mautrix SDK 构建。
Module path: `rua.plus/saber`

**架构**: `main.go` → `internal/bot` → `internal/{matrix,ai,config,cli,mcp,db}`

**关键依赖**: `maunium.net/go/mautrix`, `github.com/sashabaranov/go-openai`, `github.com/modelcontextprotocol/go-sdk`, `log/slog`

---

## Build, Test, Lint Commands

**构建标签 (E2EE 必需)**: `-tags goolm`

```bash
make build                              # → bin/saber
make test                               # 全部测试
go test -v -tags goolm ./internal/ai -run TestService    # 单个测试函数
go test -cover -race -tags goolm ./...  # 覆盖率 + 竞态检测
make lint                               # golangci-lint
make run                                # 运行应用
```

---

## Code Style Guidelines

### 格式化与命名

- **缩进**: Tab，4 空格视觉宽度
- **导入顺序**: 标准库 → 外部库 → 内部包（组间空行分隔）
- **命名**: 包名小写单词，导出类型 PascalCase，非导出函数 camelCase
- **错误变量**: `Err` 前缀 | **策略/工厂**: `*Strategy`, `*Factory` 后缀

### 错误处理与日志

```go
// ✅ 使用 %w 包装上下文
if err != nil {
    return fmt.Errorf("failed to load: %w", err)
}

// 日志：使用 slog 结构化日志，禁止记录敏感数据
slog.Info("服务初始化完成", "provider", cfg.Provider)
```

### 注释规范

**所有注释必须使用中文**。所有导出标识符必须有注释，注释以标识符名称开头。详细规范见 [`docs/comments.md`](docs/comments.md)。

---

## Architecture Patterns

### HTTP 客户端共享

```go
import "rua.plus/saber/internal/mcp/servers"
client := servers.GetSharedHTTPClient()  // 复用连接池
```

### Strategy 模式 (AI 客户端)

```go
type ClientStrategy interface {
    CreateClientConfig(cfg *config.ModelConfig) openai.ClientConfig
    Name() string
}
factory.RegisterStrategy(&MyProviderStrategy{})
```

### Factory 模式 (MCP 服务器)

```go
type MCPServerFactory interface {
    Create(ctx context.Context, name string, cfg *config.ServerConfig) (*mcp.Client, *mcp.ClientSession, error)
    Type() string
}
```

### Circuit Breaker

```go
cb := NewCircuitBreaker(5, 30*time.Second)
if !cb.Allow() { return ErrCircuitOpen }
```

---

## Testing Guidelines

```go
func TestValidate(t *testing.T) {
    tests := []struct { name string; config Config; wantErr bool }{
        {"valid", Config{URL: "https://example.com"}, false},
        {"missing URL", Config{}, true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := tt.config.Validate()
            if (err != nil) != tt.wantErr { t.Errorf("error = %v", err) }
        })
    }
}
```

**要求**: 成功和失败路径覆盖 | 使用 `t.TempDir()` | 表驱动测试

---

## Security Guidelines

**禁止提交**: `config.yaml` | `*.session` | `*.key` | `*.db`

**配置权限**: `0o600` | **认证优先**: access_token > password

**stdio MCP**: 必须配置 `allowed_commands` | **HTML**: 使用 `SanitizeHTML()`

---

## Development Workflow

```bash
git checkout -b feature/description
make lint && make test && make fmt
git commit -m "feat(scope): 描述"  # format: <type>(<scope>): <subject>
```

## Common Issues

```bash
make clean && make build   # 干净重建
export GOFLAGS="-tags=goolm"  # 编辑器构建标签
```

## References

- [Effective Go](https://go.dev/doc/effective_go)
- [mautrix-go Documentation](https://docs.mau.fi/mautrix-go/)