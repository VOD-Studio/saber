# Saber

一个集成 AI 功能的 Matrix 机器人，使用 Go 和 mautrix SDK 构建。

## 功能特性

- **Matrix 协议**: 通过 mautrix-go 完整支持 Matrix 协议
- **端到端加密**: 可选的 E2EE 支持，使用 goolm（纯 Go 实现，无需 CGO）
- **AI 集成**: 内置 AI 对话功能，支持 OpenAI 兼容的 API
- **流式响应**: 实时流式输出，智能消息编辑
- **上下文管理**: 每个房间独立的持久化对话上下文
- **多模型支持**: 配置多个 AI 模型，各自独立参数
- **可扩展命令**: 清晰的命令注册和分发系统
- **自动重连**: 弹性连接，支持指数退避
- **私聊自动回复**: 在私聊中自动响应 AI 消息

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

| 命令              | 描述               |
| ----------------- | ------------------ |
| `!ping`           | 检查机器人是否在线 |
| `!help`           | 列出所有可用命令   |
| `!ai <message>`   | 与 AI 对话         |
| `!ai-clear`       | 清除对话上下文     |
| `!ai-context`     | 显示上下文信息     |

### 私聊

当启用 `direct_chat_auto_reply` 时，机器人在私聊中会自动响应消息，无需 `!ai` 前缀。

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
    ai/
      service.go          # AI 服务编排
      client.go           # OpenAI 兼容客户端
      context_manager.go  # 对话上下文管理
      stream_handler.go   # 流式响应处理
      retry_handler.go    # 重试逻辑和退避
```

## 开发

由于使用了 goolm 纯 Go 实现的加密，则需要在编译时添加 tag。Makefile 已经添加好了，如果编辑器也需要，则可以使用环境变量来添加。

```sh
# mautrix 加密需要的 tag
export GOFLAGS="-tags=goolm"
```

### 构建命令

```bash
make build     # 构建二进制文件
make run       # 使用 go run 运行
make test      # 运行测试
make fmt       # 格式化代码
make lint      # 运行代码检查
make clean     # 清理构建产物
```

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
- [tint](https://github.com/lmittmann/tint) - 带颜色的结构化日志

## 许可证

MIT License