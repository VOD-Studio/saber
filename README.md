# Saber

一个使用 Go 构建的 AI Agent，默认运行本机聊天服务，提供 Charm TUI，可选接入 Matrix。

## 功能特性

### 平台支持

- **Matrix 协议**: 通过 mautrix-go 完整支持 Matrix 协议
- **端到端加密**: Matrix 可选的 E2EE 支持，使用 goolm（纯 Go 实现，无需 CGO）

### AI 功能

- **AI 集成**: 内置 AI 对话功能，支持 OpenAI 兼容的 API
- **多提供商支持**: 同时配置多个 AI 提供商，运行时动态切换模型
- **工具调用**: 支持 MCP (Model Context Protocol) 工具调用，AI 可执行网络搜索、网页抓取等操作
- **流式响应**: 实时流式输出，智能消息编辑
- **图片理解**: 支持发送图片让 AI 理解和分析（需模型支持视觉功能）
- **上下文管理**: 每个房间/群组独立的持久化对话上下文

### 交互特性

- **可扩展命令**: 清晰的命令注册和分发系统
- **自动重连**: 弹性连接，支持指数退避
- **私聊自动回复**: 在私聊中自动响应 AI 消息
- **群聊提及回复**: 在群聊中被 @mention 时自动响应
- **回复延续对话**: 回复机器人的消息时自动继续对话
- **主动聊天**: AI 驱动的主动消息，支持静默检测、定时触发、新成员欢迎
- **人格管理**: 支持为不同房间设置不同的机器人人格，包括内置人格和自定义人格

### 娱乐功能

- **Meme 搜索**: 支持搜索 GIF/Sticker/Meme 并发送到聊天（使用 Klipy API）

## 快速开始

### 前置要求

- Go 1.27.1 或更高版本
- （仅 Matrix 入口需要）一个 Matrix 账号
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
./bin/saber -generate-config -o config.yaml
```

2. 编辑 `config.yaml` 填入你的设置:

```yaml
matrix:
  enabled: false  # 可选聊天入口；接入 Matrix 时设为 true

ai:
  enabled: true
  # 多提供商配置（推荐）
  providers:
    openai:
      type: "openai"
      base_url: "https://api.openai.com/v1"
      api_key: "your-api-key"
      models:
        gpt-4o-mini:
          model: "gpt-4o-mini"
  default_model: "openai.gpt-4o-mini"  # 使用完全限定名称：提供商.模型名
```

### 运行

```bash
./bin/saber
# 在另一个终端打开 TUI 聊天
./bin/saber chat
# 或指定配置文件路径
./bin/saber -c /path/to/config.yaml
# 启用调试日志
./bin/saber -v
```

直接运行 `./bin/saber` 或 `./bin/saber serve` 启动常驻服务，默认监听 `127.0.0.1:8320`。首次启动在配置目录创建 `.saber-token`（0600）；HTTP 接口使用 Bearer 令牌认证。会话、任务和增量事件保存到 `tasks.db`，订阅断开不取消任务。Matrix 是独立的可选聊天入口。使用 `./bin/saber chat` 进入 TUI，支持流式 Markdown、工具状态、会话切换、模型与思考等级选择。界面预览和快捷键见 [终端聊天](docs/tui.md)。

Matrix 仅在 `matrix.enabled: true` 时校验账号并连接服务器。旧配置需要按[新配置结构](docs/configuration.md)重新整理。AI 未启用时服务继续运行，聊天入口提示完成模型配置。

### CLI 标志

| 标志               | 缩写 | 默认值          | 描述             |
|--------------------|------|-----------------|------------------|
| `-config`          | `-c` | `./config.yaml` | 配置文件路径     |
| `-verbose`         | `-v` | `false`         | 启用调试日志     |
| `-version`         |      |                 | 显示版本信息     |
| `-generate-config` |      |                 | 生成示例配置文件 |

### Docker 部署

项目的多阶段 Distroless 镜像用于普通聊天和不需要本地执行器的部署。它不包含 Docker CLI，也没有 daemon 连接及宿主工作区挂载配置，必须保持 `execution.enabled: false`。

需要 Agent 命令执行和文件工具时，使用 [宿主二进制部署步骤](docs/execution.md#宿主二进制部署)。Saber 在宿主上调用 Docker CLI，任务命令仍在受限容器中执行。

#### 构建镜像

```bash
# 构建当前平台镜像
make docker-build

