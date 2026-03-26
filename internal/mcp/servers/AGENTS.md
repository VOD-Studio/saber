<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-26 | Updated: 2026-03-26 -->

# servers

## Purpose

内置 MCP 服务器实现，提供网页获取、互联网搜索和 JavaScript 沙箱功能。

## Key Files

| File | Description |
|------|-------------|
| `builtin.go` | 内置服务器注册和创建 |
| `web_fetch.go` | 网页获取服务器，`fetch_url` 工具 |
| `web_search.go` | 网页搜索服务器，`web_search` 工具（SearXNG） |
| `js_sandbox.go` | JavaScript 沙箱服务器，`run_js` 工具 |
| `stdio.go` | stdio 类型外部服务器连接 |
| `http.go` | HTTP 类型外部服务器连接 |
| `shared_client.go` | 共享 HTTP 客户端（连接复用、TLS 配置） |

## For AI Agents

### Working In This Directory

- **内置服务器列表**: `BuiltinServers = ["web_fetch", "web_search", "js_sandbox"]`
- **内存传输**: 内置服务器使用 `mcp.NewInMemoryTransports()` 通信

### Common Patterns

#### 内置服务器列表

```go
for _, name := range servers.BuiltinServers {
    fmt.Println(name)
}
// 输出: web_fetch, web_search, js_sandbox
```

#### 创建内置服务器

```go
client, session, err := servers.CreateBuiltinServer(ctx, "web_search", &cfg.Builtin)
```

#### 使用共享 HTTP 客户端

```go
httpClient := servers.GetSharedHTTPClient()
// 自动配置 TLS 1.2+、连接复用、超时等
```

## Available Tools

| 服务器 | 工具 | 描述 |
|--------|------|------|
| web_fetch | `fetch_url` | 获取网页内容并转换为文本 |
| web_search | `web_search` | 搜索互联网获取相关信息 |
| js_sandbox | `run_js` | 在安全沙箱中执行 JavaScript 代码 |

## Dependencies

### Internal

- `rua.plus/saber/internal/config` - 配置定义

### External

- `github.com/modelcontextprotocol/go-sdk/mcp` - MCP SDK
- `github.com/dop251/goja` - JavaScript 运行时（JS 沙箱）

<!-- MANUAL: -->