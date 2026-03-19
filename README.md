# Saber

一个集成 AI 功能的 Matrix 机器人，使用 Go 和 mautrix SDK 构建。

## 功能特性

- **Matrix 协议**: 通过 mautrix-go 完整支持 Matrix 协议
- **端到端加密**: 可选的 E2EE 支持，使用 goolm（纯 Go 实现，无需 CGO）
- **AI 集成**: 内置 AI 对话功能，支持 OpenAI 兼容的 API
- **工具调用**: 支持 MCP (Model Context Protocol) 工具调用，AI 可执行网络搜索、网页抓取等操作
- **流式响应**: 实时流式输出，智能消息编辑
- **上下文管理**: 每个房间独立的持久化对话上下文
- **多模型支持**: 配置多个 AI 模型，各自独立参数
- **可扩展命令**: 清晰的命令注册和分发系统
- **自动重连**: 弹性连接，支持指数退避
- **私聊自动回复**: 在私聊中自动响应 AI 消息
- **群聊提及回复**: 在群聊中被 @mention 时自动响应
- **回复延续对话**: 回复机器人的消息时自动继续对话
- **主动聊天**: AI 驱动的主动消息，支持静默检测、定时触发、新成员欢迎

## 快速开始

### 前置要求

- Go 1.26.1 或更高版本
- 一个 Matrix 账号
- （可选）一个 OpenAI 兼容的 API 密钥

### 安装

```bash
git clone https://github.com/your-username/saber.git
cd saber
make build
```

### 配置

1. 生成示例配置文件:

```bash
./bin/saber -generate-config
```

2. 编辑 `config.yaml` 填入你的设置:

```yaml
matrix:
  homeserver: "https://matrix.org"
  user_id: "@your-bot:matrix.org"
  device_id: "saber-bot"
  access_token: "your-access-token"

ai:
  enabled: true
  provider: "openai"
  base_url: "https://api.openai.com/v1"
  api_key: "your-api-key"
  default_model: "gpt-4o-mini"
```

### 运行

```bash
./bin/saber
# 或指定配置文件路径
./bin/saber -c /path/to/config.yaml
```

## 使用说明

### 内置命令

| 命令              | 描述                     |
| ----------------- | ------------------------ |
| `!ping`           | 检查机器人是否在线       |
| `!help`           | 列出所有可用命令         |
| `!version`        | 显示版本信息             |
| `!ai <message>`   | 与 AI 对话               |
| `!ai-clear`       | 清除对话上下文           |
| `!ai-context`     | 显示上下文信息           |
| `!mcp-list`       | 列出所有 MCP 服务器和工具 |

### 私聊

当启用 `direct_chat_auto_reply` 时，机器人在私聊中会自动响应消息，无需 `!ai` 前缀。

### 群聊提及

当启用 `group_chat_mention_reply` 时，机器人在群聊中被 @mention 时会自动响应，无需 `!ai` 前缀。

配置示例:

```yaml
ai:
  enabled: true
  group_chat_mention_reply: true  # 启用群聊提及回复
```

使用场景:

1. 用户在群聊中发送 `@botname 你好`
2. 机器人识别到提及并自动回复

### 回复延续对话

当启用 `reply_to_bot_reply` 时，用户可以通过回复机器人的消息来继续对话，无需每次都使用 `!ai` 命令。

配置示例:

```yaml
ai:
  enabled: true
  reply_to_bot_reply: true  # 启用回复延续对话
```

使用场景:

1. 用户发送 `!ai 你好`
2. 机器人回复
3. 用户直接回复机器人的消息（使用 Matrix 的回复功能）继续讨论
4. 机器人自动识别并继续对话

### 主动聊天

主动聊天功能让机器人能够在没有用户直接触发的情况下，主动向聊天室发送消息。这可以用于激活沉寂的群组、定时发送提醒、或欢迎新成员。

配置示例:

```yaml
ai:
  proactive:
    enabled: true
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
```

#### 触发类型

主动聊天支持三种触发类型:

**1. 静默触发 (Silence)**

当聊天室长时间没有活动时，机器人会主动发送消息激活氛围。

