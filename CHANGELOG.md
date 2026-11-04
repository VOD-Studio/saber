# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- 新增内置工具 `saber_trace`：把持久任务的执行事件流导出成私有 JSONL 产物，写入 `task-<id>/artifacts/`（目录 0700、文件 0600、上限 16 MiB，超出按完整行截断并置 `truncated`），返回 `{artifact, records, bytes, truncated, skipped}` 而不回传正文。每行固定为 `seq/at/task_id/batch_id/kind/round/tool/error_code/duration_ms`，未知事件类型保留 `kind` 与 `raw_bytes`，损坏记录跳过计数；正文与工具参数默认不导出，`include_content:true` 只在任务发起人或本群任务管理员下附带，并剔除 `api_key`/`token`/`authorization` 形状的键与加密推理历史。授权复用 `AuthorizeOutput`（当前任务链 + 发起人/管理员），不新增执行授权名，也不依赖执行后端。
- 技能文件化：新增 `memory.skills.dirs`（只读外部技能目录，最多 8 个绝对路径）与 `memory.skills.export_dir`（导出目录，空值禁用导出），按 agentskills.io 布局读取 `<dir>/<name>/SKILL.md`。`name` 必须与目录名一致、`description` 必填，单文件上限 256 KiB，兼容 BOM 与 CRLF；启动只扫描不写库，只有显式 `/skill import` 才导入当前空间（同名同内容跳过，同名不同内容更新），群空间无写权限时转为待确认建议。新增 `/skill files`、`/skill import [--dir …]`、`/skill export <名称> [--dir …]`（文件 0600、目录 0700，拒绝 `..` 穿越）。命令分发未命中时，正文首词为 `/技能名` 且当前空间存在同名技能则进入同一条任务链路，技能正文作为运行时指令（上限 8 KiB 且不超过输入预算八分之一），`//技能名` 为转义写法。默认不配置目录、不导出，升级无需迁移。
- 定时计划新增 `cron "<分 时 日 月 周>" <时区>` 规则（5 段、必须整段引号包裹，周日 0/7），以及 `!schedule edit <ID>`（按新规则重算并恢复 active）、`!schedule run <ID>`（立即生成普通任务、不推进 `next_run`，同一毫秒幂等，上次任务未结束时 `skipped`）与 `resume`。每条计划可带 `model=<provider.model>` 冻结单次模型，并保存 IANA 时区按墙钟推进；省略 `model=` 时沿用原模型，未知模型在落库前报错。
- 新增 Telegram、Discord 与 Slack 接入端（`platforms.telegram|discord|slack`，默认关闭）。Telegram 使用 `getUpdates` 长轮询（`poll_timeout_seconds` 默认 30），Discord 使用 Gateway WebSocket + REST（必须在开发者后台开启 `MESSAGE_CONTENT` 特权 intent），Slack 使用 Socket Mode（需 `bot_token` 与 `app_token` 两个 token）。三者都支持私聊自动回复、群聊被 `@`/回复触发、`edit_interval_ms` 流式编辑与 `http_timeout_seconds`；启用时凭据必填，未启用不校验。
- OpenAI 兼容 API：新增 `GET /v1/models` 与 `POST /v1/chat/completions`（含 SSE 流式），沿用服务端 Bearer 令牌与模型注册表 `提供商.模型` 命名。该入口是非任务、不持久化的：不写入任务数据库、不读取聊天历史、不启用工具调用；AI 关闭或模型未配置时返回 OpenAI 风格 `503` JSON（`code: ai_disabled`）。需要后台任务与工具时仍走 `/v1/sessions/*`。
- 新增 `saber acp` 编辑器接入（Agent Client Protocol，NDJSON/JSON-RPC 2.0 over stdio）。ACP 进程只做协议转换，把提示转发给已运行的 `saber serve`，模型、任务与工具仍由服务进程管理；使用前必须先启动 serve 并生成 `server.token_file`，启动时会探测 `/v1/models`，连接失败或令牌被拒直接以非零码退出。支持 `-c/--config` 与 `--server`（仅回环地址）。
- 持久任务新增上下文约定文件：运行前在授权工作目录向上查找 `AGENTS.md`、`CLAUDE.md`、`.cursorrules`、`CONTRIBUTING.md`、`.github/copilot-instructions.md`，最多 8 个、单文件 64 KiB、合计 128 KiB，以 `<file path="…">` 追加到运行时资料且不写回持久化请求；用户消息中的 `@path` / `@"path with space"` 在任务入库前展开（最多 8 个、单文件 32 KiB、合计 64 KiB），越界、目录、符号链接、二进制与非法 UTF-8 跳过，无有效路径（如邮箱）保持原文。
- 执行工具新增编辑前快照与回滚：`write_file` / `apply_patch` 写回前保存原文件（含「文件不存在」），新增 `snapshot {action: list|rollback}`；`list` 返回本任务最近 50 条摘要，`rollback` 按 `snapshot_id` 或路径恢复，覆盖前会先为当前内容再存一份并返回 `pre_rollback_snapshot_id`。快照位于任务日志目录、权限 0700/0600，不新增权限，随任务删除而失效。
- 历史回忆新增 LLM 摘要：`saber_history` 检索命中后按本次授权记录生成不超过 600 字的中文摘要（输入上限 12 KiB），结果按「空间 + 查询指纹 + 最新记录 ID」缓存 10 分钟；模型不可用、超时或返回空时降级为仅返回原文，不影响检索。新增用户画像：从当前空间已保存记忆派生（最多 50 条、输入 8 KiB、输出 800 字），存 `memory_profiles` 并按条目版本判定新鲜度，后台按空间去重刷新；`/memory profile` 查看画像与新鲜度，`/memory profile refresh`（本人私聊或本群记忆管理员）立即重建。

