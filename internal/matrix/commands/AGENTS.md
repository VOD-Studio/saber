<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-25 | Updated: 2026-03-25 -->

# commands

## Purpose

Matrix 机器人命令处理实现包，提供命令注册、分发和各种命令处理器。实现命令模式，支持基于前缀 (`!command`) 和基于提及 (`@bot:command`) 两种命令格式。

## Key Files

| File | Description |
|------|-------------|
| `register.go` | 命令注册表，管理命令注册、分发和解析 |
| `ai.go` | AI 相关命令：`!ai`, `!ai-clear`, `!ai-context`, `!ai-models`, `!ai-current`, `!ai-switch` |
| `help.go` | 帮助命令 `!help`，生成 HTML 表格格式的命令列表 |
| `meme.go` | Meme 相关命令：`!meme`, `!gif`, `!sticker` |
| `ping.go` | Ping 命令 `!ping`，响应 Pong |
| `version.go` | 版本命令 `!version`，显示构建信息 |

## For AI Agents

### Working In This Directory

- **接口优先**: 所有命令处理器实现 `CommandHandler` 接口
- **依赖注入**: 命令处理器通过构造函数接收服务接口
- **并发安全**: `Registry` 使用 `sync.RWMutex` 保护命令映射

### Testing Requirements

- Mock `Sender` 和 `TextOnlySender` 接口进行测试
- 测试命令解析逻辑（前缀格式和提及格式）
- 测试并发注册和分发

### Common Patterns

#### 定义命令处理器

```go
type MyCommand struct {
    sender Sender
    // 其他依赖...
}

func (c *MyCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
    // 处理命令...
    return c.sender.SendFormattedText(ctx, roomID, html, plain)
}
```

#### 注册命令

```go
registry := commands.NewRegistry(client, botID)
registry.RegisterWithDesc("help", "显示帮助信息", helpCmd)
registry.Register("ping", pingCmd)
```

#### 解析命令

```go
parsed := registry.Parse(messageBody)
if parsed != nil {
    info, ok := registry.Get(parsed.Command)
    if ok {
        info.Handler.Handle(ctx, userID, roomID, parsed.Args)
    }
}
```

## Available Commands

| 命令 | 描述 | 文件 |
|------|------|------|
| `!ai` | 与 AI 对话 | `ai.go` |
| `!ai-clear` | 清除对话上下文 | `ai.go` |
| `!ai-context` | 显示对话上下文信息 | `ai.go` |
| `!ai-models` | 列出可用模型 | `ai.go` |
| `!ai-current` | 显示当前模型 | `ai.go` |
| `!ai-switch` | 切换模型 | `ai.go` |
| `!help` | 显示帮助信息 | `help.go` |
| `!meme` | 搜索 Meme | `meme.go` |
| `!gif` | 搜索 GIF | `meme.go` |
| `!sticker` | 搜索贴纸 | `meme.go` |
| `!ping` | 测试响应 | `ping.go` |
| `!version` | 显示版本信息 | `version.go` |

## Key Interfaces

### CommandHandler

```go
type CommandHandler interface {
    Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error
}
```

### Sender

```go
type Sender interface {
    SendFormattedText(ctx context.Context, roomID id.RoomID, html, plain string) error
}
```

### AIService

```go
type AIService interface {
    Chat(ctx context.Context, roomID id.RoomID, userID id.UserID, message string) (string, error)
    ClearContext(roomID id.RoomID, userID id.UserID)
    GetContextInfo(roomID id.RoomID, userID id.UserID) string
    ListModels() []string
    GetCurrentModel() string
    SetModel(model string) error
}
```

## Dependencies

### Internal

无直接内部依赖（通过接口解耦）

### External

- `maunium.net/go/mautrix` - Matrix SDK
- `maunium.net/go/mautrix/id` - Matrix ID 类型

<!-- MANUAL: -->