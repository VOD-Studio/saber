# 通用平台接入系统

## 背景

Saber 已有平台无关的消息处理核心（`chat`、`conversation`、`agent`、`model`），Matrix 作为首个适配器实现了 `chat.Adapter`。但 `ai.Service` 仍直接 import `matrix` 包、持有 `matrix.CommandService`，`bot.go` 将 Matrix 初始化与 HTTP 服务生命周期交织在一起。目标是让任意聊天平台以相同方式接入，Saber 退化为「AI agent + 可插拔平台接入层」。

## 现状

```
bot.go
 ├─ initConfig
 ├─ if matrix.enabled → initMatrixClient (sync, event handlers, e2ee)
 ├─ initServices (ai.Service 构造时注入 matrixService + mediaService)
 ├─ setupEventHandlers (mautrix syncer 注册)
 ├─ startSync (Matrix sync goroutine)
 └─ server.Serve (HTTP server, 始终运行)
```

`ai.Service` 中的 Matrix 耦合：

- `import "rua.plus/saber/internal/matrix"`
- 字段 `matrixService *matrix.CommandService`、`mediaService *matrix.MediaService`
- `NewService` 签名要求 `matrixService`/`mediaService` 参数
- `handleAICommand` 直接创建 `matrix.NewChatAdapter`
- `promptProvider` 用 `id.RoomID`（Matrix 类型）
- 硬编码 `message.Session.Platform == "matrix"`

## 目标架构

```
bot.go
 ├─ initConfig
 ├─ ai.NewService(cfg, opts...)           ← 无平台依赖
 ├─ platform.Registry
 │   ├─ terminal.New(cfg)                 ← HTTP server 包装
 │   ├─ matrix.New(cfg)                   ← 现有 Matrix 逻辑
 │   ├─ violet.New(cfg)                   ← 新增
 │   └─ ...（Discord / Slack / Telegram）
 ├─ for p := range reg.Enabled()
 │     p.Start(ctx, aiService.HandleChat) ← 入站：平台→handler
 ├─ server.Serve(ctx, ...)                ← HTTP server 始终运行
 └─ shutdown: reg.Stop() + server.Close()
```

## 改造步骤

### 1. 提取 `internal/platform` 包

```go
// internal/platform/platform.go

// Platform 一个可插拔聊天平台接入端。
type Platform interface {
    // Name 平台标识，用于 chat.Session.Platform 和配置键
    Name() string
    // Start 连接平台并开始接收消息
    Start(ctx context.Context, handler chat.Handler) error
    // Stop 优雅断开
    Stop()
}

// Registry 平台注册表
type Registry struct{ platforms map[string]Platform }

func (r *Registry) Register(p Platform)
func (r *Registry) Enabled(cfg *config.Config) []Platform
```

每个 Platform 持有自己的 `chat.Adapter` 实现。`Start` 收到平台消息后构造 `chat.Message` 并调用 `handler`；`handler` 内部用 Platform 持有的 adapter 回复。

### 2. 解耦 `ai.Service`

```diff
- import "rua.plus/saber/internal/matrix"
- matrixService *matrix.CommandService
- mediaService  *matrix.MediaService

+ // 平台无关，由 Platform 注入
```

- `NewService` 改为函数式选项：`NewService(cfg, WithExecutor(...), WithMCP(...))`
- `HandleChat` 签名不变——已经是 `(ctx, chat.Message, chat.Adapter)`
- `PromptProvider` 接口的 `GetSystemPrompt` 参数从 `id.RoomID` 改为 `chat.Session`
- 移除 `handleAICommand` 中的 `matrix.NewChatAdapter` 创建——adapter 由平台注入
- 移除硬编码 `message.Session.Platform == "matrix"`——改为通用 prompt provider 接口
- Matrix 特有逻辑（reply body 提取等）移入 Matrix adapter 内部

### 3. Matrix 适配器改造

现有 `matrix.ChatAdapter` 已经实现了 `chat.Adapter`。改造为 `Platform` 实现：

