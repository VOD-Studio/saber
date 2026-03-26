<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-26 | Updated: 2026-03-26 -->

# bot

## Purpose

机器人生命周期管理包，负责应用初始化、服务编排和优雅关闭。是整个应用的"主控"模块，协调所有其他内部包。

## Key Files

| File | Description |
|------|-------------|
| `bot.go` | 核心初始化逻辑：配置加载、Matrix 客户端、服务初始化、事件处理 |
| `errors.go` | 自定义错误类型，支持退出码 |

## For AI Agents

### Working In This Directory

- **入口函数**: `Run(info matrix.BuildInfo) error`
- **初始化顺序**: 严格遵守 `initConfig → initMatrixClient → initServices → setupEventHandlers → startSync`
- **服务依赖**: 理解服务间的依赖关系至关重要

### Testing Requirements

- 使用 `bot.IsExitCode(err)` 检查是否为退出码错误
- 测试初始化流程时 Mock 外部依赖

### Common Patterns

#### 初始化流程

```go
// 完整初始化流程
state := &appState{info: info}
state.initConfig()           // 1. 配置加载
state.initMatrixClient()     // 2. Matrix 客户端
state.initServices()         // 3. AI/MCP/Proactive 服务
state.setupEventHandlers()   // 4. 事件处理器
state.startSync(ctx)         // 5. 开始同步
```

#### 优雅关闭

```go
// shutdown 方法并行关闭所有服务
// 使用 WaitGroup 和带超时的 context
```

## Dependencies

### Internal

- `rua.plus/saber/internal/ai` - AI 服务
- `rua.plus/saber/internal/cli` - 命令行解析
- `rua.plus/saber/internal/config` - 配置加载
- `rua.plus/saber/internal/matrix` - Matrix 客户端
- `rua.plus/saber/internal/mcp` - MCP 管理
- `rua.plus/saber/internal/meme` - Meme 服务

### External

- `github.com/lmittmann/tint` - 彩色日志输出
- `maunium.net/go/mautrix` - Matrix SDK
- `maunium.net/go/mautrix/event` - Matrix 事件类型

<!-- MANUAL: -->