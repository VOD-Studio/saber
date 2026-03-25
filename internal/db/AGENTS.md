<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-25 | Updated: 2026-03-25 -->

# db

## Purpose

SQLite 数据库驱动注册包，支持 CGO 和纯 Go 两种驱动实现。

## Key Files

| File | Description |
|------|-------------|
| `sqlite_nocgo.go` | 纯 Go SQLite 驱动（modernc.org/sqlite），用于 CGO_ENABLED=0 |

## For AI Agents

### Working In This Directory

- **构建标签**: 使用 `//go:build !cgo` 条件编译
- **当前状态**: CGO 驱动已被移除，仅保留纯 Go 驱动

### Common Patterns

#### 使用方式

```go
// 在 main.go 中通过空白导入注册驱动
import _ "rua.plus/saber/internal/db"
```

#### SQLite 驱动选择

| 文件 | 构建标签 | 驱动 |
|------|----------|------|
| `sqlite_nocgo.go` | CGO_ENABLED=0 | `modernc.org/sqlite` |

## Dependencies

### Internal

无

### External

- `modernc.org/sqlite` - 纯 Go SQLite 驱动

<!-- MANUAL: -->