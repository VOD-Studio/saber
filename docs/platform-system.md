# 通用平台接入系统

## 背景

Saber 已有平台无关的消息处理核心（`chat`、`conversation`、`agent`、`model`），Matrix 作为首个适配器实现了 `chat.Adapter`。但 `ai.Service` 仍直接 import `matrix` 包、持有 `matrix.CommandService`，`bot.go` 将 Matrix 初始化与 HTTP 服务生命周期交织在一起。目标是让任意聊天平台以相同方式接入，Saber 退化为「AI agent + 可插拔平台接入层」。

## 现状

```
bot.go
 ├─ initConfig
 ├─ if platforms.matrix.enabled → initMatrixClient（登录 + E2EE 初始化，不含 sync）
 ├─ if platforms.terminal.enabled → 预绑定监听（端口冲突在副作用之前失败）
 ├─ initServices
 │   ├─ ai.Service + 任务/人格/meme/MCP
 │   └─ Registry：terminal → violet → matrix（注册顺序即启动顺序）
 ├─ for p := range reg.Enabled() → p.Start(ctx, handleChat)
 │   ├─ terminal.Start：创建令牌、在后台 serve /v1/*，Stop 关闭监听并等待
 │   └─ matrix.Start：注册 EventHandler/presence/自动进房，后台跑 StartSyncWithReconnect
 └─ shutdown → reg.Stop()（取消同步、关闭 HTTP、关闭 AI/MCP/记忆）
```

`run()` 不再自己注册事件、起 sync 或 serve HTTP：这三件事分别由 `matrix` 与
`terminal` 平台在 `Start`/`Stop` 内完成，`bot` 只按注册表顺序转发生命周期。

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
 │       ├─ terminal：自建 HTTP 监听并 serve
 │       └─ matrix：注册事件 + 后台 sync
 └─ shutdown: reg.Stop()（terminal 关监听、matrix 取消同步）
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

现有 `matrix.ChatAdapter` 已经实现了 `chat.Adapter`。平台接管会话生命周期，
登录与 E2EE 仍留在装配层（需要口令、pickle key 等凭据），装配完把依赖注入平台：

```go
// internal/platform/matrix/matrix.go
type Platform struct {
    session Session              // 会话运行面：Register/Online/AutoJoin/Sync
    events  *matrix.EventHandler // 已装配的入站处理器
    cancel  context.CancelFunc
    done    chan struct{}
}

func New(cfg *config.Config, commands *matrix.CommandService, media *matrix.MediaService, opts ...Option) *Platform

func (p *Platform) Start(ctx context.Context, handler chat.Handler) error {
    // 幂等：已在运行时直接返回；Register 失败则中止
    p.session.Register(p.events)   // syncer.OnSync / OnEventType(message|member)
    p.session.Online()             // presence 失败只告警
    p.session.AutoJoin()
    go p.session.Sync(runCtx)      // presence.StartSyncWithReconnect
}

func (p *Platform) Stop() { p.cancel(); <-p.done }  // 幂等
```

`Session` 让同步与事件注册可替换，`lifecycle_test.go` 用替身验证「Start 只注册并启动
一次同步、重复 Start/Stop 安全、Stop 取消并等待退出」；真实实现
`mautrixSession`（`session.go`）包住 `MatrixClient` 与 `PresenceService`。

`bot` 侧只保留装配：`initMatrixClient`（登录、会话保存、E2EE）→ `newMatrixPlatform`
注入 `NewSession(client, autoJoinRooms)` 与 `newEventHandler()`（含主动聊天管理器）。

### 4. Terminal 适配器

`internal/platform/terminal` 把 `internal/server` 的 HTTP 入口包装成平台：

```go
// internal/platform/terminal/terminal.go
func New(listen, tokenPath string, service *ai.Service,
    commands func() server.CommandDispatcher, opts ...Option) *Platform

func (p *Platform) Start(ctx context.Context, handler chat.Handler) error {
    token, err := server.Token(p.tokenPath, true) // 令牌只在真正启动时生成
    listener := p.prebound                        // 否则 net.Listen(p.listen)
    go server.Serve(runCtx, srv, listener)        // 不阻塞 Start
    return nil
}

func (p *Platform) Stop() { p.cancel(); srv.Close(); <-p.done }  // 幂等
```

