package execution

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// maxSnapshotBytes 限制单个快照保存的原文件大小，超过时拒绝修改以保证可回滚。
	maxSnapshotBytes = 16 << 20
	// snapshotListMax 是单次 list 返回的最大条目数，避免把整段编辑历史灌进模型上下文。
	snapshotListMax = 50
)

// fileSnapshot 是任务私有的编辑前快照；Content 以 base64 编码写入 JSON。
//
// Absent 为真时表示修改前文件不存在，回滚即删除该文件。
type fileSnapshot struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	SHA256    string    `json:"sha256,omitempty"`
	Absent    bool      `json:"absent,omitempty"`
	Content   []byte    `json:"content,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// snapshotSummary 是 list 返回的摘要，不包含文件正文。
type snapshotSummary struct {
	ID        string    `json:"snapshot_id"`
	Path      string    `json:"path"`
	Absent    bool      `json:"absent"`
	SHA256    string    `json:"sha256,omitempty"`
	Size      int       `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

func (s fileSnapshot) summary() snapshotSummary {
	return snapshotSummary{ID: s.ID, Path: s.Path, Absent: s.Absent, SHA256: s.SHA256, Size: len(s.Content), CreatedAt: s.CreatedAt}
}

// snapshotDir 返回任务私有的快照目录；任务删除后目录才消失，因此终态任务仍可回滚。
func (e *Executor) snapshotDir(taskID int64) string {
	return filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(taskID, 10), "snapshots")
}

// snapshotAllowed 报告当前身份是否可使用快照工具；只要具备任一写入能力即可。
func (e *Executor) snapshotAllowed(ctx context.Context) bool {
	return e.Check(ctx, "write_file") == nil || e.Check(ctx, "apply_patch") == nil
}

// cleanSnapshotPath 规范化工作区相对路径，拒绝绝对路径与向上穿越。
func cleanSnapshotPath(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) {
		return "", errors.New("snapshot path is required")
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return "", errors.New("snapshot path must be workspace-relative")
	}
	cleaned := path.Clean(filepath.ToSlash(p))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("snapshot path escapes workspace")
	}
	return cleaned, nil
}

// saveSnapshot 在修改前保存原内容；absent 为真时记录“文件不存在”。
func (e *Executor) saveSnapshot(taskID int64, p string, content []byte, absent bool) (*fileSnapshot, error) {
	if len(content) > maxSnapshotBytes {
		return nil, errors.New("file exceeds 16 MiB snapshot limit")
	}
	clean, err := cleanSnapshotPath(p)
	if err != nil {
		return nil, err
	}
	record := fileSnapshot{Path: clean, Absent: absent, CreatedAt: time.Now().UTC()}
	if !absent {
		sum := sha256.Sum256(content)
		record.SHA256 = hex.EncodeToString(sum[:])
		record.Content = slices.Clone(content)
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, err
	}
	record.ID = hex.EncodeToString(random[:])
	data, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	dir := e.snapshotDir(taskID)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	temp, err := os.CreateTemp(dir, ".snapshot-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if err = temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return nil, err
	}
	if _, err = temp.Write(data); err != nil {
		_ = temp.Close()
		return nil, err
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return nil, err
	}
	if err = temp.Close(); err != nil {
		return nil, err
	}
	if err = os.Rename(temp.Name(), filepath.Join(dir, record.ID+".json")); err != nil {
		return nil, err
	}
	return &record, nil
}

// readSnapshot 读取当前任务的单条快照；ID 只能是程序生成的小写十六进制。
func (e *Executor) readSnapshot(taskID int64, id string) (*fileSnapshot, error) {
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return nil, errors.New("invalid snapshot_id")
	}
	data, err := os.ReadFile(filepath.Join(e.snapshotDir(taskID), id+".json"))
	if err != nil {
		return nil, err
	}
	var record fileSnapshot
	if err = json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("snapshot %s is corrupt: %w", id, err)
	}
	if record.ID != id {
		return nil, errors.New("snapshot file does not match its id")
	}
	return &record, nil
}

