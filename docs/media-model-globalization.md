# 图片模型配置全局化设计方案

> 把「图片/多模态模型」配置从 `matrix.media.model` 提升到全局 `ai.media_model`，
> 让 violet 等非 matrix 平台也能在收到带图片的消息时切换到多模态模型。

## 1. 问题

violet 平台入站图片链路已打通：`normalizeImageMessage` 把图片下载转 data URL 填入 `chat.Attachment`，`taskInput`（`internal/ai/tasks.go:169-171`）正确构造 OpenAI 多模态 `image_url` 消息。但实测默认模型 `podlink.qwen3.8-flash` 是纯文本模型，上游返回 `this model does not support image input`。

根因在 `internal/ai/service.go:448-449`（`handleChat` 内）：

```go
if len(message.Attachments) > 0 && s.config.Matrix.Media.Model != "" {
    modelName = s.config.Matrix.Media.Model
}
```

图片模型选择绑死在 `config.Matrix.Media.Model`——Matrix 平台专属配置。violet 平台够不到，且 `Matrix.Media` 在 `config.example.yaml`/`ExampleConfig()` 里都没示例，用户无从发现。结果：带图片的消息一律走默认文本模型，多模态能力空转。

## 2. 决策

| 决策点 | 选择 | 理由 |
|---|---|---|
| 字段位置 | 新增 `AIConfig.MediaModel`（`ai.media_model`） | 图片模型是 AI 层关注点，与平台无关；所有平台的消息都走同一个 `handleChat` |
| `Matrix.Media.Model` 处置 | 删除字段 | 它只被 `service.go:448` 一处读，迁移后无人消费；保留只会让两份配置打架。`KnownFields(true)`（`config.go:808`）会拒绝未知字段，但 `Model` 删除后旧 config 里的 `matrix.media.model` 会被拒——这是 breaking change，在 CHANGELOG 写明 |
| `Matrix.Media` 其余字段 | 保留不动 | `Enabled/MaxSizeMB/TimeoutSec` 是 Matrix 入站图片解析的开关与限额（`chat_adapter.go:45`、`bot.go:336`），与模型选择无关 |
| 覆盖语义 | 保留：带附件且配置了 media_model 时强制覆盖调用方传入的模型 | 与现状一致；`!ai-gpt-4` 显式指定文本模型时，若消息带图仍切到多模态模型，否则图片会丢 |
| 向后兼容 | 不做 fallback 合并 | breaking change 显式化，避免两份配置长期共存；CHANGELOG + 迁移说明引导用户改 `matrix.media.model` → `ai.media_model` |

## 3. 改动清单

### 3.1 config：新增字段 + 校验 + 示例

**`internal/config/config.go`**

- `AIConfig`（83-96 行）加字段：
  ```go
  // MediaModel 是带图片消息使用的多模态模型（provider.model 全限定或 ai.models 别名）。
  // 留空则带图消息仍走 default_model，纯文本模型会拒绝图片输入。
  MediaModel string `yaml:"media_model,omitempty"`
  ```
- `DefaultAIConfig`（276-288 行）：`MediaModel` 留空串，无需显式设。
- `(*AIConfig).Validate`（485-538 行）：`MediaModel` 非空时，复用 `ParseModelID` + provider 存在性检查，与 `DefaultModel` 同款（参考 496-502 行）。
- `MediaConfig`（249-255 行）：**删除 `Model` 字段**，更新注释说明模型选择已移到 `ai.media_model`。
- `DefaultMediaConfig`（403-411 行）：删掉 `Model: ""`。
- `ExampleConfig`（888-955 行）：在 `ai:` 段补 `media_model` 注释样例（如 `# media_model: "podlink.qwen3-vl-plus"`）。

**`config.example.yaml`**：在 `ai:` 段补 `media_model` 注释样例。

### 3.2 ai：改读取源

**`internal/ai/service.go:448-449`**

```go
if len(message.Attachments) > 0 && s.config.AI.MediaModel != "" {
    modelName = s.config.AI.MediaModel
}
```

唯一消费点，改这一行即可。violet 与 matrix 的消息都走 `handleChat`，自动都受益。

### 3.3 测试（必须补，覆盖率门禁）

**`internal/config/config_test.go`**
- `TestAIConfigValidate` 或新 case：`MediaModel` 非空时 provider 不存在要报错；空串不校验。
- `TestMediaConfig`（800 行）：更新，确认 `Model` 字段已删除（旧测试若引用 `Media.Model` 要删）。
- `DefaultAIConfig` 默认值断言加 `MediaModel == ""`。

**`internal/ai/service_test.go`**
- 新增 `TestHandleChat_MediaModelSwitchesOnAttachments`：构造带 `Attachments` 的 `chat.Message`，配置 `AI.MediaModel`，断言 `taskRequest`/`chatProcessor.Handle` 收到的 `req.Model` 等于 media model。
- 新增 `TestHandleChat_MediaModelEmptyFallsBack`：`MediaModel` 空时带图消息仍走 `default_model`。
- 新增 `TestHandleChat_MediaModelOverridesExplicitModel`：`HandleChatModel` 传入文本模型 + 带图消息 + 配了 media_model，断言切到 media model（保留覆盖语义）。

这三个测试需要能观测到最终 `req.Model`。看 `chatProcessor.Handle` 是否暴露模型名，或用 spy adapter 拦截。若现有测试基建不便观测，可在 `taskRequest` 出口加一个可观测钩子，或直接测 `taskRequest`（它是 `*Service` 方法，可单测）。

**`internal/bot/bot_init_test.go:692`、`bot_services_extended_test.go:322`**
- 现有 `TestMediaConfig` 只测 `MaxSizeMB`，不碰 `Model`，删字段后检查是否引用了 `Media.Model`，若引用则删。

### 3.4 文档

- `CHANGELOG.md`：breaking change 条目，说明 `matrix.media.model` → `ai.media_model`。
- `config.example.yaml`：补 `ai.media_model` 示例。

## 4. 提交拆分

遵循 AGENTS.md「每完成一个功能点提交一次」，CI 绿后进下一个。

1. `refactor(config): 图片模型配置从 matrix.media.model 提升到 ai.media_model`
   - `AIConfig.MediaModel` 新增 + `Validate` + 默认值
   - `MediaConfig.Model` 删除 + `DefaultMediaConfig` 更新
   - `ExampleConfig` + `config.example.yaml` 补示例
   - config 测试更新
2. `refactor(ai): 图片模型切换读取 ai.media_model`
   - `service.go:448` 改读取源
   - 新增 3 个 `handleChat` 图片模型切换测试
3. `docs: CHANGELOG 记录 media_model 迁移`

## 5. 不在本次范围

- violet 平台自己的入站图片大小上限仍硬编码 `maxMediaBytes`（`media.go:15`），不参数化（YAGNI）。
- `Matrix.Media.Enabled/MaxSizeMB/TimeoutSec` 保留不动，是 Matrix 平台专属入站开关。
- 不做 `matrix.media.model` → `ai.media_model` 的自动迁移合并（breaking change 显式化）。

## 6. 验证

改完后，在你的 `config.yaml` 的 `ai:` 段加一行：
```yaml
ai:
  media_model: "podlink.<某个多模态模型>"  # 如 qwen3-vl-plus / claude / gpt-4o
```
重启 saber，在 violet 聊天里发图，日志应显示模型切到 media_model，上游不再返回 "does not support image input"。
