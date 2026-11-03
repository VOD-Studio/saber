package execution

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

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

// Recover 清理本执行器的残留容器；没有活动 Docker 工作区或旧记录时不探测 Docker。
func (e *Executor) Recover(ctx context.Context) error {
	if !e.cfg.Enabled {
		return nil
	}
	records, err := e.readEnvironmentRecords()
	if err != nil {
		return err
	}
	needDocker := false
	for _, record := range records {
		if record.Backend == "docker" && record.State == envActive {
			needDocker = true
		}
	}
	for name := range e.activeWorkspaces() {
		if backend, err := e.cfg.BackendFor(name); err == nil && backend == "docker" {
			needDocker = true
		}
	}
	if !needDocker {
		return nil
	}
	return e.backend("docker").Recover(ctx)
}

func (e *Executor) activeWorkspaces() map[string]bool {
	active := make(map[string]bool, len(e.cfg.Grants))
	for _, grant := range e.cfg.Grants {
		active[grant.Workspace] = true
	}
	return active
}

// Run 检查权限后在任务环境中执行；非零退出码作为普通结果返回。
// exec 使用执行超时；环境打开与后台作业使用完整任务 context。
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
	if tool == "process" {
		return e.runProcess(ctx, args, run)
	}
	if tool != "exec" {
		// 文件工具使用共享 Go 实现，两个后端产生相同的分页与错误语义。
		return e.runFileTool(ctx, tool, args, run)
	}
	identity, _ := chat.IdentityFromContext(ctx)
	grant, _ := e.grant(identity)
	workspace := e.cfg.Workspaces[grant.Workspace]
	// 防止宿主管理员替换已授权路径为指向其他目录的符号链接。
	resolved, resolveErr := filepath.EvalSymlinks(run.dir)
	if resolveErr != nil || resolved != run.dir {
		return result, errors.New("workspace path changed since authorization")
	}
	entry, err := e.openEnvironment(ctx, run, grant.Workspace, workspace.Env)
	if err != nil {
		return result, err
	}
	command, _ := args["command"].(string)
	archive, err := e.openOutput(run.id)
	if err != nil {
		return result, err
	}
	result.LogPath = archive.Path
	result.OutputID = archive.ID
	writer := &archiveWriter{file: archive.file}
	defer func() {
		err = errors.Join(err, archive.close(e))
		result.Summary = strings.ToValidUTF8(writer.summary.String(), "\uFFFD")
		if err != nil {
			result.Error = err.Error()
		}
	}()
	execCtx, cancel := context.WithTimeout(ctx, e.Timeout())
	defer cancel()
	writer.cancel = cancel
	process, err := entry.environment.Start(execCtx, command, writer)
	if err != nil {
		return result, err
	}
	code, waitErr := process.Wait()
	if writer.err != nil {
		return result, writer.err
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if execCtx.Err() != nil {
		return result, execCtx.Err()
	}
	if waitErr != nil {
		return result, waitErr
	}
	result.ExitCode = code
	return result, nil
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
		"process":      {"action", "command", "id", "offset", "limit", "wait_seconds"},
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
	if tool == "process" {
		return validateProcessArgs(args)
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

// validateProcessArgs 只接受当前 action 适用的参数，未知字段一律拒绝。
func validateProcessArgs(args map[string]any) error {
	action, ok := args["action"].(string)
	if !ok || action == "" {
		return errors.New("action is required")
	}
	allowed := map[string]map[string]bool{
		"start":  {"action": true, "command": true},
		"list":   {"action": true},
		"status": {"action": true, "id": true},
		"kill":   {"action": true, "id": true},
		"wait":   {"action": true, "id": true, "wait_seconds": true},
		"logs":   {"action": true, "id": true, "offset": true, "limit": true},
	}
	fields, ok := allowed[action]
	if !ok {
		return fmt.Errorf("unknown process action %q", action)
	}
	for key := range args {
		if !fields[key] {
			return fmt.Errorf("argument %q is not valid for process action %q", key, action)
		}
	}
	if action == "start" {
		command, valid := args["command"].(string)
		if !valid || command == "" || strings.ContainsRune(command, 0) {
			return errors.New("command is required")
		}
		return nil
	}
	if action != "list" {
		id, valid := args["id"].(string)
		if !valid || id == "" || strings.ContainsRune(id, 0) {
			return errors.New("id is required")
		}
	}
	if action == "wait" {
		if err := validateInteger(args, "wait_seconds", 0, processWaitMax); err != nil {
			return err
		}
	}
	if action == "logs" {
		if err := validateInteger(args, "offset", 0, 0); err != nil {
			return err
		}
		if err := validateInteger(args, "limit", outputPageMin, outputPageMax); err != nil {
			return err
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