// loadSnapshots 读取当前任务的全部快照并按时间倒序排列。
func (e *Executor) loadSnapshots(taskID int64) ([]fileSnapshot, error) {
	dir := e.snapshotDir(taskID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	records := make([]fileSnapshot, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		record, err := e.readSnapshot(taskID, id)
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID > records[j].ID
		}
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	return records, nil
}

// listSnapshots 按时间倒序返回当前任务的快照摘要，最多 50 条。
func (e *Executor) listSnapshots(taskID int64) ([]snapshotSummary, error) {
	records, err := e.loadSnapshots(taskID)
	if err != nil {
		return nil, err
	}
	summaries := make([]snapshotSummary, 0, min(len(records), snapshotListMax))
	for _, record := range records[:min(len(records), snapshotListMax)] {
		summaries = append(summaries, record.summary())
	}
	return summaries, nil
}

// latestSnapshot 返回指定路径最近的一条快照，用于按 path 回滚。
func (e *Executor) latestSnapshot(taskID int64, p string) (*fileSnapshot, error) {
	records, err := e.loadSnapshots(taskID)
	if err != nil {
		return nil, err
	}
	for i := range records {
		if records[i].Path == p {
			return &records[i], nil
		}
	}
	return nil, fmt.Errorf("no snapshot for path %q", p)
}

// readCurrentContent 读取工作区文件当前内容；不存在时返回 exists=false。
func readCurrentContent(root *workspaceRoot, p string) ([]byte, bool, error) {
	file, err := root.openFile(p, os.O_RDONLY, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	if info.Size() > maxSnapshotBytes {
		return nil, false, errors.New("file exceeds 16 MiB snapshot limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSnapshotBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxSnapshotBytes {
		return nil, false, errors.New("file exceeds 16 MiB snapshot limit")
	}
	return data, true, nil
}

// writeWorkspaceFile 通过钉住的根写入完整内容，保留文件原有的 0600 语义。
func writeWorkspaceFile(root *workspaceRoot, p string, content []byte) error {
	file, err := root.openFile(p, os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	if err = file.Truncate(0); err != nil {
		_ = file.Close()
		return err
	}
	if _, err = file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// validateSnapshotArgs 只接受当前 action 适用的参数。
func validateSnapshotArgs(args map[string]any) error {
	action, ok := args["action"].(string)
	if !ok || action == "" {
		return errors.New("action is required")
	}
	stringArg := func(key string) (string, bool) {
		value, exists := args[key]
		if !exists {
			return "", true
		}
		text, valid := value.(string)
		if !valid || strings.ContainsRune(text, 0) {
			return "", false
		}
		return text, true
	}
	id, okID := stringArg("snapshot_id")
	path, okPath := stringArg("path")
	if !okID || !okPath {
		return errors.New("snapshot_id and path must be strings")
	}
	switch action {
	case "list":
		if id != "" || path != "" {
			return errors.New("list accepts only action")
		}
	case "rollback":
		if id == "" && path == "" {
			return errors.New("rollback requires snapshot_id or path")
		}
	default:
		return fmt.Errorf("unknown snapshot action %q", action)
	}
	return nil
}

// runSnapshotTool 执行 snapshot 工具：list 只读摘要，rollback 先备份当前内容再恢复。
func (e *Executor) runSnapshotTool(ctx context.Context, args map[string]any, run scope) (Result, error) {
	if err := e.Check(ctx, "snapshot"); err != nil {
		return Result{ExitCode: -1}, err
	}
	action, _ := args["action"].(string)
	switch action {
	case "list":
		summaries, err := e.listSnapshots(run.id)
		if err != nil {
			return Result{ExitCode: -1}, err
		}
		return Result{
			ExitCode: 0,
			Data:     map[string]any{"snapshots": summaries, "count": len(summaries)},
			Summary:  fmt.Sprintf("snapshots: %d", len(summaries)),
		}, nil
	case "rollback":
		return e.rollbackSnapshot(args, run)
	default:
		return Result{ExitCode: -1}, fmt.Errorf("unknown snapshot action %q", action)
	}
}

// rollbackSnapshot 恢复原内容；覆盖与快照不同的现有内容前会再保存一份快照。
func (e *Executor) rollbackSnapshot(args map[string]any, run scope) (Result, error) {
	result := Result{ExitCode: -1}
	var (
		record *fileSnapshot
		err    error
	)
	if id, _ := args["snapshot_id"].(string); id != "" {
		record, err = e.readSnapshot(run.id, id)
	} else if p, _ := args["path"].(string); p != "" {
		clean, cleanErr := cleanSnapshotPath(p)
		if cleanErr != nil {
			return result, cleanErr
		}
		record, err = e.latestSnapshot(run.id, clean)
	} else {
		return result, errors.New("rollback requires snapshot_id or path")
	}
	if err != nil {
		return result, err
	}
	target, err := cleanSnapshotPath(record.Path)
	if err != nil {
		return result, err
	}
	root, err := openWorkspaceRoot(run.dir)
	if err != nil {
		return result, err
	}
	defer func() { _ = root.close() }()
	if err = root.checkParents(target); err != nil {
		return result, err
	}
	current, exists, err := readCurrentContent(root, target)
	if err != nil {
		return result, err
	}
	data := map[string]any{"path": target, "snapshot_id": record.ID}

	unchanged := exists && !record.Absent && string(current) == string(record.Content)
	if !unchanged && exists {
		// 回滚会丢掉当前内容，先存一份快照，避免不可逆。
		pre, saveErr := e.saveSnapshot(run.id, target, current, false)
		if saveErr != nil {
			return result, saveErr
		}
		data["pre_rollback_snapshot_id"] = pre.ID
	}
	if record.Absent {
		if exists {
			if err = root.checkParents(target); err != nil {
				return result, err
			}
			if err = root.root.Remove(target); err != nil {
				return result, err
			}
			data["deleted"] = true
		}
	} else if !unchanged {
		if err = writeWorkspaceFile(root, target, record.Content); err != nil {
			return result, err
		}
		data["restored"] = true
	}
	data["unchanged"] = unchanged
	result.ExitCode = 0
	result.Data = data
	result.Summary = "rolled back: " + target
	return result, nil
}

// snapshotBeforeChange 在写文件前保存原内容，返回是否记录了 absent。
func (e *Executor) snapshotBeforeChange(root *workspaceRoot, run scope, p string) error {
	content, exists, err := readCurrentContent(root, p)
	if err != nil {
		return err
	}
	_, err = e.saveSnapshot(run.id, p, content, !exists)
	return err
}

// snapshotBeforeWriteData 用已经读出的原内容保存快照，避免重复读取。
func (e *Executor) snapshotBeforeWriteData(run scope, p string, data []byte) error {
	_, err := e.saveSnapshot(run.id, p, data, false)
	return err
}
