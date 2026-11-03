package execution

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"rua.plus/saber/internal/chat"
)

const (
	maxRunningProcesses = 8
	processWaitDefault  = 10
	processWaitMax      = 30
)

// processRecord 保存一个后台作业的终态、归档句柄与取消函数。
type processRecord struct {
	id      string
	taskID  int64
	command string
	archive *outputArchive
	proc    ownedProcess
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	code    int
	exited  bool
	err     string
	started time.Time
}

func (p *processRecord) status() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	data := map[string]any{"id": p.id, "status": "running", "output_id": p.archive.ID, "started_at": p.started.UTC().Format(time.RFC3339)}
	if p.exited {
		data["status"] = "exited"
		data["exit_code"] = p.code
	}
	if p.err != "" {
		data["error"] = p.err
	}
	return data
}

func (p *processRecord) running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.exited
}

// stop 取消作业并等待清理；可重复调用。
func (p *processRecord) stop(ctx context.Context) error {
	p.cancel()
	if p.proc != nil {
		if err := p.proc.Stop(ctx); err != nil {
			return err
		}
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runProcess 实现 process 工具；作业只能在当前任务内被访问。
func (e *Executor) runProcess(ctx context.Context, args map[string]any, run scope) (Result, error) {
	result := Result{ExitCode: -1}
	action, _ := args["action"].(string)
	switch action {
	case "start":
		return e.startProcess(ctx, args, run)
	case "list":
		return e.listProcesses(run)
	case "status":
		record, err := e.processFor(run.id, args)
		if err != nil {
			return result, err
		}
		result.ExitCode = 0
		result.Data = record.status()
		result.Summary = "process " + record.id + " " + record.status()["status"].(string)
		return result, nil
	case "wait":
		record, err := e.processFor(run.id, args)
		if err != nil {
			return result, err
		}
		seconds := processWaitDefault
		if value, ok := args["wait_seconds"]; ok {
			var valid bool
			seconds, valid = intArgument(value)
			if !valid || seconds < 0 || seconds > processWaitMax {
				return result, fmt.Errorf("wait_seconds must be between 0 and %d", processWaitMax)
			}
		}
		if seconds > 0 {
			timer := time.NewTimer(time.Duration(seconds) * time.Second)
			defer timer.Stop()
			select {
			case <-record.done:
			case <-timer.C:
			case <-ctx.Done():
				return result, ctx.Err()
			}
		}
		result.ExitCode = 0
		result.Data = record.status()
		result.Summary = "process " + record.id + " " + record.status()["status"].(string)
		return result, nil
	case "logs":
		record, err := e.processFor(run.id, args)
		if err != nil {
			return result, err
		}
		offset, limit := 0, outputPageDefault
		if value, ok := args["offset"]; ok {
			var valid bool
			offset, valid = intArgument(value)
			if !valid {
				return result, errors.New("offset must be a non-negative integer")
			}
		}
		if value, ok := args["limit"]; ok {
			var valid bool
			limit, valid = intArgument(value)
			if !valid || limit < outputPageMin || limit > outputPageMax {
				return result, fmt.Errorf("limit must be between %d and %d", outputPageMin, outputPageMax)
			}
		}
		_, active := e.activeOutputs.Load(record.archive.ID)
		data, err := readOutputPage(record.archive.Path, record.archive.ID, offset, limit, active)
		if err != nil {
			return result, err
		}
		result.ExitCode = 0
		result.Data = data
		result.Summary = "logs " + record.id
		return result, nil
	case "kill":
		record, err := e.processFor(run.id, args)
		if err != nil {
			return result, err
		}
		if record.running() {
			if err := record.proc.Stop(ctx); err != nil {
				return result, err
			}
		}
		select {
		case <-record.done:
		case <-ctx.Done():
			return result, ctx.Err()
		}
		result.ExitCode = 0
		result.Data = record.status()
		result.Summary = "process " + record.id + " stopped"
		return result, nil
	default:
		return result, fmt.Errorf("unknown process action %q", action)
	}
}

// ProcessCount 返回指定任务仍在运行的后台作业数。
func (e *Executor) ProcessCount(taskID int64) int {
	e.processMu.Lock()
	defer e.processMu.Unlock()
	count := 0
	for _, record := range e.processes[taskID] {
		if record.running() {
			count++
		}
	}
	return count
}

func (e *Executor) processFor(taskID int64, args map[string]any) (*processRecord, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return nil, errors.New("id is required")
	}
	e.processMu.Lock()
	record := e.processes[taskID][id]
	e.processMu.Unlock()
	if record == nil {
		return nil, errors.New("process not found in current task")
	}
	return record, nil
}

func (e *Executor) listProcesses(run scope) (Result, error) {
	result := Result{ExitCode: -1}
	e.processMu.Lock()
	records := make([]*processRecord, 0, len(e.processes[run.id]))
	for _, record := range e.processes[run.id] {
		records = append(records, record)
	}
	e.processMu.Unlock()
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		items = append(items, record.status())
	}
	result.ExitCode = 0
	result.Data = map[string]any{"processes": items}
	result.Summary = fmt.Sprintf("%d processes", len(items))
	return result, nil
}

func (e *Executor) startProcess(ctx context.Context, args map[string]any, run scope) (Result, error) {
	result := Result{ExitCode: -1}
	command, _ := args["command"].(string)
	identity, ok := chat.IdentityFromContext(ctx)
	grant, ok := e.grant(identity)
	if !ok {
		return result, errors.New("process requires a workspace grant")
	}
	e.processMu.Lock()
	running := 0
	for _, record := range e.processes[run.id] {
		if record.running() {
			running++
		}
	}
	e.processMu.Unlock()
	if running >= maxRunningProcesses {
		return result, fmt.Errorf("task already has %d running processes", maxRunningProcesses)
	}
	workspace := e.cfg.Workspaces[grant.Workspace]
	entry, err := e.openEnvironment(ctx, run, grant.Workspace, workspace.Env)
	if err != nil {
		return result, err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return result, err
	}
	id := "p_" + hex.EncodeToString(random[:])
	archive, err := e.openOutput(run.id)
	if err != nil {
		return result, err
	}
	writer := &archiveWriter{file: archive.file}
	procCtx, cancel := context.WithCancel(ctx)
	writer.cancel = cancel
	process, err := entry.environment.Start(procCtx, command, writer)
	if err != nil {
		cancel()
		_ = archive.close(e)
		return result, err
	}
	record := &processRecord{id: id, taskID: run.id, command: command, archive: archive, proc: process, cancel: cancel, done: make(chan struct{}), started: time.Now()}
	e.processMu.Lock()
	if e.processes[run.id] == nil {
		e.processes[run.id] = make(map[string]*processRecord)
	}
	e.processes[run.id][id] = record
	e.processMu.Unlock()
	go func() {
		code, waitErr := process.Wait()
		closeErr := archive.close(e)
		record.mu.Lock()
		record.code = code
		record.exited = true
		switch {
		case waitErr != nil:
			record.err = waitErr.Error()
		case writer.err != nil:
			record.err = writer.err.Error()
		case closeErr != nil:
			record.err = closeErr.Error()
		}
		record.mu.Unlock()
		close(record.done)
	}()
	result.ExitCode = 0
	result.Data = map[string]any{"id": id, "status": "running", "output_id": archive.ID}
	result.Summary = "started process " + id
	result.OutputID = archive.ID
	return result, nil
}
