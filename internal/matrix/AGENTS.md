<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-26 | Updated: 2026-03-26 -->

# matrix

## Purpose

Matrix 协议客户端封装包，提供连接管理、认证、事件处理、命令路由、媒体服务、在线状态和端到端加密支持。

## Key Files

| File | Description |
|------|-------------|
| `client.go` | Matrix 客户端封装，会话管理，E2EE 初始化 |
| `handlers.go` | 事件处理器，命令分发，并发控制 |
| `context.go` | Matrix 相关上下文键（EventID, MediaInfo 等） |
| `crypto.go` | E2EE 加密服务封装 |
| `media.go` | 媒体文件下载和处理 |
| `mention.go` | @mention 检测和处理 |
| `presence.go` | 在线状态管理和自动重连 |
| `reply.go` | 消息回复和引用 |
| `rooms.go` | 房间管理服务 |
| `testing_helpers.go` | 测试辅助函数 |

## For AI Agents

### Working In This Directory

- **构建标签**: 需要 `-tags goolm` 支持 E2EE
- **并发控制**: 使用 semaphore 限制并发事件处理

### Testing Requirements

- 使用 `testing_helpers.go` 中的辅助函数
- Mock Matrix 客户端进行测试

### Common Patterns

#### 创建 Matrix 客户端

```go
client, err := matrix.NewMatrixClient(&cfg.Matrix)
if err != nil {
    return fmt.Errorf("创建 Matrix 客户端失败: %w", err)
}
```

#### 处理命令

```go
cs := matrix.NewCommandService(mautrixClient, botID, &buildInfo)
cs.RegisterCommandWithDesc("ai", "与AI对话", aiCommand)
```

#### 发送消息

```go
// 发送文本
matrixService.SendText(ctx, roomID, content)

// 发送回复
matrixService.SendReply(ctx, roomID, content, eventID)

// 发送 HTML
matrixService.SendFormattedText(ctx, roomID, html, plain)
```

#### 上下文元数据

```go
// 设置事件 ID（用于回复）
ctx = matrix.WithEventID(ctx, eventID)

// 设置媒体信息
ctx = matrix.WithMediaInfo(ctx, mediaInfo)

// 获取用户上下文
ctx = appcontext.WithUserContext(ctx, userID, roomID)
```

## Dependencies

### Internal

- `rua.plus/saber/internal/config` - 配置定义
- `rua.plus/saber/internal/mcp` - MCP 管理（用于 MCP 命令）

### External

- `maunium.net/go/mautrix` - Matrix SDK
- `github.com/microcosm-cc/bluemonday` - HTML 净化
- `golang.org/x/sync/semaphore` - 并发控制

<!-- MANUAL: -->