- 定时计划支持纯聊天模式：`!schedule` 与 `saber_schedule` 创建时按当前授权收集工具，不含执行或 MCP 工具的计划以 `requires_execution=0` 冻结，不再申请工作区；运行时只用启动目录并把工具重新裁剪为聊天工具，创建后获得授权也不会升级。新增 `delivery: origin|silent`（命令入口可用目标末尾 `--silent`），`silent` 不向聊天发送、结果只落库，可用 `!task status/logs` 查询。计划运行会注入创建来源的记忆与技能快照（私聊只注入个人、群聊只注入本群）。`schedules` 表新增 `requires_execution`、`delivery` 列，旧库打开时幂等迁移，旧计划仍要求执行授权。
- 子代理：新增内置 `delegate` 工具（`agent.subagents`，默认开启）。顶层任务可把独立子目标交给子 Runtime 并行处理；子代理深度固定为 1、不继承聊天历史，`tools` 只能是父工具集的子集；同一轮多个委派按 `max_parallel` 并行，工具仍按工作区租约串行，子代理用量上推父任务账本。委派起止写入 `tasks.db` 的 `task_subagents`，并产生 `subagent_started`/`subagent_finished` 事件，SSE、TUI 与 `!task logs` 可见。
- 运行中转向：新增 `!task redirect <ID> <文本>` 与「重定向任务 #ID …」自然语言入口（权限同 `cancel`）。补充要求排队到目标任务：运行中的任务在下一轮模型请求前注入，排队中的任务在开始执行时注入；单任务未生效上限 8 条。转向文本持久化在 `task_steering`（打开数据库时幂等建表），消费与标记在同一事务内完成，重启不会重复注入；被注入内容以 `steered` 事件进入 SSE、TUI 与 `!task logs`。
- 执行工具新增危险命令绊线 `execution.command_guard`（默认 `block`，`off` 关闭）：`exec` 与 `process start` 在打开环境前按大小写不敏感的字面量规则拦截读取凭据、容器提权参数与根目录删除一类命令，命中时返回 `命令被安全策略拒绝（命中规则 "<规则>"），未执行`，不回显命令正文；`execution.command_guard_deny` 可追加最多 64 条自定义子串。它是防误操作的绊线，不是沙箱边界。

### Changed

- 命令分发在注册表未命中时不再直接按普通聊天处理：首词为 `/技能名` 且当前可信空间存在同名技能时，转为一次技能任务；`//` 前缀保留为普通文本转义。帮助与命令目录仍只列出真实注册命令。
- 记忆快照在原有条目之外追加当前空间画像（独立字节预算，放不下页眉页脚时不注入）；画像与记忆条目版本不一致时只触发一次后台刷新，不阻塞本次回复。历史摘要默认复用 `memory.review_model`，未配置时使用 `ai.default_model`。
- 本机服务对 `/v1/models` 与 `/v1/chat/completions` 单独放行：即使任务服务未装配，这两个端点仍可返回模型列表或明确的 `ai_disabled` 错误；其余 `/v1/*` 仍要求任务服务可用。
- 平台接入端接管聊天入口生命周期：`platform.Platform.Start/Stop` 成为 Matrix 同步/投递与本机终端监听的唯一入口，`bot.Run` 不再手动启动 sync 与 HTTP 服务；关闭时先停止入站，再等待任务与端口释放。`platforms.terminal.enabled` 默认 `true`；设为 `false` 后不创建本机令牌、不监听端口，`saber chat`/TUI 会明确连接失败，常驻服务与只读检查仍可用。Matrix 与 Violet 仍默认关闭。
- `agent.context.max_input_tokens` 与房间历史截断改用真正的 token 近似（4 个 UTF-8 字节折算 1 token），不再把序列化 JSON 的字节数直接当 token：此前 `max_input_tokens: 32768` 实际只相当于约 8k token，压缩触发得比配置值早约 4 倍。按字节调过参的部署应把该值除以约 4（例如改为 `8192`）以保持原有触发时机；`max_input_tokens` 现在逐字段统计消息正文、工具定义与保留的推理数据。
- 支持不设输出上限：全局 `ai.max_tokens: 0` 时主请求、评估与摘要请求都省略 `max_output_tokens`/`max_tokens`，交由模型或上游默认，避免推理模型的思考 token 被固定上限截断；任务仍按实际用量记账，超过 `agent.budget.max_total_tokens` 后拒绝后续请求。模型级 `max_tokens: 0` 仍表示继承全局。
- `agent.budget.max_total_tokens: 0` 表示不设任务用量上限：不再创建预算账本，实际用量仍汇总在任务结果中；`max_rounds`、`agent.timeout_seconds` 与单次输出上限继续生效。
- 同时执行的任务数从硬编码的 4 改为 `agent.max_concurrent_tasks`（默认 4，范围 1 到 64；省略或 0 使用默认值，越界启动时报 `agent.max_concurrent_tasks 必须在 1 到 64 之间`）。工作目录互斥不变，仍只有不同目录的任务能并行到该上限。

### Fixed

- `ai.enabled: false` 时 `memory.enabled: true` 的部署此前仍拿不到记忆服务：初始化在 AI 分支之后才打开数据库，`/memory` 与 `/skill` 只能回复「需启用 memory.enabled」。现在记忆库与 `ai.Service` 在 AI 关闭时也会装配，`/skill files`、`/skill import`、`/skill export` 与只读记忆命令无需模型即可使用；模型请求仍由 `IsEnabled` 拒绝并返回明确错误。
- 没有执行授权的用户此前只能在 `!schedule` 命令里创建纯聊天计划：模型与自然语言入口看不到 `saber_schedule`，因为工具只在 `execution.Workspace` 成功后才追加。现在只要任务服务可用且存在可信身份就暴露该工具；创建计划时是否申请执行授权仍由冻结的工具集合决定，不改变无授权用户的执行能力。
- 软限评估只看到工作状态和已完成轮次的工具结果：模型没有用 `saber_task_state` 记录计划时，第 5 轮（默认软限）就会被判成「无计划、无发现，仅有环境探测结果」而终止，即使它刚请求了下一步工具调用；任务链的原始用户指令也从未进入评估输入。现在评估输入包含逐字原始指令（按 2048 字节保留最近的）、最近轮次的助手文本，以及当前轮刚请求、尚未执行的工具调用（`BudgetProgress.Recent` 的最后一项），提示词同时说明「工作状态为空不等于没有进展」。
- 预算账本此前混用两种单位：预留按序列化字节扣减 `max_total_tokens`，记账在提供商返回 usage 时又按真实 token 累加，同一个整数里既有字节又有 token，长任务会在不可预期的位置提前耗尽。现在账本只保留 token 单位，预留与记账共用同一估算器；实际消耗超过预留时完整计入，只让后续请求失败。嵌套运行（子代理）不再替换 ctx 中的父账本，而是保留自己的上限并把每笔消耗上推父账本，父账本余额同时限制子运行的准入——此前每个子运行都有独立的 `max_total_tokens`，总开销随子运行数量成倍放大且父任务完全看不到。`BudgetStats` 新增 `main_usage`/`aux_usage` 拆分，`Result.Usage` 只表示主请求用量，任务状态里的「用量」补上 token 单位。
- 预算评估与摘要请求的输出上限原本固定为 512/1024 token，而推理模型的思考 token 同样计入 `max_output_tokens`（Responses）或 completion tokens，思考一旦超过上限，上游就以 `incomplete: max_output_tokens` 截断；评估失败随后被当成「轮数预算耗尽」终止任务，用户只能看到底层模型错误，前半段回复也没有任何实际动作。现在两类辅助请求沿用模型自身的 `max_tokens`，时限沿用请求超时，并遵守 `agent.retry` 的瞬时错误重试，一次 5xx 不再直接判死整次任务。

### Security

