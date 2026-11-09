package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"rua.plus/saber/internal/chat"
)

type processTransferRecord struct {
	SourceID  int64    `json:"source_id"`
	TargetID  int64    `json:"target_id"`
	Directory string   `json:"directory"`
	Processes []string `json:"processes"`
	State     string   `json:"state"`
}

// SetProcessTransferAuthorizer 接入权威任务库的同用户、同代号、合法续接关系复核。
func (e *Executor) SetProcessTransferAuthorizer(authorize func(context.Context, context.Context) error) {
	e.transferMu.Lock()
	e.transferAuthorize = authorize
	e.transferMu.Unlock()
}

func (p *processRecord) watchOwner() {
	for {
		p.mu.Lock()
		owner, version, changed := p.ownerCtx, p.ownerVersion, p.ownerChanged
		p.mu.Unlock()
		var ownerDone <-chan struct{}
		if owner != nil {
			ownerDone = owner.Done()
		}
		select {
		case <-p.done:
			return
		case <-changed:
			continue
		case <-ownerDone:
			p.mu.Lock()
			current := p.ownerVersion == version
			p.mu.Unlock()
			if current {
				p.cancel()
				return
			}
		}
	}
}

func (e *Executor) transferPath(id int64) string {
	return filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(id, 10), "process-transfer.json")
}

func (e *Executor) validTransferredOutput(origin, target int64, id string) bool {
	for range 64 {
		if origin == target {
			return true
		}
		data, err := os.ReadFile(e.transferPath(target))
		if err != nil {
			return false
		}
		var record processTransferRecord
		if json.Unmarshal(data, &record) != nil || record.TargetID != target || record.State != "committed" || record.SourceID <= 0 {
			return false
		}
		found := false
		for _, process := range record.Processes {
			if process == id {
				found = true
				break
			}
		}
		if !found {
			return false
		}
		target = record.SourceID
	}
	return false
}

