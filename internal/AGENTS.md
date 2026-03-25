<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-25 | Updated: 2026-03-25 -->

# internal

## Purpose

包含 Saber 机器人的所有内部实现包。每个子目录对应一个独立的功能模块，通过清晰的接口边界实现松耦合。

## Key Files

本目录本身不包含 Go 源文件，仅作为包容器。

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `ai/` | AI 服务核心：对话管理、流式响应、工具调用、主动聊天（见 `ai/AGENTS.md`） |
| `bot/` | 机器人生命周期管理：初始化、运行、优雅关闭（见 `bot/AGENTS.md`） |
| `cli/` | 命令行参数解析（见 `cli/AGENTS.md`） |
| `config/` | YAML 配置加载、验证和默认值管理（见 `config/AGENTS.md`） |
| `context/` | 上下文键定义，提供类型安全的上下文值存取（见 `context/AGENTS.md`） |
| `db/` | SQLite 数据库驱动注册（CGO/非 CGO 双驱动支持）（见 `db/AGENTS.md`） |
| `matrix/` | Matrix 客户端封装：连接、认证、事件处理、媒体服务（见 `matrix/AGENTS.md`） |
| `mcp/` | MCP (Model Context Protocol) 集成：服务器管理、工具调用（见 `mcp/AGENTS.md`） |
| `meme/` | GIF/Sticker 搜索服务（Klipy API 集成）（见 `meme/AGENTS.md`） |

## For AI Agents

### Working In This Directory

- **包隔离**: 所有内部包通过 `internal/` 路径隔离，外部项目无法直接导入
- **依赖方向**: 遵循单向依赖：`bot → {matrix, ai, config, cli, mcp, meme}`，`ai → {matrix, config, mcp}`
- **接口优先**: 跨包调用通过接口抽象，避免直接依赖具体实现

### Testing Requirements

- 每个包有独立的 `*_test.go` 文件
- 测试需要 `-tags goolm` 构建标签
- 使用 `t.TempDir()` 创建临时文件
- 表驱动测试优先

### Common Patterns

#### 初始化顺序

```
bot.Run()
    ├── cli.Parse()           # 解析命令行
    ├── config.Load()         # 加载配置
    ├── matrix.NewMatrixClient()  # 创建 Matrix 客户端
    ├── mcp.NewManagerWithBuiltin()  # 初始化 MCP 管理器
    ├── ai.NewService()       # 创建 AI 服务（依赖 MCP）
    └── ai.NewProactiveManager()  # 创建主动聊天管理器（最后）
```

#### 服务依赖关系

```
                    ┌─────────────┐
                    │    bot      │
                    └──────┬──────┘
                           │
        ┌──────────────────┼──────────────────┐
        │                  │                  │
        ▼                  ▼                  ▼
   ┌─────────┐       ┌──────────┐       ┌─────────┐
   │ matrix  │       │    ai    │       │   mcp   │
   └─────────┘       └────┬─────┘       └─────────┘
                           │
                    ┌──────┴──────┐
                    │             │
                    ▼             ▼
               ┌────────┐   ┌─────────┐
               │ matrix │   │   mcp   │
               └────────┘   └─────────┘
```

## Dependencies

### Internal

无（这是最顶层内部包）

### External

各子包有独立的外部依赖，参见各自的 AGENTS.md

<!-- MANUAL: -->