- 新增接入端与导出路径按最小暴露处理：Discord 出站固定 `allowed_mentions.parse: []`，模型输出中的 `@everyone`/角色提及不会变成真实 ping；技能导出使用 0600 文件与 0700 目录，名称与目标路径校验并拒绝 `..` 穿越；ACP 的 `--server` 只接受回环地址，令牌仅从 `server.token_file` 读取且不写入 stdout；OpenAI 兼容端点沿用 Bearer 令牌并拒绝带 `Origin` 的浏览器跨源请求。
- `execution.command_guard` 新增 `ask`：命中危险命令规则时不再直接拒绝，而是登记一次性审批请求，返回规则名与审批码（不回显命令正文），由同一机器人账号下该工作区的有效授权成员或本群 `task_admins` 执行 `!approve <码>`/`!deny <码>`。审批只存内存，Saber 重启后全部失效；默认有效期 600 秒（`execution.approval_ttl_seconds`，60..3600），同时最多保留 32 条待审批请求（`execution.approval_max_pending`，1..256）。批准绑定工具、命令、身份与工作区目录的指纹，只能消费一次。默认值仍为 `block`。
- 只接受真正被检查的授权：`capabilities` 里的 `publish`、`deploy`、`cross_directory` 从未有对应检查，现在启动时直接报 `capability "publish" is not enforced by this build (only external is supported)`，不再让配置看起来像已生效的保护；`room: "*"` 或成员 `"*"` 的通配授权只允许配在 `docker` 后端，`local` 下发通配会在启动时被拒绝并提示 `local execution is not a sandbox`。

## [0.5.0] - 2026-11-03

### Added

- P0 单 Agent 长任务能力：持久任务状态（原始指令、计划、发现、产物、未决问题）与运行内上下文压缩；压缩按完整模型批次提交检查点，失败不删除历史；续接从检查点水位恢复。`agent.max_rounds` 默认 40，新增 `agent.budget` 自动评估追加轮数并以 `max_total_tokens` 封顶。
- 执行能力补全：新增 `backend: local|docker`（local 默认，开启执行时必须显式选择）、任务级持久环境、后台 `process` 工具与取消清理；分页 `read_file`、`search_files`、`glob_files`、`read_output` 及不可猜测输出句柄。输出/命令超过 16 MiB、取消或超时会终止整组子进程；清理失败锁定工作区并跨重启阻断。
- 真实能力与诊断：`/status`、`GET /v1/info` 的 `capabilities/commands`、TUI 服务端命令目录、`saber doctor [-c PATH] [--json]` 只读检查；配置生成器与文档补充 local/docker、预算、压缩、工具授权与 local 隔离限制。
- 通用任务文件交付：新增可选端口 `chat.FileAdapter`（`UploadFile` + `SendUploadedFile`），`!task logs` 与 `read_file deliver=true` 产物不再绑定 Matrix，任何实现该端口的平台都能收到文件；Matrix 已实现。Violet 的 Bot API 目前没有通用文件消息，暂不实现，任务产物降级为一条文字提示。

### Fixed

- Violet 入站正文里的自定义表情此前只以 `[name:id]` token 投给模型，模型看不到表情图片，问「这是谁」只能读到一串 UUID。现在按消息快照的 `custom_emote` 下载表情图，按字节魔数识别 MIME 后作为图片附件投递，token 从正文剥离；单张下载失败只丢该附件、正文照常投递，纯表情消息剥离后正文为空仍按附件投递。
- 媒体命令能力改从已注册平台的 `chat.ImageAdapter` / `chat.FileAdapter` 推导，Violet 的梗图命令可进入帮助、命令目录和共享分发；入站回复包装保留图片发送能力，图片发送成功后的错误不再补发第二张失败卡片。Violet 仍无通用文件能力，不发布 `task logs`。
- 记忆历史投影的任务启动回调复用初始化屏障，在任务管理器与工作目录发布之后运行，修复启用记忆时后台回调读取 `Service.tasks` 的数据竞争；重启时仍同步恢复已持久化的终态历史。
- 后台记忆复盘仅在模型与工具循环成功结束后标记历史已处理；模型错误、超时、取消及轮数耗尽保留待复盘记录，后续扫描或重启可继续处理。失败产生的 token 用量仍计入配额，完成标记与用量写入错误不再被忽略。
- Matrix 私聊自动回复与共享命令统一由聊天 adapter 根据当前房间成员状态设置可信 `Direct` 身份；私聊命令保存的个人记忆可用于后续自动回复，不会注入同一用户的群聊或其他用户私聊，成员状态查询失败时不授予个人空间读取权。
- 任务结果投递不再无限重试：退避封顶 256 秒后原先会永久重试。现在新增 `agent.task_delivery_max_attempts`（默认 20，约 51 分钟持续故障）限制投递尝试次数，用尽后投递状态变为 `abandoned`，保留最后错误并写 ERROR 日志，不再被投递循环取到；尝试次数持久化，重启后继续累计，已持久化的投递分片不受影响。
- 未启用 Matrix（如仅 violet）时，任务带文件产物会因 `matrixService` 为 nil 触发 panic，投递 goroutine 崩溃带走整个进程，且任务仍为 pending 导致重启后崩溃循环。现在文件交付经平台的 `chat.FileAdapter`，不依赖 `matrixService`；没有文件能力的平台降级为幂等文字提示并按成功处理（不无限重试），`deliverTaskFile` 对空发送器返回错误，任务投递 panic 会被 recover 并按退避重试，`read_file deliver=true` 仅在平台不支持文件发送时提前返回错误。

## [0.4.0] - 2026-09-29

### Changed

- 图片模型配置从 `matrix.media.model` 提升到全局 `ai.media_model`：带图片消息的多模态模型选择不再绑定 Matrix 平台，violet 等非 matrix 平台的带图消息也能切换到多模态模型。`matrix.media` 的 `enabled`/`max_size_mb`/`timeout_sec` 保留，仍是 Matrix 入站图片解析的开关与限额。

### Removed

- 删除 `MediaConfig.Model`（`matrix.media.model`）字段。旧配置里的 `matrix.media.model` 现会被 `KnownFields` 拒绝，需迁移到 `ai.media_model`。

## [0.3.0] - 2026-09-28

### Added

- 沙箱网络访问权限：命令执行容器默认开启网络权限（`bridge` 模式），支持外网请求与 DNS 解析；支持在 `execution` 配置中通过 `network: false`（或 `network_enabled: false`）显式关闭网络进行断网隔离（`none` 模式）

- 本机 TUI/终端新增共享命令入口：`POST /v1/sessions/{session}/commands` 把界面内命令之外的斜杠指令交给同一命令注册表执行，身份由服务端令牌与操作系统用户确定，回执以本地轮次展示且不写入任务历史；`/memory` 及 `task`、`schedule` 等命令无需再依赖 Matrix/Violet 入口