// TransferProcesses 在源 Finalize 前移交整个任务环境；目标接管成功后源才不再拥有清理责任。
// 两个上下文必须由任务管理器产生并已绑定 execution.WithTask，不能使用模型提供的身份或编号。
func (e *Executor) TransferProcesses(sourceCtx, destinationCtx context.Context) (err error) {
	e.transferMu.Lock()
	defer e.transferMu.Unlock()
	if e.transferAuthorize == nil {
		return errors.New("process transfer requires the authoritative task relationship verifier")
	}
	if err = e.transferAuthorize(sourceCtx, destinationCtx); err != nil {
		return err
	}
	sourceID, sourceDir, sourceOK := TaskScope(sourceCtx)
	targetID, targetDir, targetOK := TaskScope(destinationCtx)
	if !sourceOK || !targetOK || sourceID == targetID || sourceDir != targetDir {
		return errors.New("process transfer requires distinct persistent tasks in the same canonical workspace")
	}
	sourceIdentity, sourceOK := chat.IdentityFromContext(sourceCtx)
	targetIdentity, targetOK := chat.IdentityFromContext(destinationCtx)
	if !sourceOK || !targetOK || identityKey(sourceIdentity) != identityKey(targetIdentity) {
		return errors.New("process transfer cannot cross trusted identities")
	}
	if err = e.Check(sourceCtx, "process"); err != nil {
		return err
	}
	if err = destinationCtx.Err(); err != nil {
		return err
	}
	if _, closed := e.closedTasks.Load(targetID); closed {
		return errors.New("process transfer target environment is closed")
	}
	defer func() {
		if err != nil {
			e.blocked.Store(sourceDir, true)
			e.writeQuarantine(sourceID, err)
		}
	}()
	e.derivedMu.Lock()
	derived := e.derived[sourceDir]
	e.derivedMu.Unlock()
	if derived != nil {
		derived.mu.Lock()
		defer derived.mu.Unlock()
		if derived.closed || derived.closing {
			return errors.New("derived workspace is closing")
		}
	}
	if derived == nil {
		if err = e.Check(destinationCtx, "process"); err != nil {
			return err
		}
	}
	if e.devtools != nil {
		if err = e.devtools.CloseScope(developerScope(sourceID, sourceDir)); err != nil {
			return err
		}
	}
	e.processMu.Lock()
	defer e.processMu.Unlock()
	e.envMu.Lock()
	defer e.envMu.Unlock()
	if e.envs[targetID] != nil || len(e.processes[targetID]) != 0 {
		return errors.New("process transfer target already owns an execution environment")
	}
	records := e.processes[sourceID]
	entry := e.envs[sourceID]
	if derived != nil {
		entry = derived.entry
	}
	if entry == nil && len(records) == 0 && derived == nil {
		return nil
	}
	transfer := processTransferRecord{SourceID: sourceID, TargetID: targetID, Directory: sourceDir, State: "prepared"}
	for id := range records {
		transfer.Processes = append(transfer.Processes, id)
	}
	if err = writePrivateJSON(e.transferPath(targetID), transfer); err != nil {
		return err
	}
	if derived != nil {
		e.derivedMu.Lock()
		derived.tasks[targetID] = true
		err = derived.persist(envActive)
		e.derivedMu.Unlock()
		if err != nil {
			return err
		}
		if err = e.Check(destinationCtx, "process"); err != nil {
			return err
		}
	}
	if entry != nil {
		lockedEntry := entry
		lockedEntry.mu.Lock()
		defer lockedEntry.mu.Unlock()
		if entry.environment == nil && entry.state == "" {
			entry = nil
		}
	}
	if entry != nil {
		if entry.state != envActive {
			return errors.New("source execution environment is not active")
		}
		if derived == nil {
			if err = e.writeEnvironmentRecord(environmentRecord{TaskID: targetID, Backend: entry.backend, Workspace: entry.workspace, State: envActive}); err != nil {
				return err
			}
		}
		switch environment := entry.environment.(type) {
		case *dockerEnvironment:
			environment.spec.TaskID = targetID
		case *remoteEnvironment:
			oldRecord := environment.record
			newRecord := oldRecord
			newRecord.TaskID = targetID
			newFile := filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(targetID, 10), "remote.json")
			if err = writeRemoteRecord(newFile, newRecord); err != nil {
				return err
			}
			oldRecord.State = envClosed
			if err = writeRemoteRecord(environment.recordFile, oldRecord); err != nil {
				return err
			}
			environment.record = newRecord
			environment.recordFile = newFile
			environment.spec.TaskID = targetID
		}
		entry.taskID = targetID
		entry.spec.TaskID = targetID
		if derived == nil {
			if err = e.markEnvironmentClosed(environmentRecord{TaskID: sourceID, Backend: entry.backend, Workspace: entry.workspace}); err != nil {
				return err
			}
		}
	}
	for _, record := range records {
		record.persistMu.Lock()
		record.mu.Lock()
		old := persistedProcess{ID: record.id, TaskID: sourceID, OutputID: record.archive.ID, Started: record.started, Exited: record.exited, Code: record.code, Error: record.err, PTY: record.pty, TransferredTo: targetID}
		next := old
		next.TaskID = targetID
		next.TransferredTo = 0
		record.mu.Unlock()
		destination := filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(targetID, 10), "processes", record.id+".json")
		if err = writePrivateJSON(destination, next); err == nil {
			err = writePrivateJSON(filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(sourceID, 10), "processes", record.id+".json"), old)
		}
		if err != nil {
			record.persistMu.Unlock()
			return err
		}
		record.mu.Lock()
		record.taskID = targetID
		record.ownerCtx = e.DeveloperContext(destinationCtx)
		record.ownerVersion++
		close(record.ownerChanged)
		record.ownerChanged = make(chan struct{})
		record.ownerID.Store(targetID)
		record.mu.Unlock()
		record.persistMu.Unlock()
	}
	e.notifyMu.Lock()
	events := append(e.notifications[targetID], e.notifications[sourceID]...)
	if len(events) > 256 {
		e.notifyMu.Unlock()
		return errors.New("continuation process notification queue exceeds 256 events")
	}
	if err = writePrivateJSON(e.notificationPath(targetID), events); err == nil {
		err = writePrivateJSON(e.notificationPath(sourceID), []ProcessNotification{})
	}
	if err == nil {
		e.notifications[targetID] = events
		delete(e.notifications, sourceID)
	}
	e.notifyMu.Unlock()
	if err != nil {
		return err
	}
	transfer.State = "committed"
	if err = writePrivateJSON(e.transferPath(targetID), transfer); err != nil {
		return err
	}
	if derived != nil {
		e.derivedMu.Lock()
		delete(derived.tasks, sourceID)
		if derived.parentID == sourceID {
			derived.parentID = targetID
		}
		derived.ownerID = targetID
		err = derived.persist(envActive)
		e.derivedMu.Unlock()
		if err != nil {
			return err
		}
	}
	delete(e.processes, sourceID)
	e.processes[targetID] = records
	if derived == nil && entry != nil {
		delete(e.envs, sourceID)
		e.envs[targetID] = entry
	}
	return nil
}

// InvalidateTaskReads 清除压缩后不再可见的原文覆盖授权，但保留待交付诊断。
func (e *Executor) InvalidateTaskReads(ctx context.Context) error {
	if e.devtools == nil {
		return nil
	}
	if err := e.Check(ctx, "read_file"); err != nil {
		return err
	}
	scope := e.DeveloperScope(ctx)
	if scope == "" {
		return fmt.Errorf("read invalidation requires persistent task")
	}
	e.devtools.InvalidateReadScope(scope)
	return nil
}
