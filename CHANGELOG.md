# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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

[Unreleased]: https://github.com/your-username/saber/compare/v0.0.5...HEAD
[0.0.5]: https://github.com/your-username/saber/compare/v0.0.4...v0.0.5
[0.0.4]: https://github.com/your-username/saber/compare/v0.0.3...v0.0.4
[0.0.3]: https://github.com/your-username/saber/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/your-username/saber/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/your-username/saber/releases/tag/v0.0.1