# 构建多架构镜像（amd64 + arm64）
make docker-buildx

# 构建并加载到本地
make docker-load
```

#### 运行容器

```bash
# 使用 make 运行（开发环境）
make docker-run

# 或手动运行
docker run --rm -it \
  -v ./config.yaml:/data/config.yaml:ro \
  -v ./data:/data \
  --name saber \
  saber:latest
```

#### 推送到仓库

```bash
# 设置仓库地址并推送
make docker-push DOCKER_REGISTRY=your-registry.com/
```

#### Docker 镜像特点

- **基础镜像**: Distroless（无 shell，最小攻击面）
- **多架构**: 支持 linux/amd64 和 linux/arm64
- **静态链接**: 无需运行时依赖
- **非 root 运行**: 安全默认配置
- **体积优化**: 约 20MB（最终镜像）

#### docker-bake.hcl

项目使用 `docker-bake.hcl` 配置多架构构建，支持以下变量：

| 变量         | 默认值    | 描述            |
|--------------|-----------|-----------------|
| `REGISTRY`   | `""`      | Docker 仓库地址 |
| `VERSION`    | `dev`     | 镜像版本标签    |
| `GIT_COMMIT` | `unknown` | Git commit hash |
| `GIT_BRANCH` | `unknown` | Git 分支名      |
| `BUILD_TIME` | `""`      | 构建时间        |

## 使用说明

### 内置命令

| 命令                   | 描述                        |
|------------------------|-----------------------------|
| `!ping`                | 检查机器人是否在线          |
| `!help`                | 列出所有可用命令            |
| `!version`             | 显示版本信息                |
| `!ai <message>`        | 与 AI 对话                  |
| `!ai clear`            | 清除对话上下文              |
| `!ai context`          | 显示上下文信息              |
| `!ai models`           | 列出所有可用模型            |
| `!ai switch <id>`      | 切换默认模型                |
| `!ai current`          | 显示当前默认模型            |
| `!mcp list`            | 列出所有 MCP 服务器和工具   |
| `!meme <keyword>`      | 搜索并发送 GIF/Sticker/Meme |
| `!meme --gif <kw>`     | 搜索并发送 GIF 动图         |
| `!meme --sticker <kw>` | 搜索并发送 Sticker 贴纸     |
| `!meme --meme <kw>`    | 搜索并发送 Meme 图片        |
| `!persona list`        | 列出所有可用人格            |
| `!persona set <id>`    | 设置当前房间的人格          |
| `!persona clear`       | 清除当前房间的人格设置      |
| `!persona status`      | 显示当前房间的人格状态      |
| `!persona new ...`     | 创建新的自定义人格          |
| `!persona del <id>`    | 删除自定义人格              |

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
matrix:
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
      times: [ "09:00", "12:00", "18:00" ]
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
  times: [ "09:00", "12:00", "18:00" ]  # 24 小时制，格式 "HH:MM"
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

### 多提供商配置

Saber 支持同时配置多个 AI 提供商，使用完全限定名称（`提供商.模型名`）标识模型：

```yaml
ai:
  enabled: true
  providers:
    # OpenAI 提供商
    openai:
      type: "openai"
      base_url: "https://api.openai.com/v1"
      api_key: "your-openai-key"
      models:
        gpt-4o-mini:
          model: "gpt-4o-mini"
        gpt-4o:
          model: "gpt-4o"
          temperature: 0.5
    # Azure OpenAI 提供商
    azure:
      type: "azure"
      base_url: "https://your-resource.openai.azure.com"
      api_key: "your-azure-key"
      models:
        gpt-4:
          model: "gpt-4"
    # Ollama 本地模型
    ollama:
      type: "openai"  # Ollama 兼容 OpenAI API
      base_url: "http://localhost:11434/v1"
      models:
        llama3:
          model: "llama3"
        qwen2:
          model: "qwen2"
  # 默认模型使用完全限定名称
  default_model: "openai.gpt-4o-mini"