设计要点：

- `platforms.terminal.enabled=false` 时平台不注册、`run()` 不预绑定监听、不生成令牌：
  `saber chat` 拿不到令牌会明确报错「请先运行 saber serve」，而不是静默连一个不存在的服务。
- 命令分发器用 `func() server.CommandDispatcher` 延迟解析：命令注册表在平台登记之后才装配完成。
- `WithListener` 允许 `run()` 在初始化服务之前先占住端口，让端口冲突早于 `tasks.db`
  等副作用失败；终端平台接管该监听并在 `Stop` 时关闭。
- 本机入站的回复路径是 `ai.Service` + 共享命令注册表，因此 `Start` 的 `chat.Handler`
  参数仅为满足接口，HTTP 请求不经过它。

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
            // 终端入口起不来视为致命错误：进程没有本机入口就没有意义
            return err
        }
    }
    <-ctx.Done()
    return nil // 平台在 shutdown 里统一 Stop
```

注意：平台的 `SetCommands` 目录快照在 `buildCommands()` 结束时按 `Enabled()` 推送，
所以注册必须发生在 `buildCommands()` 之前；`terminal` 用延迟解析的命令分发器绕开
「命令注册表晚于平台登记」的循环依赖。

### 6. 配置结构

```yaml
platforms:
  terminal:
    enabled: true
  matrix:
    enabled: false
  violet:
    enabled: false
matrix:
  homeserver: "https://matrix.org"
  user_id: "@bot:matrix.org"
```

Matrix 的启用开关已移到 `platforms.matrix.enabled`；账号、媒体等明细仍在顶层
`matrix:` 节，接入端仍按 `cfg.Matrix` 读取明细。将整节搬到 `platforms.matrix`
还需要决定默认值合并与旧配置兼容语义。

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
| POST | `/api/v1/chat/bot/avatar` | multipart 单个 `file`，上传并替换当前 Bot Token 所属账号头像；返回最新 profile |
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

图片由 `chat.ImageAdapter` 上传并发送，帮助、命令目录和分发从注册的出站 adapter 推导媒体能力。Violet 可使用 `/meme`，配置仍沿用 `matrix.meme`（`enabled` 与 Klipy `api_key`），不要求启用 Matrix；入站回复包装保留图片接口并记录发送成功。Violet 尚不实现 `chat.FileAdapter`，因此不公开 `/task logs`。

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

## Telegram 接入（`internal/platform/telegram`）

Telegram 走 Bot API 长轮询（`getUpdates`），不需要公网地址、证书或 webhook 注册，部署在 NAT 后面即可用。出站用 `sendMessage` / `editMessageText` / `sendChatAction`，adapter 声明 `Edit/Typing/Reply`，没有图片与文件能力。

```yaml
platforms:
  telegram:
    enabled: false
    bot_token: "123456:ABC-DEF..."   # BotFather 签发
    api_base: "https://api.telegram.org"  # 自建反代或测试假 API
    account: telegram                # 参与历史键，留空即 telegram
    direct_chat_auto_reply: true
    group_chat_mention_reply: true
    edit_interval_ms: 200
    http_timeout_seconds: 15
    poll_timeout_seconds: 30
