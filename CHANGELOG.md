# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[0.0.2]: https://github.com/your-username/saber/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/your-username/saber/releases/tag/v0.0.1

