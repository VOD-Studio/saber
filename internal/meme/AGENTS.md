<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-25 | Updated: 2026-03-25 -->

# meme

## Purpose

GIF/Sticker 搜索服务，集成 Klipy API，支持搜索和下载 GIF、贴纸和梗图。

## Key Files

| File | Description |
|------|-------------|
| `service.go` | Klipy API 客户端，搜索和下载功能 |
| `command.go` | Matrix 命令处理器（`!meme`, `!gif`, `!sticker`） |

## For AI Agents

### Working In This Directory

- **API 提供商**: Klipy (https://api.klipy.com)
- **共享客户端**: 使用 `mcp/servers.GetSharedHTTPClient()` 复用连接

### Common Patterns

#### 创建服务

```go
memeSvc := meme.NewService(&cfg.Meme)
```

#### 搜索内容

```go
// 搜索 GIF
results, err := memeSvc.Search(ctx, "funny cat", meme.ContentTypeGIF)

// 随机获取一个
gif, err := memeSvc.GetRandom(ctx, "funny cat", meme.ContentTypeGIF)
```

#### 下载图片

```go
data, err := memeSvc.DownloadImage(ctx, gif)
```

## Content Types

| 类型 | 常量 | 描述 |
|------|------|------|
| GIF | `ContentTypeGIF` | 动图 |
| Sticker | `ContentTypeSticker` | 贴纸 |
| Meme | `ContentTypeMeme` | 静态梗图 |

## Commands

| 命令 | 描述 |
|------|------|
| `!meme [--gif\|--sticker\|--meme] <关键词>` | 搜索并发送梗图 |
| `!gif <关键词>` | 搜索 GIF |
| `!sticker <关键词>` | 搜索贴纸 |

## Dependencies

### Internal

- `rua.plus/saber/internal/config` - 配置定义
- `rua.plus/saber/internal/mcp/servers` - 共享 HTTP 客户端

### External

无（使用标准库 HTTP 客户端）

<!-- MANUAL: -->