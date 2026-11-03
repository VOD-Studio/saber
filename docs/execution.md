# 自主工具与执行权限

执行工具默认关闭，关闭时聊天、任务、计划与记忆不受影响。开启后必须先显式选择后端：

- `local`（默认）：命令以 Saber 宿主用户身份运行，无容器隔离；适合单机可信环境。
- `docker`：每个任务一个预装 Python 3 的容器，工作区挂载到 `/workspace`；适合需要文件系统边界或断网的场景。可在 `workspaces.<name>.backend` 覆盖全局后端。

升级时必须显式写 `backend`：`execution.enabled: true` 且活动工作区的 `workspace.backend` 与 `execution.backend` 都为空时，启动会拒绝并提示 `execution backend must be explicit when enabling execution`。已有 Docker 部署补 `backend: docker`，新部署生成配置默认 `backend: local`。

## 工具

模型可连续调用以下工具，不需要逐条确认：

- `exec {command}`：在当前工作区运行 shell 命令；非零退出作为可纠正的工具错误返回，不自动重跑。
- `process {action,...}`：`start|list|status|wait|logs|kill` 管理本任务后台作业；每任务最多 8 个运行中作业，作业 ID 为 `p_<32hex>`，只能由当前任务访问。
- `read_file {path, offset?, limit?, byte_offset?, deliver?}`：分页读取文本；`offset` 是 0-based 行号，`limit` 为 1..1000（默认 200），单页正文最多 4096 字节。超长行用返回的 `next_byte_offset` 续读；二进制或非法 UTF-8 会被拒绝。`deliver=true` 改为保存完整文件快照并交付，不能与分页参数同时使用。
- `list_files {path?}`：列出直属条目，展示链接名称但不跟随。
- `write_file {path, content}`、`apply_patch {path, edits:[{old,new}]}`：父目录必须存在；补丁要求每处 `old` 唯一匹配后才写回，单文件上限 16 MiB。
- `search_files {pattern, path?, file_glob?, case_sensitive?, offset?, limit?, include_hidden?}`：Go RE2 正则搜索文件内容，返回 `path`、1-based `line` 与命中文本；默认跳过隐藏项和 `.git`。
- `glob_files {pattern, path?, offset?, limit?, include_hidden?}`：按路径段匹配 `* ? []` 与 `**`（匹配零个或多个路径段）。
- `read_output {output_id, offset?, limit?}`：按原始字节游标回读任何工具或后台作业的输出归档，`limit` 为 4..4096。

搜索与 glob 都按字典序深度优先遍历，最多 100000 项、32 层；内容搜索跳过首 8 KiB 含 NUL 或超过 4 MiB 的文件，累计读取上限 64 MiB。触达硬限时返回 `truncated:true`、`eof:false`、`next_offset:null`，应缩小 `path` 或 `file_glob`。搜索/glob 的 `data` 编码后不超过 8 KiB。

所有文件工具共用 Go 实现，两个后端语义一致。工作区路径通过 `os.OpenRoot` 钉住，逐组件拒绝链接；Unix 上以 `O_NOFOLLOW|O_NONBLOCK` 打开后校验普通文件，避免符号链接、FIFO 与末组件替换。`local` 后端不会对整个工作区做挂载节点扫描；Docker 后端在打开执行环境前和每次新命令前检查挂载中的 IPC/设备节点。

## 输出、回读与取消

每次调用分配不可猜测的 `output_id`（`t<taskID>-<32hex>`），完整输出写在 `<log_dir>/task-<taskID>/output-<32hex>.log`，目录 `0700`、文件 `0600`。模型只看到有界 `summary` 与句柄；日志超过 16 MiB 时保留已写内容并终止对应进程。`read_output` 的 `offset` 必须落在 UTF-8 字符边界；运行中的日志读到当前末尾返回 `eof:false` 和 `caught_up:true`，writer 关闭后到末尾才 `eof:true`。

`read_output` 只接受当前任务或祖先任务的句柄，并校验调用者为源任务发起人或本群任务管理员；不能凭同房间猜句柄读取其他人的日志。终态任务与合法续接任务仍可回读。

`process` 的 `logs` 复用同一套字节分页。`wait` 默认 10 秒、上限 30 秒，超时只返回 `running`，不会杀进程；`kill` 幂等。成功查询固定 `exit_code: 0`，启动/协议/权限错误为 `-1` 并带 `error`。

前台 `exec` 使用 `execution.timeout_seconds`；环境打开与后台作业使用完整任务 context，不继承单次工具超时。取消、超时或输出超限会向整组进程 TERM，最多 2 秒后 KILL，并等待输出收集结束。

## 环境与清理

每个持久化任务首次需要执行时才创建环境：

- `local`：任务级私有 `TMPDIR`、工作区 cwd 与已校验的 `workspaces.<name>.env`；只额外保留宿主 `PATH`、`HOME`、`LANG`、`LC_ALL`，不复制机器人 API/token 环境。每条命令使用独立进程组，`cd/export` 不跨命令保留，但 `$TMPDIR` 中的文件与后台作业可复用。
- `docker`：每个任务 create/start 一次容器，PID 1 用 Python 长期 sleep 保活；命令通过 `docker exec` 的 NDJSON supervisor 执行。容器只读根、非 root、cap-drop、no-new-privileges、128 pids、512 MiB/1 CPU、工作区唯一 bind mount，不挂 Docker socket 或凭据；`/tmp` 为 256 MiB 的 `rw,nosuid,nodev` tmpfs（允许 `go test` 生成临时可执行文件），网络为 bridge 或显式 `network:false` 时的 none；`--pull=never`，不自动下载镜像。