```

- **入站**：只处理 `message` 更新，`edited_message`/`channel_post` 与无正文消息跳过；`from.is_bot` 与 `getMe` 得到的自身 ID 一并过滤。私聊按 `direct_chat_auto_reply`；群组/超级群要求 `@botusername` 或回复 bot 的消息（`group_chat_mention_reply`），频道消息一律跳过。提及按实体（含 `text_mention`）与文本兜底识别，剥离后交给共享 Handler；`/cmd@bot` 剥离后仍能被命令分发器识别。
- **会话**：`Conversation = chat_id`，`Thread = message_thread_id`（论坛话题），回复带 `reply_parameters`。
- **限制**：正文按 4000 字符截断（平台上限 4096）；发送使用纯文本，不解析 MarkdownV2（避免转义失败导致整条消息被拒）；Telegram 没有幂等发送键，任务重试可能重复发一条；编辑与上一帧相同时按成功处理。

## Discord 接入（`internal/platform/discord`）

出站走 REST（`POST/PATCH /channels/{id}/messages`），入站走 Gateway WebSocket（`/gateway/bot` 取地址，Identify/Heartbeat/READY/MESSAGE_CREATE）。adapter 声明 `Edit/Typing/Reply`。

```yaml
platforms:
  discord:
    enabled: false
    bot_token: "..."                 # 开发者后台 Bot → Reset Token
    api_base: "https://discord.com/api/v10"
    account: discord
    direct_chat_auto_reply: true
    group_chat_mention_reply: true
    edit_interval_ms: 200
    http_timeout_seconds: 15
```

- **开发者后台**：必须为 bot 打开 **Message Content Intent**（`MESSAGE_CONTENT`，特权 intent），否则频道与私聊的 `content` 是空串，接入端只会看到附件消息并跳过。Bot 还需 `bot` scope 与发送消息/读消息历史的权限；邀请到服务器后要给它目标频道的 View Channel / Send Messages / Read Message History。
- **入站**：跳过 `author.bot`、自身 ID、类型非 0/19 的系统事件与空正文（纯附件消息当前不支持）；私聊（无 `guild_id`）按 `direct_chat_auto_reply`，服务器频道要求 `<@bot>` 提及或回复 bot 的消息。发送统一带 `allowed_mentions.parse=[]`，模型正文里的 `@everyone` 不会变成真实 ping。
- **会话**：`Conversation = channel_id`（Discord 线程本身就是独立频道，因此 `Thread` 留空）；回复带 `message_reference`。
- **限制**：正文按 1900 字符截断（平台上限 2000）；Gateway 断线按 1s..60s 指数退避重连并重新 Identify（不实现 Resume，断线期间的事件不补发）；`TransactionID` 哈希成 25 字符 `nonce` 并 `enforce_nonce`，任务重试在 Discord 的去重窗口内不会重复发送。

## Slack 接入（`internal/platform/slack`）

入站走 Socket Mode（`apps.connections.open` 换取 WebSocket，事件先 ack `envelope_id` 再处理），出站走 Web API（`chat.postMessage` / `chat.update`）。adapter 声明 `Edit/Reply`，`SetTyping` 是空操作。

```yaml
platforms:
  slack:
    enabled: false
    bot_token: "xoxb-..."            # Bot User OAuth Token
    app_token: "xapp-..."            # App-Level Token，需 connections:write
    api_base: "https://slack.com/api"
    account: slack
    direct_chat_auto_reply: true
    group_chat_mention_reply: true
    edit_interval_ms: 200
    http_timeout_seconds: 15
```

- **Slack 应用配置**：开启 **Socket Mode**；App-Level Token 需要 `connections:write`；Bot Token 至少需要 `chat:write`、`channels:history`、`groups:history`、`im:history`、`mpim:history`、`app_mentions:read`；Event Subscriptions 订阅 `message.im`、`message.channels`、`message.groups`、`message.mpim`。Socket Mode 不需要公网 Request URL。
- **入站**：跳过带 `subtype`（含 `message_changed`/`message_deleted`/`bot_message`）、`bot_id`、自身 `user_id`、`edited` 与空正文的事件；私聊（`channel_type=im`，或频道 ID 以 `D` 开头）按 `direct_chat_auto_reply`，频道要求 `<@bot>` 提及或在线程里回复 bot 的消息（接入端记录自己发出的 ts 判断线程父消息）。
- **会话**：`Conversation = channel`，`Thread = thread_ts`；顶层消息的回复会以该消息为父开线程，任务投递沿用同一会话与线程。
- **限制**：正文按 39000 字符截断（平台上限约 40000）；发送前转义 `&`/`<`/`>`，避免正文里的 `<@U...>`、`<!everyone>` 被解析成提及或链接（代价是 `<http://...|text>` 形式的 Slack 链接会显示成字面量）；Slack 没有幂等发送键，任务重试可能重复发一条；Socket Mode 连接断开由服务端 `disconnect` 或读错误触发，按 1s..60s 指数退避重连。

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

