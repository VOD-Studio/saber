<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-25 | Updated: 2026-03-25 -->

# cli

## Purpose

命令行参数解析包，使用 Go 标准库 `flag` 实现简单的命令行界面。

## Key Files

| File | Description |
|------|-------------|
| `flags.go` | 命令行参数定义和解析 |

## For AI Agents

### Working In This Directory

- **简单包**: 只有一个文件和一个公开函数 `Parse()`
- **无外部依赖**: 仅使用 Go 标准库

### Common Patterns

#### 使用方式

```go
flags := cli.Parse()
// flags.ConfigPath - 配置文件路径
// flags.Verbose - 是否启用调试日志
// flags.ShowVersion - 是否显示版本
// flags.GenerateConfig - 是否生成示例配置
```

#### 支持的参数

| 参数 | 简写 | 默认值 | 描述 |
|------|------|--------|------|
| `-config` | `-c` | `./config.yaml` | 配置文件路径 |
| `-verbose` | `-v` | `false` | 启用调试日志 |
| `-version` | | `false` | 显示版本信息 |
| `-generate-config` | | `false` | 生成示例配置 |

## Dependencies

### Internal

无

### External

- `flag` - Go 标准库

<!-- MANUAL: -->