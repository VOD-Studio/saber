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

> 实施注记（S6 现状）：落地时只做到了「新平台的配置一律在 `platforms.<name>`，
> 注册表与启停完全由 `Config.PlatformEnabled` 决定」。Matrix 的明细配置仍在顶层
> `matrix:` 节，`platforms.matrix` 尚未接管：直接把整节搬过去，会让只写了
> `platforms.matrix.enabled` 的配置把其余字段的默认值一起覆盖掉，而用 `yaml.Node`
> 局部解码又会丢掉现有 `KnownFields(true)` 的未知字段检查。这两条语义要先定清楚，
> 属独立批次；接入端与配置读取仍按 `cfg.Matrix` 取值。

### 7. HTTP API 增强（未实施）

当前 `server.go` 中 `session()` 硬编码 `Platform: "terminal"`。改为支持请求指定平台（向后兼容）：

```
POST /v1/sessions/{session}/messages
X-Platform: violet         # 可选，默认 terminal
X-Account: blog.example.com
X-Sender-ID: user-uuid
```

## 新增平台适配器示例：Violet

Violet 是一个全栈博客平台，内置聊天系统。Saber 通过 Violet Bot API 以 bot 虚拟用户身份接入，实现在 `internal/platform/violet`（包名 `violetplatform`）。端点与语义以 Violet 侧 `/api/v1/openapi.json` 的「聊天 Bot」标签为准：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/v1/chat/bot/profile` | 取 `user_id`/`username`：自回声过滤与被 @ 判定的依据 |
| GET | `/api/v1/chat/bot/events` | SSE 事件流（入站） |
| GET | `/api/v1/chat/bot/conversations` | 会话列表，首次连接用来打水位基线与预热形态缓存 |
| GET | `/api/v1/chat/bot/conversations/{cid}` | 会话详情，主要取 `kind`（`direct`/`room`） |
| GET | `/api/v1/chat/bot/conversations/{cid}/messages` | 历史（新→旧，cursor 分页），也是断线恢复通道 |
| POST | `/api/v1/chat/bot/conversations/{cid}/messages` | 创建 `pending` 回复或普通文本消息，`Idempotency-Key` 头必填 |
| PATCH | `/api/v1/chat/bot/conversations/{cid}/messages/{mid}` | 更新同一回复的累计正文、公开思考摘要和状态；路径带 cid 是因为服务端按会话做归属校验 |
| POST | `/api/v1/chat/bot/conversations/{cid}/typing` | 上报输入状态，204 无响应体 |

成功响应统一是 `{"data": ..., "meta": {"pagination": ...}}`，失败是 `{"error": CODE, "message": ...}`；接入端把状态码留在 `apiError` 里，因为 401/403（凭据问题，重试无用）与 5xx/网络错误（该退避重连）的处置完全不同。

### 入站：SSE 订阅与断线补拉

`violet.go` 的 `supervise` 维持「连接—读帧—退避重连」循环（1s 起、60s 封顶、带抖动），`sse.go` 按规范逐帧解析（多行 `data:` 以 LF 连接，注释与 `id:`/`retry:` 行忽略，未以空行收尾的残帧丢弃）。服务端每 30 秒发一次心跳注释帧，因此 90 秒没有字节就判定链路被掐断并重连。普通 API 请求带 `http_timeout_seconds` 总超时，事件流单独用不设总超时的 client——否则长连接会在超时那一刻被自己掐掉。

`message.created` 的 data 自带消息全文，规范化成 `chat.Message` 时做四件事：

1. `@(username:uuid)` 降级成 `@username`（uuid 对模型是噪声，@name 才是「在跟谁说话」），`@(all:all)` 降级成 `@all`；自定义表情 token `[name:uuid]` 原样保留
2. 只有提及没有正文的消息（`mentionResidue` 为空）不触发回答
3. `sender.id == profile.user_id` 的自回声、`type != text`、已删除、缺发送者的消息一律跳过——服务端已不向 bot 投递 bot 自己的消息，这里再挡一道
4. 会话形态按 `kind` 决定：`direct` 受 `direct_chat_auto_reply` 控制，`room`（含形态查不到的情况）必须被点名且受 `group_chat_mention_reply` 控制。形态取不到时按群聊从严：宁可漏答一条私聊，也不要在没被 @ 的房间里插话

协议**不写 `id:` 行、不支持 `Last-Event-ID` 补发**，所以恢复通道是消息历史：每个会话记 `created_at` 水位，重连后按水位之后补拉（最多 2 页 × 50 条，超出只补最近部分并告警），反转成时间正序再入队，首次连接只打基线（否则每次启动都会把站内旧消息当新问题回答一遍）。SSE 与补拉必然重叠，因此入站先过一道按消息 ID 的有界去重集，再进 4 worker + 有界队列（满时背压等待而非丢弃）。

### 出站：`chat.Adapter`

`adapter.go` 声明 `Edit`/`Typing`/`Reply`/`ReplyState` 四项能力，`Send` 返回平台消息 ID 供后续编辑，`Reply.TransactionID` 原样作 `Idempotency-Key`（Violet 按 (会话, 发送者, 幂等键) 唯一，同键重发返回同一条消息；超过列宽 128 的键收敛为 SHA-256）。两处平台自己的约束：

- 正文按 Unicode 字符截到 10000 以内并留截断标记；只有 `pending`、`thinking`、`failed` Bot 回复可使用空正文，普通文本消息仍拒绝空白正文
- 编辑有全局最小间隔节流（`edit_interval_ms`，默认 200ms ≈ 300 次/分钟配额）。后台任务合并模型增量后最多按此频率更新；最终投递也走 `Edit`，失败时按原幂等键重试

Violet 当前的写协议使用顶层字段：POST `{"content":"","reply_to_id":"...","status":"pending"}` 创建占位消息；PATCH `{"content":"...","thinking":"...","status":"streaming","revision":1}` 提交累计快照，之后每次修订递增 `revision`。失败使用 `status=failed`，报错写入 `content`，已生成正文接在报错后面。Violet 的读模型仍把状态放在 `bot_reply` 中；`sender_kind` 和 `updated_at` 由服务端维护，历史和 `message.updated` SSE 返回同一快照。Violet 原子更新正文、thinking、状态和版本，拒绝终态回退；后台 Bot 开关控制 thinking 的保存和返回。旧 Bot 省略 `status` 时仍走普通文本消息行为。

占位 POST 失败后，任务流不按 200ms 编辑节拍重复发送；网络错误指数退避，429 依照 `Retry-After` 等待。聊天链路在创建任务前失败时，Saber 发送一条 `pending → failed` 的错误卡片；任务执行失败时由持久化终态投递把错误与部分正文放进原卡片。服务端拒绝写入时只能记录错误并等待可重试的终态投递，无法绕过 Violet 限流。

### 消息流转

```
Violet SSE: message.created
    │
    ▼