# Matrix 平台生命周期：Start 只启动一次 sync、Stop 取消并等待、重复调用安全
go test -tags goolm ./internal/platform/matrix -run Lifecycle

# 终端平台：Start 监听可用、Stop 后连接关闭、关闭开关时 bot 不监听
go test -tags goolm ./internal/platform/terminal
go test -tags goolm ./internal/bot -run TestRun_Terminal

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
5. 在 `internal/bot/bot.go` 的 `initServices` 里 `s.registerPlatform(<name>platform.New(s.cfg), aiService)`。注册点必须在 `buildCommands()` 之前（它在末尾按 `Enabled()` 推送命令目录），并且连接生命周期放在平台自己的 `Start`/`Stop` 内，`run()` 只转发注册表的启动与停止
6. 如需接收任务与定时计划结果：实现可选端口 `platform.TaskDelivery`（`DeliveryAdapter() chat.Adapter`），`registerPlatform` 会据此注册投递器；不实现就只接即时消息
7. 如需 `!ai`/`!task` 等聊天命令：实现 `ai.ChatEntrypoint` 并 `aiService.SetChatEntrypoint(...)`。当前它是**单例 setter 且签名带 mautrix `id.UserID`/`id.RoomID`**，同一进程只能挂一个平台（现在是 Matrix）；第二平台要挂命令入口，得先把这个端口改成平台无关并按平台名注册
8. 如需主动聊天：实现 `ai.ProactiveRooms`（见下文）并在 `initProactiveManager` 注入。没有 notice 语义的平台要么把 `SendNotice` 降级成普通文本，要么像 Violet 一样干脆不接
9. 如需推文/公开内容互动：实现可选的 `chat.TweetAdapter`（推文与评论的读写端口），由 `ai.Service.SetTweetAdapter` 注入；平台再用 `SetTweetHandler(aiService.HandleTweet)` 把 SSE/回调事件翻译成合成 `chat.Message` 交给公开回复链路。两个端口都是可选的，不实现时 `saber_tweet` 不会出现在工具列表，普通聊天不受影响
10. 如需表情：实现可选的 `chat.EmojiAdapter`（`ListEmojis`/`FavoriteEmoji`/`UnfavoriteEmoji`），由 `ai.Service.SetEmojiAdapter` 注入。入站正文里的表情 token 若被平台剥离，应通过 `chat.Message.Context` 把 token 清单与复用说明交还模型；不实现时 `saber_emoji` 不会出现在工具列表
11. 如需自主换头像：实现 `chat.AvatarAdapter`（`GetAvatar`/`SetAvatar`），通过 `ai.Service.SetAvatarAdapter(platformName, adapter)` 按平台注入。`saber_avatar` 在执行时复核可信账号和 `commands.admins` 写权限，文件来源还需当前工作区的 `read_file` 授权；平台只能操作自身账号，不能接受模型生成的目标用户。

## 实施进度

