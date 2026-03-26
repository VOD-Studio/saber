<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-26 | Updated: 2026-03-26 -->

# qq

## Purpose

QQ 机器人适配器模块，提供 QQ 官方机器人 API 的 Go 封装。支持私聊消息、群 @ 消息处理，集成 AI 对话和命令系统。

## Key Files

| File | Description |
|------|-------------|
| `adapter.go` | QQ 适配器核心：WebSocket 连接管理、事件分发、生命周期控制 |
| `commands.go` | 命令系统：注册表、解析器、内置命令（ping/help/version） |
| `ai_command.go` | AI 命令处理器：`!ai` 系列命令实现 |
| `context.go` | 上下文管理器：用户对话历史管理 |
| `handlers.go` | 事件处理器：私聊消息、群 @ 消息处理 |

## For AI Agents

### Working In This Directory

- **依赖**: `github.com/tencent-connect/botgo` 是腾讯官方 QQ 机器人 SDK
- **适配器模式**: `Adapter` 封装所有 QQ 相关操作，与 Matrix 模块并行
- **命令系统**: 使用 `CommandRegistry` 注册和分发命令

### Testing Requirements

- 使用 Mock 客户端测试命令处理器
- 测试上下文管理器的并发安全性

### Common Patterns

#### 创建 QQ 适配器

```go
adapter, err := qq.NewAdapter(&cfg.QQ, &cfg.AI, aiService, &buildInfo)
if err != nil {
    log.Fatal(err)
}
defer adapter.Stop()

if err := adapter.Start(ctx); err != nil {
    log.Fatal(err)
}
```

#### 注册自定义命令

```go
registry := qq.NewCommandRegistry()
registry.Register("mycmd", &MyCommand{}, "自定义命令描述")
```

#### 事件处理流程

```
WebSocket Event → DefaultHandler → CommandRegistry.Parse()
                                    ↓
                           ┌───────────────────┐
                           │ 是命令？           │
                           └───────────────────┘
                              ↓            ↓
                           是: Dispatch   否: AI 回复（如启用）
```

## Available Commands

| 命令 | 描述 | 文件 |
|------|------|------|
| `!ping` | 检查机器人是否在线 | `commands.go` |
| `!help` | 列出所有可用命令 | `commands.go` |
| `!version` | 显示版本信息 | `commands.go` |
| `!ai <message>` | 与 AI 对话 | `ai_command.go` |
| `!ai clear` | 清除对话上下文 | `ai_command.go` |
| `!ai context` | 显示上下文信息 | `ai_command.go` |
| `!ai models` | 列出可用模型 | `ai_command.go` |
| `!ai switch <id>` | 切换默认模型 | `ai_command.go` |
| `!ai current` | 显示当前模型 | `ai_command.go` |

## Key Interfaces

### CommandHandler

```go
type CommandHandler interface {
    Handle(ctx context.Context, userID, groupID string, args []string, sender CommandSender) error
}
```

### CommandSender

```go
type CommandSender interface {
    Send(ctx context.Context, userID, groupID, message string) error
}
```

### SimpleAIService

```go
type SimpleAIService interface {
    IsEnabled() bool
    Chat(ctx context.Context, userID, message string) (string, error)
    ChatWithSystem(ctx context.Context, userID, systemPrompt, message string) (string, error)
}
```

## Dependencies

### Internal

- `rua.plus/saber/internal/config` - 配置定义（QQConfig, AIConfig）
- `rua.plus/saber/internal/ai` - AI 服务（SimpleService, ModelRegistry）

### External

- `github.com/tencent-connect/botgo` - QQ 官方机器人 SDK
- `github.com/tencent-connect/botgo/dto` - QQ 数据传输对象
- `github.com/tencent-connect/botgo/event` - QQ 事件处理器

<!-- MANUAL: -->