violetplatform.Platform.supervise → connectOnce → readSSEFrames
    │ admit（去重 + 推水位）→ 4 worker 队列 → normalizeMessage
    │ 构造 chat.Message
    ▼
aiService.HandleChat(ctx, msg, violetAdapter)
    │ 持久化任务 → POST pending 占位消息
    ▼
agent.Runtime → task_events（完整增量与执行记录）
    │ 公开思考摘要与正文增量异步合并
    ├─ Edit() → PATCH thinking/streaming（≥edit_interval_ms；静默时续期）
    ▼
任务终态入库 → 幂等 Send() 取回同一消息 ID → Edit() 提交 completed/failed；失败重试
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

# 关掉 Matrix 也能注册并启动 violet（含 run() 起停）
go test -tags goolm ./internal/bot -run Violet

# Violet 接入端：假服务端覆盖 SSE/补拉/去重/提及剥离/自回声/节流/幂等
go test -tags goolm -race -cover ./internal/platform/violet
```

S8 还需要一台真实 Violet 实例（管理端 `/admin/chat-bots` 注册 bot 并取回明文凭据）做一次联调，逐项确认：

1. 私聊直接提问能被回答，回答是原地编辑定稿的一条消息（不是刷屏多条）
2. 群聊只有被 `@` 时回答；正文里的 `@(saber:uuid)` 以可读形式出现，不带 uuid
3. 自己发的消息不会引来自答（自回声）
4. 回答中途重启 Saber，重连后断线期间的提问被补答且只答一次；启动时不会回放旧消息
5. 长回答不被限流打断（编辑间隔 ≥ 平台配额），超过 10000 字符的答案被截断而不是发送失败
6. `!ai xxx` 被当提问回答，`!task`/`!schedule` 在本平台被跳过而不是一本正经地回答命令

## 新增平台清单

开发者接入新聊天平台时：

1. 在 `internal/platform/<name>/` 创建包（包名沿用 `<name>platform`，目录名 `<name>`）
2. 实现 `chat.Adapter`（出站：`Send`/`Edit`/`SetTyping`/`Capabilities`）。`chat.Reply.Text` 用 Markdown 书写，富文本渲染由 adapter 自己决定；`Reply.TransactionID` 非空时必须当作幂等键使用，任务重发才不会刷两遍屏
3. 实现 `Platform.Start`（入站：订阅事件流 / 注册 webhook / sync loop）。把平台消息规范化成 `chat.Message` 后调用共享 handler：`ControlText` 只在平台有「引用回退包装」时给出，否则留 nil
4. 在 `config.yaml` 的 `platforms.<name>` 下添加配置，并在 `config.Config.PlatformEnabled` 登记该平台的开关——注册表只认这个映射，未登记的平台即使注册了也不会启动
5. 在 `internal/bot/bot.go` 的 `initServices` 里 `s.registerPlatform(<name>platform.New(s.cfg), aiService)`。注册点必须留在 Matrix 专属装配（人格、`!ai` 命令、主动聊天、meme）的提前 return 之前，否则关掉 Matrix 就什么都注册不了
6. 如需接收任务与定时计划结果：实现可选端口 `platform.TaskDelivery`（`DeliveryAdapter() chat.Adapter`），`registerPlatform` 会据此注册投递器；不实现就只接即时消息
7. 如需 `!ai`/`!task` 等聊天命令：实现 `ai.ChatEntrypoint` 并 `aiService.SetChatEntrypoint(...)`。当前它是**单例 setter 且签名带 mautrix `id.UserID`/`id.RoomID`**，同一进程只能挂一个平台（现在是 Matrix）；第二平台要挂命令入口，得先把这个端口改成平台无关并按平台名注册
8. 如需主动聊天：实现 `ai.ProactiveRooms`（见下文）并在 `initProactiveManager` 注入。没有 notice 语义的平台要么把 `SendNotice` 降级成普通文本，要么像 Violet 一样干脆不接

## 实施进度

| 阶段 | 状态 | 落点 |
|---|---|---|
| S1 `platform` 接口与注册表 | 已完成 | `internal/platform/platform.go` |
| S2 解耦 `ai.Service` | 基本完成 | 函数式选项 `WithMatrix`/`WithMCP`；`PromptProvider` 用 `chat.Session`；`chat.Message.ControlText` 承接引用回退剥离；`handleAICommand`、`!task`/`!schedule` 与 `!ai` 子命令回执均经 `ChatEntrypoint`；主动聊天经 `ai.ProactiveRooms`；`ai` 内已无 `matrix.NewChatAdapter`，也不再调用 Matrix 普通消息发送；旧 `ResponseHandler`/`StreamEditor` 死代码已删除 |
| S3 Matrix 平台接入端 | 进行中 | `internal/platform/matrix` 已持有账号、出站/投递 adapter、会话与事件定位、主动聊天房间端口（`Rooms`），并注册任务投递；同步循环与事件处理器注册仍在 `internal/bot` |
| S4 Terminal 平台 | 未开始 | HTTP server 仍在 `bot.go` 直接 serve |
| S5 `bot.go` 按配置启动平台 | 已完成 | `run()` 经 `Registry.Enabled()` 启动并统一 `Stop`；注册表按 `platforms.<name>` 决定启停，注册点不再被 Matrix 的提前 return 挡死，已注册 matrix + violet |
| S6 配置结构迁移 | 部分 | 新增 `platforms:` 节与 `Config.PlatformEnabled`，`platforms.terminal.enabled`、`platforms.violet.*` 已迁入；Matrix 明细仍在顶层 `matrix:` 节（见下文「配置结构」注） |
| S7 Violet 适配器 | 已完成 | `internal/platform/violet`：SSE 订阅 + 断线按消息历史补拉 + 提及剥离 + 自回声过滤 + 幂等发送 + 编辑节流；单测覆盖 86.7% |
| S8 联调验证 | 未开始 | 需要一台开好 Bot 凭据的真实 Violet 实例（`/admin/chat-bots` 注册），验证见文末「验收」 |

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

装配顺序见 `internal/bot/bot.go`：构造平台 → （Matrix 才有）`aiService.SetChatEntrypoint` → `s.registerPlatform` 按 `platform.TaskDelivery` 注册任务投递并 `Registry.Register` → `run()` 按 `Registry.Enabled()` 依次 `Start(ctx, aiService.HandleChat)`。

`ai.ProactiveRooms` 与 `ai.ChatEntrypoint` 都是可选端口：Violet 两个都不挂，只走 `HandleChat`。`TaskDelivery`（`DeliveryAdapter`）也是可选端口，matrix 与 violet 都实现了它。

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

Violet 接入带出来的缺口（属有意取舍，不是待修的 bug）：

- `ai.ChatEntrypoint` 是单例 setter 且签名带 mautrix 类型，命令入口目前由 Matrix 独占。Violet 因此只走 `HandleChat`：`!ai <内容>` 剥前缀当提问，`!task`/`!schedule`/上下文命令在本平台被跳过。要消掉这条，需要把端口改成平台无关 + 按平台名注册多个入口。
- 任务产出的文件不投递：`internal/ai/task_logs.go` 的上传仍按 `Session.Platform == "matrix"` 限定，Violet 的 Bot API 也只开放文本消息，没有媒体上传通道。
- 主动聊天不接 Violet：`ai.ProactiveRooms.SendNotice` 没有对等语义（Violet 聊天没有低优先级通知类型），房间元数据一侧的 `kind`/成员数虽然够用，接进来也得先决定 notice 怎么降级。
- Matrix 的同步循环与事件处理器注册仍在 `internal/bot`，Violet 的连接生命周期则在平台接入端内部——两边对称之前，`Platform.Start` 的语义仍不统一（S3 未收尾的部分）。