- 长期记忆首个版本（P1）：新增平台无关的 `internal/memory` 模块，以独立 `memory.db` 按“用户 + 平台 + 机器人账号 + 发送者”与“群 + 平台 + 机器人账号 + 房间”两类作用域隔离保存条目，线程不改变群空间边界；作用域只从可信身份推导，模型工具与文本命令不接受用户、账号或房间参数
- 记忆条目更新与删除使用条目 ID 加预期版本发现并发覆盖，容量按 Unicode 字符在同一事务内校验，完全重复的内容返回已有条目而不重复保存
- 群共享记忆的写入区分权限：命令管理员与该群主会话的写入者可直接生效，普通成员的保存请求形成待确认建议，由有权限者确认或拒绝，审批时重新检查权限与目标版本
- 新增 `saber_memory` 模型工具与 `memory list/status/add/edit/forget/pending/approve/reject` 共享文本命令；有副作用的命令沿用持久化幂等回执，保存失败不会显示“已经记住”
- 记忆快照在任务续接恢复之后、Agent 运行之前按当前可信身份装配并追加为运行时资料，不写回持久化请求，下一次顶层任务重新读取；单次注入不超过配置上限与总输入预算的四分之一，明确保存的条目优先，未注入数量会被记录
- 定时计划任务在读取个人记忆前重新验证接收场景，无法证明为当前本人私聊时不注入个人资料
- 新增默认关闭的 `memory` 配置段（`enabled`/`max_chars`/`inject_max_bytes`），启用后 `memory.db` 加入执行器受保护路径

- 可追溯的历史回忆（P2）：任务达到终态（completed、failed、cancelled、interrupted）时在同一事务中记录待处理投影，由后台流水线异步消费并提取本轮用户正文、公开回答及来源信息，投影至 `memory.db`
- 历史全文索引基于纯 Go modernc SQLite FTS5，采用 trigram 分词器支持中文、英文及混合语料子串检索；少于三个 Unicode 字符的关键词自动回退至限定作用域与时间上限的字面模糊匹配，覆盖两字及单字中文召回
- 新增 `saber_history` 模型工具，支持关键词检索（`action: "search"`）与围绕指定记录前后翻阅（`action: "context"`）；返回记录 ID、时间、发言人、来源任务与原文片段，达到预算时提供游标与截断提示
- 历史检索与详情读取强制在查询前绑定当前可信身份推导的作用域（私聊仅查本人私聊历史，群聊仅查本群历史），拒绝跨用户、跨群及无效 ID 查询，不泄露其他会话内容
- 支持历史索引保守回填（`BackfillHistory`），系统重启或崩溃恢复时自动排空未处理终态任务投影并确认出队，保证幂等去重且不丢记录

- 自动形成长期记忆（P3）：新增后台轻量复盘流水线（`internal/ai/memory_review.go` 与 `internal/memory/review.go`），在已持久化的对话轮次上异步提炼稳定偏好与群共识，失败不影响主聊链路响应，复盘任务不递归触发
- 复盘运行于受限工具沙箱：仅允许 `saber_memory` 与 `saber_history` 工具，禁止 bash、文件写入与 MCP 工具，并受 30 秒超时、最多 2 轮模型请求、单次输入上限与独立模型（`review_model`）约束
- 自动新增、修改与删除遵循安全规则：个人空间自动新增直接生效（标为非显式保存），替换与删除必须形成待确认建议；群共享空间所有自动提炼（新增、修改、删除）均先形成待确认建议，由管理员确认
- 防重复学习与来源排除机制：删除条目与拒绝建议时自动将来源任务记录至 `memory_excluded_sources`，阻止旧复盘重新恢复已删除或已拒绝的事实；已复盘任务通过 SQLite 独立记录保证崩溃恢复幂等性与去重
- 空间与全局配额控制：支持配置 `max_scope_reviews`、`max_scope_tokens`、`max_global_reviews` 与 `max_global_tokens`，超出配额自动跳过复盘并记录日志
- 新增 `memory pause` 与 `memory resume` 共享文本命令，支持按空间随时暂停/恢复自动记忆提炼（群聊需管理员权限）；`/memory status` 实时展示容量、自动学习状态、复盘次数与 token 消耗

- 程序性经验与技能库（P4）：在 `internal/memory` 中扩展 `skill_entries` 与 `skill_changes` 数据表，将复杂工作流与排错经验沉淀为结构化程序性技能（Skill），支持按需渐进加载、版本乐观锁与审批建议流
- 渐进式发现与加载（Progressive Disclosure）：系统提示词仅在可用时注入轻量技能目录（`name`、`description` 与版本 `v`），完整正文不进入初始提示词；模型根据任务意图按需调用 `saber_skill(action="read")` 读取完整操作步骤与排错说明，大幅节省 Token 预算并避免注意力稀释
- 严格作用域隔离与权限管理：个人技能（User Scope）仅在私聊与终端生效，群共享技能（Group Scope）全群共享且与个人隔离；群内普通成员创建、修改或删除技能自动转为待确认建议，由群管理员审批生效
- CAS 乐观锁版本并发控制：技能更新与删除强制比对当前版本号，防止并发修改覆盖他人成果；审批待确认建议时进行二次版本比对，避免过时提议破坏最新技能
- 新增 `saber_skill` 模型工具（`catalog/read/add/replace/remove`）与 `/skill` 系列共享文本命令（`list/show/add/edit/delete/pending/approve/reject`），有副作用的操作受持久化幂等回执保护，TUI 菜单增加 `/skill` 快捷指令
- 后台复盘提炼流水线升级：在受限沙箱中引入 `saber_skill`，支持在已持久化的成功任务中自动识别复杂工作流与排错恢复经验，提炼脱敏为可复用规范技能

## [0.2.0] - 2026-09-24

### Added

- 新增平台无关的聊天命令注册表，统一解析 `!` 与 `/` 前缀、别名、权限、平台能力和目录描述；命令一经识别不会进入模型
- 人格绑定使用完整会话键；旧 Matrix 房间绑定保留待核查，明确确认所属账号后才关联到该账号
- Matrix 与 Violet 共用 `ping/help/version`、AI、任务、计划、MCP 和人格命令执行；Violet 启动与重连时使用 Bot Token 发布协议版本 1 的命令目录，旧服务不支持目录接口时聊天继续运行
- 新增精确配置的命令管理员和会话写入授权；模型切换、共享人格写入及上下文清理按完整平台身份检查，重复事件重发持久化回执而不重复执行副作用

### Changed

- 删除仅供 Matrix 使用的 `ai.ChatEntrypoint` 单例及旧 AI 子命令路由；Matrix 自动回复由接入端显式提供消息来源和 adapter，任务日志下载由命令入口传递事件 ID
- `ai clear/context` 覆盖持久化任务续接代号，清理后的新轮次不会恢复旧任务链，旧任务和日志仍可查询
- Matrix 启用开关从 `matrix.enabled` 移至 `platforms.matrix.enabled`，与 Violet 平行；账号及媒体等明细仍在顶层 `matrix`，旧开关需手动迁移