```

### 模型切换命令

```bash
!ai models              # 查看所有可用模型（显示完全限定名称）
!ai switch openai.gpt-4o   # 切换到指定模型
!ai current             # 查看当前默认模型
```

**注意**: 通过命令切换的默认模型在重启后会恢复为配置文件中的设置。

### 图片理解

机器人支持图片理解功能，只需在私聊或 @机器人 的消息中发送图片，AI 会自动分析图片内容。

配置示例:

```yaml
matrix:
  media:
    enabled: true           # 启用媒体处理
    max_size_mb: 10         # 最大文件大小（MB）
    timeout_sec: 30         # 处理超时时间（秒）
    # model: "openai.gpt-4o"       # 指定视觉模型（留空使用默认模型）
```

使用场景:

1. 用户在私聊中发送图片
2. 机器人自动调用视觉模型分析图片
3. 用户可以追加文字说明，如"这张图片里有什么？"

### MCP 工具调用

Saber 支持 MCP (Model Context Protocol) 工具调用，让 AI 能够执行实际操作，如网络搜索、网页抓取等。

#### 内置工具

开启 MCP 并配置执行授权后，可使用以下内置工具：

| 工具         | 描述                             |
|--------------|----------------------------------|
| `fetch_url`  | 获取网页内容并转换为文本         |
| `web_search` | 搜索互联网获取相关信息           |
| `run_js`     | 在安全沙箱中执行 JavaScript 代码 |

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
      args: [ "--root", "/home/user/documents" ]
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

使用 `!mcp list` 命令查看当前可用的所有 MCP 服务器和工具。

### Meme 搜索

Saber 支持 Meme 搜索功能，可以通过 Klipy API 搜索 GIF、Sticker 和 Meme 图片并直接发送到聊天。

配置示例:

```yaml
matrix:
  meme:
    enabled: true
    api_key: "your-klipy-api-key"  # 从 partner.klipy.com 获取
    max_results: 5
    timeout_seconds: 10
