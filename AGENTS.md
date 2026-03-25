<!-- Generated: 2026-03-25 | Updated: 2026-03-25 -->

# Saber

## Purpose

Saber 是一个基于 Matrix 协议的 AI 机器人，支持 AI 对话、端到端加密、MCP 工具集成和主动聊天功能。使用 Go 1.26.1 开发，模块路径为 `rua.plus/saber`。

## Key Files

| File | Description |
|------|-------------|
| `main.go` | 应用入口点，初始化并运行机器人 |
| `go.mod` | Go 模块定义和依赖管理 |
| `go.sum` | 依赖校验和 |
| `Makefile` | 构建脚本，支持多平台编译和 Docker |
| `Dockerfile` | Docker 容器构建配置 |
| `docker-bake.hcl` | Docker buildx 多架构构建配置 |
| `config.example.yaml` | 示例配置文件 |
| `config.yaml` | 实际配置文件（不提交到版本控制） |

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `internal/` | 内部包，包含所有核心功能模块（见 `internal/AGENTS.md`） |
| `docs/` | 项目文档（见 `docs/AGENTS.md`） |
| `bin/` | 构建产物输出目录 |

## For AI Agents

### Working In This Directory

- **构建标签**: E2EE 功能需要 `-tags goolm`
- **编辑器配置**: 设置 `export GOFLAGS="-tags=goolm"` 以获得正确的 IDE 支持
- **依赖管理**: 使用 `go mod tidy` 管理依赖
- **代码风格**: 遵循 `docs/comments.md` 中的注释规范（中文注释）

### Testing Requirements

```bash
# 运行所有测试
make test

# 运行单个包测试
go test -v -tags goolm ./internal/ai

# 覆盖率测试
go test -cover -race -tags goolm ./...
```

### Common Patterns

- **初始化顺序**: `Run() → initConfig() → initMatrixClient() → initServices() → setupEventHandlers() → startSync()`
- **服务依赖**: MCPManager → AIService（AI 服务需要 MCP 工具）；MediaService 在 AIService 之前；ProactiveManager 最后
- **策略模式**: AI 客户端使用 `ClientStrategy` 接口，通过 `factory.GetDefaultFactory().RegisterStrategy()` 注册
- **工厂模式**: MCP 服务器使用 `MCPServerFactory` 接口

## Dependencies

### Internal

- `internal/bot` - 机器人初始化和生命周期管理
- `internal/ai` - AI 服务（对话、流式响应、工具调用）
- `internal/matrix` - Matrix 客户端封装
- `internal/config` - 配置加载和验证
- `internal/mcp` - MCP 工具集成
- `internal/cli` - 命令行参数解析
- `internal/context` - 上下文键定义
- `internal/db` - SQLite 数据库驱动
- `internal/meme` - GIF/Sticker 搜索服务

### External

| Package | Purpose |
|---------|---------|
| `maunium.net/go/mautrix` | Matrix 协议 SDK |
| `github.com/sashabaranov/go-openai` | OpenAI 兼容 API 客户端 |
| `github.com/modelcontextprotocol/go-sdk` | MCP Go SDK |
| `modernc.org/sqlite` | 纯 Go SQLite 驱动（非 CGO） |
| `github.com/dop251/goja` | JavaScript 运行时（用于 JS 沙箱） |
| `gopkg.in/yaml.v3` | YAML 解析 |

## Build Commands

```bash
make build          # 标准构建 → bin/saber
make build-prod     # 生产构建（优化、静态链接）
make build-all      # 跨平台构建（macOS/Linux/Windows/FreeBSD/OpenBSD/Loong64）
make test           # 运行测试
make lint           # golangci-lint 检查
make run            # 运行应用
```

## Security Notes

- **切勿提交**: `config.yaml`, `*.session`, `*.key`, `*.db`
- **配置权限**: 建议设置为 `0o600`
- **认证优先级**: access_token > password
- **stdio MCP**: 必须配置 `allowed_commands` 白名单
- **HTML 输出**: 使用 `matrix.SanitizeHTML()` 防止 XSS

<!-- MANUAL: 以下是手动添加的额外说明 -->

## Architecture

```
main.go
    └── bot.Run()
            ├── initConfig()          # 配置加载
            ├── initMatrixClient()    # Matrix 客户端初始化
            │       └── initCrypto()  # E2EE 加密初始化
            ├── initServices()        # 服务初始化
            │       ├── initMCPManager()
            │       ├── ai.NewService()
            │       └── initProactiveManager()
            ├── setupEventHandlers()  # 事件处理器
            └── startSync()           # 开始同步
```