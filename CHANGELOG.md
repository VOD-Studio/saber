# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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

- 回复任务消息可恢复原始目标、约束和工具轨迹，补充要求按持久化前序依赖排队；续接和消息关联支持重启恢复，并复查当前发言人的工作区权限

- 回复任务回执时使用原始正文识别取消等控制指令，显式命令先去除 Matrix 引用回退文本，避免控制操作排入执行队列

### Changed

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
