<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-26 | Updated: 2026-03-26 -->

# ai

## Purpose

AI 服务核心包，提供 AI 对话管理、流式响应、工具调用（MCP）、上下文管理和主动聊天功能。封装 OpenAI 兼容 API 客户端，支持多提供商配置。

## Key Files

| File | Description |
|------|-------------|
| `service.go` | AI 服务编排器，协调所有 AI 相关操作 |
| `client.go` | OpenAI 兼容 API 客户端封装，支持连接复用 |
| `context_manager.go` | 对话上下文管理，每个房间独立的历史记录 |
| `stream_handler.go` | 流式响应处理器 |
| `stream_editor.go` | 流式消息编辑器，用于实时更新 Matrix 消息 |
| `stream_tool_handler.go` | 支持工具调用的流式处理器 |
| `proactive.go` | 主动聊天管理器入口 |
| `proactive_decision.go` | 主动聊天决策算法 |
| `proactive_state.go` | 主动聊天状态管理 |
| `proactive_triggers.go` | 主动聊天触发器（静默检测、定时、新成员欢迎） |
| `model_registry.go` | 模型注册表，管理可用模型 |
| `model_commands.go` | 模型相关命令（切换、列表） |
| `strategy.go` | AI 客户端策略模式实现 |
| `client.go` | 客户端策略接口和工厂 |
| `retry_handler.go` | 重试和降级处理 |
| `circuit_breaker.go` | 熔断器实现 |
| `testing_helpers.go` | 测试辅助函数 |

## For AI Agents

### Working In This Directory

- **构建标签**: 需要 `-tags goolm`
- **依赖关系**: 依赖 `matrix.CommandService`、`mcp.Manager`、`matrix.MediaService`
- **初始化顺序**: MCP Manager → Media Service → AI Service → Proactive Manager

### Testing Requirements

- 使用 `testing_helpers.go` 中的辅助函数创建测试实例
- Mock 客户端使用 `NewClientWithModel` 创建
- 测试需要 `-tags goolm`

### Common Patterns

#### 创建 AI 服务

```go
aiService, err := ai.NewService(&cfg.AI, commandService, mcpManager, mediaService)
```

#### 生成简单响应

```go
response, err := aiService.GenerateSimpleResponse(ctx, systemPrompt, userMessage)
```

#### 生成带模型的响应

```go
response, err := aiService.GenerateSimpleResponseWithModel(ctx, modelName, temperature, systemPrompt, userMessage)
```

## Dependencies

### Internal

- `rua.plus/saber/internal/config` - 配置定义
- `rua.plus/saber/internal/matrix` - Matrix 命令服务
- `rua.plus/saber/internal/mcp` - MCP 工具管理
- `rua.plus/saber/internal/context` - 上下文键

### External

- `github.com/sashabaranov/go-openai` - OpenAI 客户端
- `golang.org/x/time/rate` - 速率限制
- `maunium.net/go/mautrix/id` - Matrix ID 类型

<!-- MANUAL: -->