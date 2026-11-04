# 通用聊天接入

Saber 的同步消息处理链路为（未调用 `Service.EnableTasks` 的嵌入式场景）：

```text
Matrix 命令/消息 ── ChatAdapter ──┐
                               ├─ chat.Handler ─ conversation.Processor ─ agent.Runtime
内存消息 ──────── memory.Adapter ┘                         │
                                                  chat.Presenter
                                                         │
                                            原接入账号的回复 adapter
```

正式 Matrix 应用调用 `Service.EnableTasks`，将用户消息保存到 SQLite 后快速回复任务编号；后台执行和结果投递由 `task.Manager` 管理，详见 [持久化任务](tasks.md)。任务模式使用独立输入快照，不共享房间历史，也不调用同步 Presenter。

`task`、`agent`、`chat`、`conversation`、`model`、`mcp` 均不依赖 Matrix 类型或 SDK，包括间接依赖。
`ai.Service` 当前作为应用装配和 Matrix 专用命令兼容层：`HandleChat` 可由任意 adapter 调用，模型实际实现已迁到 `model`。
Matrix 命令与人格服务保留平台适配职责，不进入通用核心；主动聊天只需平台实现 `ai.ProactiveRooms`（会话枚举、元数据与主动投递，见 [平台接入系统](platform-system.md)）。
当前二进制启动仍使用现有 Matrix 配置；终端及其他实际平台的启动入口属于下一阶段。

## 消息与会话

`chat.Message` 包含 `Session`、消息 ID、发送者 ID、正文、引用 ID 和附件。
`Session` 由 `Platform`、`Account`、`Conversation`、可选 `Thread` 组成。核心将原生 ID 视为不透明字符串，格式校验归接入端。
`chat.ConversationInfo` 是接入端给出的会话元数据快照（标识、展示名、成员数、是否加密），只供主动聊天这类无入站消息的场景；平台没有的语义保持零值。
`Session.Key()` 对各字段明确编码，不能用字符串分隔符拼接代替。

同名会话在不同平台或账号之间不会共享历史；线程与主会话也各自独立。
默认不进行跨平台身份绑定或记忆合并。工具限流的用户键包含平台、账号和发送者，会话键还包含会话及线程。
平台账号和发送者来自 adapter，不从模型参数中读取。

Matrix adapter 保留入站消息 ID、引用和线程，下载并解密私有图片，再将可供模型读取的图片 Data URL 放入附件。
当前消息与引用图片共同组成一条多模态用户消息。历史只保留文本和图片名称标记，不保存 Base64 图片载荷。
当前通用核心支持文本和图片；未支持的附件类型会明确返回错误。

## 处理与回复

`chat.Handler` 的形状是：

```go
func(context.Context, chat.Message, chat.Adapter) (agent.Result, error)
```

`conversation.Processor.Handle` 接收消息、模型请求模板和回复 adapter。
模板的 `Messages` 用于系统提示等前缀；处理器读取历史并追加当前用户消息一次，然后运行 Agent。
同一会话的运行和最终交付串行执行，不同会话可以并发。排队使用可取消的 context，取消后不会把待处理消息写入历史。
处理超时从出队后开始，覆盖本次 Agent 执行和回复交付；父 context 的更早截止时间仍有效。
清除历史通过 `Processor.Clear` 排在当前运行之后，避免刚清空又被未完成的回答写回。

回复 adapter 声明 `Edit`、`Typing`、`Reply` 能力，并实现 `Send`、`Edit`、`SetTyping`。
Presenter 只调用已声明的可选能力：支持编辑时展示临时内容并更新最终回复；不支持编辑时只发送最终回答。
模型的 `Stream` 设置独立于聊天平台编辑能力。

模型重试时重置当前尝试的展示缓冲。临时编辑失败允许最终交付再次尝试，但不会重跑模型或工具。
最终回答先写入历史，再发送到平台；发送失败时返回成功的 Agent Result 和独立的交付错误。
Matrix 回复和编辑均保留原消息引用，线程回复保留线程根关系。

## Violet 推文互动

Violet 除了站内聊天，还有第二条入站通道：推文评论与引用。它复用同一套模型与历史，但出站目标不是会话消息，而是推文下的公开评论。

```text
Violet SSE：tweet.commented / tweet.quoted
        │
        ▼
平台组装合成 chat.Message（Session.Conversation = tweet:<id>，Context = 互动快照）
        │
        ▼
ai.Service.HandleTweet ─ handleChat(useTasks=false, publicThread=true)
        │                          └─ 不建持久化任务、不准备工具、不注入记忆
        ▼
tweetReplyAdapter.Send ─> 平台写端点 ─> 公开评论
```

- 合成消息的真身仍是 `chat.Message`：`Text` 是评论或引用正文，`Context` 是平台生成的推文/评论快照，`ReplyTo` 是所回复的评论 ID。`Context` 不参与命令识别，也不会混入用户消息；核心把它包装成明确分隔的不可信运行时资料，并追加「只输出公开评论正文」的系统约束。
- 推文回合使用零能力的回复 adapter：`Capabilities()` 全零，不支持编辑、输入状态或流式状态。平台只支持一次性发送最终正文，空白正文直接跳过，避免在公开时间线留下空评论。
- 推文回合 `useTasks=false`：不写入任务库、不回执任务编号；因此模型不可用或发送失败只记服务端日志，不会在推文下补一张公开的失败卡片。
- 推文回合不向模型提供任何工具，且执行阶段对工具调用整体拒绝，防止把私聊记忆或执行结果带进公开内容。
- `saber_tweet` 是主动侧的可选工具，只在「Violet 平台启用 + 推文接入启用 + 身份账号匹配」时出现，用于发布/回复/点赞/删除推文与评论，以及读取推文和评论。它与推文自动回评互不依赖：关闭自动回评仍可让管理员通过聊天或定时计划调用工具。

