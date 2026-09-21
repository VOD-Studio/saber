# OpenAI Responses / Podlink

在 `ai.providers` 中设置 `api: openai-responses` 后，Saber 使用 go-openai 的 `CreateResponse` / `CreateResponseStream` 调用 `POST {base_url}/responses`。`base_url` 应包含 `/v1`，不要包含 `/responses`。也支持 `type: openai-responses`；显式 `api` 优先。

将下面的 provider 合并到已有 `config.yaml` 的 `ai.providers`，替换密钥。`models` 是 Saber 的 map 格式，不是 OMP 的列表格式。模型标识和输出上限来自用户提供的 Podlink 清单，实际可用性与额度由中继决定。这里的 `max_tokens` 会实际作为输出预算发送，普通聊天可省略这些大值，继承全局默认 8192。请求时限为 `ai.request_timeout_seconds`，整个任务时限为 `agent.timeout_seconds`，流式开关为 `agent.stream`。

```yaml
ai:
  default_model: podlink-responses.gpt-5.6-sol
  providers:
    podlink-responses:
      type: openai
      api: openai-responses
      reasoning_effort: medium
      base_url: http://127.0.0.1:8317/v1
      api_key: "YOUR_PODLINK_KEY"
      models:
        gpt-5.6-sol:
          model: gpt-5.6-sol
          max_tokens: 65536
        qwen3.8-max:
          model: qwen3.8-max
          max_tokens: 131072
        qwen3.7-max:
          model: qwen3.7-max
          max_tokens: 65536
        qwen3.7-plus:
          model: qwen3.7-plus
          max_tokens: 65536
        glm-5.2:
          model: glm-5.2
          max_tokens: 131072
        deepseek-v4-pro:
          model: deepseek-v4-pro
          max_tokens: 384000
        qwen3.8-flash:
          model: qwen3.8-flash
          max_tokens: 131072
```

重启 Saber 后用 `!ai-models` 查看列表，使用 `!ai-switch podlink-responses.gpt-5.6-sol` 选择模型，或修改 `ai.default_model`。其他模型同样使用 `podlink-responses.<模型名>`，名称中的点不会影响解析。

- `api` 可配置在 provider 或模型上，模型级覆盖 provider；省略时沿用原有 Chat Completions。未知协议（含 `anthropic-messages`）明确报配置错误。本次只实现 Responses，Claude 等仅声明 Messages 的模型不能由此配置启用。
- 支持系统/开发者/用户消息、文本和用户图片、函数工具定义、工具结果回传，以及流式/非流式 Agent 执行。图片是否可用取决于模型。
- `max_tokens` 映射为 `max_output_tokens`。可在 AI 全局、提供商或模型上设置 `reasoning_effort`，按模型 > 提供商 > 全局继承；所有层级均为空时使用上游默认。显式 `none` 请求关闭思考，仍需上游支持。Responses 默认不发送 `temperature`，避免推理模型拒绝请求；仅 `reasoning_effort: none` 时发送现有温度设置。
- 工具 schema 显式保留原有 `strict` 设置。流式文字复用现有编辑逻辑，工具参数从终态完整输出读取，确认成功后才执行工具。断流、失败、截断和非法工具身份均报错，不当作完成。
- 每轮发送完整历史并设置 `store: false`，不依赖中继保存 `previous_response_id`。工具续轮回传原始 Responses 输出（包括加密推理及消息 `phase`），任务续接和重启恢复也保留这些字段。
- 上游内置工具、自定义工具、音频等未接入 Saber 的工具执行器；遇到不支持的内容明确报错。Chat Completions 备用模型仍使用其原协议；已生成的 Responses 工具历史由普通消息/工具记录转换供其使用。

协议参考：[OpenAI 函数调用](https://developers.openai.com/api/docs/guides/function-calling)、[流式 Responses](https://developers.openai.com/api/docs/guides/streaming-responses)。

本地回归：`go test -tags goolm ./internal/model ./internal/config ./internal/agent ./internal/task`。

真实中继验收（会发起三次模型请求，不连接 Matrix、不执行外部工具）：

```sh
SABER_RESPONSES_CONFIG="$PWD/config.yaml" \
SABER_RESPONSES_MODEL=podlink-responses.gpt-5.6-sol \
go test -tags goolm ./internal/model -run '^TestResponses_LivePodlink$' -count=1 -v
```

2026-09-21 本机 Podlink 实测（结果受当时上游额度和模型行为影响）：

| 模型 | 非流式文本 | 流式工具闭环 |
| --- | --- | --- |
| `gpt-5.6-sol` | 通过 | 通过 |
| `qwen3.7-max` | 通过 | 通过 |
| `qwen3.7-plus` | 通过 | `auto` 通过；推理模式拒绝 `required` |
| `qwen3.8-flash` | 通过 | `auto` 返回了回答但未调用工具；推理模式拒绝 `required` |
| `deepseek-v4-pro` | 通过 | 上游配额不足 |
| `qwen3.8-max` | 上游配额不足 | 未完成验收 |
| `glm-5.2` | 上游配额不足 | 未完成验收 |

7 个模型均出现在中继模型列表中；配置可加载不代表每个模型的当前额度和工具行为均已通过验收。上述验收直接调用 Saber 模型客户端，未验证真实 Matrix 群消息展示。
