# 配置

Saber 使用一套服务端配置，默认启动 HTTP 服务，`saber chat` 单独连接。Matrix 是可选接入。生成配置：

```bash
./bin/saber -generate-config -o config.yaml
chmod 600 config.yaml
```

生成器只输出常用设置，不预置无法调用的模型。先配置 `ai.providers` 和 `ai.default_model`，再将 `ai.enabled` 改为 `true`。未配置 AI 时，服务仍可启动并报告配置状态。

当前格式不兼容旧配置。加载器拒绝未知字段及多个 YAML 文档，不自动迁移 `ai.provider`、`ai.base_url`、`ai.api_key` 或旧的 AI/Matrix 混合字段。

## 模型与协议

```yaml
ai:
  enabled: true
  default_model: podlink-responses.gpt-5.6-sol
  max_tokens: 8192
  temperature: 0.7
  reasoning_effort: ""
  request_timeout_seconds: 120
  system_prompt: ""
  rate_limit_per_minute: 0
  providers:
    podlink-responses:
      type: openai
      api: openai-responses
      base_url: http://127.0.0.1:8317/v1
      api_key: YOUR_PODLINK_KEY
      models:
        gpt-5.6-sol:
          model: gpt-5.6-sol
          reasoning_effort: high
          max_tokens: 8192
        qwen3.8-flash:
          model: qwen3.8-flash
```

- 模型 ID 为 `提供商.模型名`，模型名可以包含点。`api` 支持 `openai-completions` 和 `openai-responses`，默认是前者；不支持 `anthropic-messages`。
- `max_tokens` 是每次请求的输出预算，不是模型上下文窗口或能力上限。模型值覆盖全局值；省略或设为 0 时继承。备用模型也使用自己的预算。
- `temperature` 的模型值覆盖全局值，显式 0 不等于省略。Responses 仅在思考等级为 `none` 时发送温度。
- 思考等级：TUI 本轮选择 > 模型/别名 > 提供商 > AI 全局；省略或空字符串表示继承。全部为空时使用上游默认。等级由对应模型决定，Saber 原样传递，不自动降级。
- `request_timeout_seconds` 是单次 HTTP 请求的总时限，包含等待与流式读取。模型可覆盖全局值。它独立于整个任务的时限。
- `rate_limit_per_minute: 0` 表示不限制；`system_prompt` 对各接入通用，Matrix 还可合并人格提示。
- `ai.models` 可定义快捷别名，使用 `provider` 引用 `ai.providers` 中的名称；省略时沿用默认模型的提供商。提供商专用扩展放在 `extra` 下。

完整 Podlink 模型清单见 [Responses 接入](responses.md)。

## 任务执行

```yaml
agent:
  stream: true
  max_rounds: 5
  timeout_seconds: 600
  max_tool_output_bytes: 32768
  context:
    enabled: true
    max_messages: 50
    max_input_tokens: 32768
  retry:
    enabled: true
    max_retries: 3
    initial_delay_ms: 1000
    max_delay_ms: 30000
    backoff_factor: 2
    fallback_enabled: false
    fallback_models: []
  circuit_breaker:
    enabled: false
    failure_threshold: 5
    reset_timeout: 30
```

`stream` 控制模型传输，TUI 和 Matrix 都遵守。关闭后 TUI 仍显示任务状态，在完整回答到达后展示正文。TUI 自己管理刷新和 Markdown 渲染，没有 Matrix 消息编辑阈值配置。

`max_rounds` 包含最终回答；`timeout_seconds` 覆盖整次任务的模型请求、重试等待与工具执行。`execution.timeout_seconds` 单独限制一次执行工具调用。

重试只重试当前模型请求，不重放已执行的工具。`retry.enabled: false` 关闭同模型重试；备用模型由单独的 `fallback_enabled` 控制，开启时必须填写 `fallback_models`。熔断状态在任务之间保留。

上下文在每次模型请求前应用预算，包含新增工具结果。旧历史按完整用户轮次移除，保留系统提示和当前轮，工具调用与结果、Responses 推理数据一起保留或移除。`enabled: false` 表示新消息不继承前序聊天历史，当前任务内的工具循环仍保留。

`max_input_tokens` 使用序列化输入的字节数保守估算，包含工具定义和保留的推理数据，并非提供商 tokenizer 的精确计数。`max_messages` 包含系统提示、用户、助手及工具消息。当前轮本身超过预算时明确报错，不静默截断。大型图片、工具 schema 或较长的加密推理记录可能需要更高预算。

这些限制只裁剪发给模型的上下文，不删除 `tasks.db` 中的消息、任务、事件和结果。当前没有历史自动过期/删除功能，也不再暴露旧的 `expiry_minutes`、`inactive_room_hours`。

## 服务端和可选能力

```yaml
server:
  listen: "127.0.0.1:8320"
  token_file: ".saber-token"
matrix:
  enabled: false
platforms:
  terminal:
    enabled: true
  violet:
    enabled: false
mcp:
  enabled: false
execution:
  enabled: false
shutdown:
  timeout_seconds: 30
```

