package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"
)

// sessionSpec 是程序确定的单任务环境输入；模型不能覆盖。
type sessionSpec struct {
	TaskID    int64
	Workspace string
	Env       map[string]string
	LogDir    string
}

// backend 只为已授权任务打开环境，授权与工具分派仍由 Executor 负责。
type backend interface {
	Open(context.Context, sessionSpec) (environment, error)
	Recover(context.Context) error
}

// environment 是任务级持久环境；命令之间复用目录、临时目录与后台作业。
type environment interface {
	Start(context.Context, string, io.Writer) (ownedProcess, error)
	Close(context.Context) error
}

// ownedProcess 是单个前台或后台命令的生命周期句柄。
type ownedProcess interface {
	Wait() (int, error)
	Stop(context.Context) error
}

// envEntry 以条目级互斥串行化 Open 与 Close，并固定状态迁移。
type envEntry struct {
	taskID      int64
	workspace   string
	backend     string
	spec        sessionSpec
	mu          sync.Mutex
	state       string
	environment environment
}

const (
	envOpening = "opening"
	envActive  = "active"
	envClosing = "closing"
	envClosed  = "closed"
)

// environmentRecord 是崩溃后阻断误用旧环境的私有标记。
type environmentRecord struct {
	TaskID    int64  `json:"task_id"`
	Backend   string `json:"backend"`
	Workspace string `json:"workspace"`
	State     string `json:"state"`
}

func (e *Executor) recordPath(taskID int64) string {
	return filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(taskID, 10), "environment.json")
}

func (e *Executor) writeEnvironmentRecord(record environmentRecord) error {
	path := e.recordPath(record.TaskID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".environment-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if err = temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err = temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func (e *Executor) markEnvironmentClosed(record environmentRecord) error {
	record.State = envClosed
	return e.writeEnvironmentRecord(record)
}

// readEnvironmentRecords 只读取本执行器私有目录内的任务记录。
func (e *Executor) readEnvironmentRecords() ([]environmentRecord, error) {
	entries, err := os.ReadDir(e.cfg.LogDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []environmentRecord
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) < 6 || entry.Name()[:5] != "task-" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(e.cfg.LogDir, entry.Name(), "environment.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var record environmentRecord
		if err = json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// openEnvironment 惰性打开任务环境；打开前先写 active 记录，失败不启动进程。
func (e *Executor) openEnvironment(ctx context.Context, run scope, workspace string, env map[string]string) (*envEntry, error) {
	if _, closed := e.closedTasks.Load(run.id); closed {
		return nil, errors.New("task environment is closed")
	}
	e.envMu.Lock()
	entry := e.envs[run.id]
	if entry == nil {
		entry = &envEntry{taskID: run.id}
		e.envs[run.id] = entry
	}
	e.envMu.Unlock()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.state == envClosing || entry.state == envClosed {
		return nil, errors.New("task environment is closing or closed")
	}
	name, err := e.cfg.BackendFor(workspace)
	if err != nil {
		return nil, err
	}
	if entry.state == envActive {
		if entry.workspace != run.dir || entry.backend != name {
			return nil, errors.New("task environment changed workspace or backend")
		}
		return entry, nil
	}
	entry.state = envOpening
	record := environmentRecord{TaskID: run.id, Backend: name, Workspace: run.dir, State: envActive}
	if err := e.writeEnvironmentRecord(record); err != nil {
		entry.state = envClosed
		return nil, fmt.Errorf("cannot record task environment: %w", err)
	}
	spec := sessionSpec{TaskID: run.id, Workspace: run.dir, Env: env, LogDir: e.cfg.LogDir}
	impl, err := e.backend(name).Open(ctx, spec)
	if err != nil {
		entry.state = envClosed
		return nil, err
	}
	entry.environment, entry.workspace, entry.backend, entry.spec, entry.state = impl, run.dir, name, spec, envActive
	return entry, nil
}

func (e *Executor) backend(name string) backend {
	if impl, ok := e.backends[name]; ok {
		return impl
	}
	switch name {
	case "docker":
		e.backends[name] = &dockerBackend{cfg: e.cfg, owner: e.owner, executor: e}
	default:
		e.backends[name] = &localBackend{executor: e}
	}
	return e.backends[name]
}

// CloseTask 停止后台作业、关闭环境并归档终态；失败时锁定工作区等待人工核查。
func (e *Executor) CloseTask(ctx context.Context, taskID int64) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	e.processMu.Lock()
	records := e.processes[taskID]
	delete(e.processes, taskID)
	e.processMu.Unlock()
	e.closedTasks.Store(taskID, true)
	var errs []error
	for _, record := range records {
		if err := record.stop(cleanupCtx); err != nil {
			errs = append(errs, err)
		}
	}
	e.envMu.Lock()
	entry := e.envs[taskID]
	delete(e.envs, taskID)
	e.envMu.Unlock()
	var workspace string
	if entry != nil {
		entry.mu.Lock()
		if entry.state != envActive && entry.state != envOpening {
			entry.mu.Unlock()
			return errors.Join(errs...)
		}
		entry.state = envClosing
		workspace = entry.workspace
		record := environmentRecord{TaskID: taskID, Backend: entry.backend, Workspace: entry.workspace, State: envActive}
		if entry.environment != nil {
			if err := entry.environment.Close(cleanupCtx); err != nil {
				errs = append(errs, err)
			}
		}
		entry.state = envClosed
		entry.mu.Unlock()
		if len(errs) == 0 {
			if err := e.markEnvironmentClosed(record); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		if workspace != "" {
			e.blocked.Store(workspace, true)
		}
		e.writeQuarantine(taskID, err)
		return err
	}
	return nil
}

// Close 在所有任务停止后兜底清理环境；可重复调用。
func (e *Executor) Close(ctx context.Context) error {
	e.envMu.Lock()
	var ids []int64
	for id := range e.envs {
		ids = append(ids, id)
	}
	e.envMu.Unlock()
	var errs []error
	for _, id := range ids {
		if err := e.CloseTask(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (e *Executor) writeQuarantine(taskID int64, cause error) {
	path := filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(taskID, 10), "quarantine")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(cause.Error()+"\n"), 0600)
}

// BlockedDirectories 返回已隔离工作区的规范路径快照，供任务领取时排除。
func (e *Executor) BlockedDirectories() []string {
	var dirs []string
	e.blocked.Range(func(key, _ any) bool {
		if dir, ok := key.(string); ok {
			dirs = append(dirs, dir)
		}
		return true
	})
	slices.Sort(dirs)
	return dirs
}

// loadEnvironmentRecords 在启动时阻断无法证明清理完成的旧环境。
func (e *Executor) loadEnvironmentRecords() error {
	records, err := e.readEnvironmentRecords()
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.State != envActive {
			continue
		}
		switch record.Backend {
		case "docker":
			// Docker 残留由 Recover 按 owner 标签清理后再标记 closed。
		case "local", "":
			if record.Workspace != "" {
				e.blocked.Store(record.Workspace, true)
			}
		default:
			if record.Workspace != "" {
				e.blocked.Store(record.Workspace, true)
			}
		}
	}
	return nil
}

// finishDockerRecords 在容器清理成功后把对应记录标为 closed。
func (e *Executor) finishDockerRecords(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	records, err := e.readEnvironmentRecords()
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.State != envActive || record.Backend != "docker" {
			continue
		}
		if err := e.markEnvironmentClosed(record); err != nil {
			return err
		}
	}
	return nil
}