### Fixed

- 定时计划命令保留目标正文原有的空格、换行和引号；Violet 群聊开头提及后的 `//` 转义仍作为普通消息发送
- Release 说明写入 runner 临时目录，避免交叉编译时 Go 将干净的标签工作树标记为 dirty

## [0.1.0] - 2026-09-23

### Added

- Agent 流式事件新增独立 `thinking_delta`，仅转发 Responses 明确标为公开的思考摘要；加密推理和原始推理文本仍只用于模型协议，不进入聊天展示
- Violet 回复在任务受理时创建空正文 `pending` 消息，随后以同一幂等键和消息 ID 更新思考摘要、正文、生成状态与失败原因；生成期间每 15 秒续期，终态投递保留失败前已生成的正文
- 执行授权 `grants` 支持用 `room: "*"` 与 `users: ["*"]` 授权精确平台账号下的所有会话和成员，精确授权优先；`task_admins` 仍要求精确 ID
- 配置文件是符号链接时，运行状态跟随真实配置路径保存，以便将私有状态移出被授权的源码工作区
- 新增 `agent.task_receipt_enabled` 开关，默认关闭所有聊天平台任务提交后的「已接收，任务 #…」回执；任务持久化与最终结果投递不变，开启可恢复原有回执
- 新增 `internal/platform/violet` 平台接入端：以 Violet Bot API 的 bot 虚拟用户身份接入站内聊天，SSE 订阅 `message.created`、按 `@(name:uuid)` 剥离提及并把「只 @ 不提问」判为空内容、按 `GET /profile` 的 `user_id` 兜底过滤自回声、按消息 ID 有界去重；协议不支持 `Last-Event-ID` 补发，因此断线后改走消息历史接口按 `created_at` 水位补拉（首次连接只打基线，不回放旧消息），出站 `chat.Adapter` 提供 Send/Edit/SetTyping，编辑按 `platforms.violet.edit_interval_ms` 全局节流到 ≥200ms 以匹配 300 次/分钟的配额，超 10000 字符的正文按 Unicode 字符截断，发送复用 `chat.Reply.TransactionID` 作为必填的 `Idempotency-Key` 使任务重发不刷两遍屏

- 新增 `platforms:` 配置节与 `Config.PlatformEnabled`：`terminal.enabled`（默认可用、可显式关闭）与 `violet` 接入参数（endpoint/bot_token/account/自动回复开关/`edit_interval_ms` 默认 200ms/HTTP 超时）集中登记；`platform.Registry.Enabled` 改为按注册顺序遍历并只认配置开关，不再对 terminal/matrix 名称做硬编码判断，Matrix 关闭时其余平台也能正常启停

- Matrix 出站消息按 Markdown 渲染：`chat.Adapter` 的回复正文约定为 Markdown，`matrix.ChatAdapter` 用 mautrix 渲染为 `formatted_body`（含表格与删除线），`body` 保留 Markdown 原文供纯文本客户端回退；模型输出里的原始 HTML 一律转义，不会当作可信标记，AI 正文回复与任务投递同样受益

- 新增 `internal/platform/matrix` 平台接入端：持有 Matrix 账号，向 `ai` 注入聊天命令入口（`ai.ChatEntrypoint`）并注册任务结果投递器，Matrix 的 `!ai`/`!task run`/私聊与提及回复不再由 `ai` 核心构造平台 adapter

- 新增 `internal/platform` 包：定义可插拔聊天平台接入端的 `Platform` 接口与 `Registry` 注册表，为后续解耦 `ai.Service` 与 Matrix、接入 Violet 等新平台提供统一入口
- `ai.PromptProvider.GetSystemPrompt` 改用 `chat.Session` 取代 Matrix `id.RoomID`，`taskRequest` 不再硬编码 `message.Session.Platform == "matrix"`；人格提供者按平台自行决定是否注入，为非 Matrix 平台复用提示词链路扫除障碍

- TUI 空输入区键入 `/` 即时弹出命令菜单，支持方向键选择、名称筛选和回车执行；保留带参数命令与 Esc 返回编辑，不干扰正文斜杠或粘贴

- 新增 `saber chat` Charm v2 TUI：自适应会话侧栏、流式 Markdown/代码高亮、工具详情、模型和思考等级选择、历史续接、断线续读；退出界面不停止服务端，Esc 可取消当前任务

- 默认启动本机常驻服务（`saber serve`），提供认证的会话接口与 SSE 事件续读；持久化会话续接、消息去重、取消及每轮模型/思考等级覆盖

- 任务执行独立于聊天入口：可按平台注册结果投递，并通过持久化游标续读文本与工具事件，未启用 Matrix 时同样可运行任务

- 思考等级 `reasoning_effort` 支持 AI 全局、提供商、模型/别名逐级覆盖；同时传递到 Responses 和 Chat Completions 的全部流式及非流式入口，空值保留上游默认

- Matrix 通过默认关闭的 `matrix.enabled` 显式启用，与本机服务共用 Agent 和任务能力

- 新增 `openai-responses` 协议：支持 go-openai 非流式/流式请求、文本与图片、现有 Agent 工具循环和流式编辑；保留工具续轮及任务恢复所需的加密推理输出，明确拒绝断流和截断响应，并提供 Podlink Responses 模型配置示例

- Matrix `!schedule once/every/weekdays/list/status/pause/delete` 和同逻辑自然语言工具，回显时区与下次运行；负责人、工作目录和汇报目标绑定真实来源，每次发生独立投递并关联原消息/话题
- SQLite 持久化定时计划核心：一次性、固定周期和带 IANA 时区的工作日时间；触发与普通任务入队原子提交，重启合并错过周期、未结束时跳过并记录，派发与执行前复查负责人权限
- 自主工具接入持久化群聊任务：按真实身份筛选工具并逐次复验，命令失败可反馈模型继续修正；`read_file(deliver=true)` 将文件快照加密上传并回复原消息，发送失败只重试投递
- Docker 容器执行模块与服务端成员/群/工作区权限：提供命令、读写文件、目录检查和精确补丁工具；默认断网、唯一工作区挂载、无宿主凭据继承，支持输出归档、文件快照及超时/取消清理
- SQLite 持久化任务队列：消息去重、独立请求与执行记录、工作目录串行执行、取消与重启中断恢复；结果投递独立重试，不重放已执行任务
- Matrix 聊天任务立即回复编号，支持 `!task run/list/status/cancel`、常用中文任务操作和 `saber_task` 模型工具；结果引用原消息与话题，固定发送事务 ID 防止投递重试产生重复消息
- 通用 `chat` 消息、会话、回复和能力契约，以及内存聊天 adapter；会话键包含平台、账号和线程
- 独立 `conversation` 处理链路：统一历史组装、同会话串行调度、可取消排队、附件输入和按平台能力展示回复
- 独立 `internal/agent` 运行时：统一模型与工具循环，记录每轮响应、请求尝试、工具参数和结果；支持轮数、总时长、工具输出长度限制及明确终态
- 无聊天平台、无网络的 Agent 验收测试：工具失败反馈与参数纠正、流式/非流式一致性、取消后停止派发、超时和预算耗尽

### Fixed

- `web_fetch` 在 DNS 解析前拒绝十进制、十六进制、八进制及缩写形式的 IPv4 主机名，避免 SSRF 防护结果随系统解析器变化
- 百炼 Qwen3.8 的 Responses `reasoning_text.delta` 按公开摘要增量实时上报，生成中即可展示思考内容；其他模型仍只转发 `reasoning_summary_text.delta`
- Violet 流式回复跨模型轮次和重试保留已展示的思考摘要与草稿，新尝试的首段正文到来时原地替换草稿，避免工具调用后卡片内容短暂消失
- 修正 Violet Bot API 生成回复使用顶层 `status`、`thinking` 和递增 `revision` 的写协议；空正文占位不再被误判为普通文本并返回 400，失败重试遵守退避与 `Retry-After`，避免快速耗尽发送配额；Saber 处理错误会写入同一条失败回复卡片
- `ai.enabled: false` 时启动不再 panic：`initServices` 提前返回使平台注册表保持 nil，`run()` 调用 `Registry.Enabled` 会空指针崩溃（同时带走 `internal/bot` 整包测试），现无论 AI 是否启用先建注册表

- 上下文预算应用于持久化续聊和每次模型调用，按完整轮次裁剪并同步清理 Responses 推理数据；保留数据库历史，当前输入过大时明确终止
- 模型温度支持显式零值，备用模型使用自己的输出预算和温度；熔断状态跨任务保留，MCP 关闭时不加载内置服务器

- 聊天任务使用所选模型的输出预算和温度，遵守非流式设置及重试开关；模型请求超时由配置传入并覆盖流式读取，移除固定 30 秒总时限与 10 秒响应头时限

- 任务续接复用运行时响应校验，缺失或重复调用 ID、错误类型及截断响应仅保留文字诊断；同时修复旧版本已持久化的异常工具历史，保留合法的工具失败反馈

- 群聊与私聊回复非任务机器人消息时保留被引用内容；仅在持久化任务关联成功后使用新正文续接，控制指令仍从原始正文直接识别

- 明确 Agent 执行器采用宿主二进制 + 本机 Docker CLI/daemon 部署，补充安装、持久化状态、用户服务及真实群验收步骤；现有 Distroless 镜像仅支持关闭执行器的部署

- 续接可跨过已取消但尚未执行的补充轮次，幂等保存已恢复上下文，避免丢失祖先任务目标或重复拼接轨迹

- 增加 `!task logs <ID>` 和任务工具日志入口，发起人或本群管理员可取得失败、超时、取消及中断任务的完整执行记录与命令日志，逐文件加密并复用投递进度

- 增加按平台、机器人账号和群精确配置的 `execution.task_admins`，允许管理员取消同群任务、暂停和删除计划，管理权限不隐含执行权限

- 多附件投递持久化每份文件的加密上传元数据与发送状态，上传和发送分别限时；慢网及重启重试不再从第一份文件重新上传

- 回复任务消息可恢复原始目标、约束和工具轨迹，补充要求按持久化前序依赖排队；续接和消息关联支持重启恢复，并复查当前发言人的工作区权限

- 回复任务回执时使用原始正文识别取消等控制指令，控制识别使用去除 Matrix 引用回退后的新正文，避免控制操作排入执行队列

### Changed

- CI 将格式与 lint、测试与覆盖率、构建拆为独立任务，并固定兼容 Go 1.27 的 lint 版本；标签发布前校验版本和发布说明，上传 11 个平台的二进制文件及 SHA256 校验值
- Matrix 的 300 字节/3 秒首段阈值及 500ms/5 次编辑限制仅用于 Matrix；后台任务现在把模型增量合并成平台临时回复，Violet 使用自己的 `edit_interval_ms`（默认 200ms）原地更新，终态按稳定幂等键定稿
- 聊天中的已完成任务只发送模型回答正文，不再附加「任务 #ID：completed」；空回答显示「任务已完成」，失败及中断仍保留编号和状态
- Matrix 关闭时，执行器不再检查未启用入口的 E2EE 会话与密钥默认路径，源码工作区无需额外配置 `matrix.e2ee_session_path`
- 平台注册不再被 Matrix 启用状态挡死：`internal/bot` 的 `initServices` 原先在 Matrix 客户端为空时提前返回，关掉 Matrix 后平台注册表永远是空的；现在 Matrix 与 Violet 各自按条件注册，人格服务、`!ai` 命令注册、主动聊天与 Meme 仍按原条件装配。新增可选端口 `platform.TaskDelivery`（`DeliveryAdapter`），`registerPlatform` 据此注册任务投递，没有该端口的平台只接即时消息；启用 violet 但 `platforms.violet` 不合法时在启动阶段直接报错，而不是只留一条平台 Start 失败的 warning


- 优雅关闭的四路并行停止改用 `sync.WaitGroup.Go`：`internal/bot` 不再手写 `wg.Add(1)`/`defer wg.Done()`，关闭计数由标准库成对管理，消除编辑器/staticcheck 的 WaitGroup.Go 提示，行为与超时语义不变

- 主动聊天的房间元数据改经平台端口 `ai.ProactiveRooms`：会话枚举与元数据统一由新增的 `chat.ConversationInfo` 描述，`internal/platform/matrix` 的 `Rooms` 包装 Matrix 房间服务作为参考实现，`internal/ai/proactive*.go` 不再依赖 `internal/matrix`；未命名会话的名称回退为会话标识，决策提示词与日志不再出现空房间名

- `!ai clear/context/models/switch/current` 的回执改经平台注入的 `ChatEntrypoint` adapter 发出并以 Markdown 书写：`internal/ai` 不再调用 `matrix.CommandService` 的任何普通消息发送方法，`WithMatrix` 只剩命令注册、旧历史账号与任务文件上传；未接入平台时统一返回 `ai.ErrNoChatEntrypoint`，不再因 nil 命令服务 panic

- 删除 `ai` 中已无调用方的旧响应链路（`ResponseHandler`、`StreamEditor`、`SmartStreamHandler` 与 `ResponseContext`/`ResponseMode`）：自统一 Agent 运行时接入后 `Service.respHandler` 仅构造不使用，流式展示已全部由 `conversation.Processor` 与 `chat.Adapter` 承担；`internal/ai` 因此不再直接发送 Matrix 事件回复、typing 与 `m.replace` 关系

