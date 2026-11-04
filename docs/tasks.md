# 持久化任务

启用 AI 后，应用自动在配置文件同目录创建 `tasks.db`（权限 `0600`）。`!ai <内容>`、模型快捷命令、已启用的私聊自动回复、群聊提及和回复机器人都会创建任务。数据库写入成功后模型调用和工具执行在后台继续；完成时聊天中显示回答正文。Matrix 在失败或中断时显示任务编号和状态，Violet 则在同一回复卡片上显示失败状态并保留部分正文。`agent.task_receipt_enabled: true` 时 Matrix 额外回复「已接收，任务 #42」，默认不发送。数据库写入失败时不执行任务。

Violet 在任务入库后立即创建空正文 `pending` 回复，因此即使关闭 `agent.stream`，首段正文出现前也有持久化反馈；它不受 `agent.task_receipt_enabled` 控制，也不再额外发送任务编号回执。运行开始标为 `thinking`，收到正文后标为 `streaming`；启用 `agent.stream` 时按 `platforms.violet.edit_interval_ms` 合并增量，静默期间每 15 秒续期。终态入库后以相同幂等键取回消息并编辑为 `completed` 或 `failed`；失败卡片显示错误并保留最后一次模型尝试的部分正文。Matrix 仍按原有展示阈值更新临时回复。临时编辑失败不会重跑任务；重启后可重试终态投递。

```text
!task run 检查这个项目并总结问题
!task list
!task status 42
!task cancel 42
!task redirect 42 先看部署配置，再检查日志
!task logs 42
```

`list` 展示本群最近 20 条任务。`status` 展示发起人、目录、状态、原消息链接、话题、执行记录数量、待生效补充要求数量、结果与投递状态。查询限定在当前平台、机器人账号和群内；任务发起人及 `execution.task_admins` 精确授权的本群管理员可取消任务。取消已结束任务只返回现有结果。

`redirect` 把补充要求排队到目标任务，供运行中转向使用：任务仍 `queued` 或 `running` 时不取消、不重开，运行中的任务在**下一轮模型请求之前**注入，排队中的任务在开始执行时注入。回执区分「已排队，将在下一轮生效」与「任务开始时生效」；目标已进入终态时明确拒绝，提示直接回复结果续接或新建任务。权限与 `cancel` 相同（发起人或本群任务管理员），文本去首尾空白后限制 1..4096 字节，单任务未生效的补充要求最多 8 条，超出返回错误且不写入。运行中的消费与标记在同一事务内完成，因此重启后不会重复注入；若任务在消费前结束，未投递的行保留在库中并计入 `!task status` 的待生效数量，不会静默丢弃或自动重放；`!task --` 只把后续文字作为新输入，不会重新注入旧的补充要求。

向机器人发送「查看任务列表」「查询任务 42 的状态」「取消任务 #42」会直接调用同一套查询与取消逻辑，不等待模型；「重定向任务 #12 <文本>」「补充任务 #12 <文本>」走与 `!task redirect` 相同的排队逻辑，编号后必须有空格或冒号分隔正文。其他自然表达可由模型调用 `saber_task` 工具；群和用户身份来自消息入口，模型参数不能覆盖身份。需要及时取消运行中任务时，使用 `!task cancel` 或上述直接识别的中文格式。

独立任务不混入其他人的房间历史。回复结果或原交办消息可续接；开启接收回执后也可回复回执续接。系统按平台消息 ID 关联任务，恢复原始目标、约束、模型响应和工具结果，不从引用正文猜任务编号。后续轮次有新编号并保存关联关系；前序运行中时先排队，结束后再加入补充要求，不改变已派发的命令。同一回执的多条补充按最新后续轮次串联。重启后关联仍保留；中断操作缺少结果时明确标为未知，要求先核查副作用。续接必须使用相同的当前授权工作区，执行工具仍按当前发言人的权限重新筛选。`!ai clear/context` 仍操作同步会话历史，对持久化任务无影响。

## 执行与恢复

- `tasks` 保存编号、账号、发起人、群、来源消息/话题、工作目录、请求快照、状态、完整运行结果与投递进度；`task_events` 按顺序保存模型尝试、工具开始/结束等执行记录。模型文本增量不逐条写入数据库，完整模型响应仍会保存。
- `task_steering` 保存运行中补充要求：`id`、`task_id`、`text`、`created_at`、未生效时为空的 `delivered_at`。表与索引随数据库打开幂等创建，旧库无需手工迁移。运行开始时按任务读取未投递行并在同一事务内标记 `delivered_at`，因此重启后不会重复注入；被注入的文本以 `steered` 事件写入 `task_events`，`/v1/sessions/{s}/tasks/{id}/events` 与 `!task logs` 可见。
- 同一个账号、群和 Matrix 事件 ID 有唯一约束。重复送达及重启后重送都返回原任务编号；开启接收回执后，回执使用固定 Matrix 事务 ID。
- 获得执行授权的成员使用管理员为该账号、群和成员配置的工作区，详见 [执行权限](execution.md)；工具始终在容器中操作挂载为 `/workspace` 的目录。未授权成员仍可聊天，但没有命令、文件或 MCP 工具。没有工作区授权的纯聊天任务沿用启动目录作为调度键。
- 重启时重新筛选当前允许的工具，并核对任务目录与当前授权目录；授权目录发生变化时拒绝执行旧任务，避免操作错误目录。
- 调度器将路径转为绝对路径并解析符号链接。工具暂未声明读写属性，因此同目录的所有任务都串行执行；不同目录的并发任务数由 `agent.max_concurrent_tasks` 限制（默认 4，范围 1 到 64）。运行中的取消请求在执行器退出后才释放目录。
- 启动时 `queued` 任务继续执行，旧 `running` 任务变为 `interrupted`。中断任务不自动重放；工具的开始事件在派发前落盘，结束事件用于辅助核查，但无法证明远端副作用恰好完成了一次。核查后需要继续的工作应明确创建新任务。
- 工具开始日志写入失败时取消运行，防止产生未记录的副作用。保存终态失败时任务保持 `running` 并占住目录，需处理存储故障；重启将其标为中断，不会再次执行。
- 结果与终态先落盘，然后独立发送。网络失败按退避时间只重试汇报和已归档文件（1、2、4…秒，封顶 256 秒）；尝试次数（含首次）达到 `agent.task_delivery_max_attempts`（默认 20，约 51 分钟持续故障）后，投递状态变为 `abandoned`：记录最后一次错误并写 ERROR 日志，不再自动重试，任务结果与已发送的分片仍保留，可用 `!task status`、`!task logs` 查看。尝试次数持久化在 `tasks.db`，重启后继续累计（停止服务时正在发送的那次会因 context canceled 失败并多计一次，影响很小）。放弃后不会再向用户发送提示，因为原投递通道本身已持续不可用；需要时由管理员核查后重新提交任务；文本和每个文件均使用固定 Matrix 事务 ID，回复关系始终指向原消息及原话题。
- 排队取消直接变为 `cancelled`；运行取消通知执行器退出，不能撤销已经发生的外部副作用。停止服务时运行任务标为中断，排队任务保留。

任务正文、附件、工具参数及结果可能包含私密信息；数据库与 WAL/SHM 文件均不可提交。一份数据库只供一个机器人进程使用。超长汇报截断为 12,000 字符，完整结果保留在数据库；发起人或本群任务管理员可用 `!task logs <ID>` 下载已结束任务的完整请求、结果及事件 JSON，以及每条命令的完整输出文件，包含失败、超时、取消和重启中断情况。每份文件独立上传、加密、记录发送进度；重复同一下载消息可续传，新消息可重新下载。运行中的任务需结束或取消后再下载，避免把变化中的日志当作完整记录。

## 批次（`saber_batch`）

模型可用内置 `saber_batch` 把多条互不依赖的指令一次提交成独立任务，`action` 取 `submit`、`status`、`cancel`。`submit` 必填 `items`（1..20 条），每条为 `{text, silent?}`：`text` 去首尾空白后必须有内容且不超过 8000 字符，`silent: true` 表示结果只落库、不向聊天投递；可选 `label` 是批次说明，不超过 256 字符，不参与执行。校验在落库前完成，条目进入存储层后在同一事务里写入 1 条批次与 N 条任务，任一条不合法整批拒绝，不会留下「有任务无批次」的半批。`batch_id` 形如 `b_<16hex>`，只由程序生成。

批次条目各自是根任务：只带本条的 `text`，不继承提交者的对话历史、任务链或检查点，条目之间也不建立关联，因此批次不是续接。它也不绕过工作目录互斥与 `agent.max_concurrent_tasks`：同一授权工作区的条目照样串行执行，不能用它做并行加速。

