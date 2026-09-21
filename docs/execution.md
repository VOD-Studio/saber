# 自主工具与执行权限

第一版提供 `exec`、`read_file`、`list_files`、`write_file`、`apply_patch`。模型可连续调用工具，使用退出码和错误信息修正下一步操作；已授权成员不需要逐条确认。权限来自管理员的 YAML 配置，聊天、网页、文件、模型工具参数和 MCP 工具描述都不能修改授权。

默认不开放执行权限，也不自动授权已有 MCP 工具。未授权成员可以继续进行无工具的聊天。

## 配置

Docker daemon 必须可用。管理员预先安装包含 `/usr/bin/env`、Python 3 和 `/bin/sh` 的可信镜像，例如 `docker pull python:3.13-slim`；程序使用 `--pull=never`，不自动拉取模型指定的镜像。开发工具链应预装在镜像中，生产环境建议使用固定镜像 digest。

```yaml
execution:
  enabled: true
  image: "python:3.13-slim"
  log_dir: "/srv/saber-private/execution"
  timeout_seconds: 60
  workspaces:
    project:
      path: "/srv/workspaces/project"
      env:
        LANG: "C.UTF-8"
  grants:
    - platform: "matrix"
      account: "@saber:example.org"
      room: "!room:example.org"
      users: ["@alice:example.org", "@bob:example.org"]
      workspace: "project"
      tools: ["exec", "read_file", "list_files", "write_file", "apply_patch"]
      capabilities: []
  mcp_requirements: {}
```

工作区必须是现有绝对路径，同群同成员只绑定一个工作区，不支持聊天参数任意切换目录。不同工作区不可嵌套，避免通过父子目录绕过任务串行。启动时会拒绝包含 Saber 配置、Matrix 会话/密钥、任务数据库、人格数据库或日志目录的工作区；不要把其他服务的凭据文件放进工作区。权限变更由管理员修改配置并重启，恢复排队任务时重新核对目录和工具权限。

容器使用宿主当前非 root UID/GID，Saber 以 root 启动时改为 `65534:65534`。管理员需提前给该用户工作区写权限。环境变量只取 `workspaces.<name>.env`，不接受模型传入额外变量；拒绝 Saber 的 Matrix token、模型密钥值以及保留的凭据变量名。按需配置工作区专用凭据，不复制机器人的运行环境。

## 隔离与权限边界

- 每次工具调用创建独立容器；只将指定工作目录挂载到 `/workspace`，禁用递归挂载，不挂载 Docker socket、Saber 配置或日志目录。挂载前检查工作区，不允许夹带 Unix socket、FIFO 或设备节点，避免绕过断网限制连接宿主服务。
- 固定断网、只读根文件系统、非 root 用户、移除全部 capabilities、禁止提权；限制 128 个进程、512 MiB 内存和 1 CPU，并提供 64 MiB 的临时 `/tmp`。进程环境从空环境开始构造。
- 工作区文件在各次调用之间保留；容器进程、临时文件和 shell 环境不跨调用保留。`exec` 拥有整个工作区内的读写能力，不能通过只隐藏 `write_file` 把 shell 变成只读。
- 文件工具只接受相对路径，拒绝 `..`、绝对路径、符号链接和非普通文件；`list_files` 可列举目录和链接名称，但不跟随链接。容器中的命令可以读镜像自身文件，不能据此访问未挂载的宿主目录。
- 取消或超时会强制删除本次容器，结束全部子进程。清理失败会封锁该工作区的后续工具调用，需检查 Docker 并重启；启动时先按执行器专属标签清理残留容器，再恢复排队任务。一个任务数据库和日志目录只能由一个 Saber 进程使用。