```go
// internal/platform/matrix/matrix.go

type MatrixPlatform struct {
    cfg        *config.Config
    client     *matrix.MatrixClient
    adapter    *matrix.ChatAdapter
    handler    chat.Handler
    cancel     context.CancelFunc
}

func (m *MatrixPlatform) Name() string { return "matrix" }

func (m *MatrixPlatform) Start(ctx context.Context, handler chat.Handler) error {
    m.handler = handler
    m.adapter = matrix.NewChatAdapter(
        matrix.NewCommandService(m.client, ...),
        matrix.NewMediaService(m.client, ...),
        m.cfg.Matrix.Media,
        m.cfg.Matrix.StreamEdit.Enabled,
        handler,
    )
    // 现有 sync + event handler 注册逻辑
    go m.client.StartSyncWithReconnect(ctx, ...)
}

func (m *MatrixPlatform) Stop() { m.cancel() }
```

### 4. Terminal 适配器

现有 HTTP server (`internal/server`) 即 terminal 平台入口。包装为 `Platform`：

```go
// internal/platform/terminal/terminal.go

type TerminalPlatform struct {
    cfg *config.Config
}

func (t *TerminalPlatform) Name() string { return "terminal" }

func (t *TerminalPlatform) Start(ctx context.Context, handler chat.Handler) error {
    // 无需主动连接——HTTP server 已在 bot.go 中启动
    // server.New 内部的 session() 会在收到请求时调用 handler
    return nil
}

func (t *TerminalPlatform) Stop() {}
```

### 5. `bot.go` 重构

```go
func run(ctx context.Context, info BuildInfo) error {
    cfg := loadConfig()

    aiService := ai.NewService(cfg,
        ai.WithMCP(mcpManager),
        ai.WithExecutor(executor),
        ai.WithTasks(taskDir),
    )

    reg := platform.NewRegistry()
    reg.Register(terminal.New(cfg))
    reg.Register(matrix.New(cfg))
    // reg.Register(violet.New(cfg))  ← 新增平台在此注册

    for _, p := range reg.Enabled() {
        if err := p.Start(ctx, aiService.HandleChat); err != nil {
            slog.Warn("平台启动失败", "platform", p.Name(), "error", err)
        }
    }

    token, _ := server.Token(...)
    srv := server.New(aiService, token)
    return server.Serve(ctx, srv, listener)
}
```

### 6. 配置结构

```yaml
platforms:
  terminal:
    enabled: true

  matrix:
    enabled: false
    # 现有 Matrix 配置字段全部移入此节
    homeserver: ""
    user_id: ""
    # ...

  violet:
    enabled: false
    endpoint: "http://127.0.0.1:8080"
    bot_token: "${VIOLET_BOT_TOKEN}"
    account: "default"
    auto_reply_dm: true
    mention_reply: true
```

向后兼容：保留顶层 `matrix:` 节作为别名，加载时映射到 `platforms.matrix`。

### 7. HTTP API 增强

当前 `server.go` 中 `session()` 硬编码 `Platform: "terminal"`。改为支持请求指定平台（向后兼容）：

```
POST /v1/sessions/{session}/messages
X-Platform: violet         # 可选，默认 terminal
X-Account: blog.example.com
X-Sender-ID: user-uuid
```

## 新增平台适配器示例：Violet

Violet 是一个全栈博客平台，内置聊天系统。Saber 通过 Violet Bot API 以 bot 身份接入。

### 入站：SSE 订阅

Saber 订阅 Violet 的事件流，收到 bot 相关消息后构造 `chat.Message` 调用 `handler`：

```go
// internal/platform/violet/violet.go

func (v *VioletPlatform) Start(ctx context.Context, handler chat.Handler) error {
    go v.subscribeSSE(ctx, handler)
    return nil
}

func (v *VioletPlatform) subscribeSSE(ctx context.Context, handler chat.Handler) {
    for {
        req, _ := http.NewRequestWithContext(ctx, "GET",
            v.endpoint+"/api/v1/chat/bot/events", nil)
        req.Header.Set("Authorization", "Bearer "+v.botToken)

        resp, err := v.client.Do(req)
        if err != nil {
            select {
            case <-ctx.Done(): return
            case <-time.After(reconnectDelay): continue
            }
        }

        scanner := bufio.NewScanner(resp.Body)
        for scanner.Scan() {
            event := parseSSEEvent(scanner)
            if event.Type == "message.created" {
                msg := v.toChatMessage(event)
                go handler(ctx, msg, v.adapter)
            }
        }
        resp.Body.Close()
    }
}

func (v *VioletPlatform) toChatMessage(e BotEvent) chat.Message {
    return chat.Message{
        Session: chat.Session{
            Platform:     "violet",
            Account:       v.account,
            Conversation:  e.ConversationID,
        },
        ID:       e.Message.ID,
        SenderID: e.Message.SenderID,
        Text:     e.Message.Content,
        ReplyTo:  e.Message.ReplyToID,
    }
}
```