```

使用方式:

```
!meme happy           # 搜索 GIF（默认）
!meme --gif happy     # 搜索 GIF
!meme --sticker hello # 搜索 Sticker
!meme --meme cat      # 搜索 Meme
```

### 人格管理

人格功能允许为不同房间设置不同的机器人"性格"，每个人格有独特的系统提示词，会在 AI 对话时与基础系统提示词合并。

#### 内置人格

| ID         | 名称 | 描述                   |
|------------|------|------------------------|
| `catgirl`  | 猫娘 | 可爱活泼，句尾加"喵～" |
| `butler`   | 管家 | 优雅恭敬，英伦风格     |
| `pirate`   | 海盗 | 豪爽冒险，说话带"啊哈" |
| `tsundere` | 傲娇 | 表面冷淡内心温柔       |
| `poet`     | 诗人 | 文雅古风，喜用诗词     |

#### 使用方式

```
!persona list              # 列出所有可用人格
!persona set catgirl       # 设置当前房间为猫娘人格
!persona status            # 查看当前房间的人格状态
!persona clear             # 清除当前房间的人格设置
```

#### 创建自定义人格

```
!persona new robot "机器人" "你是一个友好的机器人助手" "机器人人格"
```

参数说明：

- `robot` - 人格 ID（唯一标识符）
- `"机器人"` - 显示名称
- `"你是一个友好的机器人助手"` - 系统提示词
- `"机器人人格"` - 描述信息

#### 删除自定义人格

```
!persona del robot
```

注意：内置人格不可删除。

#### 数据存储

人格数据存储在 SQLite 数据库中，默认位于配置文件同目录下的 `persona.db`。

## 配置参考

完整字段、默认值和可直接使用的示例见 [配置说明](docs/configuration.md)。

| 配置节 | 职责 |
|---|---|
| `server` | 本机 HTTP 服务及令牌 |
| `ai` | 提供商、协议、模型参数、请求超时 |
| `agent` | 任务轮数、总超时、流式传输、重试、上下文预算 |
| `matrix` | 可选聊天接入、媒体、主动聊天、Meme |
| `mcp` / `execution` | 可选工具与服务端授权 |
| `shutdown` | 优雅关闭时限 |

`-generate-config` 输出以服务端为中心的精简配置；Matrix、MCP 和本地执行默认关闭，不预置空密钥模型。旧配置不自动迁移，未知字段明确报错。模型输出预算与温度按模型覆盖全局设置；思考等级支持模型、提供商、全局继承和 TUI 本轮覆盖。

`agent.context` 裁剪实际模型输入，保留完整工具调用轮次，不删除数据库历史。请求时限、任务时限和单次工具时限分别配置，详见配置说明。Responses 模型清单见 [接入说明](docs/responses.md)。

## 架构

Saber 的 Matrix 聊天入口通过 `chat.Handler` 接收并持久化任务，由 `task.Manager` 后台调用 Agent Runtime，结果单独投递。
嵌入式调用可继续使用 `conversation.Processor` 的同步会话模式。Matrix 和内存 adapter 共用消息、会话和回复契约；核心 `task`、`agent`、`chat`、`conversation`、`model`、`mcp` 不依赖 Matrix SDK。
应用默认运行本机 HTTP/SSE 服务，统一管理 Agent 与持久化任务。聊天入口读取事件或注册终态投递器；Matrix 的同步和投递仅在启用时装配。
接口、会话隔离及验收命令见 [通用聊天接入](docs/chat-adapters.md)。
任务命令、恢复规则和验收命令见 [持久化任务](docs/tasks.md)。
自主命令/文件工具、容器隔离和成员授权见 [执行权限配置](docs/execution.md)。
定时计划命令、时区与重启语义见 [持久化定时计划](docs/schedules.md)。

```
saber/
  main.go                          # 入口点
  main_test.go                     # 主测试
  Makefile                         # 构建和 Docker 命令
  Dockerfile                       # Docker 多阶段构建
  docker-bake.hcl                  # Docker 多架构构建配置
  config.example.yaml              # 示例配置文件
  internal/
    chat/                          # 通用消息、会话、回复与展示能力契约
      memory/                      # 无网络聊天 adapter
    platform/                      # 可插拔平台接入端接口与注册表
      matrix/                      # Matrix 平台接入端：聊天命令入口、任务投递与主动聊天房间端口
    conversation/                  # 平台无关的历史、串行调度与回复交付
    task/                          # SQLite 任务与定时计划、执行日志、目录互斥和结果重试
    execution/                     # 容器命令/文件工具、权限检查、日志与交付文件
    agent/
      runtime.go                   # 独立执行循环、预算、取消和工具错误回传
      types.go                     # 模型与工具接口、运行事件和每轮记录
    bot/
      bot.go                       # 机器人初始化和生命周期
      errors.go                    # 错误定义
    server/                        # 本机会话接口、令牌认证和持久化事件流
    tui/                           # Charm 聊天界面、模型选择、断线续读
    cli/
      flags.go                     # 命令行标志解析
    config/
      config.go                    # 配置加载和验证
      provider.go                  # 提供商配置和模型 ID 解析
    context/
      keys.go                      # 上下文键定义
      user.go                      # 用户上下文工具
    db/
      sqlite_nocgo.go              # SQLite 纯 Go 驱动（默认使用）
    matrix/
      client.go                    # Matrix 客户端封装
      crypto.go                    # E2EE 支持
      handlers.go                  # 事件处理和命令分发
      chat_adapter.go              # 通用消息、媒体和回复契约适配
      presence.go                  # 在线状态管理
      rooms.go                     # 房间操作
      context.go                   # 上下文工具
      mention.go                   # 提及解析服务
      reply.go                     # 回复工具
      media.go                     # 媒体上传和处理
      commands/                    # 命令模块
        register.go                # 命令注册
        ping.go                    # !ping 命令
        help.go                    # !help 命令
        version.go                 # !version 命令
        ai.go                      # AI 命令处理
        meme.go                    # Meme 命令
    model/                         # 不依赖聊天平台的模型核心
      core.go                      # 客户端缓存和请求限流
      client.go                    # 协议分流与 OpenAI 兼容客户端
      responses.go                 # Responses 消息、工具与流式终态适配
      strategy.go                  # 提供商策略
      model_registry.go            # 模型注册与选择
      agent.go                     # Runtime 模型接口适配
      stream_tool_handler.go       # 模型增量拼接
      retry_handler.go             # 重试逻辑和退避
      circuit_breaker.go           # 熔断器
    ai/                            # 应用装配与旧 Matrix 命令兼容层
      service.go                   # 装配通用聊天处理器
      agent.go                     # 模型/MCP 装配与旧回复入口兼容
      model_compat.go              # 旧模型名称兼容，不包含重复实现
      commands.go                  # Matrix AI 命令路由
      context_manager.go           # Matrix 历史命令适配通用存储
      proactive.go                 # 主动聊天管理器
      proactive_rooms.go           # 主动聊天的平台房间端口与会话降级
      proactive_triggers.go        # 触发器实现（静默/定时）
      proactive_state.go           # 房间状态跟踪
      proactive_decision.go        # AI 决策引擎
      tools.go                     # 工具管理
    mcp/
      manager.go                   # MCP 管理器
      factory.go                   # MCP 服务器工厂模式
      config.go                    # MCP 配置验证
      tools.go                     # 工具管理
      middleware.go                # 中间件
      validation.go                # 输入验证
      logging.go                   # 日志中间件
      servers/                     # MCP 服务器实现
        builtin.go                 # 内置服务器注册
        shared_client.go           # 共享 HTTP 客户端
        web_fetch.go               # 网页抓取工具
        web_search.go              # 网络搜索工具
        js_sandbox.go              # JavaScript 沙箱
        stdio.go                   # Stdio MCP 服务器
        http.go                    # HTTP MCP 服务器
    meme/
      service.go                   # Meme 服务（Klipy API）
      command.go                   # !meme 命令处理
    persona/
      types.go                     # Persona 结构体定义
      builtin.go                   # 内置人格定义
      service.go                   # 人格服务（CRUD、房间映射）
      commands.go                  # !persona 命令处理
