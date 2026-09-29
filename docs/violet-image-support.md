# Violet 平台图片能力设计方案

> 跨仓库设计：让 saber 在 violet 站内聊天中发送与接收图片消息。
> 范围：出站（saber → violet）+ 入站（violet 用户图片 → saber 模型）。
> 不含：图片消息的流式生成编辑、分片上传、秒传。

## 1. 目标与决策

| 决策点 | 选择 | 理由 |
|---|---|---|
| Bot 上传通道 | 新增 `POST /api/v1/chat/bot/media`，走 `BotAuth` | 与 session 上传链路解耦，鉴权语义清晰；bot 图片通常小且整体上传，不做分片/秒传 |
| 入站图片解析 | 与出站一起做 | 一次到位双向打通；violet `/uploads/*` 公开可读，下载无鉴权问题 |
| 图片消息编辑 | 不支持，一次性发送 | 图片无流式生成语义；`bot_reply` 状态机只挂文本生成 |

## 2. 现状基线

### violet 侧（`api/`）已就绪
- `domain/chat/entity.go:42` 已有 `MessageImage` 类型；`Message.mediaIDs []shared.ID`（`entity.go:280`）；`NewImageMessage`（`entity.go:337`）。
- `application/chat/service.go:884` `SendMessage` 已支持 image 分派 + `chatImage`（`service.go:1814`）做归属/类型/状态/purpose 四重校验；`purpose="chat"` 在白名单（`service.go:1830`）。
- `SendMessageInput.MediaIDs`（`service.go:110`）、`EditMessageInput.MediaIDs`（`service.go:143`）字段已存在。
- `MessageDTO.Media []MediaDTO`（`service.go:280`）已含 URL/thumbnail/width/height/mime/size。
- `/uploads/*` 公开读取路由（`router.go:120`，无鉴权）。

### violet 侧缺口
1. `handler/chat/bot.go:SendMessage` 请求体只有 `content/reply_to_id/status`，硬编码 `Type=MessageText`（`bot.go:147`）。
2. `handler/chat/bot.go:EditMessage` 纯文本分支不传 `MediaIDs`（本次不实现编辑，可不改）。
3. 无 bot 鉴权的上传通道：`/uploads/*` 只认 `SessionAuth` cookie（`router.go:320`）。

### saber 侧（`internal/platform/violet/`）缺口
1. `adapter.go` 的 `*Adapter` 未实现 `chat.ImageAdapter.SendImage` → `meme` 等命令走回退提示（`internal/meme/chat_command.go:18`）。
2. `client.go` 无上传方法；`request`（`client.go:218`）只支持 JSON body。
3. `types.go` `outgoingMessage` 无图片字段；`messageDTO`（`types.go:31`）无 media 字段。
4. `violet.go:399` `normalizeMessage` 跳过 `Type!="text"` 的入站消息。

## 3. violet 侧实现

> 完整设计与接口契约见 violet 仓库 `docs/prd/0032-bot-media-api.md`（续 PRD-0029 Bot API）。此处只列 saber 侧需要依赖的接口摘要，避免两边重复维护。

violet 侧分 3 个提交，详见 violet 文档：
1. `feat(chat): bot 专用媒体上传端点` — `POST /api/v1/chat/bot/media`，multipart 整体上传，`BotAuth` 鉴权，落盘建 `upload.File`（ownerID=bot.UserID, purpose=chat, status=ready），返回 file_id。
2. `feat(chat): bot SendMessage 支持图片消息` — 请求体加 `type/media_ids`，`type=image` 透传 `MessageImage` 走既有 `SendBotMessage` 链路。
3. `feat(chat): bot 事件流携带 media 字段` — 验证/补全 SSE 与历史消息的 `Media` 序列化。

### saber 侧依赖的接口契约

**`POST /api/v1/chat/bot/media`**（multipart/form-data，`Authorization: Bearer <bot_token>`）
- 表单：`file`（图片字节，必填）、`filename`（可选，仅元数据）
- 校验：≤10MB、MIME 白名单 `image/png|jpeg|gif|webp`、存储名内部生成
- 响应 `201`：`{ "id", "url", "mime_type", "width", "height", "size" }`

**`POST /api/v1/chat/bot/conversations/{id}/messages`** 扩展字段
- 请求体加 `type`（默认 `"text"`，可设 `"image"`）与 `media_ids`（image 时必填的 file_id 列表）
- `type=image` 时 `status` 禁止设 `pending`（图片不进流式状态机）
- 响应同 0029 的 `MessageDTO`（含 `Media`）

**入站**：violet bot 事件流 `message.created`（`type=image`）携带 `media: [{url, mime_type, width, height, ...}]`。`/uploads/*` 是站内公开路径（`router.go:120` 无鉴权），saber 可直连下载。

## 4. saber 侧实现（`internal/platform/violet/`）

### 4.1 client.go：新增上传 + 图片消息发送