```yaml
silence:
  enabled: true
  threshold_minutes: 60      # 静默超过 60 分钟触发
  check_interval_minutes: 15 # 每 15 分钟检查一次
```

工作流程:
1. 定时检查所有已加入的房间
2. 计算每个房间距离最后一条用户消息的时间
3. 如果静默时长超过阈值，触发 AI 决策
4. AI 决定是否发送消息以及发送什么内容

**2. 定时触发 (Schedule)**

在指定的时间点发送消息，适合用于每日提醒、问候等场景。

```yaml
schedule:
  enabled: true
  times: ["09:00", "12:00", "18:00"]  # 24 小时制，格式 "HH:MM"
```

工作流程:
1. 每分钟检查当前时间是否匹配配置的时间点
2. 每个时间点每天只触发一次
3. 日期变化后自动重置触发状态

**3. 新成员触发 (New Member)**

当有新成员加入房间时，自动发送欢迎消息。

```yaml
new_member:
  enabled: true
  welcome_prompt: "用友好的方式欢迎新成员加入"
```

工作流程:
1. 监听房间成员变更事件
2. 检测到新成员加入时触发
3. 使用 AI 生成个性化欢迎消息（如 AI 未启用则使用简单模板）

#### AI 决策引擎

当触发器被激活后，AI 决策引擎会根据当前上下文决定:
- 是否应该发送消息
- 发送什么内容

决策上下文包括:
- 房间名称和成员数量
- 活动水平（low/medium/high）
- 距离最后一条消息的时间
- 今日已发送的主动消息数
- 触发类型（inactivity/scheduled/new_user）

决策配置:

```yaml
decision:
  model: ""           # 指定决策使用的模型（留空使用默认模型）
  temperature: 0.8    # 决策温度（0-2，较高值更有创造性）
  prompt_template: "" # 自定义决策提示词（留空使用默认模板）
```

决策响应格式 (JSON):
```json
{
  "should_speak": true,
  "reason": "房间已静默 2 小时，适合发送消息激活氛围",
  "content": "大家好，有什么有趣的话题想聊聊吗？"
}
```

#### 频率限制

为避免过度打扰，主动聊天内置了频率限制:

```yaml
max_messages_per_day: 5   # 每个房间每天最多 5 条主动消息
min_interval_minutes: 60  # 两次主动消息之间至少间隔 60 分钟
```

限制规则:
- 达到每日上限后，当天不再发送主动消息
- 未达到最小间隔时，跳过本次触发
- 频率限制独立应用于每个房间

#### 最佳实践

1. **合理设置静默阈值**: 建议设置为 60-120 分钟，避免在短暂停顿后打扰对话
2. **控制每日消息量**: 建议 3-5 条，避免让机器人显得过于活跃
3. **选择合适的发送时间**: 定时触发应避开深夜和凌晨
4. **自定义欢迎提示词**: 根据群组主题定制欢迎风格
5. **监控决策日志**: 观察 AI 的决策理由，适时调整配置

#### 故障排查

**问题: 主动消息没有发送**

检查以下几点:
1. `proactive.enabled` 是否为 `true`
2. AI 服务是否正常启用（`ai.enabled: true`）
3. 检查日志中的频率限制信息
4. 确认房间是否有足够的活动记录（新加入的房间可能缺少历史数据）

**问题: 定时触发不工作**

1. 确认时间格式正确（"HH:MM"，24 小时制）
2. 检查系统时区设置
3. 查看日志确认触发器是否检测到时间匹配

**问题: AI 决策总是返回不发送**

1. 检查决策温度设置（过低的温度可能导致保守决策）
2. 查看决策上下文中的活动水平是否为 "high"
3. 确认今日消息数是否已达到上限

### 多模型命令

在 `config.yaml` 中配置多个模型:

```yaml
ai:
  models:
    fast:
      model: "gpt-4o-mini"
      temperature: 0.3
    creative:
      model: "gpt-4o"
      temperature: 0.9
```

然后使用模型特定命令: `!ai-fast <message>`, `!ai-creative <message>`。

### MCP 工具调用

Saber 支持 MCP (Model Context Protocol) 工具调用，让 AI 能够执行实际操作，如网络搜索、网页抓取等。

#### 内置工具

Saber 默认启用以下内置 MCP 工具：