```

## 开发

由于使用了 goolm 纯 Go 实现的加密，则需要在编译时添加 tag。Makefile 已经添加好了，如果编辑器也需要，则可以使用环境变量来添加。

```sh
# mautrix 加密需要的 tag
export GOFLAGS="-tags=goolm"
```

### 构建命令

```bash
make build       # 构建二进制文件（纯 Go，静态链接）
make build-prod  # 构建优化的生产版本（额外内联优化）
make build-all   # 构建所有平台（macOS/Linux/Windows/FreeBSD/OpenBSD/Loong64）
make run         # 使用 go run 运行
make test        # 运行测试
make test-cover  # 运行测试并生成覆盖率报告
make fmt         # 格式化代码
make lint        # 运行代码检查
make clean       # 清理构建产物
```

### 构建说明

项目使用纯 Go 编译（`CGO_ENABLED=0`），使用 `modernc/sqlite` 作为 SQLite 驱动：

| 命令              | 特点                                     | 适用场景 |
|-------------------|------------------------------------------|----------|
| `make build`      | 纯 Go，静态链接                          | 日常开发 |
| `make build-prod` | 纯 Go + 激进内联优化 (`-gcflags="-l=4"`) | 生产部署 |
| `make build-all`  | 交叉编译 6 个平台 × 2 架构               | 发布版本 |

**构建参数说明**：

| 参数                | 作用                              |
|---------------------|-----------------------------------|
| `CGO_ENABLED=0`     | 禁用 CGO，强制纯 Go 编译          |
| `-tags goolm`       | 使用纯 Go 的 E2EE 实现            |
| `-trimpath`         | 移除文件系统路径，可复现构建      |
| `-ldflags="-s -w"`  | 移除符号表和调试信息，减小体积    |
| `-ldflags="-X ..."` | 运行时注入版本、Git commit 等信息 |
| `-gcflags="-l=4"`   | 激进内联优化（仅 build-prod）     |

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
- 配置文件与会话文件包含密钥，建议手动执行 `chmod 600 config.yaml`（程序不再强制校验权限）
- 会话文件包含访问令牌，使用 `0600` 权限保护
- 生产环境使用访问令牌而非密码
- E2EE pickle 密钥应安全存储

## 依赖

- [mautrix-go](https://github.com/mautrix/go) - Matrix 客户端库
- [go-openai](https://github.com/sashabaranov/go-openai) - OpenAI 客户端
- [go-sdk](https://github.com/modelcontextprotocol/go-sdk) - MCP (Model Context Protocol) SDK
- [goja](https://github.com/dop251/goja) - JavaScript 运行时（用于 JS 沙箱）
- [tint](https://github.com/lmittmann/tint) - 带颜色的结构化日志
- [bluemonday](https://github.com/microcosm-cc/bluemonday) - HTML 净化库
- [modernc/sqlite](https://gitlab.com/cznic/sqlite) - 纯 Go SQLite 驱动

## 许可证

MIT License