Docker 仍需要可信的宿主和 daemon；容器不是独立虚拟机。参数依据 [Docker 容器运行说明](https://docs.docker.com/engine/containers/run/) 和 [bind mount 说明](https://docs.docker.com/engine/storage/bind-mounts/) 设置。

## MCP 与外部能力

所有 MCP 实际调用都经过 `Manager.CallTool` 的同一权限检查，工具列表也按同一策略筛选。默认拒绝未知工具、未授权成员、账号/群不符、工作区不符和缺少能力的调用；猜工具名、伪造 `user_id` 参数或保留旧请求快照都不能绕过。

MCP 服务器是由管理员配置的外部能力，不会自动被上述本地容器隔离。管理员必须按工具实际最大权限声明要求，不使用服务器自报的 `readOnly`、描述或模型对命令目的的判断作为授权依据。每个 MCP 工具都需要 `external`，发布、部署、访问其他目录时再分别要求 `publish`、`deploy`、`cross_directory`。例如：

```yaml
execution:
  # 其余 image/workspaces 等字段同上
  mcp_requirements:
    "mcp:release:publish": ["publish"]
    "mcp:production:deploy": ["deploy"]
    "mcp:archive:read": ["cross_directory"]
    "mcp:web_fetch:web_fetch": []
  grants:
    - platform: "matrix"
      account: "@saber:example.org"
      room: "!room:example.org"
      users: ["@alice:example.org"]
      workspace: "project"
      tools: ["exec", "read_file", "mcp:release:publish"]
      capabilities: ["external", "publish"]
```

这个授权允许指定发布工具，但不允许部署或访问其他目录。具备通用远程命令、网络请求或代理执行能力的 MCP 服务应按全部可实现的外部副作用授权，不能把它声明成受限的只读工具。本地 `exec` 始终断网，授予 `publish` 不会为它打开网络；第一版的外部发布/部署通过单独配置和授权的 MCP 工具完成。stdio MCP 仍要求原有命令白名单，其子进程只得到 `PATH` 和显式 `env`，不再继承机器人环境。

## 输出与交付

工具结果包含 `exit_code`、前 8 KiB 的 `summary`、完整输出的 `log_path`，失败时附加 `error`。命令的非零退出作为工具失败反馈模型，不自动重跑命令。单次工具输入上限 4 MiB、输出上限 16 MiB；超出输出上限会终止容器，保留停止前已归档输出。单次超时默认 60 秒，总任务仍受 `ai.tool_calling.timeout_seconds` 与 `max_iterations` 限制。

`write_file` 创建/覆盖完整文件；`apply_patch` 接收 `{path, edits: [{old, new}]}`，先验证每处旧文本唯一匹配，再写回。它是单文件精确替换格式，不接受 unified diff。

生成文件后调用 `read_file` 并设置 `deliver: true`。执行器在工作区外保存不可变快照；任务完成后以 `m.file` 回复原消息并保留话题。上传内容始终加密，解密信息通过消息事件传递；E2EE 房间由 Matrix 客户端加密事件。每个文件使用稳定事务 ID，发送失败只重试归档文件和汇报，不再执行命令。交付前再次检查原成员的文件读取权限。授权已撤销时保留待投递状态供管理员处理。

完整日志及快照保存在 `<log_dir>/task-<ID>/`，权限为私有，不能被执行容器修改。备份/迁移任务数据库时应同时迁移日志目录；重建数据库时使用新的日志目录，以免重用编号。日志不自动删除，按部署方的保留规则归档清理。

## 验收

```sh
go test -tags goolm ./internal/execution ./internal/task ./internal/ai ./internal/mcp/...

# 用预先安装的可信 Python 镜像运行真实容器验收
SABER_EXECUTION_TEST_IMAGE=python:3.13-slim \
  go test -race -tags goolm ./internal/execution ./internal/ai \
  -run 'TestDockerExecution|TestTaskAutonomousDockerWorkflow'

make fmt-check lint test-cover-check build
```

真实容器测试覆盖：凭据不继承、文件越界/符号链接拒绝、失败后继续修改、完整日志、快照交付、超时/取消清理。群聊验收使用本地模拟 Matrix 与模型，验证单次交办驱动多条命令、文件内容修正、加密附件、投递失败不重跑以及 `!task cancel` 清理容器。生产 homeserver 和模型仍需部署验收。
