<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-26 | Updated: 2026-03-26 -->

# mcp

## Purpose

MCP (Model Context Protocol) 集成包，提供服务器管理、工具发现、工具调用和速率限制功能。支持内置服务器、stdio 进程和 HTTP 三种连接类型。

## Key Files

| File | Description |
|------|-------------|
| `manager.go` | MCP 管理器，服务器生命周期和工具调用 |
| `factory.go` | 服务器工厂接口和默认工厂 |
| `config.go` | MCP 配置验证 |
| `tools.go` | 工具列表获取和处理 |
| `validation.go` | 参数验证 |
| `middleware.go` | 中间件（速率限制等） |
| `logging.go` | MCP 日志记录 |
| `testing_helpers.go` | 测试辅助函数 |

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `servers/` | 内置 MCP 服务器实现（见 `servers/AGENTS.md`） |

## For AI Agents

### Working In This Directory

- **工具调用流程**: `ListTools()` → `GetServerForTool()` → `CallTool()`
- **用户上下文要求**: 调用工具前必须通过 `WithUserContext()` 设置 userID 和 roomID

### Testing Requirements

- 使用 `testing_helpers.go` 中的辅助函数
- Mock MCP 服务器进行测试

### Common Patterns

#### 创建 MCP 管理器

```go
mgr := mcp.NewManagerWithBuiltin(&cfg.MCP)
mgr.InitBuiltinServers(ctx)
```

#### 获取可用工具

```go
tools := mgr.ListTools()
for _, tool := range tools {
    fmt.Println(tool.Name, tool.Description)
}
```

#### 调用工具

```go
// 必须设置用户上下文
ctx = appcontext.WithUserContext(ctx, userID, roomID)

// 获取工具所在服务器
serverName := mgr.GetServerForTool(toolName)

// 调用工具
result, err := mgr.CallTool(ctx, serverName, toolName, args)
```

## Dependencies

### Internal

- `rua.plus/saber/internal/config` - 配置定义
- `rua.plus/saber/internal/context` - 上下文键
- `rua.plus/saber/internal/mcp/servers` - 内置服务器

### External

- `github.com/modelcontextprotocol/go-sdk/mcp` - MCP SDK

<!-- MANUAL: -->