| 工具        | 描述                               |
| ----------- | ---------------------------------- |
| `fetch_url` | 获取网页内容并转换为文本           |
| `web_search`| 搜索互联网获取相关信息             |
| `run_js`    | 在安全沙箱中执行 JavaScript 代码   |

#### 配置示例

```yaml
mcp:
  enabled: true
  # 内置工具配置
  builtin:
    web_search:
      max_results: 5           # 最大返回结果数
      timeout_seconds: 20      # 请求超时时间
    js_sandbox:
      enabled: true            # 启用 JS 沙箱
      timeout_ms: 5000         # 执行超时时间
      max_memory_mb: 64        # 最大内存限制
```

#### 外部 MCP 服务器

Saber 支持连接外部 MCP 服务器：

```yaml
mcp:
  enabled: true
  servers:
    # stdio 类型服务器
    filesystem:
      type: stdio
      enabled: true
      command: "/path/to/mcp-server-filesystem"
      args: ["--root", "/home/user/documents"]
      timeout_seconds: 30
    # http 类型服务器
    remote-server:
      type: http
      enabled: false
      url: "https://mcp.example.com/api"
      token: "your-bearer-token"
```

#### 使用工具

当 AI 需要使用工具时，会自动调用相应的 MCP 工具。例如：

```
用户: 帮我搜索一下 Go 语言的最佳实践
AI: [调用 web_search 工具] 我找到了以下信息...
```

#### 查看可用工具

使用 `!mcp-list` 命令查看当前可用的所有 MCP 服务器和工具。

## 配置参考

### Matrix 设置

| 字段                | 必填    | 描述                             |
| ------------------- | ------- | -------------------------------- |
| `homeserver`        | 是      | Matrix 服务器 URL                |
| `user_id`           | 是      | 机器人的 Matrix ID（如 `@bot:matrix.org`） |
| `device_id`         | 否      | 设备标识符                       |
| `device_name`       | 否      | 设备显示名称                     |
| `access_token`      | 否      | 访问令牌（推荐）                 |
| `password`          | 否      | 用于首次登录的密码               |
| `auto_join_rooms`   | 否      | 启动时自动加入的房间列表         |
| `enable_e2ee`       | 否      | 启用端到端加密                   |
| `e2ee_session_path` | 如果启用 E2EE | 加密会话数据库路径         |

### AI 设置

| 字段                      | 必填        | 描述                         |
| ------------------------- | ----------- | ---------------------------- |
| `enabled`                 | 否          | 启用 AI 功能                 |
| `provider`                | 如果启用    | 提供商名称（如 `openai`）    |
| `base_url`                | 如果启用    | API 基础 URL                 |
| `api_key`                 | 如果启用    | API 密钥                     |
| `default_model`           | 如果启用    | 默认使用的模型               |
| `max_tokens`              | 否          | 每次响应的最大 token 数      |
| `temperature`             | 否          | 响应随机性（0-2）            |
| `stream_enabled`          | 否          | 启用流式响应                 |
| `direct_chat_auto_reply`  | 否          | 私聊自动回复                 |
| `group_chat_mention_reply`| 否          | 群聊 @mention 时自动回复     |
| `reply_to_bot_reply`      | 否          | 回复机器人消息时自动回复     |
| `proactive`               | 否          | 主动聊天配置（见下表）       |

### 主动聊天设置

| 字段                      | 默认值  | 描述                             |
| ------------------------- | ------- | -------------------------------- |
| `enabled`                 | `false` | 启用主动聊天功能                 |
| `max_messages_per_day`    | `5`     | 每个房间每天最大主动消息数       |
| `min_interval_minutes`    | `60`    | 两次主动消息的最小间隔（分钟）   |

### 静默检测设置

| 字段                    | 默认值 | 描述                         |
| ----------------------- | ------ | ---------------------------- |
| `enabled`               | `true` | 启用静默触发                 |
| `threshold_minutes`     | `60`   | 静默阈值（分钟）             |
| `check_interval_minutes`| `15`   | 检查间隔（分钟）             |

### 定时触发设置

