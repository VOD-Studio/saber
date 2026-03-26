# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[0.0.5]: https://github.com/your-username/saber/compare/v0.0.4...v0.0.5
[0.0.4]: https://github.com/your-username/saber/compare/v0.0.3...v0.0.4
[0.0.3]: https://github.com/your-username/saber/compare/v0.0.2...v0.0.3
[0.0.2]: https://github.com/your-username/saber/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/your-username/saber/releases/tag/v0.0.1