| 阶段 | 状态 | 落点 |
|---|---|---|
| S1 `platform` 接口与注册表 | 已完成 | `internal/platform/platform.go` |
| S2 解耦 `ai.Service` | 基本完成 | 函数式选项 `WithMatrix`/`WithMCP`；`PromptProvider` 用 `chat.Session`；`chat.Message.ControlText` 承接引用回退剥离；`handleAICommand`、`!task`/`!schedule` 与 `!ai` 子命令回执均经 `ChatEntrypoint`；主动聊天经 `ai.ProactiveRooms`；`ai` 内已无 `matrix.NewChatAdapter`，也不再调用 Matrix 普通消息发送；旧 `ResponseHandler`/`StreamEditor` 死代码已删除 |
| S3 Matrix 平台接入端 | 已完成 | `internal/platform/matrix`：账号与出站/投递 adapter、主动聊天房间端口（`Rooms`）、`Session` 运行面（`session.go` 真实实现 + `lifecycle_test.go` 替身）；`Start` 内注册事件、上报 presence、自动进房并跑 `StartSyncWithReconnect`，`Stop` 取消并等待；`bot` 只保留登录与 E2EE 装配 |
| S4 Terminal 平台 | 已完成 | `internal/platform/terminal`：`Start` 创建令牌并在后台 serve `internal/server`，`Stop` 关闭监听并等待；支持复用 `run()` 预绑定的监听；关闭开关时不注册、不监听、不生成令牌 |
| S5 `bot.go` 按配置启动平台 | 已完成 | `run()` 经 `Registry.Enabled()` 按注册顺序（terminal → violet → matrix）启动并统一 `Stop`；`run()` 内不再有事件注册、sync 或 HTTP serve |
| S6 配置结构迁移 | 部分 | 新增 `platforms:` 节与 `Config.PlatformEnabled`，`platforms.terminal.enabled`、`platforms.matrix.enabled`、`platforms.violet.*` 已迁入；Matrix 明细仍在顶层 `matrix:` 节（见上文「配置结构」注） |
| S7 Violet 适配器 | 已完成 | `internal/platform/violet`：SSE 订阅 + 断线按消息历史补拉 + 提及剥离 + 自回声过滤 + 幂等发送 + 编辑节流；单测覆盖 86.7% |
| S8 联调验证 | 未开始 | 需要一台开好 Bot 凭据的真实 Violet 实例（`/admin/chat-bots` 注册），验证见文末「验收」 |

`ai` 剩余的 Matrix 触点：`internal/ai/service.go` 为注册 Matrix 命令、绑定旧历史账号而保留的 `WithMatrix`。任务文件（完整日志与 `read_file deliver=true` 产物）不再直接依赖 Matrix：`deliverTask` 与 `sendTaskLogs` 都经可选端口 `chat.FileAdapter`（`UploadFile` + `SendUploadedFile`，上传载荷由任务库持久化以便断点续传）交付，平台在 `RegisterTaskDelivery` 时传入的 adapter 若实现该接口即登记为具备文件能力；Matrix 用 `UploadTaskFile`/`SendUploadedTaskFile` 实现。没有文件能力的平台（当前是 Violet）会降级：产物被跳过并记录警告，用户收到一条幂等提示「任务产生了 N 个文件，当前平台无法发送」，文字结果照常投递且不重试，`read_file deliver=true` 会被提前拒绝，`!task logs` 返回明确错误。任务投递函数的 panic 由 `task.Manager` recover 并按退避重试。普通文本消息的发送已全部改经平台端口，`internal/ai/proactive*.go` 也不再引用 `internal/matrix`（房间元数据与主动投递经 `ai.ProactiveRooms`）。

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

`ai.ProactiveRooms` 与 `ai.ChatEntrypoint` 都是可选端口：Violet 两个都不挂，只走 `HandleChat`。`TaskDelivery`（`DeliveryAdapter`）也是可选端口，matrix 与 violet 都实现了它。推文互动另有可选的 `chat.TweetAdapter` + `SetTweetHandler`：平台负责事件去重、合成 `chat.Message` 与公开回复 adapter，`ai.Service.HandleTweet` 负责「无任务、无工具、无记忆」的公开回合。

## 平台端口 `ai.ProactiveRooms`

主动聊天没有入站消息可依据，需要平台提供四个能力；端口边界上一律使用字符串会话标识（`chat.ConversationInfo.Conversation`）：

| 方法 | 作用 |
|---|---|
| `ListConversations` | 枚举可主动投递的会话，静默检测与定时触发以此为扫描范围 |
| `ConversationInfo` | 读取单个会话的元数据（名称、成员数、是否端到端加密），用于决策上下文与欢迎语气 |
| `SendText` | 以普通消息投递主动内容，返回平台消息标识 |
| `SendNotice` | 以低优先级通知投递主动内容，返回平台消息标识 |