| 字段      | 默认值                         | 描述               |
| --------- | ------------------------------ | ------------------ |
| `enabled` | `true`                         | 启用定时触发       |
| `times`   | `["09:00", "12:00", "18:00"]`  | 触发时间点列表     |

### 新成员欢迎设置

| 字段            | 默认值                       | 描述             |
| --------------- | ---------------------------- | ---------------- |
| `enabled`       | `true`                       | 启用新成员欢迎   |
| `welcome_prompt`| `"用友好的方式欢迎新成员加入"` | 欢迎提示词       |

### 决策模型设置

| 字段             | 默认值 | 描述                           |
| ---------------- | ------ | ------------------------------ |
| `model`          | `""`   | 决策使用的模型（留空用默认）   |
| `temperature`    | `0.8`  | 决策温度（0-2）                |
| `prompt_template`| `""`   | 自定义决策提示词（留空用默认） |

### 上下文设置

| 字段            | 默认值  | 描述               |
| --------------- | ------- | ------------------ |
| `enabled`       | `true`  | 启用上下文管理     |
| `max_messages`  | `50`    | 最大保留消息数     |
| `max_tokens`    | `8000`  | 最大上下文 token 数 |
| `expiry_minutes`| `60`    | 上下文过期时间     |

### 重试设置

| 字段              | 默认值 | 描述                   |
| ----------------- | ------ | ---------------------- |
| `enabled`         | `true` | 启用失败重试           |
| `max_retries`     | `3`    | 最大重试次数           |
| `initial_delay_ms`| `1000` | 初始延迟               |
| `max_delay_ms`    | `30000`| 最大延迟               |
| `backoff_factor`  | `2.0`  | 指数退避乘数           |
| `fallback_models` | `[]`   | 降级使用的模型列表     |

### MCP 设置

| 字段       | 必填 | 描述                         |
| ---------- | ---- | ---------------------------- |
| `enabled`  | 否   | 启用 MCP 功能                |
| `servers`  | 否   | 外部 MCP 服务器配置          |
| `builtin`  | 否   | 内置工具配置                 |

### MCP 内置工具设置

#### web_search 配置

| 字段             | 默认值 | 描述                         |
| ---------------- | ------ | ---------------------------- |
| `instances`      | `[]`   | SearXNG 实例列表（留空使用默认）|
| `max_results`    | `5`    | 最大返回结果数（最大 10）    |
| `timeout_seconds`| `20`   | 请求超时时间                 |

#### js_sandbox 配置

| 字段               | 默认值  | 描述                   |
| ------------------ | ------- | ---------------------- |
| `enabled`          | `true`  | 启用 JS 沙箱           |
| `timeout_ms`       | `5000`  | 执行超时时间（毫秒）   |
| `max_memory_mb`    | `64`    | 最大内存限制（MB）     |
| `max_output_length`| `10000` | 最大输出长度（字符）   |

### MCP 服务器设置

| 字段    | 必填         | 描述                             |
| ------- | ------------ | -------------------------------- |
| `type`  | 是           | 服务器类型: `builtin`, `stdio`, `http` |
| `enabled`| 否          | 是否启用                         |
| `command`| stdio 必填  | 可执行文件路径                   |
| `args`  | 否           | 命令参数                         |
| `env`   | 否           | 环境变量                         |
| `url`   | http 必填    | 服务器地址                       |
| `token` | 否           | Bearer 认证令牌                  |
| `timeout_seconds`| 否   | 调用超时时间                     |

## 架构

```
saber/
  main.go                 # 入口点
  internal/
    bot/
      bot.go              # 机器人初始化和生命周期
    cli/
      flags.go            # 命令行标志解析
    config/
      config.go           # 配置加载和验证
    matrix/
      client.go           # Matrix 客户端封装
      crypto.go           # E2EE 支持
      handlers.go         # 事件处理和命令分发
      presence.go         # 在线状态管理
      rooms.go            # 房间操作
      context.go          # 上下文工具
      mention.go          # 提及解析服务
      reply.go            # 回复工具
    ai/
      service.go          # AI 服务编排
      client.go           # OpenAI 兼容客户端
      context_manager.go  # 对话上下文管理
      stream_handler.go   # 流式响应处理
      stream_editor.go    # 流式消息编辑
      stream_tool_handler.go # 工具调用流处理
      retry_handler.go    # 重试逻辑和退避
      proactive.go        # 主动聊天管理器
      proactive_triggers.go # 触发器实现（静默/定时）
      proactive_state.go  # 房间状态跟踪
      proactive_decision.go # AI 决策引擎
    mcp/
      manager.go          # MCP 管理器
      config.go           # MCP 配置验证
      tools.go            # 工具管理
      middleware.go       # 中间件
      validation.go       # 输入验证
      logging.go          # 日志中间件
      servers/
        builtin.go        # 内置服务器注册
        web_fetch.go      # 网页抓取工具
        web_search.go     # 网络搜索工具
        js_sandbox.go     # JavaScript 沙箱
        stdio.go          # Stdio MCP 服务器
        http.go           # HTTP MCP 服务器
```

