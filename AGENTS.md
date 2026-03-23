# AGENTS.md

This file provides guidance to Qoder (qoder.com) when working with code in this repository.

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
make build-prod                         # 生产构建（纯 Go，静态链接）
make test                               # 全部测试
go test -v -tags goolm ./internal/ai -run TestService    # 单个测试函数
go test -cover -race -tags goolm ./...  # 覆盖率 + 竞态检测
make lint                               # golangci-lint
make run                                # 运行应用
```

**编辑器配置**:
```bash
export GOFLAGS="-tags=goolm"
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
slog.Info("服务初始化完成", "default_model", cfg.DefaultModel)
```

### 注释规范

**所有注释必须使用中文**。所有导出标识符必须有注释，注释以标识符名称开头。详细规范见 [`docs/comments.md`](docs/comments.md)。

---

## Architecture Patterns

### 服务初始化流程

应用启动遵循严格的初始化顺序 (`internal/bot/bot.go`):

```
Run() → initConfig() → initMatrixClient() → initServices() → setupEventHandlers() → startSync()
```

**服务依赖关系**:
- `MCPManager` 先于 `AIService` 初始化（AI 服务依赖 MCP 工具）
- `MediaService` 在 `AIService` 之前创建（处理图片消息）
- `ProactiveManager` 最后初始化（依赖 AI 服务）

### HTTP 客户端共享

所有 MCP 服务器共用一个 HTTP 客户端，复用连接池：

```go
import "rua.plus/saber/internal/mcp/servers"
client := servers.GetSharedHTTPClient()
```

### Strategy 模式 (AI 客户端)

支持多 AI 提供商，通过策略模式扩展：

```go
type ClientStrategy interface {
    CreateClientConfig(cfg *config.ModelConfig) openai.ClientConfig
    Name() string
}
// 注册新策略
factory.GetDefaultFactory().RegisterStrategy(&MyProviderStrategy{})
```

内置策略: `openai` (兼容 Ollama/vLLM/LocalAI), `azure`

### 多提供商配置

支持同时配置多个 AI 提供商，使用完全限定名称标识模型：

```yaml
ai:
  providers:
    openai:
      type: "openai"
      base_url: "https://api.openai.com/v1"
      api_key: "..."
      models:
        gpt-4o-mini: {model: "gpt-4o-mini"}
    ollama:
      type: "openai"
      base_url: "http://localhost:11434/v1"
      models:
        llama3: {model: "llama3"}
  default_model: "openai.gpt-4o-mini"  # 完全限定名称：提供商.模型名
```

**关键函数**:
- `config.ParseModelID(id)`: 解析完全限定名称，返回提供商和模型名
- `config.FormatModelID(provider, model)`: 生成完全限定名称
- `AIConfig.GetModelConfig(modelID)`: 获取模型配置，支持提供商级和全局级合并

### Factory 模式 (MCP 服务器)

MCP 服务器通过工厂模式创建，支持三种类型：

```go
type MCPServerFactory interface {
    Create(ctx context.Context, name string, cfg *config.ServerConfig) (*mcp.Client, *mcp.ClientSession, error)
    Type() string
}
```

服务器类型: `builtin` (内置工具), `stdio` (子进程), `http` (远程服务)

### Circuit Breaker

用于 AI 请求的熔断保护：

```go
cb := NewCircuitBreaker(5, 30*time.Second)
if !cb.Allow() { return ErrCircuitOpen }
cb.RecordSuccess()  // 或 cb.RecordFailure()
```

### 上下文传递模式

请求上下文通过 `context.Value` 传递元数据：

```go
// 设置用户/房间上下文
ctx = ai.WithUserContext(ctx, userID, roomID)
ctx = matrix.WithEventID(ctx, eventID)
ctx = matrix.WithMediaInfo(ctx, mediaInfo)

// 获取上下文
userID, ok := ai.GetUserFromContext(ctx)
eventID := matrix.GetEventID(ctx)
```

### AI 响应模式

AI 服务根据配置自动选择响应模式 (`internal/ai/service.go`):

| 模式 | 条件 | 特点 |
|------|------|------|
| `ResponseModeDirect` | 非流式，无工具 | 单次请求-响应 |
| `ResponseModeStreaming` | 流式，无工具 | 实时编辑消息 |
| `ResponseModeToolCalling` | 非流式，有工具 | 工具调用循环 |
| `ResponseModeStreamingWithTools` | 流式，有工具 | 流式 + 工具调用 |

工具调用最多迭代 5 次 (`maxToolIterations`)。

### 命令注册模式

命令通过 `CommandService` 注册，支持多种触发方式：

```go
cs := matrix.NewCommandService(client, botUserID, &buildInfo)

// 显式命令
cs.RegisterCommandWithDesc("ai", "描述", handler)

// 隐式触发
cs.SetDirectChatAIHandler(handler)      // 私聊自动回复
cs.SetMentionAIHandler(handler)          // @提及回复
cs.SetReplyAIHandler(handler)            // 回复机器人消息
```

### SQLite 双驱动系统

根据 CGO 开关选择不同驱动 (`internal/db/`):

| 文件 | 构建条件 | 驱动 |
|------|----------|------|
| `sqlite_cgo.go` | CGO_ENABLED=1 | `mattn/go-sqlite3` |
| `sqlite_nocgo.go` | CGO_ENABLED=0 | `modernc/sqlite` |

---

## Testing Guidelines

### 测试命名规范

测试函数命名格式: `Test<FunctionName>_<Scenario>`

```go
func TestNewService_NilConfig(t *testing.T) { ... }
func TestNewService_InvalidConfig(t *testing.T) { ... }
func TestNewService_ValidConfig(t *testing.T) { ... }
```

### 表驱动测试

```go
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
                t.Errorf("error = %v", err)
            }
        })
    }
}
```

### 测试辅助函数

测试辅助函数以 `create` 或 `new` 前缀命名:

```go
func createTestAIConfig() *config.AIConfig {
    cfg := config.DefaultAIConfig()
    cfg.Enabled = true
    cfg.DefaultModel = "openai.gpt-4o-mini"
    cfg.Providers = map[string]config.ProviderConfig{
        "openai": {
            Type:    "openai",
            BaseURL: "https://api.openai.com/v1",
            Models: map[string]config.ModelConfig{
                "gpt-4o-mini": {Model: "gpt-4o-mini"},
            },
        },
    }
    return &cfg
}
```

**要求**: 成功和失败路径覆盖 | 使用 `t.TempDir()` | 表驱动测试 | 每个测试独立

---

## Security Guidelines

**禁止提交**: `config.yaml` | `*.session` | `*.key` | `*.db`

**配置权限**: `0o600` | **认证优先**: access_token > password

**stdio MCP**: 必须配置 `allowed_commands` 白名单

**HTML 输出**: 使用 `matrix.SanitizeHTML()` 防止 XSS

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
- [go-openai Documentation](https://pkg.go.dev/github.com/sashabaranov/go-openai)
- [MCP Go SDK](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk)