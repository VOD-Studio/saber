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
已有 Matrix 命令、人格服务和主动聊天功能保留平台适配职责，不进入通用核心。
当前二进制启动仍使用现有 Matrix 配置；终端及其他实际平台的启动入口属于下一阶段。

## 消息与会话

`chat.Message` 包含 `Session`、消息 ID、发送者 ID、正文、引用 ID 和附件。
`Session` 由 `Platform`、`Account`、`Conversation`、可选 `Thread` 组成。核心将原生 ID 视为不透明字符串，格式校验归接入端。
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