### 出站：`chat.Adapter` 实现

```go
// internal/platform/violet/adapter.go

type VioletAdapter struct {
    endpoint string
    token    string
    client   *http.Client
}

func (a *VioletAdapter) Capabilities() chat.Capabilities {
    return chat.Capabilities{Edit: true, Typing: true, Reply: true}
}

func (a *VioletAdapter) Send(ctx context.Context, reply chat.Reply) (string, error) {
    // POST /api/v1/chat/bot/conversations/{conversationId}/messages
    // body: { content, idempotency_key, reply_to_id }
    // 返回 Violet 消息 ID
}

func (a *VioletAdapter) Edit(ctx context.Context, messageID string, reply chat.Reply) error {
    // PATCH /api/v1/chat/bot/messages/{messageId}
    // body: { content }
}

func (a *VioletAdapter) SetTyping(ctx context.Context, session chat.Session, active bool) error {
    // POST /api/v1/chat/bot/conversations/{conversationId}/typing
    // body: { is_typing: active }
}
```

### 消息流转

```
Violet SSE: message.created
    │
    ▼
VioletPlatform.subscribeSSE
    │ 构造 chat.Message
    ▼
aiService.HandleChat(ctx, msg, violetAdapter)
    │
    ▼
conversation.Processor.Handle
    │
    ├─ agent.Runtime (AI 模型 + 工具)
    │
    ▼
chat.Presenter (流式回调)
    │
    ├─ Send()    → POST /bot/.../messages → 创建占位消息
    ├─ Edit()    → PATCH /bot/messages/{id} → 逐步更新
    ├─ SetTyping → POST /bot/.../typing
    └─ Finish()  → 最终 Edit 定稿
```

## 实施顺序

| 阶段 | 内容 | 依赖 |
|---|---|---|
| S1 | 提取 `platform.Platform` 接口 + `Registry` | 无 |
| S2 | 解耦 `ai.Service`：移除 `matrix` 包依赖，函数式选项 | S1 |
| S3 | Matrix 适配器改造为 `Platform` 实现（不破坏现有功能） | S2 |
| S4 | Terminal 适配器包装 | S2 |
| S5 | `bot.go` 重构：按配置启动平台 | S3, S4 |
| S6 | 配置结构迁移 + 向后兼容 | S5 |
| S7 | Violet 适配器实现 | S5 + Violet Bot API 就绪 |
| S8 | 联调验证 | S7 |

## 验收

```sh
# 核心包无 Matrix 依赖
go test -tags goolm ./internal/conversation -run TestCoreDependencies

# Matrix 适配器功能不回归
go test -tags goolm ./internal/matrix -run TestChatAdapter

# 内存 adapter 调用 HandleChat 无 Matrix 客户端
go test -tags goolm ./internal/ai -run TestService_HandleChat_MemoryWithoutMatrix

# 平台注册与启动
go test -tags goolm ./internal/platform
```

## 新增平台清单

开发者接入新聊天平台时：

1. 在 `internal/platform/<name>/` 创建包
2. 实现 `chat.Adapter`（出站：`Send`/`Edit`/`SetTyping`/`Capabilities`）
3. 实现 `Platform.Start`（入站：订阅事件流 / 注册 webhook / sync loop）
4. 在 `config.yaml` 的 `platforms.<name>` 下添加配置
5. 在 `bot.go` 中 `reg.Register(<name>.New(cfg))`
6. 如需 `!ai`/`!task` 等聊天命令：实现 `ai.ChatEntrypoint` 并 `aiService.SetChatEntrypoint(...)` + `aiService.RegisterTaskDelivery(name, adapter)`
7. 如需主动聊天：实现 `ai.ProactiveRooms`（见下文）并在 `initProactiveManager` 注入

## 实施进度

