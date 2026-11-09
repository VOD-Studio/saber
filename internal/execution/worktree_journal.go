package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type worktreeRecord struct {
	Base     string                  `json:"base"`
	Path     string                  `json:"path"`
	Backend  string                  `json:"backend"`
	Tasks    map[int64]bool          `json:"tasks"`
	State    string                  `json:"state"`
	Executed bool                    `json:"executed"`
	Kind     string                  `json:"kind"`
	Initial  map[string]worktreeFile `json:"initial,omitempty"`
	OwnerID  int64                   `json:"owner_id"`
}

func (d *derivedWorkspace) persist(state string) error {
	return writePrivateJSON(d.journal, worktreeRecord{Base: d.base, Path: d.path, Backend: d.backend, Tasks: d.tasks, State: state, Executed: d.executed, Kind: "isolated_git_copy", Initial: d.initial, OwnerID: d.ownerID})
}

func (e *Executor) recoverWorktrees(ctx context.Context) error {
	dir := filepath.Join(e.cfg.LogDir, "worktree-leases")
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, file := range files {
		if !file.Type().IsRegular() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		journal := filepath.Join(dir, file.Name())
		data, err := os.ReadFile(journal)
		if err != nil {
			return err
		}
		var record worktreeRecord
		if err = json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.State == envClosed {
			continue
		}
		if filepath.Dir(record.Path) != filepath.Join(e.cfg.LogDir, "worktrees") || filepath.Base(record.Path)+".json" != file.Name() {
			return errors.New("invalid worktree cleanup journal path")
		}
		trusted := false
		for _, workspace := range e.cfg.Workspaces {
			if workspace.Path == record.Base {
				trusted = true
				break
			}
		}
		if !trusted {
			errs = append(errs, fmt.Errorf("worktree base %s is no longer an authorized workspace; inspect before removal", record.Base))
			continue
		}
		if record.Backend == "local" && record.Executed {
			e.blocked.Store(record.Base, true)
			errs = append(errs, fmt.Errorf("local worktree %s may contain restart-orphaned processes; inspect and mark its journal closed before releasing %s", record.Path, record.Base))
			continue
		}
		if _, err = os.Stat(record.Path); errors.Is(err, os.ErrNotExist) {
			record.State = envClosed
			if err = writePrivateJSON(journal, record); err != nil {
				return err
			}
			continue
		}
		if record.Kind != "isolated_git_copy" {
			return errors.New("unknown worktree journal kind; inspect before cleanup")
		}
		if err = e.archiveWorktree(ctx, &derivedWorkspace{base: record.Base, path: record.Path, initial: record.Initial, ownerID: record.OwnerID}); err != nil {
			e.blocked.Store(record.Base, true)
			errs = append(errs, err)
			continue
		}
		if err = os.RemoveAll(record.Path); err != nil {
			e.blocked.Store(record.Base, true)
			errs = append(errs, err)
			continue
		}
		record.State = envClosed
		if err = writePrivateJSON(journal, record); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}
