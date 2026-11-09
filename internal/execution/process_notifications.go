package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProcessNotification 是当前任务后台进程的真实完成、匹配或心跳事件。
type ProcessNotification struct {
	ProcessID string    `json:"process_id"`
	Kind      string    `json:"kind"`
	OutputID  string    `json:"output_id"`
	Text      string    `json:"text,omitempty"`
	ExitCode  int       `json:"exit_code,omitempty"`
	Error     string    `json:"error,omitempty"`
	At        time.Time `json:"at"`
}

type persistedProcess struct {
	ID            string    `json:"id"`
	TaskID        int64     `json:"task_id"`
	OutputID      string    `json:"output_id"`
	Started       time.Time `json:"started_at"`
	Exited        bool      `json:"exited"`
	Code          int       `json:"exit_code"`
	Error         string    `json:"error,omitempty"`
	PTY           bool      `json:"pty"`
	TransferredTo int64     `json:"transferred_to,omitempty"`
}

func writePrivateJSON(file string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(file), ".state-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if err = temp.Chmod(0600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	err = errors.Join(err, temp.Close())
	if err != nil {
		return err
	}
	return os.Rename(temp.Name(), file)
}

func (e *Executor) persistProcess(record *processRecord) error {
	record.persistMu.Lock()
	defer record.persistMu.Unlock()
	record.mu.Lock()
	data := persistedProcess{ID: record.id, TaskID: record.taskID, OutputID: record.archive.ID, Started: record.started, Exited: record.exited, Code: record.code, Error: record.err, PTY: record.pty}
	record.mu.Unlock()
	return writePrivateJSON(filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(data.TaskID, 10), "processes", data.ID+".json"), data)
}

func (e *Executor) notificationPath(id int64) string {
	return filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(id, 10), "process-notifications.json")
}
func (e *Executor) enqueueNotification(taskID int64, event ProcessNotification) error {
	e.notifyMu.Lock()
	defer e.notifyMu.Unlock()
	if len(e.notifications[taskID]) >= 256 {
		return errors.New("process notification queue reached 256 events; drain notifications before continuing")
	}
	event.At = time.Now().UTC()
	events := append(e.notifications[taskID], event)
	if err := writePrivateJSON(e.notificationPath(taskID), events); err != nil {
		return err
	}
	e.notifications[taskID] = events
	return nil
}

// DrainProcessNotifications 原子消费当前可信任务的通知；持久化失败时不会丢失事件。
func (e *Executor) DrainProcessNotifications(ctx context.Context) ([]ProcessNotification, error) {
	if err := e.Check(ctx, "process"); err != nil {
		return nil, err
	}
	id, _, ok := TaskScope(ctx)
	if !ok {
		return nil, errors.New("process notifications require persistent task")
	}
	e.notifyMu.Lock()
	defer e.notifyMu.Unlock()
	events := e.notifications[id]
	if len(events) == 0 {
		return nil, nil
	}
	if err := writePrivateJSON(e.notificationPath(id), []ProcessNotification{}); err != nil {
		return nil, err
	}
	delete(e.notifications, id)
	return events, nil
}

func (e *Executor) loadProcessRecords() error {
	entries, err := os.ReadDir(e.cfg.LogDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "task-") {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(entry.Name(), "task-"), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		data, err := os.ReadFile(e.notificationPath(id))
		if err == nil {
			var events []ProcessNotification
			if err = json.Unmarshal(data, &events); err != nil {
				return err
			}
			e.notifications[id] = events
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		files, err := os.ReadDir(filepath.Join(e.cfg.LogDir, entry.Name(), "processes"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, file := range files {
			if !file.Type().IsRegular() || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(e.cfg.LogDir, entry.Name(), "processes", file.Name()))
			if err != nil {
				return err
			}
			var stored persistedProcess
			if err = json.Unmarshal(data, &stored); err != nil {
				return err
			}
			if stored.TransferredTo != 0 {
				continue
			}
			outputTask, hexID, err := parseOutputID(stored.OutputID)
			if err != nil || stored.TaskID != id || stored.ID+".json" != file.Name() {
				return errors.New("invalid persisted process ownership")
			}
			if outputTask != id && !e.validTransferredOutput(outputTask, id, stored.ID) {
				return errors.New("persisted process has no committed ownership transfer")
			}
			if !stored.Exited {
				stored.Exited = true
				stored.Code = -1
				stored.Error = "process interrupted by service restart; command was not replayed"
			}
			done := make(chan struct{})
			close(done)
			record := &processRecord{id: stored.ID, taskID: id, archive: &outputArchive{ID: stored.OutputID, Path: e.outputPath(id, hexID)}, done: done, started: stored.Started, exited: true, code: stored.Code, err: stored.Error, pty: stored.PTY}
			record.ownerID.Store(id)
			if e.processes[id] == nil {
				e.processes[id] = map[string]*processRecord{}
			}
			e.processes[id][record.id] = record
			if err = e.persistProcess(record); err != nil {
				return err
			}
		}
	}
	return nil
}

type processWatchWriter struct {
	writer   io.Writer
	executor *Executor
	owner    *atomic.Int64
	id       string
	outputID string
	patterns []*regexp.Regexp
	matched  []bool
	mu       sync.Mutex
	tail     string
}

func (w *processWatchWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	if err != nil {
		return n, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	text := w.tail + string(data[:n])
	if len(text) > 8192 {
		text = text[len(text)-8192:]
	}
	for i, pattern := range w.patterns {
		if !w.matched[i] && pattern.MatchString(text) {
			w.matched[i] = true
			message := text
			if len(message) > 1024 {
				message = message[len(message)-1024:]
			}
			if err := w.executor.enqueueNotification(w.owner.Load(), ProcessNotification{ProcessID: w.id, OutputID: w.outputID, Kind: "watch", Text: message}); err != nil {
				return n, err
			}
		}
	}
	if len(text) > 1024 {
		w.tail = text[len(text)-1024:]
	} else {
		w.tail = text
	}
	return n, nil
}

func (e *Executor) heartbeat(record *processRecord, seconds int) {
	if seconds <= 0 {
		return
	}
	ticker := time.NewTicker(time.Duration(seconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-record.done:
			return
		case <-ticker.C:
			if err := e.enqueueNotification(record.ownerID.Load(), ProcessNotification{ProcessID: record.id, OutputID: record.archive.ID, Kind: "heartbeat", Text: "process is still running"}); err != nil {
				record.mu.Lock()
				record.err = fmt.Sprintf("notification delivery failed: %v", err)
				record.mu.Unlock()
				return
			}
		}
	}
}
