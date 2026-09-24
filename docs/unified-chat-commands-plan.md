# 通用聊天命令与 Violet 接入方案

状态：通用文本命令已实施；Violet 媒体能力与真实平台验收待完成。本文记录 Saber 侧的命令定义、执行和目录发布约定；Violet 侧的接口、存储与聊天框交互见其仓库 `docs/prd/0030-saber-slash-commands.md`。两份文档共同定义跨仓库协议。

## 目标与现状

目标是在 Matrix 和 Violet 使用同一份命令定义与处理逻辑。Matrix 保留现有 `!` 用法；Violet 以 `/` 作为菜单和输入用法。命令仍作为普通聊天消息收发，回复由消息来源平台的 adapter 投递。

实施前 `ai.ChatEntrypoint` 是单例，方法签名使用 mautrix 用户和房间类型；Matrix 注册 `!ai`、`!task` 等命令，Violet 则只把普通提问和 `!ai <正文>` 交给共享聊天链路，跳过其他 `!` 命令。`!ai clear/context` 只作用于同步聊天历史，持久化任务的续接上下文仍独立存在。人格按 Matrix 房间 ID 绑定，任务日志和 meme 使用 Matrix 文件上传。通用化分别处理了这些实际依赖。

## 命令接口

新增平台无关的命令模块，集中维护注册表、解析、帮助、目录和执行入口。命令定义包含稳定 ID、路径、别名、中文描述、参数、所属功能、所需平台能力、作用域、执行权限和处理函数；帮助及目录从同一份定义生成。

```go
// 示意：最终类型名和错误返回形式以实现为准。
Dispatch(ctx context.Context, msg chat.Message, reply chat.Adapter) (handled bool, err error)
Describe(platform string, capabilities CommandCapabilities) []Descriptor
```

`Dispatch` 的 `handled=true` 表示命令已被识别，即使参数或权限检查失败也不再送给模型。命令处理器只使用 `chat.Message`、`chat.Identity`、`chat.Session` 与出站 adapter；平台 SDK 类型留在 Matrix/Violet 接入端。机器人装配各功能的处理器，命令模块不 import AI、Matrix 或 Violet。

入口顺序固定为：接入端验证来源身份与消息形态 → 生成 `chat.Message` → 命令分发 → 未识别时走普通聊天。取消、查询等控制命令在创建 AI 任务之前执行，不能被该会话的长任务阻塞。`ping/help/version` 的注册不依赖 `ai.enabled`；AI 相关命令按实际配置启用。迁移后删除只供 Matrix 使用的 `ChatEntrypoint` 单例；仍被 Agent 旧入口使用的会话、事件 ID 和出站能力，改由其 Matrix 调用点显式提供，不能直接删去。

| 命令 | 平台无关业务处理 | 现有特殊点 |
| --- | --- | --- |
| `ping/help/version` | 基础状态、注册表帮助 | Matrix 当前自行注册 |
| `ai <正文>`、`ai clear/context/models/current/switch`、模型快捷别名 | 聊天、上下文和模型注册表 | 当前入口及部分子命令带 mautrix 类型 |
| `task run/list/status/cancel` | 任务创建、查询和控制 | 沿用任务权限、会话归属与持久化去重 |
| `schedule once/every/weekdays/list/status/pause/delete` | 定时计划 | 创建和修改继续检查当前执行授权 |
| `persona list/set/clear/status/new/del` | 人格读写 | 当前键是 Matrix 房间 ID，回复由 Matrix 发送 |
| `mcp list` | MCP 信息查询 | 按启用状态和允许披露的内容生成目录 |
| `task logs`、`meme` | 命令语义可共用 | Violet Bot API 尚无文件或图片上传发送能力 |

文本命令完成通用化后，`task logs`、`meme` 仍需单独补齐 Violet 媒体能力。在补齐之前，Violet 的目录不发布这些命令；手工输入要得到明确的“该平台暂不支持”回执。

## 语法与兼容

- Matrix 继续接受 `!task list` 等原有写法，也可接受由同一注册表定义的 `/` 文本写法；Matrix 客户端可能自行处理 `/`，文档继续推荐 `!`。
- Violet 推荐 `/task list`。过渡期接受已有的 `!ai <正文>`和其他 `!` 别名，但所有前缀都进同一个解析器。
- 命令只识别正文开头，或由平台确认的“开头提及本 bot”之后。引用回退、代码块、URL 与正文中的斜杠不参与命令识别。`//` 转义开头斜杠。
- 保留用户正文的换行、空格与引号；`/ai <正文>`将剩余内容原样作为问题。`/ai -- <正文>`用于问题开头恰好是 `clear` 等保留词的情况。
- 已知命令参数错误返回用法；未知 `/` 命令返回未知命令提示；两者均不得当作 AI 问题执行。原有 `!ai <普通问题>` 的对话语义保留。
- 群聊命令在 Violet 的入站原文为 `@(saber:<bot-user-id>) /task list`。接入端只删除开头寻址自身的提及，不删除命令参数中的提及。没有可信目标的群聊命令不执行。

## 作用域与授权

执行身份只能来自平台适配器。命令处理时用完整的 `chat.Identity` 校验平台、机器人账号、会话和发送者，不从文本参数或模型输出推断身份。

