# 编辑器接入（Agent Client Protocol）

`saber acp` 让支持 [Agent Client Protocol](https://agentclientprotocol.com/) 的编辑器（例如 Zed）通过 stdin/stdout 使用 Saber 的模型。ACP 进程本身只做协议转换：它把编辑器的提示转成 OpenAI 兼容请求，转发给已经运行的 `saber serve`。模型、任务和工具仍由服务进程管理，ACP 不重复装配它们。

## 前置条件

1. 已配置好 `ai.enabled` 与至少一个模型，并启动本机服务：

   ```sh
   saber serve -c ./config.yaml
   ```

2. 配置文件中有可用的 `server.listen` 与 `server.token_file`。令牌文件由服务启动时生成，ACP 只读取，不会打印或转发到其他主机。

ACP 启动时会请求一次 `/v1/models` 探测服务：连接失败或令牌被拒会直接在 stderr 说明原因并以非零码退出。

## 用法

```sh
saber acp [-c ./config.yaml] [--server http://127.0.0.1:8320]
```

| 选项 | 说明 |
| --- | --- |
| `-c`, `--config` | 配置文件路径，默认 `./config.yaml` |
| `--server` | Saber 服务地址，默认 `http://<server.listen>`；只接受回环地址 |
| `-h`, `--help` | 输出用法 |

## 编辑器配置

以 Zed 为例，在 `settings.json` 中注册外部 Agent：

```json
{
  "agent_servers": {
    "Saber": {
      "command": "/absolute/path/to/saber",
      "args": ["acp", "-c", "/absolute/path/to/config.yaml"]
    }
  }
}
```

如果服务不在默认地址，追加 `--server`：

```json
{
  "agent_servers": {
    "Saber": {
      "command": "/absolute/path/to/saber",
      "args": ["acp", "-c", "/absolute/path/to/config.yaml", "--server", "http://127.0.0.1:8320"]
    }
  }
}
```

编辑器通过管道启动该进程后即可选择 Saber 会话；进程退出由编辑器关闭 stdin 触发。

## 协议覆盖范围

传输格式为按行分隔的 JSON-RPC 2.0（NDJSON），每行一个完整的请求、响应或通知。

| 方法 | 方向 | 说明 |
| --- | --- | --- |
| `initialize` | 客户端 → Agent | 返回 `protocolVersion: 1`、`agentCapabilities.promptCapabilities`、`loadSession: false` 和空 `authMethods` |
| `session/new` | 客户端 → Agent | 生成 256 位随机 `sessionId`；接受 `cwd`、`mcpServers`，本版本只记录不生效 |
| `session/prompt` | 客户端 → Agent | 提交文本提示，流式返回 `session/update` 通知，结束时 `stopReason` 为 `end_turn` 或 `cancelled` |
| `session/cancel` | 客户端 → Agent | 取消该会话在途的 HTTP 请求；返回空结果 `{}` |
| `session/update` | Agent → 客户端 | `sessionUpdate: "agent_message_chunk"`，`content.type` 为 `text` |

错误码：

| 码 | 场景 |
| --- | --- |
| `-32700` | 行内容不是合法 JSON |
| `-32600` | 不是单个 JSON 对象，或缺少 `method`、`jsonrpc` 不是 `2.0` |
| `-32601` | 未知方法 |
| `-32602` | 参数类型或内容无效（例如非 `text` 内容块、未知会话） |
| `-32000` | 上游服务返回错误（消息已截断，不含令牌） |
| `-32001` | 上游鉴权失败 |

无 `id` 的请求按通知处理，不返回响应；`session/cancel` 因此可以在提示进行中立即生效。

`session/new` 可选接受 `model` 字段指定本轮会话使用的模型标识；省略时使用服务端默认模型。

## 限制

- 不启用工具调用：编辑器发出的文件读取、终端等请求不会被代理，Saber 只返回文本。
- 不透传 MCP 服务：`session/new` 中的 `mcpServers` 仅记录，不会连接到 Saber。
- 会话只保存在内存：ACP 进程退出即丢失历史，不支持 `session/load`（`loadSession: false`）。
- 必须先运行 `saber serve`；ACP 不加载模型、不访问任务数据库。
- 只支持纯文本提示，图片、音频与嵌入上下文会被明确拒绝。
- 服务地址必须是回环地址，避免把本机令牌发往其他主机。