令牌和任务数据库位于配置文件目录，令牌首次启动自动创建，权限为 `0600`。不同服务实例必须使用独立的数据目录和监听端口。

MCP 默认关闭，不加载内置或外部服务器。开启后还必须通过 `execution` 配置当前身份的工作区、工具列表、`mcp_requirements` 和所需能力。只把 `mcp.enabled` 改为 true 不代表工具已授权；本地 TUI 也不自动获得权限。具体配置见 [执行与 MCP 授权](execution.md)。

## Matrix 接入示例

以下内容仅在需要 Matrix 时合并到配置中。通用模型和任务设置仍使用上面的 `ai` 与 `agent`。

```yaml
matrix:
  enabled: true
  homeserver: "https://matrix.org"
  user_id: "@your-bot:matrix.org"
  device_id: "saber-bot"
  device_name: "Saber Bot"
  access_token: "YOUR_MATRIX_TOKEN"
  enable_e2ee: true
  e2ee_session_path: "./saber.session"
  max_concurrent_events: 10
  direct_chat_auto_reply: true
  group_chat_mention_reply: true
  reply_to_bot_reply: true
  media:
    enabled: true
    max_size_mb: 10
    timeout_sec: 30
    model: ""
  proactive:
    enabled: false
    max_messages_per_day: 5
    min_interval_minutes: 60
    silence:
      enabled: true
      threshold_minutes: 60
      check_interval_minutes: 15
    schedule:
      enabled: true
      times: ["09:00", "12:00", "18:00"]
    new_member:
      enabled: true
      welcome_prompt: "用友好的方式欢迎新成员加入"
    decision:
      model: ""
      temperature: 0.8
      prompt_template: ""
      stream_enabled: true
  meme:
    enabled: false
    api_key: ""
    max_results: 5
    timeout_seconds: 10
```

Matrix 任务使用独立的结果投递器；旧的 `stream_edit` 参数不再暴露为配置。媒体下载、群聊触发、主动消息与 Meme 配置都归属 `matrix`，不影响 TUI。

## 平台接入（platforms）

`platforms` 决定哪些聊天入口会被启动。注册表只认这里的开关：平台名没有登记开关就不会启动，接入新平台不必再改注册表代码。

```yaml
platforms:
  terminal:
    enabled: true # 默认开启；关掉后 saber chat 与 TUI 不再受理消息
  violet:
    enabled: false
    endpoint: "https://blog.example.com"
    bot_token: "violet_bot_xxx"
    account: ""                      # 留空则取 endpoint 主机名
    direct_chat_auto_reply: true
    group_chat_mention_reply: true
    edit_interval_ms: 200
    http_timeout_seconds: 15
```

- `terminal.enabled` 控制本机 HTTP 聊天入口。它不依赖任何外部平台，因此默认开启；Matrix 与 Violet 默认关闭，需要显式启用。
- Violet 的 `endpoint` 与 `bot_token` 在 `enabled: true` 时为必填项，协议必须是 `http(s)://`。凭据在 Violet 管理端 `/admin/chat-bots`（权限点 `chat:bot-manage`）注册签发，可反复回看明文；配置文件仍按 `0600` 保存，不要提交进仓库。
- `account` 参与会话与历史的存储键，改动它等于换一份历史、限流额度也重新计。多实例接同一个站点时应当显式区分。
- 触发规则：私聊由 `direct_chat_auto_reply` 控制，群聊由 `group_chat_mention_reply` 控制且必须被 `@` 到（Violet 服务端本来就只把被点名的群聊消息推给 bot，Saber 再判一次）。正文里的 `@(username:uuid)` 会降级成 `@username` 再交给模型；只 @ 一句没有内容不会触发回答。
- `edit_interval_ms` 是平台自己的出站编辑下限。Violet 的写端点按 bot 用户限流，编辑配额约 300 次/分钟，因此流式回复的编辑会等待到该间隔再发（默认 200ms），而不是被丢弃——最终定稿同样走编辑，丢一次就等于丢掉答案。它独立于 `matrix.stream_edit` 的全局展示节奏。
- `http_timeout_seconds` 只作用于普通 API 请求，事件流是长连接，靠 30 秒心跳与 90 秒静默看门狗判活。
- 断线恢复：Violet 的 bot 事件流不支持 `Last-Event-ID` 补发，Saber 在重连后按消息历史接口补齐水位之后的消息，并按消息 ID 去重；首次连接只打水位基线，不会把站内旧消息当新问题回答一遍。
- 能力边界：Violet 只走 `HandleChat` 这条最小链路。`!ai <内容>` 会被剥掉前缀当提问，其余 `!` 开头命令（`!task`、`!schedule`、上下文命令）在本平台无落点、直接跳过；任务产出的文件不投递（上传通道仍按 Matrix 限定）；主动聊天未接入 Violet（`SendNotice` 没有对等语义）。
- Matrix 的明细配置仍在顶层 `matrix:` 节。`platforms.matrix` 尚未接管它：直接搬迁会让 `platforms.matrix` 里的部分字段把顶层默认值覆盖掉，得先定清「别名与默认值的合并语义」，属独立批次。新平台的配置一律写进 `platforms.<name>`。

