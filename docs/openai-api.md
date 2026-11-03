# OpenAI 兼容 API

Saber 提供少量 OpenAI 兼容端点，便于本地脚本和已有客户端直接使用已配置的模型。该入口是**非任务、不持久化**的：请求不写入任务数据库、不读取聊天历史，也不启用工具调用。需要后台任务、工具或续接历史时，请使用 `/v1/sessions/*` 接口。

## 认证

所有请求沿用服务端令牌，与 `/v1/info` 等本机接口一致：

```sh
Authorization: Bearer <server.token>
```

不接受带 `Origin` 头的浏览器跨源请求。令牌只放在请求头，不放入 URL。

## 端点

### `GET /v1/models`

返回 OpenAI 风格模型列表，来源为 Saber 的模型注册表。默认模型排在 `data` 首项，`id` 使用 `提供商.模型` 格式。

```sh
curl -sS http://127.0.0.1:8080/v1/models \
  -H "Authorization: Bearer $SABER_TOKEN"
```

```json
{
  "object": "list",
  "data": [
    {"id": "podlink-responses.gpt-5.6-sol", "object": "model", "created": 1762200000, "owned_by": "saber"}
  ]
}
```

### `POST /v1/chat/completions`

支持的请求字段：

| 字段 | 说明 |
| --- | --- |
| `model` | 模型标识；省略或空字符串时使用当前默认模型 |
| `messages` | `role` 仅支持 `system`、`user`、`assistant`，`content` 必须是字符串 |
| `stream` | `true` 时返回 `text/event-stream` |
| `temperature` | 0 到 2；省略时沿用模型配置 |
| `max_tokens` | 正整数；省略时沿用模型配置 |
| `n` | 仅接受 `1` |

其余字段一律返回 `unsupported_parameter`，不会静默忽略，包括 `tools`、`functions`、`tool_choice`、`stream_options`、`logprobs`、`top_p` 等。

非流式请求：

```sh
curl -sS http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $SABER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "podlink-responses.gpt-5.6-sol",
    "messages": [
      {"role": "system", "content": "你是简洁的中文助手。"},
      {"role": "user", "content": "用一句话解释 SSE。"}
    ],
    "temperature": 0.3,
    "max_tokens": 256
  }'
```

响应：

```json
{
  "id": "chatcmpl-1f0f5c9a8b7d6e5f4a3b2c1d",
  "object": "chat.completion",
  "created": 1762200000,
  "model": "podlink-responses.gpt-5.6-sol",
  "choices": [
    {"index": 0, "message": {"role": "assistant", "content": "……"}, "finish_reason": "stop"}
  ],
  "usage": {"prompt_tokens": 24, "completion_tokens": 18, "total_tokens": 42}
}
```

流式请求：

```sh
curl -N -sS http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $SABER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "podlink-responses.gpt-5.6-sol",
    "messages": [{"role": "user", "content": "数到三"}],
    "stream": true
  }'
```

数据块顺序固定为：首块仅含 `delta.role`，随后每个文本增量一个 `delta.content` 块，最后一块 `finish_reason` 为 `stop`，随后 `data: [DONE]`。客户端断开连接会取消正在进行的模型运行。

```text
data: {"id":"chatcmpl-...","object":"chat.completion.chunk","created":1762200000,"model":"podlink-responses.gpt-5.6-sol","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","created":1762200000,"model":"podlink-responses.gpt-5.6-sol","choices":[{"index":0,"delta":{"content":"一"},"finish_reason":null}]}

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","created":1762200000,"model":"podlink-responses.gpt-5.6-sol","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

## 错误

错误使用 OpenAI 风格结构，不含密钥或内部堆栈：

```json
{"error": {"message": "模型不存在", "type": "invalid_request_error", "code": "model_not_found"}}
```

| 状态码 | 典型 `code` | 场景 |
| --- | --- | --- |
| 400 | `invalid_json`、`invalid_request` | 请求体不是单个 JSON 对象或字段类型错误 |
| 400 | `unsupported_parameter` | 请求了未实现的参数（工具、`n>1`、`logprobs` 等） |
| 400 | `unsupported_role`、`unsupported_content` | 非纯文本消息 |
| 400 | `too_many_messages`、`message_too_large` | 超过消息条数或单条长度限制 |
| 401 / 403 | — | 令牌缺失、错误或带浏览器 `Origin` |
| 404 | `model_not_found` | 模型不在注册表中 |
| 413 | `request_too_large` | 请求体超过 1 MiB |
| 502 | `upstream_error`、`incomplete_response` | 上游模型失败或未正常完成 |
| 503 | `ai_disabled` | `ai.enabled` 未开启或没有可用模型 |

## 限制

- 请求体不超过 1 MiB。
- `messages` 不超过 100 条，单条 `content` 不超过 128 KiB。
- 只支持纯文本内容；图片、音频和工具调用均明确拒绝。
- 每次请求独立运行，不写入任务数据库，也不读取或保存聊天历史；`X-Saber-Session` 不生效。
- 流式响应不使用 `stream_options`，不会发送单独的 usage 块。