- `ai` 不再构造任何平台 adapter：`ChatEntrypoint` 增设 `OutboundAdapter`/`EventID`，`runAgentReply` 与任务日志的来源事件定位改由平台端口提供，`internal/ai/agent.go`、`internal/ai/task_logs.go` 去掉 `internal/matrix` 依赖

- `!ai clear`/`!ai context` 的会话作用域改由平台端口 `ChatEntrypoint.Session` 解析，`internal/ai/commands.go` 不再硬编码 `Platform: "matrix"` 与 Matrix 线程上下文键

- `ai` 的只读命令（`!task list/status/cancel/logs`、`!schedule ...`）改经平台注入的 `ChatEntrypoint.NormalizeCommand` 取得通用消息与回执 adapter，`ai/tasks.go`、`ai/schedules.go` 不再 import `internal/matrix`；`ChatEntrypoint` 未注入时统一返回 `ai.ErrNoChatEntrypoint`

- `chat.Message` 新增 `ControlText` 与 `CommandText()`：Matrix 引用回退的剥离改由接入层 adapter 完成，`ai` 识别任务控制指令时不再读取 Matrix 上下文键，`matrix.GetReplyBody` 收敛为包内私有

- `ai.NewService` 改为函数式选项签名 `NewService(cfg, opts...)`：Matrix 命令与媒体服务经 `WithMatrix` 注入、MCP 管理器经 `WithMCP` 注入，不带选项即可构造无平台依赖的 AI 服务，对应 `docs/platform-system.md` S2 的解耦步骤

- TUI 快捷键调整：Esc 停止当前会话正在执行或排队的任务，菜单内优先返回；Ctrl+C 有输入时清空、菜单内先清空并关闭菜单、空输入时退出，保留 Ctrl+Q 和 `/quit`；退出不取消服务端任务

- TUI 参考 Crush 重绘视觉：深紫灰面板、渐变字标和欢迎页、带状态的会话列表；移除输入区与选择器的整圈边框，将多行底栏收敛为输入区内的模型信息和简短操作提示，并修复嵌套 ANSI 样式造成的面板底色断层

- TUI 选择器支持名称与标识搜索并覆盖展示，保留草稿和历史位置；工具显示紧凑参数摘要，阅读历史时提示新内容，Ctrl+Home/End 跳转首尾

- TUI 输入框按内容与自动折行增高至六行，按实际组件高度分配对话区；宽屏正文限制为 108 列，支持 Ctrl+B 或 `/sidebar` 手动切换侧栏

- TUI 统一画布背景与消息左边距，收紧会话列表，模型短名称和用量移至回答末尾，输入区集中展示当前模型与情境快捷键

- 配置按 `ai`、`agent`、`matrix` 分工重整，移除旧单提供商迁移及旧字段；严格拒绝未知字段，默认生成精简服务端配置，Matrix/MCP/执行器均默认关闭，不预置空密钥模型

- 示例配置不再预填 Matrix 用户和伪令牌，默认输出上限改为 8192 tokens；旧 Matrix 部署需添加 `matrix.enabled: true`，未启用 AI 时本机服务仍提供配置状态

- MCP 工具改为默认拒绝，内置与外部服务器均需要管理员显式授权；发布、部署、跨目录能力独立声明，stdio 进程不再继承 Saber 环境变量（安全敏感变更）
- 应用启动时启用配置文件同目录下的 `tasks.db`；用户聊天转为后台任务，不读取或写入共享房间历史。工作目录固定为启动目录，重启后目录不匹配时拒绝执行
- Matrix 通过 `ChatAdapter` 规范化消息、图片、引用和线程，与内存聊天 adapter 共用 `conversation.Processor`；回复能力决定是否显示增量，模型流式传输独立配置
- 模型客户端、注册表、重试和流式解析迁移至 `internal/model`；旧 `ai` 名称通过兼容层引用，核心依赖检查禁止引入 Matrix SDK
- 会话历史迁移至平台无关存储，按平台、账号、原生会话及线程隔离；读取前清理过期消息，图片输入只组装一次，历史仅保存文本和附件标记
- MCP 调用改用通用来源身份，限流按平台与账号隔离；Matrix 历史清理命令等待当前会话运行结束，编辑后的回复保留原引用及线程关系
- 流式和非流式聊天入口共用 Agent Runtime，Matrix 展示通过运行事件更新；最终回答统一写入会话历史一次
- 模型重试与备用模型切换缩小到单次请求，避免失败后重放已执行工具；流式工具调用按索引排序，并拒绝未完整结束的响应
- 工具请求保留 `max_tokens`、`temperature` 等设置；MCP `IsError` 与序列化错误统一反馈给模型
- `ai.tool_calling` 新增 `timeout_seconds`、`max_tool_output_bytes`；旧配置可省略，默认 120 秒、32 KiB；最大轮数包含最终回答，最后一轮不再派发工具

#### 配置文件

- 加载配置时不再强制校验文件权限必须为 0600：移除 `checkFilePermissions` 与 `SABER_ALLOW_INSECURE_CONFIG` 逃生开关，权限管控交由部署环境负责

#### 工具链

- 构建工具链升级到 Go 1.27.1：`go.mod` 语言版本 `1.26.1` → `1.27.1`，Docker 构建镜像 `golang:1.26-alpine` → `golang:1.27.1-alpine`
- 文档与贡献指南同步：README 前置要求、`AGENTS.md` 中的 Go 版本说明改为 1.27.1

### Removed

#### QQ 机器人

- 移除 QQ 频道机器人全部功能：`internal/qq` 包（适配器、API 客户端、事件处理、命令注册、上下文管理）
- 移除 `qq` 配置节与 `QQConfig`（含 `Validate`、`DefaultQQConfig`），示例配置不再包含 QQ
- 移除仅为 QQ 服务的 `ai.SimpleService` 简化对话服务
- 移除依赖 `github.com/tencent-connect/botgo`

## [0.0.5] - 2026-03-26

### Added

#### Persona 人格系统

- Persona 人格系统：支持自定义 AI 人格和行为模式
- 内置人格定义：预设多种人格模板
- Persona 数据库服务层：持久化人格配置
- `!persona` 命令：动态切换和管理人格

#### Meme 表情命令

- `!meme` 命令：对接 Klipy API 获取表情图片

#### 私聊/群聊语气区分

- 决策提示词模板支持房间类型区分
- 欢迎消息和主动聊天消息支持私聊/群聊语气区分
- DecisionContext 添加 IsDirect 字段

#### 多提供商 AI 配置

- ProviderConfig 结构：支持配置多个 AI 提供商
- AIConfig 集成多提供商配置
- 兼容旧版单提供商配置格式

#### 配置增强

- ShutdownConfig：优雅关闭配置（超时时间等）
- MaxConcurrentEvents：最大并发事件数配置
- 可配置的 semaphore 数量

#### 上下文管理

- 清理不活跃房间功能：自动清理长期未活跃的对话上下文