## Violet 表情

表情没有独立的消息类型：模型把 token（系统表情 `[doge]`、自定义表情 `[cat:<uuid>]`）直接写进回复正文，Violet 负责校验、持久化并渲染。Saber 只负责把目录与收藏整理成工具可用的形状。

- 系统表情随 `GET /chat/bot/emojis` 按分组下发，token 开箱即用，无需收藏。
- 自定义表情默认私有：bot 只能看到自己自制（`owned`）与已收藏（`favorited`）的那部分，没有全站目录可查。收藏/取消收藏走 `POST`/`DELETE /chat/bot/emojis/{id}/favorite`，成功返回 204。
- 入站消息里的自定义表情 token 仍按原逻辑从正文剥离、图片作为附件投递；平台额外在 `chat.Message.Context` 里附上 token 清单。该区块会被上层当作不可信资料，因此只陈述「本条消息引用了哪些 token」，不写任何指令；收藏与复用的步骤由系统提示和工具说明负责。
- `saber_emoji` 的 `list` 读取目录并按 `query`/`group` 过滤，`favorite`/`unfavorite` 接受 `emoji_id`（UUID）或 `[name:uuid]` token；系统 token 会被拒绝，因为收藏对它们没有意义。工具只在「Violet 启用 + 账号匹配」时出现，使用表情本身不需要管理员授权。

## 账号头像

Matrix 与 Violet 实现可选的 `chat.AvatarAdapter`，通过 `ai.Service.SetAvatarAdapter(platformName, adapter)` 装配。模型工具 `saber_avatar` 只操作当前平台接入账号，平台、账号与发送者均来自可信上下文；每次执行重新检查平台开关、账号匹配与授权，旧工具快照不能绕过撤销。

| 动作 | 参数 | 权限与行为 |
|---|---|---|
| `get` | `{"action":"get"}` | 读取当前公开头像，返回 `{ "url": "…" }`；未设置时为空串 |
| `set` | `{"action":"set","attachment":1}` | 使用本轮用户的第 1 张图片附件；需 `commands.admins` |
| `set` | `{"action":"set","image_path":"avatar.png"}` | 使用当前任务工作区相对路径；需 `commands.admins` 与 `read_file` 授权 |

管理员可以发送或引用图片后说「把你自己的头像换成这张图」。图片源二选一，支持 PNG/JPEG/GIF/WebP、最多 10 MiB；附件只能使用接入端已下载的 Data URL，不从历史图片或任意远程地址取图。工作区读取钉住根目录，拒绝绝对路径、越界链接与非普通文件。没有图片时模型应请求图片或在已授权工作区准备，不得声称已经更换。

Matrix 上传公开原图并更新自身 `avatar_url`，返回 MXC URI；即使来源消息使用 E2EE，账号头像也不加密。Violet 调用 `POST /api/v1/chat/bot/avatar`（multipart 单个 `file`），服务端按当前 Bot Token 保存头像素材并事务更新 bot 引用、虚拟用户资料与素材引用计数，返回最新 profile；失败不覆盖旧头像。部署时先升级 Violet 服务端再升级 Saber。公开推文互动回合仍禁止所有工具。

## 内存 adapter

测试或嵌入式调用可注入同一 Handler：

```go
adapter := memory.New("local-account", chat.Capabilities{}, handler)
result, err := adapter.Receive(ctx, chat.Message{
    Session:  chat.Session{Conversation: "conversation-1"},
    SenderID: "user-1",
    Text:     "你好",
})
```

`Receive` 由 adapter 填充可信的 `memory` 平台与账号，调用方只提供会话、发送者和内容。
`Replies()` 返回回复快照，测试不需要 Matrix 账号、模型凭据或外部网络。
要调用现有模型配置，可将已装配的 `Service.HandleChat` 作为 handler；没有 Matrix 客户端时仍可运行内存对话。
更低层的独立装配可直接组合 `model.AgentModel`、`agent.Runtime` 和 `conversation.Processor`。

## 验收

```sh
# 通用消息、能力降级、会话隔离、排队取消和历史清理
go test -tags goolm ./internal/chat/... ./internal/conversation

# 同一处理器同时接收 Matrix 和内存消息，比较实际模型请求及工具身份
go test -tags goolm ./internal/matrix -run TestChatAdapter

# 不配置 Matrix，通过内存 adapter 调用本地模拟模型客户端
go test -tags goolm ./internal/ai -run TestService_HandleChat_MemoryWithoutMatrix

# 工具限流来源隔离
go test -tags goolm ./internal/mcp -run TestRateLimiter_ChatIdentityIsolation
```

`conversation.TestCoreDependencies` 检查核心包的完整 Go 依赖图，防止未来通过间接引用重新引入 Matrix SDK。
这些验收使用纯内存或本地 `httptest`；不能替代真实 homeserver、加密媒体和模型提供商的上线验收。