- `submit`：返回 `{batch_id, task_ids}`，任务编号按 `items` 顺序排列。
- `status`：`batch_id` 必填。返回 `{batch_id, total, counts, items}`；`counts` 始终包含 `queued`、`running`、`completed`、`failed`、`cancelled`、`interrupted` 六个键并补零，`items` 每项只有 `{task_id, status, created_at}`（`created_at` 为 UTC 的 RFC 3339 纳秒时间），正文与结果仍需用 `!task status`、`!task logs` 查看。
- `cancel`：`batch_id` 必填，逐条取消仍在 `queued` 或 `running` 的任务，返回 `{cancelled, skipped}`。已进入终态的任务与取消失败的任务都进 `skipped`，不算失败，也不把部分成功伪装成整批成功。

`batch_id` 只接受 `b_<16hex>` 形状；提交、查询与取消都按 platform + account + room 过滤并复用 `CanManage` 判定，只有批次创建者或本群任务管理员能查询与取消，凭编号猜不到其他群或他人的批次。

已知缺口：重启恢复把 `running` 任务直接批量标为 `interrupted`，这条路径不经过批次结束逻辑，因此被中断的批次不会补写 `batch_finished` 事件，导出与 `!task logs` 会看到 `batch_submitted` 没有配对结束事件。批次状态本身不依赖该事件，`saber_batch status` 仍按任务行算出真实状态。

## 轨迹导出（`saber_trace`）

模型可用内置 `saber_trace` 把持久任务的执行事件流导出成私有 JSONL 产物：`{action: "export", task_id, format: "jsonl", include_content?}`，`action` 目前只有 `export`，`format` 省略或为 `jsonl`，`task_id` 必须是正整数。

产物写入该任务自己的 `task-<id>/artifacts/`（目录 `0700`、文件 `0600`），文件名为 `<32hex>-trace-<task_id>.jsonl`，随机前缀与交付文件同一套命名规则，调用方只能给出展示名。单份产物上限 16 MiB，超出后按完整行截断并置 `truncated`，不会写出半行。返回 `{artifact, records, bytes, truncated, skipped}`：`artifact` 是产物文件名，`records` 是写入行数，`bytes` 是产物字节数，`skipped` 是损坏记录数（单条坏记录只计数，不中断导出），正文不回传给模型。

每行固定为 `seq`、`at`、`task_id`、`batch_id`、`kind`、`round`、`tool{name, call_id, args_bytes}`、`error_code`、`duration_ms`，工具行只给参数字节数。当前版本不认识的 `kind` 仍原样保留，并附 `raw_bytes` 记录原始长度；批次任务的行带所属 `batch_id`，非批次任务为空字符串。

默认只导出事件元数据。`include_content: true` 才附带 `content`（对话正文或工具结果）与 `args`（工具参数原文），且只对 JSON 形状的正文按键名剔除凭据：键名小写后含 `apikey`、`api_key`、`token`、`authorization`、`secret`、`password`、`credential`、`cookie`、`responseshistory` 等片段的一律移除；纯文本原样保留，加密推理历史与 Checkpoint 从不进入导出器。

授权复用 `AuthorizeOutput`：只能导出当前任务及其祖先链上的任务，且要求调用者是源任务发起人或本群任务管理员。链外任务即使同群、本人发起也会报 `output belongs to an unrelated task`；缺少持久任务上下文时直接拒绝，报 `轨迹导出需要持久任务上下文`。

## 验收

```sh
# 消息去重、两个用户上下文隔离、目录互斥、取消、重启、结果重试
go test -tags goolm ./internal/task

# 运行中重定向：权限、终态拒绝、上限、排队/运行路径、重启后仍生效
go test -race -tags goolm ./internal/task -run 'TestManager_Redirect|TestManager_ConsumeSteering'

# 模拟 Matrix 与模型：快速回执、命令、自然语言、来源关系和固定投递事务
go test -tags goolm ./internal/ai -run 'TestTasks|TestNaturalTask|TestTaskReply'

# 补充要求注入轮次与事件顺序
go test -race -tags goolm ./internal/agent -run TestRuntime_Steering

# 并发竞态与仓库门禁
go test -race -tags goolm ./internal/task ./internal/ai ./internal/matrix
make fmt-check lint test-cover-check build
```

这些测试使用临时 SQLite 和本地 HTTP 服务；真实 homeserver、E2EE 与模型上游的运行验收需在部署环境完成。
