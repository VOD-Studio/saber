package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"rua.plus/saber/internal/chat"
)

type derivedWorkspace struct {
	identity string
	parentID int64
	base     string
	path     string
	tasks    map[int64]bool
	entry    *envEntry
	mu       sync.Mutex
	closing  bool
	closed   bool
	backend  string
	journal  string
	executed bool
	initial  map[string]worktreeFile
	ownerID  int64
}

func (e *Executor) authorizedDirectory(identity chat.Identity, run scope, base string) bool {
	if run.dir == base {
		return true
	}
	e.derivedMu.Lock()
	defer e.derivedMu.Unlock()
	d := e.derived[run.dir]
	return d != nil && !d.closing && d.base == base && d.identity == identityKey(identity) && d.tasks[run.id]
}

// PrepareSubagentWorkspace 创建 detached git worktree，不释放父任务对原目录的锁。
func (e *Executor) PrepareSubagentWorkspace(ctx context.Context, parentDir string) (string, func(context.Context) error, error) {
	if err := e.Check(ctx, "exec"); err != nil {
		return "", nil, err
	}
	run, ok := ctx.Value(scopeKey{}).(scope)
	if !ok || run.id <= 0 || parentDir != run.dir {
		return "", nil, errors.New("worktree requires current persistent parent task directory")
	}
	identity, _ := chat.IdentityFromContext(ctx)
	grant, _ := e.grant(identity)
	name, err := e.cfg.BackendFor(grant.Workspace)
	if err != nil {
		return "", nil, err
	}
	if name != "local" && name != "docker" {
		return "", nil, fmt.Errorf("worktree isolation is unsupported on backend %s", name)
	}
	base, err := e.Workspace(identity)
	if err != nil {
		return "", nil, err
	}
	parent := filepath.Join(e.cfg.LogDir, "worktrees")
	if err = os.MkdirAll(parent, 0700); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(parent, "child-")
	if err != nil {
		return "", nil, err
	}
	if err = os.Remove(dir); err != nil {
		return "", nil, err
	}
	d := &derivedWorkspace{identity: identityKey(identity), parentID: run.id, base: base, path: dir, tasks: map[int64]bool{run.id: true}, entry: &envEntry{taskID: run.id}, backend: name, ownerID: run.id, journal: filepath.Join(e.cfg.LogDir, "worktree-leases", filepath.Base(dir)+".json")}
	if err = d.persist(envOpening); err != nil {
		return "", nil, err
	}
	if err = materializeGitCopy(ctx, parentDir, dir); err != nil {
		cleanupErr := os.RemoveAll(dir)
		if cleanupErr == nil {
			cleanupErr = d.persist(envClosed)
		}
		if cleanupErr != nil {
			e.blocked.Store(base, true)
		}
		return "", nil, errors.Join(err, cleanupErr)
	}
	d.initial, err = captureWorktree(ctx, dir)
	if err != nil {
		cleanupErr := os.RemoveAll(dir)
		return "", nil, errors.Join(err, cleanupErr)
	}
	if err = d.persist(envActive); err != nil {
		e.blocked.Store(base, true)
		return "", nil, err
	}
	e.derivedMu.Lock()
	e.derived[dir] = d
	e.derivedMu.Unlock()
	var cleanupMu sync.Mutex
	closed := false
	cleanup := func(cleanCtx context.Context) error {
		cleanupMu.Lock()
		defer cleanupMu.Unlock()
		if closed {
			return nil
		}
		if err := e.releaseWorktree(cleanCtx, d); err != nil {
			return err
		}
		closed = true
		return nil
	}
	return dir, cleanup, nil
}

// BindSubagentWorkspace 只允许创建该 worktree 的可信父任务绑定持久子任务编号。
func (e *Executor) BindSubagentWorkspace(ctx context.Context, childID int64, dir string) error {
	if err := e.Check(ctx, "exec"); err != nil {
		return err
	}
	run, _ := ctx.Value(scopeKey{}).(scope)
	identity, _ := chat.IdentityFromContext(ctx)
	e.derivedMu.Lock()
	defer e.derivedMu.Unlock()
	d := e.derived[dir]
	if childID <= 0 || d == nil || d.closing || d.parentID != run.id || d.identity != identityKey(identity) {
		return errors.New("derived workspace binding requires its trusted parent task")
	}
	d.tasks[childID] = true
	d.ownerID = childID
	if err := d.persist(envActive); err != nil {
		delete(d.tasks, childID)
		return err
	}
	return nil
}

func (e *Executor) releaseWorktree(ctx context.Context, d *derivedWorkspace) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	e.derivedMu.Lock()
	d.closing = true
	e.derivedMu.Unlock()
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if e.devtools != nil {
		for id := range d.tasks {
			if err := e.devtools.CloseScope(developerScope(id, d.path)); err != nil {
				return err
			}
		}
	}
	d.entry.mu.Lock()
	if d.entry.environment != nil {
		if err := d.entry.environment.Close(cleanupCtx); err != nil {
			d.entry.mu.Unlock()
			e.blocked.Store(d.base, true)
			return err
		}
		d.entry.environment = nil
	}
	d.entry.state = envClosed
	d.entry.mu.Unlock()
	if err := e.archiveWorktree(cleanupCtx, d); err != nil {
		e.blocked.Store(d.base, true)
		return err
	}
	if err := os.RemoveAll(d.path); err != nil {
		e.blocked.Store(d.base, true)
		return err
	}
	if err := d.persist(envClosed); err != nil {
		return err
	}
	d.closed = true
	e.derivedMu.Lock()
	delete(e.derived, d.path)
	e.derivedMu.Unlock()
	return nil
}