**新增 `uploadMedia`**：
```go
// uploadMedia 以 multipart 上传图片到 bot 媒体端点，返回 file_id 等信息。
func (c *client) uploadMedia(ctx context.Context, data []byte, mimeType, filename string) (*mediaUploadDTO, error)
```
- 端点：`{base}/api/v1/chat/bot/media`，`POST multipart/form-data`，`Authorization: Bearer`。
- 复用 `request` 的鉴权头逻辑，但 body 是 multipart（新写一个 `requestMultipart` 或独立函数）。

**扩展 `send` 支持图片**：`outgoingMessage` 加可选字段：
```go
type outgoingMessage struct {
    Content   string   `json:"content"`
    ReplyToID string   `json:"reply_to_id,omitempty"`
    Status    string   `json:"status,omitempty"`
    Thinking  string   `json:"thinking,omitempty"`
    Revision  int64    `json:"revision,omitempty"`
    Type      string   `json:"type,omitempty"`       // 新增
    MediaIDs  []string `json:"media_ids,omitempty"`  // 新增
}
```
`send` 不变，`outgoing(reply, content)` 现构造的 `Type` 留空（文本路径默认 violet 侧回退 text）。

### 4.2 types.go：新增 DTO

```go
type mediaUploadDTO struct {
    ID       string `json:"id"`
    URL      string `json:"url"`
    MIMEType string `json:"mime_type"`
    Width    int    `json:"width"`
    Height   int    `json:"height"`
    Size     int64  `json:"size"`
}

// messageDTO 扩展入站 media（violet GET messages / SSE 已带）
type messageDTO struct {
    // ... 现有字段 ...
    Media []messageMediaDTO `json:"media,omitempty"`  // 新增
}
type messageMediaDTO struct {
    ID       string `json:"id"`
    URL      string `json:"url"`
    MIMEType string `json:"mime_type"`
    Width    *int   `json:"width,omitempty"`
    Height   *int   `json:"height,omitempty"`
}
```

### 4.3 adapter.go：实现 SendImage

```go
// SendImage 上传图片并发送一条 image 消息。
func (a *Adapter) SendImage(ctx context.Context, reply chat.Reply, data []byte, mimeType, filename string, width, height int) (string, error) {
    if err := a.validate(ctx, reply.Session); err != nil {
        return "", err
    }
    if len(data) == 0 || mimeType == "" {
        return "", errors.New("图片内容或 MIME 类型为空")
    }
    uploaded, err := a.api.uploadMedia(ctx, data, mimeType, filename)
    if err != nil {
        return "", fmt.Errorf("violet 上传图片失败: %w", err)
    }
    body := outgoingMessage{
        Content:   reply.Text,        // caption（meme 命令传 gif.Title）
        ReplyToID: reply.ReplyTo,
        Type:      "image",
        MediaIDs:  []string{uploaded.ID},
    }
    // 图片消息不走 pending/edit 状态机，Status 留空。
    created, err := a.api.send(ctx, reply.Session.Conversation, body, idempotencyKey(reply))
    if err != nil {
        return "", fmt.Errorf("violet 发送图片消息失败: %w", err)
    }
    return created.ID, nil
}
```
- `*Adapter` 实现 `chat.ImageAdapter` 后，`meme` 命令的 `adapter.(chat.ImageAdapter)` 断言（`meme/chat_command.go:17`）自动生效。

### 4.4 violet.go：入站图片解析

`normalizeMessage`（`violet.go:399`）当前对 `Type!="text"` 一律跳过。改为：
```go
switch {
case message.IsDeleted: ... // 不变
case message.Type == inboundMessageType: ... // 文本路径不变
case message.Type == "image":
    // 解析图片消息为 chat.Attachment
case message.Type != "" && message.Type != inboundMessageType && message.Type != "image":
    slog.Debug("跳过不支持的 violet 消息类型", ...)
    return chat.Message{}, false
}
```
**图片入站处理**：
1. 遍历 `message.Media`，对每个 media：
   - URL 若是相对路径，拼 `endpoint + url`（**需确认 violet 返回的是相对还是绝对 URL**；`/uploads/*` 是相对站点根）。
   - 下载字节（新增 `client.downloadMedia(ctx, url)`，或直接 `http.Get` + 复用 `a.api` 的 client）。
   - `encodeImageAsDataURL`（参照 `internal/matrix/media.go:166`）转 data URL。
   - 填 `chat.Attachment{Kind:"image", Name: filename, MIMEType: mime, URL: dataURL}`。
2. `chat.Message.Text` 取 `message.Content`（caption，可能为空）。
3. `chat.Message.HasContent()`（`chat.go:104-111`）已支持「纯附件无文本」判定，无需改。

**注意**：入站图片下载会增加延迟与内存。建议加大小上限（如 10MB）和并发上限（每条消息多图时串行或限并发）。

### 4.5 配置（可选）

`config/platforms.go:VioletConfig` 加：
```go
MediaMaxBytes int `yaml:"media_max_bytes"` // 默认 10MB，0 = 不限制
```
入站下载与出站上传共用此上限。`DefaultVioletConfig` / `Validate` 补默认值。若暂不参数化，可硬编码常量。

## 5. 数据流

