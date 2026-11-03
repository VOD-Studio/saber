package execution

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/chat"
)

//go:embed helper.py
var helper string

// Result 即使命令失败也返回退出码、摘要和完整输出存档位置。
type Result struct {
	// ExitCode 是容器退出码；启动失败或取消时为 -1。
	ExitCode int `json:"exit_code"`
	// Summary 最多包含前 8 KiB 输出。
	Summary string `json:"summary"`
	// LogPath 指向工作区外的完整输出文件，仅用于内部投递，不接受模型指定路径。
	LogPath string `json:"log_path"`
	// OutputID 是可按字节游标回读的不可变日志句柄。
	OutputID string `json:"output_id,omitempty"`
	// Artifact 是显式请求交付的不可变文件快照。
	Artifact string `json:"artifact,omitempty"`
	// Data 保存结构化工具结果，摘要不重复正文。
	Data any `json:"data,omitempty"`
	// Error 是启动、取消、超时或存储故障，不包含环境变量。
	Error string `json:"error,omitempty"`
}

// Artifact 表示从授权工作区导出并保存的文件快照。
type Artifact struct {
	// Name 是面向用户的文件名。
	Name string
	// Path 是私有归档中的绝对路径。
	Path string
}

// Recover 删除属于本执行器的残留容器；失败则禁止启动任务。
func (e *Executor) Recover(ctx context.Context) error {
	if !e.cfg.Enabled {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := docker(ctx, "ps", "-aq", "--filter", "label=saber.executor="+e.owner).Output()
	if err != nil {
		return fmt.Errorf("docker unavailable: %w", err)
	}
	for _, id := range strings.Fields(string(out)) {
		if err := e.remove(id); err != nil {
			return err
		}
	}
	return nil
}

func docker(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	// Docker CLI 只获取连接 daemon 所需变量，不携带机器人凭据。
	for _, key := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if v, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

func (e *Executor) remove(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := docker(ctx, "rm", "--force", name).CombinedOutput()
	if err != nil && !strings.Contains(string(output), "No such container") {
		return fmt.Errorf("cannot terminate task container %s: %w", name, err)
	}
	return nil
}

func (e *Executor) containerArgs(name, dir string, taskID int64) []string {
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 1 {
		uid = 65534
	}
	if gid < 1 {
		gid = 65534
	}
	networkMode := "bridge"
	if !e.cfg.IsNetworkEnabled() {
		networkMode = "none"
	}
	return []string{"create", "--pull=never", "--interactive", "--name", name, "--label", "saber.executor=" + e.owner, "--label", "saber.task=" + strconv.FormatInt(taskID, 10),
		"--network", networkMode, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--memory", "512m", "--cpus", "1",
		"--user", fmt.Sprintf("%d:%d", uid, gid), "--mount", "type=bind,src=" + dir + ",dst=/workspace,bind-recursive=disabled",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m", "--workdir", "/workspace", "--entrypoint", "/usr/bin/env", e.cfg.Image, "-i", "PATH=/usr/local/bin:/usr/bin:/bin", "python3", "-I", "-c", helper}
}

// Run 检查权限后在单次容器中执行；退出、取消或超时都会删除整个容器。
// 非零命令退出码作为普通结果返回，供 Agent 修改下一步操作。
func (e *Executor) Run(ctx context.Context, tool string, args map[string]any) (result Result, err error) {
	result.ExitCode = -1
	if err = e.Check(ctx, tool); err != nil {
		return result, err
	}
	if !LocalTool(tool) {
		return result, errors.New("not a local tool")
	}
	run := ctx.Value(scopeKey{}).(scope)
	if run.id <= 0 {
		return result, errors.New("local tools require a persistent task")
	}
	if err = validateArgs(tool, args); err != nil {
		return result, err
	}
	if tool == "read_output" {
		// 归档读取不需要容器或挂载检查，权限由任务祖先关系单独校验。
		return e.ReadOutput(ctx, args)
	}
	if tool != "exec" {
		// 文件工具使用共享 Go 实现，两个后端产生相同的分页与错误语义。
		return e.runFileTool(ctx, tool, args, run)
	}
	identity, _ := chat.IdentityFromContext(ctx)
	grant, _ := e.grant(identity)
	w := e.cfg.Workspaces[grant.Workspace]
	// 防止宿主管理员替换已授权路径为指向其他目录的符号链接。
	resolved, resolveErr := filepath.EvalSymlinks(run.dir)
	if resolveErr != nil || resolved != run.dir {
		return result, errors.New("workspace path changed since authorization")
	}
	if err = checkMountFiles(ctx, run.dir); err != nil {
		return result, err
	}
	data, err := json.Marshal(map[string]any{"tool": tool, "args": args, "env": w.Env})
	if err != nil {
		return result, err
	}
	if len(data) > 4*1024*1024 {
		return result, errors.New("tool input exceeds 4 MiB")
	}
	archive, err := e.openOutput(run.id)
	if err != nil {
		return result, err
	}
	dir := filepath.Dir(archive.Path)
	log := archive.file
	result.LogPath = archive.Path
	result.OutputID = archive.ID
	writer := &archiveWriter{file: log}
	defer func() {
		err = errors.Join(err, archive.close(e))
		result.Summary = strings.ToValidUTF8(writer.summary.String(), "")
		if err != nil {
			result.Error = err.Error()
		}
	}()
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return result, err
	}
	name := "saber-task-" + hex.EncodeToString(random[:])
	defer func() {
		if cleanupErr := e.remove(name); cleanupErr != nil {
			e.blocked.Store(run.dir, true)
			err = errors.Join(err, cleanupErr)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, e.Timeout())
	defer cancel()
	writer.cancel = cancel
	// create 使用独立短超时，确保取消时先得到确定创建结果，再移除容器。
	createCtx, createCancel := context.WithTimeout(context.Background(), 10*time.Second)
	create := docker(createCtx, e.containerArgs(name, run.dir, run.id)...)
	output, createErr := create.CombinedOutput()
	createCancel()
	if createErr != nil {
		_, writeErr := writer.Write(output)
		return result, errors.Join(fmt.Errorf("create task container: %w", createErr), writeErr)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	cmd := docker(ctx, "start", "--attach", "--interactive", name)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = writer
	cmd.Stderr = writer
	runErr := cmd.Run()
	if writer.err != nil {
		return result, writer.err
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if runErr != nil {
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			result.ExitCode = exit.ExitCode()
		} else {
			return result, runErr
		}
	} else {
		result.ExitCode = 0
	}
	if result.ExitCode == 0 && tool == "read_file" && args["deliver"] == true {
		if err = log.Sync(); err != nil {
			return result, err
		}
		artifactDir := filepath.Join(dir, "artifacts")
		if err = os.MkdirAll(artifactDir, 0700); err != nil {
			return result, err
		}
		result.Artifact = filepath.Join(artifactDir, hex.EncodeToString(random[:])+"-"+filepath.Base(args["path"].(string)))
		// 输出文件已停止写入，硬链接固定本次读取快照，避免额外复制及半成品。
		if err = os.Link(log.Name(), result.Artifact); err != nil {
			return result, err
		}
	}
	return result, nil
}

func checkMountFiles(ctx context.Context, dir string) error {
	// Unix socket 即使断网也可连到宿主服务；拒绝随工作区夹带 IPC/设备节点。
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&(os.ModeSocket|os.ModeDevice|os.ModeNamedPipe) != 0 {
			return fmt.Errorf("workspace contains an IPC or device node: %s", path)
		}
		return nil
	})
}

type archiveWriter struct {
	mu      sync.Mutex
	file    *os.File
	size    int
	summary bytes.Buffer
	err     error
	cancel  context.CancelFunc
}

func (w *archiveWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	if w.size+len(p) > 16*1024*1024 {
		w.err = errors.New("output exceeds 16 MiB; container terminated")
		if w.cancel != nil {
			w.cancel()
		}
		return 0, w.err
	}
	n, err := w.file.Write(p)
	w.size += n
	if w.summary.Len() < 8192 {
		w.summary.Write(p[:min(n, 8192-w.summary.Len())])
	}
	w.err = err
	return n, err
}

// Artifacts 只列举执行器私有目录中的快照，不能由模型指定宿主路径。
func (e *Executor) Artifacts(taskID int64) ([]Artifact, error) {
	dir := filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(taskID, 10), "artifacts")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []Artifact
	for _, entry := range entries {
		if !entry.Type().IsRegular() || len(entry.Name()) < 34 {
			return nil, errors.New("invalid artifact archive")
		}
		result = append(result, Artifact{Name: entry.Name()[33:], Path: filepath.Join(dir, entry.Name())})
	}
	return result, nil
}

func validateArgs(tool string, args map[string]any) error {
	allowed := map[string][]string{
		"exec":         {"command"},
		"read_output":  {"output_id", "offset", "limit"},
		"read_file":    {"path", "offset", "limit", "byte_offset", "deliver"},
		"list_files":   {"path"},
		"write_file":   {"path", "content"},
		"apply_patch":  {"path", "edits"},
		"search_files": {"pattern", "path", "file_glob", "case_sensitive", "offset", "limit", "include_hidden"},
		"glob_files":   {"pattern", "path", "offset", "limit", "include_hidden"},
	}
	for key := range args {
		found := false
		for _, a := range allowed[tool] {
			if a == key {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("unexpected tool argument %q", key)
		}
	}
	text := func(key string, empty bool) bool {
		v, ok := args[key].(string)
		return ok && (empty || v != "") && !strings.ContainsRune(v, 0)
	}
	if tool == "exec" {
		if !text("command", false) {
			return errors.New("command is required")
		}
		return nil
	}
	if tool == "read_output" {
		if !text("output_id", false) {
			return errors.New("output_id is required")
		}
		if err := validateInteger(args, "offset", 0, 0); err != nil {
			return err
		}
		if err := validateInteger(args, "limit", outputPageMin, outputPageMax); err != nil {
			return err
		}
		return nil
	}
	if tool == "search_files" || tool == "glob_files" {
		if !text("pattern", false) {
			return errors.New("pattern is required")
		}
		if len(args["pattern"].(string)) > 512 {
			return errors.New("pattern exceeds 512 bytes")
		}
		if value, ok := args["path"]; ok {
			path, valid := value.(string)
			if !valid {
				return errors.New("path must be a string")
			}
			if err := validatePath(path, true); err != nil {
				return err
			}
		}
		if tool == "search_files" {
			if value, ok := args["file_glob"]; ok {
				if glob, valid := value.(string); !valid {
					return errors.New("file_glob must be a string")
				} else if _, err := splitGlob(glob); err != nil {
					return err
				}
			}
			if value, ok := args["case_sensitive"]; ok {
				if _, valid := value.(bool); !valid {
					return errors.New("case_sensitive must be boolean")
				}
			}
		}
		if value, ok := args["include_hidden"]; ok {
			if _, valid := value.(bool); !valid {
				return errors.New("include_hidden must be boolean")
			}
		}
		if err := validateInteger(args, "offset", 0, 0); err != nil {
			return err
		}
		maxLimit := searchLimitMax
		if tool == "glob_files" {
			maxLimit = globLimitMax
		}
		if err := validateInteger(args, "limit", 1, maxLimit); err != nil {
			return err
		}
		return nil
	}
	if tool == "list_files" && args["path"] == nil {
		args["path"] = "."
	}
	if !text("path", false) {
		return errors.New("path is required")
	}
	if err := validatePath(args["path"].(string), tool == "list_files"); err != nil {
		return err
	}
	if tool == "read_file" {
		if args["deliver"] != nil {
			if _, ok := args["deliver"].(bool); !ok {
				return errors.New("deliver must be boolean")
			}
		}
		if err := validateInteger(args, "offset", 0, 0); err != nil {
			return err
		}
		if err := validateInteger(args, "limit", 1, readLimitMax); err != nil {
			return err
		}
		if err := validateInteger(args, "byte_offset", 0, 0); err != nil {
			return err
		}
	}
	if tool == "write_file" && !text("content", true) {
		return errors.New("content must be a string")
	}
	if tool == "apply_patch" {
		edits, ok := args["edits"].([]any)
		if !ok || len(edits) == 0 {
			return errors.New("edits must be a nonempty array")
		}
		for _, edit := range edits {
			e, ok := edit.(map[string]any)
			if !ok || len(e) != 2 {
				return errors.New("edit requires old and new")
			}
			old, ok := e["old"].(string)
			if !ok || old == "" {
				return errors.New("old must not be empty")
			}
			if _, ok = e["new"].(string); !ok {
				return errors.New("new must be a string")
			}
		}
	}
	return nil
}

// validateInteger 校验可选整数参数；max 为 0 表示只要求非负。
func validateInteger(args map[string]any, key string, minValue, maxValue int) error {
	value, ok := args[key]
	if !ok {
		return nil
	}
	number, valid := intArgument(value)
	if !valid {
		return fmt.Errorf("%s must be an integer", key)
	}
	if number < minValue || maxValue > 0 && number > maxValue {
		if maxValue > 0 {
			return fmt.Errorf("%s must be between %d and %d", key, minValue, maxValue)
		}
		return fmt.Errorf("%s must be at least %d", key, minValue)
	}
	return nil
}

// validatePath 只接受工作区内相对路径；目录参数额外允许 "."。
func validatePath(p string, allowDot bool) error {
	if p == "" {
		return errors.New("path is required")
	}
	if allowDot && p == "." {
		return nil
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return errors.New("path must be relative")
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("invalid path component")
		}
	}
	return nil
}

// Logs 列出私有归档中的完整命令输出，不接受聊天提供的宿主路径。
func (e *Executor) Logs(taskID int64) ([]Artifact, error) {
	dir := filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(taskID, 10))
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var logs []Artifact
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "output-") || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, errors.New("invalid task log archive")
		}
		logs = append(logs, Artifact{Name: entry.Name(), Path: filepath.Join(dir, entry.Name())})
	}
	return logs, nil
}