| 阶段 | 状态 | 落点 |
|---|---|---|
| S1 `platform` 接口与注册表 | 已完成 | `internal/platform/platform.go` |
| S2 解耦 `ai.Service` | 基本完成 | 函数式选项 `WithMatrix`/`WithMCP`；`PromptProvider` 用 `chat.Session`；`chat.Message.ControlText` 承接引用回退剥离；`handleAICommand`、`!task`/`!schedule` 与 `!ai` 子命令回执均经 `ChatEntrypoint`；主动聊天经 `ai.ProactiveRooms`；`ai` 内已无 `matrix.NewChatAdapter`，也不再调用 Matrix 普通消息发送；旧 `ResponseHandler`/`StreamEditor` 死代码已删除 |
| S3 Matrix 平台接入端 | 进行中 | `internal/platform/matrix` 已持有账号、出站/投递 adapter、会话与事件定位、主动聊天房间端口（`Rooms`），并注册任务投递；同步循环与事件处理器注册仍在 `internal/bot` |
| S4 Terminal 平台 | 未开始 | HTTP server 仍在 `bot.go` 直接 serve |
| S5 `bot.go` 按配置启动平台 | 部分 | `run()` 已经过 `Registry.Enabled()` 启动并统一 `Stop`，仅注册了 matrix |
| S6 配置结构迁移 | 未开始 | 仍为顶层 `matrix:` 节 |
| S7/S8 Violet 与联调 | 未开始 | — |

`ai` 剩余的 Matrix 触点：`internal/ai/task_logs.go` 的任务文件上传（已由 `identity.Session.Platform == "matrix"` 限定），以及 `internal/ai/service.go` 为注册 Matrix 命令、绑定旧历史账号与上传任务文件而保留的 `WithMatrix`。普通文本消息的发送已全部改经平台端口，`internal/ai/proactive*.go` 也不再引用 `internal/matrix`（房间元数据与主动投递经 `ai.ProactiveRooms`）。

出站正文的格式约定：`chat.Reply.Text` 以 Markdown 书写，渲染由平台 adapter 完成（Matrix 用 `format.RenderMarkdown` 转成 `org.matrix.custom.html`，并把原始 HTML 转义），`ai` 内不再出现任何平台标记语言。

## 平台端口 `ai.ChatEntrypoint`

接入一个平台需要提供五个能力，`internal/platform/matrix` 是参考实现：

| 方法 | 作用 |
|---|---|
| `HandleCommand` | 把平台聊天命令规范化为 `chat.Message`，按指定模型交给 `ai.Service` |
| `NormalizeCommand` | 把 `!task list` 这类命令文本规范化，并返回只做一次性回执的 adapter |
| `Session` | 解析平台账号、会话与线程作用域 |
| `OutboundAdapter` | 返回带平台展示能力（编辑、媒体）的出站 adapter |
| `EventID` | 返回触发本次处理的原生事件标识，供回复定位与幂等键使用 |

装配顺序见 `internal/bot/bot.go`：构造平台 → `aiService.SetChatEntrypoint` → `aiService.RegisterTaskDelivery` → `Registry.Register` → `Start(ctx, aiService.HandleChat)`。

## 平台端口 `ai.ProactiveRooms`

主动聊天没有入站消息可依据，需要平台提供四个能力；端口边界上一律使用字符串会话标识（`chat.ConversationInfo.Conversation`）：

| 方法 | 作用 |
|---|---|
| `ListConversations` | 枚举可主动投递的会话，静默检测与定时触发以此为扫描范围 |
| `ConversationInfo` | 读取单个会话的元数据（名称、成员数、是否端到端加密），用于决策上下文与欢迎语气 |
| `SendText` | 以普通消息投递主动内容，返回平台消息标识 |
| `SendNotice` | 以低优先级通知投递主动内容，返回平台消息标识 |

Matrix 实现见 `internal/platform/matrix/rooms.go` 的 `Rooms`：它包装 `matrix.RoomService`，把房间快照收敛成 `chat.ConversationInfo`（未命名房间的名称回退为房间 ID，`rooms_test.go` 的字段漂移测试强制新字段要么映射要么显式丢弃）；`ai` 侧拿不到元数据时用 `degradedConversation` 降级为普通群聊，不阻断决策。装配见 `internal/bot/bot.go` 的 `initProactiveManager`。

已知残留（不影响端口使用，列入后续阶段）：

- `ai` 内部仍以 mautrix `id.RoomID` 作状态/频控/缓存键，`ProactiveManager` 对 Matrix 事件侧暴露的方法也仍收 `id.RoomID`，接入第二平台时需把状态键收敛为 `chat.Session`（与同步循环、事件处理器搬迁同批做）。
- `SendText`/`SendNotice` 暂由本端口提供，因此主动消息还不走 `chat.Adapter` 的 Markdown 渲染；下一步应将投递归并到 `chat.Adapter`（需先给 `chat.Capabilities` 加 notice 能力），本端口只保留枚举与元数据。