Matrix 实现见 `internal/platform/matrix/rooms.go` 的 `Rooms`：它包装 `matrix.RoomService`，把房间快照收敛成 `chat.ConversationInfo`（未命名房间的名称回退为房间 ID，`rooms_test.go` 的字段漂移测试强制新字段要么映射要么显式丢弃）；`ai` 侧拿不到元数据时用 `degradedConversation` 降级为普通群聊，不阻断决策。装配见 `internal/bot/bot.go` 的 `initProactiveManager`。

### 主动投递的持久化语义

仅 `ai.ProactiveManager.SendMessage` 这条路径（决策引擎与静默/定时触发），且需要主动聊天配置开启持久化并给出 `state_path`；新成员欢迎走独立的发送方法，不进入该队列。

- 待发队列写入 `<state_path>.outbox.json`（`0600`，条目为 `{id, room, text, notice, attempts, created_at}`，临时文件 + fsync + rename 原子替换）。条目在调用 `SendText`/`SendNotice` 之前落盘，发送成功后删除，失败则保留并累计 `attempts`；写不进队列时不发送。
- 投递是至少一次（at-least-once）：进程在「平台已发送成功、条目尚未删除」之间崩溃，重启后会重发同一条消息，因此这个窗口内可能出现一次重复投递；反过来，条目在发送前已经落盘，所以不会因为崩溃丢失尚未发出的消息。
- 启动时（状态加载之后、进入 tick 循环之前）按 `created_at` 顺序补发遗留条目，每条只尝试一次；失败条目留在队列里等下次启动，不在 tick 循环内重试，避免对持续失败的上游反复打点。`attempts` 达到 5 的条目保留为死信，不再自动重试，也不会被静默丢弃。
- 主动聊天状态（`state_path`）在状态变更后最多每 30 秒落盘一次，并在每个静默/定时 tick 结束与 `Stop` 时强制保存；不再依赖优雅关闭才持久化。

已知残留（不影响端口使用，列入后续阶段）：

- `ai` 内部仍以 mautrix `id.RoomID` 作状态/频控/缓存键，`ProactiveManager` 对 Matrix 事件侧暴露的方法也仍收 `id.RoomID`，接入第二平台时需把状态键收敛为 `chat.Session`（与同步循环、事件处理器搬迁同批做）。
- `SendText`/`SendNotice` 暂由本端口提供，因此主动消息还不走 `chat.Adapter` 的 Markdown 渲染；下一步应将投递归并到 `chat.Adapter`（需先给 `chat.Capabilities` 加 notice 能力），本端口只保留枚举与元数据。

Violet 接入带出来的缺口（属有意取舍，不是待修的 bug）：

- `ai.ChatEntrypoint` 是单例 setter 且签名带 mautrix 类型，命令入口目前由 Matrix 独占。Violet 因此只走 `HandleChat`：`!ai <内容>` 剥前缀当提问，`!task`/`!schedule`/上下文命令在本平台被跳过。要消掉这条，需要把端口改成平台无关 + 按平台名注册多个入口。
- 任务产出的文件在 Violet 上降级为文字提示：文件交付已是通用的 `chat.FileAdapter` 端口，但 Violet Bot API 目前只开放图片上传（`POST /chat/bot/media`，限 png/jpeg/gif/webp）且没有文件消息类型，`violet.Adapter` 因此不实现该端口；Violet 开放通用文件消息后，实现 `UploadFile`/`SendUploadedFile` 即可，无需改 `ai`。
- 主动聊天不接 Violet：`ai.ProactiveRooms.SendNotice` 没有对等语义（Violet 聊天没有低优先级通知类型），房间元数据一侧的 `kind`/成员数虽然够用，接进来也得先决定 notice 怎么降级。
- 已对称化：Matrix 的同步循环与事件处理器注册、Violet 的 SSE 连接、Terminal 的
  HTTP 监听都在各自平台的 `Start` 内，`Stop` 负责取消并等待退出；`bot` 不再持有任何
  平台连接生命周期。
