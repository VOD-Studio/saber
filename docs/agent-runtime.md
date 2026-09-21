# Agent Runtime

`internal/agent` 管理一次任务，不导入 `internal/ai`、MCP、Matrix 或其他聊天平台。
第一阶段复用 `go-openai` 的消息和工具描述类型，保留多模态消息，避免另建一套转换协议。

## 接口与职责

```go
runtime := agent.Runtime{
    Model: modelFunc,
    Execute: toolFunc,
    Limits: agent.Limits{MaxRounds: 5, Timeout: 120 * time.Second, MaxToolOutputBytes: 32768},
}
result, err := runtime.Run(ctx, request, emit)
```

`ModelFunc` 接收当前历史并返回一轮完整响应，流式 adapter 先拼接工具参数，同时发出文本增量。
`ToolFunc` 顺序执行单个工具，返回数据、协议失败标记或 Go error。模型和工具 adapter 必须遵守 context。
Runtime 检查工具是否在本次请求提供的工具列表中，并解析 JSON 对象参数；具体参数 schema 和执行授权仍由工具实现检查。

`ai.Service.RunAgent` 接入现有模型客户端和 MCP，返回完整轨迹，可以在 Matrix 为 nil 时调用。
调用真实 MCP 仍要求现有身份上下文，后续通用聊天接入阶段再调整这一契约。
普通聊天入口通过同一个 Runtime，主动聊天的简单生成接口暂保留现有行为。

## 限制与终态

- `MaxRounds`：包含首次响应和最终回答。一轮内可以有多个工具，按模型顺序执行。最后一轮若仍要求工具，直接 `budget_exhausted`，不执行无法继续读取结果的工具。
- `Timeout`：总时长，覆盖请求、重试和工具执行；父 context 的更早截止时间优先。已派发的远端操作不能保证撤销。
- `MaxToolOutputBytes`：限制序列化后的单条工具消息，UTF-8 安全截断，包含原始字节数标记。模型可读取截断提示并缩小查询。此限制不等价于工具进程内存限制，也不会阻止工具先生成大结果。
- 零值分别使用 5 轮、120 秒、32 KiB；负值无效，非零输出限制至少为 128 字节。
- 用量按模型报告累计，没有报告时为零；当前没有金额或总 token 预算。

终态为 `completed`、`cancelled`、`timed_out`、`budget_exhausted`、`failed`。
预算耗尽可通过 `errors.Is(err, agent.ErrBudgetExhausted)` 判断；取消和截止时间错误保留标准 context 错误。
正常结束必须有 `stop`；工具响应必须有 `tool_calls`，且调用 ID 唯一、名称非空。
`length`、缺失结束标记或不完整工具响应不会作为成功回答，也不会执行工具。

参数错误、不可用工具、MCP `IsError`、执行失败和结果序列化失败都会记录到对应 `tool_call_id` 的工具消息，让模型决定下一步。
工具本身不自动重试；模型请求重试或切换备用模型时保留已经执行的工具结果。

## 事件与记录

Run 串行发送事件：`run_started`、`model_started`、`attempt_started`、`text_delta`、`attempt_finished`、`model_completed`、`tool_started`、`tool_finished`、`run_finished`。
每次 Run 只发出一个 `run_finished`。`tool_started` 是派发前事件，事件消费者在此取消仍可阻止调用。

`Result.Rounds` 保存每轮完整或部分响应、每次请求尝试、原始工具参数、截断后的工具结果、错误与耗时。
运行失败也返回已有轨迹。文本增量不单独保存在轨迹中，工具记录与模型收到的内容一致。
记录目前在内存中，不自动落盘；调用方需要持久化时应自行处理隐私和访问控制。

事件回调可为 nil；非空时不得修改事件内引用的数据，必须及时返回。
模型 adapter 只能在当前请求内串行发送模型事件，返回后不得继续发送。
Matrix adapter 复用编辑器的阈值和节流，在重试开始时重置当前尝试的文本缓冲；最终发送错误不会重跑 Agent。

取消在每次模型请求及每个工具派发前后检查，包括重试等待。运行时不通过遗留后台 goroutine 模拟取消；不遵守 context 的工具必须在其 adapter 内修复。

## 验收

纯内存、不连接模型或聊天平台：

```sh
go test -v -tags goolm ./internal/agent
go test -race -tags goolm ./internal/agent
```

真实客户端的本地 HTTP/MCP 协议与展示 adapter 回归（仅 httptest，本地模拟服务）：

```sh
go test -v -tags goolm ./internal/ai -run 'TestAgent|TestService_RunAgent'
```

覆盖工具失败后修正参数、流式与非流式一致性、取消后不派发第二个工具、超时与轮数预算、输出截断、模型重试不重放工具、MCP 协议失败和展示失败不重跑任务。