| 操作 | 作用域与限制 |
| --- | --- |
| `task list/status`、`schedule list/status` | 维持现有会话可见范围 |
| `task cancel/logs`、`schedule pause/delete` | 发起人或该会话精确配置的任务管理员 |
| `task run`、计划创建 | 执行器按当前配置校验工作区和工具授权 |
| `ai clear/context`、`persona set/clear` | 当前 `chat.Session`；群聊修改权限由 Saber 配置明确规定 |
| `ai switch`、共享人格 `new/del` | 全局操作，需独立的命令管理员授权 |

现有 `ai switch` 会修改进程级默认模型。迁移时保留这一语义，在目录和回执中标为“全局”，限制为命令管理员；不把任务管理员、Violet 站点管理员或房间创建者自动等同于命令管理员。会话级模型选择若需要，另设新命令和持久化状态，不悄悄改变现有命令含义。

人格绑定改用完整的 `chat.Session.Key()`。迁移现有 Matrix 房间绑定时，只关联能确定归属的账号和房间；不能确定的旧记录保留并报告，不复制到所有账号。

`ai clear/context` 必须反映当前正式任务入口实际使用的上下文。清理后旧任务和日志仍可查询，但新的续接轮次不能从清理前的任务链恢复对话；清理与运行中任务并发时，旧任务终态不能写回新上下文。可在持久化会话状态中记录上下文代号，续接时只读取同一代号的任务链。`context` 回执显示当前有效上下文，不再把同步历史当作持久化任务上下文的完整状态。

Violet SSE 重连和历史补拉都可能再次投递相同消息。任务与计划继续使用已有去重键；其他会产生副作用的同步命令按平台、账号、会话、消息 ID 和命令 ID 记录执行回执。重复投递只重试回复，不重做操作。跨数据库或外部副作用在崩溃后状态不确定时，返回需核查的状态，不自动重放。

## 对 Violet 发布的命令目录

Saber 是目录内容的唯一作者。所有启用的命令装配完成后，Violet 适配器通过现有 Bot Token 向 `PUT /api/v1/chat/bot/commands` 发布当前 bot 的完整快照。启动、重连或配置变化时重发；空列表表示撤销。发布失败可重试，不阻断聊天，也不向浏览器暴露 Bot Token。Violet 存储目录，浏览器通过 Violet 的登录会话读取，不能直连 Saber 本机 HTTP 接口。

请求体协议版本为 1：

```json
{
  "schema_version": 1,
  "commands": [
    {
      "id": "task.status",
      "path": ["task", "status"],
      "description": "查看任务状态",
      "arguments": [{"name": "id", "type": "integer", "required": true}],
      "scope": "conversation"
    }
  ]
}
```

Violet 校验协议版本、大小、字段、命令 ID 和重复项，并计算内容摘要 `revision`。`path` 不带 `/` 或 `!`；使用说明和补全文本从路径与参数生成。目录按平台能力和当前配置过滤，不含未启用命令、密钥、内部工作区路径或私人数据。目录只描述 bot 可提供的功能，不能当成成员授权结果；Saber 在每次执行时重新校验权限。

Violet 侧查询端点为 `GET /api/v1/chat/conversations/{conversationId}/bot-commands`，返回当前会话中已启用 bot 的用户 ID、显示名、`revision` 和命令数组。目录更新时间不作为 bot 在线状态。回滚到不支持通用命令的 Saber 版本前，先发布空目录。

## 实施与验收

1. 建立注册表、解析器和内存 adapter 测试：覆盖 `!`/`/`、别名、参数、未知命令、转义、权限失败和“命令不入模型”。
2. 将 Matrix 内置命令、AI、任务和计划逐项迁入，保留旧用法；单独完成角色授权、人格键迁移和上下文清理语义。
3. 将 Violet 入站改为共用分发器，验证私聊、群聊寻址、多个 bot、重复事件及控制命令在 AI 繁忙时可执行。
4. 接入 Violet 目录发布与版本校验；旧 Violet 不支持目录接口时清晰记录同步失败，聊天继续可用。
5. 补齐 Violet 文件和图片能力后启用 `task logs`、`meme`；目录与实际可执行能力保持一致。

每个可独立验证的功能点按仓库规范单独提交并更新 `CHANGELOG.md`。Saber 检查 `make fmt-check lint test-cover-check build` 和 `make test`，所有 Go 命令带 `goolm` 标签。模拟平台与 `httptest` 只证明本地链路；真实 Matrix、Violet、模型、重连和多 bot 投递需要单独验收。

### 实施记录（2026-09-24）

- 已完成步骤 1–4 的文本命令链路：Saber 共用注册表、完整会话授权和持久化命令回执；Violet 已保存目录、按目标投递并提供聊天框补全。Violet 用户提及后的 NBSP 与普通空格均可作为命令前导分隔。
- `task logs` 与 `meme` 在 Violet 手输时返回平台不支持，目录不发布；Violet Bot API 仍无文件或图片上传发送通道，步骤 5 及对应真实媒体验收待该接口完成。
- 本地验证：Saber `make fmt-check lint test-cover-check build test`（覆盖率 77.6%）；Violet `make api-test api-lint api-build`、`make web-lint web-typecheck`、关闭 Node 26 实验性 Web Storage 后的 `make web-test web-build`（1214 通过、1 跳过），以及 Playwright 聊天命令和乐观消息场景（3 通过）。Violet PostgreSQL 集成测试因未配置 `BLOG_TEST_PG_DSN` 跳过；真实平台、模型与多 bot 联调尚未执行。