### 出站（saber 发图）
```
meme 命令 / AI 生图
  → adapter.(chat.ImageAdapter).SendImage(ctx, reply, data, mime, name, w, h)
  → violet adapter.SendImage
    → client.uploadMedia  POST /api/v1/chat/bot/media      → 拿 file_id
    → client.send         POST .../messages {type:image, media_ids:[file_id], content:caption, reply_to_id}
  → 返回消息 ID
```

### 入站（violet 用户发图 → saber 模型）
```
violet SSE message.created (type=image, media:[{url,...}])
  → Platform.handleEvent → normalizeMessage
    → 对每个 media：downloadMedia → encodeImageAsDataURL
    → chat.Message{Text: caption, Attachments:[{Kind:image, URL:dataURL}]}
  → handler → 模型（多模态）看到 data URL 图片
```

## 6. 测试策略

### violet 侧
- `handler/chat/bot_media_test.go`：上传成功/超大/非图片 MIME/未鉴权；返回 file_id 可被 SendMessage 引用。
- `handler/chat/bot_test.go` 扩展 `SendMessage`：`type=image` + `media_ids` 成功；`media_ids` 空时拒绝；`status=pending` 与 image 组合拒绝。
- 归属校验：bot A 上传的 file_id 不能被 bot B 引用（复用 `chatImage` 既有校验，加测试覆盖）。

### saber 侧
- `testing_helpers.go` `fakeViolet` 增加：
  - `/api/v1/chat/bot/media` 端点（multipart 解析、记入 `uploadedMedia` 列表）。
  - `serveSendMessage` 扩展：识别 `type=image`，校验 `media_ids` 引用了已上传 media。
  - 入站 `pushEvent` 支持带 `media` 的 image 消息。
- `adapter_test.go` 新增 `TestAdapter_SendImage`（参照 `matrix/chat_adapter_test.go:270`）：上传 + 发送 + reply_to 透传 + 返回 ID。
- `platform_test.go` 扩展：
  - `TestPlatform_IgnoredInbound` 里那条 `Type:"image"` 消息从「被忽略」改为「被解析为 Attachment」。
  - 新增 `TestPlatform_InboundImage`：推送带 media 的 image 消息，handler 收到的 `chat.Message.Attachments` 含 data URL。

### 覆盖率
violet 平台是新代码，按 AGENTS.md 60% 总覆盖率门禁，新增分支（上传失败、非图片 MIME、入站下载失败、相对/绝对 URL 拼接）都要覆盖。

## 7. 安全考虑（PR 必须标注）

- **新上传端点是安全敏感面**：bot token 可上传文件到服务器磁盘。必须限大小、限 MIME 白名单、限频；落盘路径不能被 filename 注入（用内部生成的存储名，`originalName` 仅作元数据）。
- **purpose=chat 归属链**：bot 上传的文件 ownerID = bot 虚拟用户，`chatImage` 校验天然拦住跨 bot 引用。
- **入站下载 SSRF**：violet `/uploads/*` 是站内路径，相对 URL 拼到 `endpoint`（配置值），不接受任意外部 URL；若 media.URL 是绝对 URL，必须校验 host == endpoint host，防止 SSE 伪造 URL 引发 SSRF。
- **引用计数**：bot 上传的图片被消息引用后 `refCount` 抬升（`service.go:911`）；消息删除时 `chatImage` 已有回滚路径，无需额外处理。

## 8. 提交拆分（遵循「每完成一个功能点提交一次」）

两个仓库各自按功能点提交，CI 绿后再进下一个。

### violet（`api/`）— 3 个提交
1. `feat(chat): bot 专用媒体上传端点` — `POST /api/v1/chat/bot/media` + 落盘建 File + 单测 + OpenAPI。
2. `feat(chat): bot SendMessage 支持图片消息` — 请求体加 `type/media_ids`，分派 `MessageImage`，单测。
3. `feat(chat): bot 事件流携带 media 字段` — 验证/补全 SSE 与历史消息的 media 序列化（若已带则该提交可省）。

### saber（`internal/platform/violet/`）— 3 个提交
4. `feat(violet): 实现出站图片发送` — `client.uploadMedia` + `adapter.SendImage` + `types` 扩展 + fake 端点 + `TestAdapter_SendImage`。
5. `feat(violet): 解析入站图片消息` — `messageDTO.Media` + `normalizeMessage` image 分支 + 下载转 data URL + `TestPlatform_InboundImage`。
6. `chore(config): violet 媒体大小上限配置`（可选）— `VioletConfig.MediaMaxBytes` + 默认值 + Validate。

每个提交：代码 + 测试，`make build && make test` 绿，gofmt/lint 绿。

## 9. 待确认事项

> violet 侧的待确认事项见 `violet/docs/prd/0032-bot-media-api.md` 末尾。saber 侧依赖以下两点在 violet 实现时定稿：

1. **`MessageDTO.Media[].URL` 是相对路径还是绝对 URL**？影响 saber 入站下载时是否拼 endpoint。
2. **violet 站点是否对 `/uploads/*` 做了访问限制**（如防盗链）？bot 直连下载若被拦，需要 violet 提供带凭证的 media 读取端点。
