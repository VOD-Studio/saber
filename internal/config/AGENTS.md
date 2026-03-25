<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-25 | Updated: 2026-03-25 -->

# config

## Purpose

配置管理包，提供 YAML 配置文件的加载、验证、默认值管理和多提供商 AI 配置支持。

## Key Files

| File | Description |
|------|-------------|
| `config.go` | 配置结构定义、加载、验证、默认值 |
| `provider.go` | 多提供商配置支持，模型 ID 解析 |

## For AI Agents

### Working In This Directory

- **配置验证**: 所有配置结构都有 `Validate()` 方法
- **默认值**: 使用 `Default*Config()` 函数获取合理默认值
- **向后兼容**: 支持旧的单提供商配置格式自动迁移

### Testing Requirements

- 测试配置验证的各种边界情况
- 测试旧格式迁移逻辑

### Common Patterns

#### 加载配置

```go
cfg, err := config.Load(path)
if err != nil {
    return fmt.Errorf("加载配置失败: %w", err)
}
```

#### 验证配置

```go
if err := cfg.AI.Validate(); err != nil {
    return fmt.Errorf("AI配置验证失败: %w", err)
}
```

#### 获取模型配置

```go
// 支持多种格式：
// - 完全限定名称（如 openai.gpt-4o-mini）
// - 别名（如 fast）
// - 简单模型名（向后兼容）
modelConfig, found := cfg.AI.GetModelConfig(modelID)
```

## Configuration Structure

```
Config
├── Matrix       # Matrix 连接配置
│   ├── Homeserver, UserID, DeviceID
│   ├── Password / AccessToken
│   └── EnableE2EE, E2EESessionPath
├── AI           # AI 服务配置
│   ├── Providers    # 多提供商配置
│   ├── DefaultModel
│   ├── Context      # 上下文管理
│   ├── StreamEdit   # 流式编辑
│   ├── Retry        # 重试策略
│   ├── Proactive    # 主动聊天
│   └── Media        # 媒体处理
├── MCP          # MCP 配置
│   ├── Servers      # 外部服务器
│   └── Builtin      # 内置工具
├── Meme         # GIF/Sticker 配置
└── Shutdown     # 关闭超时配置
```

## Dependencies

### Internal

无

### External

- `gopkg.in/yaml.v3` - YAML 解析

<!-- MANUAL: -->