首次 create/start/exec 前先原子写入 `<log_dir>/task-<id>/environment.json`，清理成功后才标为 `closed`。清理失败会保留任务为 `running`、锁定目录、写入 `quarantine` 标记并公开 `environment_cleanup_failed`；重启后该目录继续阻止领取排队任务，需管理员核查后手动标记 closed。Docker 残留按 owner 标签在启动时清理；local 重启不会按缓存 PID 猜杀，见 `saber doctor` 的旧环境记录提示。

`task.Options.Finalize` 在保存终态前清理环境，清理失败不宣布完成/取消。服务停止时在任务队列退出后再兜底关闭执行器，总清理上限 15 秒。

## local 隔离限制

`local` 不是安全沙箱：命令可读取 Saber 用户可访问的配置密钥、任务/记忆数据库和其他工作区；程序层个人/群隔离不构成 OS 隔离。没有恶意 daemon 防护、断网或资源配额；主动 `setsid`/daemon 逃逸无法强制约束；Saber 被 SIGKILL 后也不能保证清理所有 local 孤儿进程。取消验收只覆盖 Saber 持有并管理的普通命令与派生进程。不要把 `local` 授权发给不可信群成员；需要强隔离或断网时使用 `backend: docker`。因此 `local` 后端不接受 `room: "*"` 或成员 `"*"` 的通配授权，启动时直接拒绝。

`local` 支持 Darwin/Linux/FreeBSD/OpenBSD 并使用 `/bin/sh`；其他平台保持二进制可编译，活动 local 工作区含 `exec`/`process` 授权时明确报错 `local execution is unsupported on this platform; configure backend: docker`。`network: false` 对 local 不生效，配置时会拒绝启动（`local backend cannot enforce network isolation`）。

## 配置

```yaml
execution:
  enabled: true
  backend: local # 或 docker；活动工作区必须显式选择
  # image: "python:3.13-slim" # 仅 docker，需预先安装
  # network: false # 仅 docker；默认开启
  log_dir: "/srv/saber-private/execution"
  timeout_seconds: 60
  workspaces:
    project:
      path: "/srv/workspaces/project"
      # backend: docker # 可选，覆盖全局
      env:
        LANG: "C.UTF-8"
  task_admins:
    - platform: "matrix"
      account: "@saber:example.org"
      room: "!room:example.org"
      users: ["@operator:example.org"]
  grants:
    - platform: "matrix"
      account: "@saber:example.org"
      room: "!room:example.org"
      users: ["@alice:example.org"]
      workspace: "project"
      tools: ["exec", "read_output", "read_file", "list_files", "write_file", "apply_patch", "search_files", "glob_files", "process"]
      capabilities: []
  mcp_requirements: {}
```

`task_admins` 只授予同群任务取消、日志读取与计划暂停/删除，不授予执行工具。`room` 或 `users` 使用 `"*"` 通配时，该工作区的有效后端必须是 `docker`；`local` 会直接拒绝启动并提示 `workspace "..." cannot grant wildcard room/users on the local backend`。工作区必须是现有绝对路径，不得包含 Saber 配置、任务/人格/记忆数据库或执行日志；不同工作区不可嵌套。启动时会克隆并冻结授权，恢复任务时重新核对目录与工具权限。

Docker 部署还要求 Saber 与 Docker CLI/daemon 在同一宿主用户会话中，镜像预装 `/usr/bin/env`、Python 3 与 `/bin/sh`。生产环境建议固定镜像 digest。现有 Distroless 运行镜像只适用于 `execution.enabled: false`。

## MCP 与外部能力

所有 MCP 实际调用都经过同一权限检查，默认拒绝未知工具、未授权成员、账号/群不符和缺少能力的调用。本构建只实施 `external` 一种能力：每个 MCP 工具都必须在 `mcp_requirements` 声明，并在授权里单独授予 `external`，不信任服务端自报的只读描述。`publish`、`deploy`、`cross_directory` 没有任何对应检查，配置它们会在启动时报 `capability "publish" is not enforced by this build (only external is supported)`，不会静默当作已保护。

```yaml
execution:
  mcp_requirements:
    "mcp:release:publish": []
  grants:
    - platform: "matrix"
      account: "@saber:example.org"
      room: "!room:example.org"
      users: ["@alice:example.org"]
      workspace: "project"
      tools: ["exec", "read_file", "mcp:release:publish"]
      capabilities: ["external"]
```

stdio MCP 仍要求原有命令白名单，其子进程只得到 `PATH` 和显式 `env`。

## 诊断

`saber doctor [-c PATH] [--json]` 只读检查配置、模型、agent/context/budget、命令权限、工作区与私有文件权限、SQLite 快照、local 平台与 `/bin/sh`，以及仅对实际使用的 Docker 工作区执行 `docker info`、镜像检查与残留容器列举。它不监听端口、不创建 token/目录/数据库、不清理容器、不发模型请求；普通模式输出 `ok|warning|error code message`，`--json` 输出 `{checks, ok}`。退出码：0 无错误、1 存在配置/依赖错误、2 非法参数。

`/status` 命令与 `GET /v1/info` 的 `capabilities` 使用同一真实能力判断：`available|disabled|unauthorized|unavailable`，并显式给出 `backend` 与 `sandboxed`。本机 `info` 只代表固定的 `terminal/saber/status` 诊断会话，具体 TUI 会话授权仍以 `/status` 为准。