#### 错误处理

- 机器人错误处理结构：结构化错误类型
- 相关错误处理方法

#### 构建与部署

- Docker 支持：Distroless 基础镜像，多架构支持 (amd64/arm64)
- 新平台构建：FreeBSD、OpenBSD、LoongArch64 (龙芯)

### Changed

#### 架构重构

- Matrix 命令模块化：拆分 ping/help/ai/meme/version 命令到独立文件
- 命令注册机制：统一的命令注册和管理
- AI 服务拆分：service.go 拆分为职责单一的模块
- 事件处理拆分：提取到 events.go，精简 handlers.go
- BuildInfo 统一：移动到 commands 包

#### 代码质量

- 使用 ruacontext 包统一管理上下文操作
- 优雅关闭超时与错误类型增强
- 移除 CGO 相关 SQLite 驱动，简化部署

### Performance

- 上下文清理与并发限制优化
- HTTP 连接池复用

### Security

- 配置文件权限强制检查（0600）
- SABER_ALLOW_INSECURE_CONFIG 环境变量允许禁用权限检查

### Fixed

- 工具调用迭代次数逻辑错误
- 测试环境中的驱动重复注册问题
- 并发测试中的竞态条件
- 配置加载时保留默认值

### Tests

- 添加 bot 初始化和 shutdown 的单元测试
- 添加 AI 配置相关功能的单元测试
- 添加 IsDirect 相关功能的单元测试
- 添加加密存根实现

## [0.0.4] - 2026-03-23

### Added

#### 主动聊天功能

- 主动聊天系统：AI 根据决策引擎自主发起对话
- 消息时间记录：跟踪用户消息时间用于主动聊天决策
- 流式请求支持：决策引擎可使用流式请求进行决策

#### MCP 工具集成

- 完整的 MCP (Model Context Protocol) 集成
- 内置服务器支持：工具到服务器的自动映射
- Web 搜索工具：使用 SearXNG 实例进行网络搜索
- Web 获取工具：带 SSRF/XSS 防护的网页内容获取
- JS 沙箱工具：安全的 JavaScript 代码执行
- 外部 MCP 服务器配置支持

#### 流式工具调用

- 流式响应中的工具调用支持
- StreamToolHandler 流式工具处理器
- 工具调用的实时处理流程

#### 模型管理

- ModelRegistry 模型注册表：动态模型管理
- 模型切换命令：`!model` 命令动态切换模型
- 模型命令处理集成

#### 图片识别

- AI 对话中的图片识别能力
- 支持配置专用视觉模型进行图片识别
- 媒体处理配置扩展

#### 数据库支持

- SQLite 数据库支持，双驱动系统（CGO/纯 Go）
- 数据库惰性初始化和错误处理

#### 其他功能

- `!version` 命令：显示版本和构建信息
- 系统提示词配置支持
- 请求速率限制器

### Changed

#### 架构重构

- Bot 初始化逻辑从 Run() 提取到独立函数
- AI 命令处理完全重构，模块化设计
- Matrix 事件处理拆分为专注的处理函数
- Strategy 模式抽象 AI 客户端
- Circuit Breaker 集成到重试处理器

#### 性能优化

- HTTP 连接池复用
- 字符串拼接优化
- 上下文管理器优化

### Security

- stdio MCP 服务器命令白名单
- HTTP 客户端强制 TLS 1.2 最低版本
- 配置文件强制 0600 权限
- HTML 输出使用 bluemonday 消毒

### Fixed

- SQLite 驱动移除 panic，改用惰性初始化
- ContextManager.Stop() 支持安全多次调用
- 定时触发器的房间列表获取功能
- 启动时过滤启动前的历史消息

## [0.0.3] - 2026-03-17

### Added

#### 回复消息功能

- 回复机器人消息时触发 AI 响应（`reply_to_bot_reply` 配置）
- 流式响应中支持回复消息
- 群聊使用 SendReply 发送回复消息
- 回复的消息作为 AI 上下文

#### 群聊提及回复

- 群聊 @提及 自动回复功能
- ParseMentions 方法解析结构化提及
- Element 客户端提及格式支持
- 提及显示名称前缀自动剥离

#### 并发处理

- 并发消息处理提升性能
- EventID 注入到处理上下文

### Changed

- 使用现代 Go 惯用法重构代码
- 改进开发工具和修复 lint 问题
- AGENTS.md 文档精简优化

### Fixed

- 配置加载时保留默认值
- 回复检测优先于提及检测避免误触发
- Element 提及格式解析
- 启动时阻止历史消息处理

## [0.0.2] - 2026-03-12

### Added

#### AI 功能

- AI 服务集成，支持多模型配置（`!ai-fast`, `!ai-creative` 等）
- 流式响应：实时输出 AI 回复，智能消息编辑
- 上下文管理：每个房间独立的持久化对话上下文
- 私聊自动回复：在私聊中无需 `!ai` 前缀即可触发 AI 响应
- AI 输入指示器：AI 响应时显示 Matrix 打字状态
- 重试机制：失败自动重试，支持指数退避和模型降级

#### E2EE 端到端加密

- 可选的 E2EE 支持，使用 goolm（纯 Go 实现，无需 CGO）
- CryptoService 接口：抽象加密服务，支持 NoopCryptoService 空实现
- OlmCryptoService：基于 mautrix CryptoHelper 的完整实现
- 持久化 pickle key：自动生成并保存加密密钥

#### 配置扩展

- E2EE 配置验证、默认值和示例生成
- 多模型配置支持，各自独立的温度、token 参数

#### 测试

- E2EE 加密模块的完整单元测试

### Changed

- 使用 tint 替换标准 slog，实现彩色日志输出
- 版本信息注入改用 ldflags，支持 git describe
- AI 客户端和服务添加完整的结构化日志
- 改进流式响应的错误处理
- 代码风格遵循现代 Go 惯用法

### Removed

- 移除未使用的 `CreateRetryConfigFromAIConfig` 占位函数

## [0.0.1] - 2026-03-10

### Added

- 初始项目结构
- Matrix 客户端基础连接
- YAML 配置文件加载
- CLI 标志解析
- 结构化日志
- 基础命令系统（`!ping`, `!help`）

[Unreleased]: https://github.com/VOD-Studio/saber/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/VOD-Studio/saber/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/VOD-Studio/saber/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/VOD-Studio/saber/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/VOD-Studio/saber/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/VOD-Studio/saber/compare/v0.0.5...v0.1.0
[0.0.5]: https://github.com/VOD-Studio/saber/compare/v0.0.4...v0.0.5
[0.0.4]: https://github.com/VOD-Studio/saber/compare/v0.0.3...v0.0.4
[0.0.3]: https://github.com/VOD-Studio/saber/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/VOD-Studio/saber/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/VOD-Studio/saber/releases/tag/v0.0.1
