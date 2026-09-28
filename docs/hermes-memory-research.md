# Hermes 长期记忆机制研究

研究日期：2026-09-28。研究范围：NousResearch/hermes-agent 的官方文档与源码；上游固定快照为 [`9a0a1625367242596d338ae2da541c4a1fc785a2`](https://github.com/NousResearch/hermes-agent/commit/9a0a1625367242596d338ae2da541c4a1fc785a2)，提交时间为 2026-09-27 22:48:07 -04:00。本文没有运行 Hermes 或连接外部记忆服务，结论属于源码核实；检索效果、模型记忆准确率与运行成本尚未实测。

## 结论

Hermes 的基本长期记忆由三部分协作：少量常驻事实、可检索的历史会话、按需读取的操作技能。模型通过工具维护这些资料，周期性后台复盘帮助发现值得保存的内容。它无需向量数据库即可提供基本跨会话记忆；Honcho 等外部服务是可选扩展。“自我改进”在这些已核实机制中指更新记忆和技能文档，不是在线修改模型权重。[记忆初始化][memory-init]、[历史检索工具][session-tool]、[技能加载][skills-loading]、[后台复盘][review-finalizer]

Saber 已选择“各用户独立记忆，群聊另有共享记忆”。Hermes 内置文件以 home/profile 为边界，这与 Saber 的用户、群聊权限不同。可以借鉴其分层和工具流程，不能把同一份 `USER.md`、`MEMORY.md` 和整库历史检索直接暴露给所有聊天用户。[内置路径][memory-path]、[profile 使用约定][memory-home]、[历史跨 profile 读取][session-profile]

## 1. 常驻事实：MEMORY.md 与 USER.md

| 项目 | 核实结果 |
| --- | --- |
| `MEMORY.md` | 保存环境事实、项目约定、工具问题、路径和端点等跨任务资料。 |
| `USER.md` | 保存用户信息、通用偏好、交流方式和长期期待。 |
| 默认容量 | 分别为 2,200 和 1,375 **字符**，可配置。Python 实现以字符串长度计数，分隔符也计入；官方约 800/500 token 仅为估算，不是 token 预算。 |
| 文件形式 | `$HERMES_HOME/memories/` 中的文本文件，条目以 `\n§\n` 分隔。 |
| 装载方式 | Agent 初始化时读取，形成系统提示词快照。普通写入即时落盘，但不立即修改这个快照。压缩后使系统提示词失效时，会重新读取文件。 |

来源：[条目用途][review-routing]、[默认值][memory-init]、[条目形式与计数][memory-store]、[快照][memory-snapshot]、[压缩后刷新][memory-refresh]。

当前提示词要求只把“与每种会话都有关”的事实放入常驻记忆；具体工作流、任务中的纠正和操作步骤进入技能。短期状态进入历史。记忆以陈述事实的形式表达，避免把用户偏好改写成能够覆盖本轮要求的命令。[提示词策略][memory-guidance]

容量满时写入失败，模型可合并或删除条目后重试；不会自动淘汰最旧内容。完全相同的条目会去重，没有在此基础文件实现中发现语义去重。外部手工写入超过上限的文件在读取时会告警、保留原文，因此这些上限并非不可突破的注入硬预算。[新增与替换][memory-mutations]、[超限读取][memory-load]

## 2. memory 工具：修改、并发与删除

`memory` 提供 `add`、`replace`、`remove`，也支持一批 `operations`。`target` 选择 `memory` 或 `user`。它没有独立的模型侧 `read/search` 动作；初始内容由系统提示词提供，部分失败结果返回当前条目。当前成功回执只返回完成状态、容量、条目数等，并不重复返回整份清单。[工具入口与 schema][memory-tool]、[成功回执][memory-success]

`replace/remove` 以唯一匹配的旧文本定位条目，歧义时失败；`replace` 替换整个条目，`old_text` 不是待替换片段。批操作在同一个目标内全部成功才写入，并仅对最终结果检查容量，可在一次调用中腾出空间并新增内容。并发写入使用独立锁文件，在锁内重新读取、修改，再临时文件写入和原子替换，避免用旧快照覆盖别人新写的内容。[定位与修改][memory-mutations]、[事务式批处理][memory-batch]、[并发更新][memory-atomic]

`memory.write_approval` 默认关闭；开启后，交互 CLI 的前台记忆写入要求确认，其余入口暂存待审。**无人值守后台复盘中的 `replace/remove` 即便总审批开关关闭，也会暂存而不直接执行。** 暂存修改绑定完整的被改条目；审批时若条目已变化则拒绝，避免把过时建议应用到新内容。[审批策略][write-approval]、[后台删除门禁][memory-background-gate]、[过时条目保护][memory-mutations]

写入和加载时会扫描一组提示注入、外传等威胁模式；加载发现异常时只在注入快照中屏蔽，原文件保留以供查看和删除。这是模式扫描，不能据此声称记忆内容绝对可信。`remove` 删除该事实条目；它不等价于删除历史会话、所有备份或外部服务中的全部副本。[扫描与读取][memory-load]、[删除动作][memory-mutations]、[外部写入通知][provider-mirror]

## 3. 历史回忆：SQLite、FTS5 与按需原文

当前 `session_search` **不调用 LLM 生成检索摘要**，其结果来自 SQLite 中实际存储的消息。按参数提供四种形态：关键词发现、围绕消息 ID 前后翻阅、按 session ID 读取、浏览近期会话。发现默认返回 3 个会话，上限 10；默认只检索 user/assistant，工具输出需显式选择。[检索实现与 schema][session-tool]

发现按会话谱系去重；默认完整展开排名第一的命中，其余给命中锚点，后续可读取更多。读取模式超过 30 条消息时返回前 20、后 10 条，每条内容最多 2,000 字符；发现模式的首尾片段上限 1,200 字符、锚点窗口上限 4,000 字符。截断有 `content_truncated` 和原长度信息。翻阅模式可每侧取 1–20 条消息，当前 `_scroll` 不传内容截断参数，能够重取完整存储内容；这也意味着极大消息可能带来较大结果。[结果裁剪][session-shaping]、[读取][session-read]、[翻阅][session-scroll]

基础检索使用 FTS5/BM25，支持短语、布尔条件、时间范围及时间排序。压缩归档的原始消息仍可检索；被 undo/rewind 隐藏的记录默认不参与检索。自动化任务被降权，一些内部子任务来源不作为普通用户历史展示。这些是召回相关性与可见性策略，不能替代用户授权。[FTS 查询][fts-search]、[来源过滤][session-sources]

中文检索并非安装 FTS5 就自然解决：上游当前提供可选的 `cjk_unicode61` 原生扩展，使用 CJK 二元词组；扩展不可用时还有 trigram/LIKE 路径，短中文词会走不同分支。Saber 使用纯 Go 构建，设计时应单独验证中文子串、混合中英文和短词召回，不应直接照搬 Hermes 的原生扩展。[CJK 扩展][fts-cjk]、[检索路由][fts-routing]

默认只读取当前 profile 的数据库；显式 `profile` 参数可选择另一个存在的 profile 只读打开。该工具接口没有 Saber 所需的“当前用户/当前群”授权参数，也不把 profile 名称视为不可由模型选择的租户身份。因此 Saber 需要在查询、读取详情和翻阅三个入口统一先约束可见空间，不能只过滤搜索摘要。[profile 选择][session-profile]、[参数分发][session-dispatch]

## 4. 自动形成记忆与压缩边界

前台模型可以在对话中调用 `memory`。周期触发方面，默认每 10 个用户轮次触发一次记忆复盘；技能另按工具迭代累计触发。最终回复交付后才启动后台复盘，没有最终回复或被中断的轮次不走这条正常后置触发路径。复盘使用独立 Agent，但共享内置记忆存储，并限制允许调用的工具。[计数器][review-tick]、[交付后触发][review-finalizer]、[复盘工具约束][review-tools]

后台复盘可关闭或改用辅助模型；它有额外 token 与模型成本。写入成功通知、预算等运行选项是目前完整产品的一部分，不能把周期提示误解为每条消息都必定提取成功。[后台复盘设置][review-settings]

压缩前当前源码调用外部 provider 的 `on_pre_compress`，把返回的记忆上下文交给摘要流程；可配置要求具备持久化 checkpoint 能力，否则停止该次压缩。提交压缩结果前还会调用 `commit_memory_session`，其已核实实现通知外部 provider 与上下文引擎。在当前内置压缩调用链中，没有核实到“每次压缩前必然启动一个只允许 memory 工具的 LLM，并把事实写进两个内置文件”的机制。规划可保留压缩前提取钩子，但不应把旧版 flush 描述标为当前必备行为。[压缩前钩子][pre-compress]、[提交边界][compression-commit]、[commit_memory_session][memory-commit]

## 5. Skills：程序性记忆

Hermes 把可重复的任务步骤、排错经验及针对某类工作的偏好写为 `SKILL.md` 和参考文件。先看到名称、描述等目录，再按需读取技能正文和参考文件，降低每次会话都携带长文档的成本。`skill_manage` 可创建、修改、删除和维护附属文件；它是长期记忆的一个独立层。[渐进加载][skills-loading]、[技能管理][skills-management]

对 Saber 的启示：偏好与事实先进入结构化记忆；可复用流程可以作为后续阶段，但应独立于身份资料。学会一个工作流意味着保存可供以后使用的说明，并不自动授予执行工作流中工具或命令的权限。这一点是 Saber 的设计要求，不是对 Hermes 全部授权实现的审计结论。

## 6. 可选外部后端与身份模型

默认内置文件能够单独工作。配置可选一个外部 provider 与内置记忆并存；也可以明确关闭内置两个存储。provider 接口涵盖初始化、系统提示、按轮预取、对话同步、会话结束、压缩前钩子和专用工具。内置成功写入后再通知外部 provider，携带写入来源和替换前内容；暂存待审批不当作已写入。具体删除、保留和故障处理仍由 provider 决定，并非一个跨本地与远端的事务。[provider 选择][provider-init]、[接口][provider-api]、[同步通知][provider-mirror]

Honcho 是可选的用户建模/语义召回后端，支持云服务或自托管。当前集成包含 `honcho_profile/search/context/reasoning/conclude` 等工具。其 gateway 用户映射是显式配置：可将运行时身份映射到特定 peer，也可加前缀避免平台 ID 碰撞；`pinUserPeer` 会把用户归并到一个 peer，默认关闭。多个 Hermes profile 可以共享 workspace 中的人类 peer、同时使用独立 AI peer。[Honcho 官方说明][honcho-doc]、[身份配置][honcho-identity-doc]、[身份解析源码][honcho-identity]

Honcho 的多用户身份映射不能反推内置 `USER.md` 已有相同隔离。Saber 第一阶段没有必要依赖 Honcho、Mem0 或向量库；先把用户和群空间、来源、删除语义及检索权限做到一致。未来添加外部服务时，身份映射和删除传播应当是接口约束，并保留本地失败可见性。

## 7. 文档差异与研究边界

| 上游描述 | 固定快照核实结果 |
| --- | --- |
| README 仍写 FTS5 + LLM summarization。 | 当前 `session_search` 明确无 LLM 调用；README 该句不能用作当前检索设计依据。 |
| Memory 文档写返回消息不截断，且列三种调用形态。 | 源码有四种形态；发现与读取有内容裁剪，翻阅当前可取回完整存储消息。 |
| Memory 文档写整个会话中快照完全不变，只下个会话读取新内容。 | 普通写入不会刷新快照，但压缩后的显式系统提示词失效会重新加载。 |
| Memory 文档写工具回执始终展示 live state。 | 当前成功回执不返回整份条目清单，失败或变更回执才按需带内容。 |

来源：[README][readme-claim]、[Memory 文档][memory-doc]、[检索工具][session-tool]、[压缩刷新][memory-refresh]、[成功回执][memory-success]。

本次未进行上游历史版本溯源，不能断言上述机制具体在哪个版本变更；只固定当前检查的 commit。没有用官方文档中的检索毫秒数、模型成本降低倍数或“无限容量”作为实测指标。也没有对全部 provider、gateway 平台和插件做权限审计。Saber 计划中的多人隔离、原文回溯和防止删除后重新学习，需要以本仓库测试与真实聊天验收单独证明。

[memory-init]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/agent/agent_init.py#L1306-L1343
[memory-path]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/memory_tool.py#L38-L40
[memory-home]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/website/docs/user-guide/features/memory.md#L22-L24
[memory-store]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/memory_tool_store.py#L88-L105
[memory-snapshot]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/memory_tool_store.py#L463-L489
[memory-refresh]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/agent/system_prompt.py#L806-L824
[memory-guidance]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/agent/prompt_builder.py#L193-L230
[memory-mutations]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/memory_tool_store.py#L276-L360
[memory-load]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/memory_tool_store.py#L133-L164
[memory-tool]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/memory_tool.py#L207-L241
[memory-success]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/memory_tool_store.py#L468-L481
[memory-batch]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/memory_tool_store.py#L397-L460
[memory-atomic]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/memory_tool_store.py#L245-L274
[write-approval]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/write_approval.py#L142-L190
[memory-background-gate]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/memory_tool.py#L166-L204
[session-tool]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/session_search_tool.py#L651-L780
[session-shaping]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/session_search_tool.py#L237-L350
[session-read]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/session_search_tool.py#L447-L477
[session-scroll]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/session_search_tool.py#L531-L574
[session-profile]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/session_search_tool.py#L434-L444
[session-dispatch]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/session_search_tool.py#L577-L616
[session-sources]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/tools/session_search_tool.py#L21-L45
[fts-search]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/hermes_state_search.py#L1085-L1143
[fts-cjk]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/hermes_state_fts.py#L20-L52
[fts-routing]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/hermes_state_search.py#L890-L922
[review-routing]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/agent/background_review.py#L336-L357
[review-tick]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/agent/turn_context.py#L705-L722
[review-finalizer]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/agent/turn_finalizer.py#L734-L766
[review-tools]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/agent/background_review.py#L1084-L1123
[review-settings]: https://hermes-agent.nousresearch.com/docs/user-guide/features/memory#running-the-review-on-a-cheaper-model-auxiliarybackground_review
[pre-compress]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/agent/conversation_compression.py#L2950-L2987
[compression-commit]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/agent/conversation_compression.py#L3688-L3711
[memory-commit]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/run_agent.py#L911-L916
[skills-loading]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/website/docs/user-guide/features/skills.md#L195-L205
[skills-management]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/website/docs/user-guide/features/skills.md#L610-L659
[provider-init]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/agent/agent_init.py#L1345-L1384
[provider-api]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/agent/memory_provider.py#L84-L201
[provider-mirror]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/agent/memory_manager.py#L773-L840
[honcho-doc]: https://hermes-agent.nousresearch.com/docs/user-guide/features/memory-providers#honcho
[honcho-identity-doc]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/plugins/memory/honcho/README.md#L196-L222
[honcho-identity]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/plugins/memory/honcho/session_peers.py#L76-L107
[readme-claim]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/README.md#L19-L26
[memory-doc]: https://github.com/NousResearch/hermes-agent/blob/9a0a1625367242596d338ae2da541c4a1fc785a2/website/docs/user-guide/features/memory.md#L219-L242