## 开发

由于使用了 goolm 纯 Go 实现的加密，则需要在编译时添加 tag。Makefile 已经添加好了，如果编辑器也需要，则可以使用环境变量来添加。

```sh
# mautrix 加密需要的 tag
export GOFLAGS="-tags=goolm"
```

### 构建命令

```bash
make build       # 构建二进制文件（可能使用 CGO）
make build-prod  # 生产构建（纯 Go，静态链接，无 CGO）
make run         # 使用 go run 运行
make test        # 运行测试
make fmt         # 格式化代码
make lint        # 运行代码检查
make clean       # 清理构建产物
```

### 构建模式说明

项目支持两种 SQLite 实现，通过 CGO 开关选择：

| 命令 | CGO | SQLite 驱动 | 适用场景 |
|------|-----|-------------|----------|
| `make build` | 自动检测 | `mattn/go-sqlite3` (CGO) | 开发环境 |
| `make build-prod` | 禁用 | `modernc/sqlite` (纯 Go) | 生产部署、容器化 |

**推荐生产环境使用 `make build-prod`**：

- 纯 Go 构建，无需 C 编译器
- 静态链接，可在任意 Linux 发行版运行（包括 Alpine）
- 构建更快，二进制更小
- 跨平台部署更简单

**`build-prod` 构建参数**：

```bash
CGO_ENABLED=0 go build -tags goolm -trimpath \
  -ldflags="-s -w -X 'main.version=...'" \
  -gcflags="-l=4" -o bin/saber .
```

| 参数 | 作用 |
|------|------|
| `CGO_ENABLED=0` | 禁用 CGO，强制纯 Go 编译 |
| `-tags goolm` | 使用纯 Go 的 E2EE 实现 |
| `-trimpath` | 移除文件系统路径，可复现构建 |
| `-ldflags="-s -w"` | 移除符号表和调试信息，减小体积 |
| `-ldflags="-X ..."` | 运行时注入版本、Git commit 等信息 |
| `-gcflags="-l=4"` | 激进内联优化，提升性能 |

### 使用 E2EE 构建

E2EE 需要 `goolm` 构建标签:

```bash
go build -tags goolm .
go run -tags goolm main.go
go test -tags goolm ./...
```

### 开发依赖

```bash
# 安装 goimports（格式化和自动导入管理）
go install golang.org/x/tools/cmd/goimports@latest

# 安装 golangci-lint（代码检查）
# macOS/Linux
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin
# 或使用 go install
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

### 代码风格

- 提交前运行 `make fmt`（使用 goimports 格式化代码并自动管理导入）
- 遵循 [Effective Go](https://go.dev/doc/effective_go) 指南
- 所有导出标识符的注释使用中文
- 永远不要忽略错误

## 安全注意事项

- 永远不要将 `config.yaml` 提交到版本控制
- 会话文件包含访问令牌，使用 `0600` 权限保护
- 生产环境使用访问令牌而非密码
- E2EE pickle 密钥应安全存储

## 依赖

- [mautrix-go](https://github.com/mautrix/go) - Matrix 客户端库
- [go-openai](https://github.com/sashabaranov/go-openai) - OpenAI 客户端
- [go-sdk](https://github.com/modelcontextprotocol/go-sdk) - MCP (Model Context Protocol) SDK
- [tint](https://github.com/lmittmann/tint) - 带颜色的结构化日志

## 许可证

MIT License