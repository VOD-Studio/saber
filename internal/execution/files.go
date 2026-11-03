package execution

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxDeliverBytes  = 16 << 20
	maxPatchBytes    = 16 << 20
	maxTextLineBytes = 16 << 20
	pageBytes        = 4096
	readLimitMax     = 1000
	readLimitDef     = 200
	listMaxEntries   = 10000
)

// workspaceRoot 用 os.OpenRoot 钉住授权目录，逐组件拒绝链接。
type workspaceRoot struct {
	root *os.Root
}

func openWorkspaceRoot(dir string) (*workspaceRoot, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &workspaceRoot{root: root}, nil
}

func (w *workspaceRoot) close() error { return w.root.Close() }

// checkParents 拒绝路径中间组件为链接或非目录，避免经目录链接逃出授权根。
func (w *workspaceRoot) checkParents(rel string) error {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		info, err := w.root.Lstat(prefix)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q contains a symbolic link", prefix)
		}
		if !info.IsDir() {
			return fmt.Errorf("path %q is not a directory", prefix)
		}
	}
	return nil
}

func (w *workspaceRoot) openFile(rel string, flag int, perm os.FileMode) (*os.File, error) {
	if err := w.checkParents(rel); err != nil {
		return nil, err
	}
	file, err := w.root.OpenFile(rel, flag|openNoFollow|openNonBlock, perm)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("only regular files are supported")
	}
	return file, nil
}

func (w *workspaceRoot) openDir(rel string) (*os.File, error) {
	if rel == "." {
		file, err := w.root.OpenFile(".", os.O_RDONLY|openNoFollow, 0)
		if err != nil {
			return nil, err
		}
		return file, nil
	}
	if err := w.checkParents(rel); err != nil {
		return nil, err
	}
	file, err := w.root.OpenFile(rel, os.O_RDONLY|openNoFollow|openNonBlock, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.IsDir() {
		_ = file.Close()
		return nil, errors.New("path is not a directory")
	}
	return file, nil
}

func (e *Executor) runFileTool(ctx context.Context, tool string, args map[string]any, run scope) (Result, error) {
	switch tool {
	case "read_file":
		return e.readFile(args, run)
	case "list_files":
		return e.listFiles(args, run)
	case "write_file":
		return e.writeFile(args, run)
	case "apply_patch":
		return e.applyPatch(args, run)
	case "snapshot":
		return e.runSnapshotTool(ctx, args, run)
	case "search_files":
		return e.searchFiles(ctx, args, run)
	case "glob_files":
		return e.globFiles(ctx, args, run)
	default:
		return Result{ExitCode: -1}, fmt.Errorf("unknown file tool %q", tool)
	}
}

func (e *Executor) readFile(args map[string]any, run scope) (Result, error) {
	result := Result{ExitCode: -1}
	path, _ := args["path"].(string)
	deliver, _ := args["deliver"].(bool)
	if deliver {
		for _, key := range []string{"offset", "limit", "byte_offset"} {
			if _, ok := args[key]; ok {
				return result, errors.New("deliver cannot be combined with offset, limit or byte_offset")
			}
		}
		return e.deliverFile(run, path)
	}
	offset, limit, byteOffset := 0, readLimitDef, 0
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
		if !valid || limit < 1 || limit > readLimitMax {
			return result, fmt.Errorf("limit must be between 1 and %d", readLimitMax)
		}
	}
	if value, ok := args["byte_offset"]; ok {
		var valid bool
		byteOffset, valid = intArgument(value)
		if !valid {
			return result, errors.New("byte_offset must be a non-negative integer")
		}
	}
	root, err := openWorkspaceRoot(run.dir)
	if err != nil {
		return result, err
	}
	defer func() { _ = root.close() }()
	file, err := root.openFile(path, os.O_RDONLY, 0)
	if err != nil {
		return result, err
	}
	defer func() { _ = file.Close() }()
	text, next, nextByte, eof, err := readFilePage(file, offset, limit, byteOffset)
	if err != nil {
		return result, err
	}
	result.ExitCode = 0
	result.Data = map[string]any{
		"path":             path,
		"offset":           offset,
		"next_offset":      next,
		"next_byte_offset": nextByte,
		"eof":              eof,
		"text":             text,
	}
	result.Summary = fmt.Sprintf("read %s from line %d to %d", path, offset, next)
	return result, nil
}

// readFilePage 按行返回最长完整行前缀，超长行用 byte_offset 续读。
func readFilePage(file *os.File, offset, limit, byteOffset int) (string, int, int, bool, error) {
	if byteOffset > maxTextLineBytes {
		return "", offset, 0, false, errors.New("byte_offset exceeds the supported line length")
	}
	reader := bufio.NewReaderSize(file, 32*1024)
	for line := 0; line < offset; line++ {
		if _, err := discardLine(reader); err != nil {
			if errors.Is(err, io.EOF) {
				return "", offset, 0, true, nil
			}
			return "", offset, 0, false, err
		}
	}
	max := byteOffset + pageBytes + utf8.UTFMax
	if max > maxTextLineBytes {
		max = maxTextLineBytes
	}
	content, newline, err := readLinePrefix(reader, max)
	if errors.Is(err, io.EOF) && len(content) == 0 {
		return "", offset, 0, true, nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", offset, 0, false, err
	}
	if byteOffset > len(content) {
		return "", offset, 0, false, fmt.Errorf("byte_offset %d exceeds line length %d", byteOffset, len(content))
	}
	if !utf8Boundary(content, byteOffset) {
		return "", offset, 0, false, errors.New("byte_offset must be a UTF-8 boundary")
	}
	atEOF := errors.Is(err, io.EOF)
	tail := content[byteOffset:]
	trimmed := false
	if !newline && len(tail) > pageBytes {
		// 超长行只返回完整字符前缀，游标在行内按字节推进。
		end := pageBytes
		for end > 0 && !utf8.RuneStart(tail[end]) {
			end--
		}
		tail = tail[:end]
		trimmed = true
	}
	if err := validateText(tail); err != nil {
		return "", offset, 0, false, err
	}
	if len(tail) == 0 && !newline {
		// 行尾没有换行且游标已在末尾：消费该行，保证下一次调用继续推进。
		return "", offset + 1, 0, true, nil
	}
	var page strings.Builder
	page.Write(tail)
	lines := 1
	nextOffset := offset
	var nextByte int
	if newline {
		nextOffset, nextByte = offset+1, 0
	} else {
		// 尚在超长行内，next_offset 不前进。
		nextByte = byteOffset + len(tail)
		if atEOF && !trimmed {
			return page.String(), offset + 1, 0, true, nil
		}
		return page.String(), nextOffset, nextByte, false, nil
	}
	for lines < limit {
		budget := pageBytes - page.Len()
		if budget <= 0 {
			break
		}
		line, complete, err := peekLine(reader, budget)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", 0, 0, false, err
		}
		if len(line) == 0 && errors.Is(err, io.EOF) {
			return page.String(), nextOffset, nextByte, true, nil
		}
		if !complete {
			// 下一行放不下：本页停在它的开头。
			break
		}
		if err := validateText(line); err != nil {
			return "", 0, 0, false, err
		}
		page.Write(line)
		lines++
		nextOffset++
		nextByte = 0
		if errors.Is(err, io.EOF) {
			return page.String(), nextOffset, nextByte, true, nil
		}
	}
	eof := false
	if _, err := reader.Peek(1); errors.Is(err, io.EOF) {
		eof = true
	}
	return page.String(), nextOffset, nextByte, eof, nil
}

// readLinePrefix 读取当前行的前缀，最多 max 字节；newline 表示前缀内含换行。
func readLinePrefix(reader *bufio.Reader, max int) ([]byte, bool, error) {
	var data []byte
	reachedEOF := false
	for len(data) < max {
		chunk, err := reader.ReadSlice('\n')
		data = append(data, chunk...)
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			reachedEOF = true
			break
		}
		return data, false, err
	}
	limit := min(len(data), max)
	if index := bytes.IndexByte(data[:limit], '\n'); index >= 0 {
		return data[:index+1], true, nil
	}
	if len(data) > max {
		data = data[:max]
	}
	if reachedEOF {
		return data, false, io.EOF
	}
	return data, false, nil
}

// discardLine 丢弃整行并返回其内容长度（不含换行）。
func discardLine(reader *bufio.Reader) (int, error) {
	length := 0
	for {
		chunk, err := reader.ReadSlice('\n')
		length += len(chunk)
		if err == nil {
			return length - 1, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return length, err
		}
		return 0, err
	}
}

// peekLine 读取下一行，最多 budget+1 字节，用于判断本页能否完整容纳。
func peekLine(reader *bufio.Reader, budget int) ([]byte, bool, error) {
	line, newline, err := readLinePrefix(reader, budget+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	if len(line) > budget {
		// 行本身超过本页剩余空间，不能返回半行。
		return line, false, err
	}
	return line, newline || errors.Is(err, io.EOF), err
}

func validateText(data []byte) error {
	if bytes.IndexByte(data, 0) >= 0 {
		return errors.New("file is binary (contains NUL); use deliver=true or exec for binary data")
	}
	if !utf8.Valid(data) {
		return errors.New("file is not valid UTF-8; use deliver=true or exec for binary data")
	}
	return nil
}

func (e *Executor) deliverFile(run scope, path string) (Result, error) {
	result := Result{ExitCode: -1}
	root, err := openWorkspaceRoot(run.dir)
	if err != nil {
		return result, err
	}
	defer func() { _ = root.close() }()
	file, err := root.openFile(path, os.O_RDONLY, 0)
	if err != nil {
		return result, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return result, err
	}
	if info.Size() > maxDeliverBytes {
		return result, errors.New("file exceeds 16 MiB delivery limit")
	}
	taskDir := filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(run.id, 10))
	artifactDir := filepath.Join(taskDir, "artifacts")
	if err = os.MkdirAll(artifactDir, 0700); err != nil {
		return result, err
	}
	if err = os.Chmod(artifactDir, 0700); err != nil {
		return result, err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return result, err
	}
	snapshot := filepath.Join(artifactDir, hex.EncodeToString(random[:])+"-"+filepath.Base(path))
	temp, err := os.CreateTemp(taskDir, ".deliver-*")
	if err != nil {
		return result, err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if err = temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return result, err
	}
	summary := &bytes.Buffer{}
	written, err := io.Copy(io.MultiWriter(temp, limitWriter{writer: summary, limit: 8192}), io.LimitReader(file, maxDeliverBytes+1))
	if err != nil {
		_ = temp.Close()
		return result, err
	}
	if written > maxDeliverBytes {
		_ = temp.Close()
		return result, errors.New("file exceeds 16 MiB delivery limit")
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return result, err
	}
	if err = temp.Close(); err != nil {
		return result, err
	}
	if err = os.Link(temp.Name(), snapshot); err != nil {
		return result, err
	}
	result.ExitCode = 0
	result.Artifact = snapshot
	result.Summary = strings.ToValidUTF8(summary.String(), "\uFFFD")
	return result, nil
}

type limitWriter struct {
	writer io.Writer
	limit  int
}

func (w limitWriter) Write(p []byte) (int, error) {
	if w.limit <= 0 {
		return len(p), nil
	}
	if len(p) > w.limit {
		p = p[:w.limit]
	}
	n, err := w.writer.Write(p)
	return n, err
}

func (e *Executor) listFiles(args map[string]any, run scope) (Result, error) {
	result := Result{ExitCode: -1}
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	root, err := openWorkspaceRoot(run.dir)
	if err != nil {
		return result, err
	}
	defer func() { _ = root.close() }()
	dir, err := root.openDir(path)
	if err != nil {
		return result, err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(listMaxEntries + 1)
	if err != nil {
		return result, err
	}
	if len(entries) > listMaxEntries {
		return result, errors.New("directory exceeds 10000 entries; inspect a subdirectory")
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		items = append(items, map[string]any{"name": entry.Name(), "directory": entry.IsDir(), "symlink": entry.Type()&os.ModeSymlink != 0})
	}
	result.ExitCode = 0
	result.Data = map[string]any{"path": path, "entries": items}
	result.Summary = fmt.Sprintf("listed %d entries in %s", len(items), path)
	return result, nil
}

func (e *Executor) writeFile(args map[string]any, run scope) (Result, error) {
	result := Result{ExitCode: -1}
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	root, err := openWorkspaceRoot(run.dir)
	if err != nil {
		return result, err
	}
	defer func() { _ = root.close() }()
	// 先保存原内容，写入失败时仍可用 snapshot 工具回滚。
	if err = e.snapshotBeforeChange(root, run, path); err != nil {
		return result, err
	}
	if err = writeWorkspaceFile(root, path, []byte(content)); err != nil {
		return result, err
	}
	result.ExitCode = 0
	result.Summary = "written: " + path
	return result, nil
}

func (e *Executor) applyPatch(args map[string]any, run scope) (Result, error) {
	result := Result{ExitCode: -1}
	path, _ := args["path"].(string)
	edits, _ := args["edits"].([]any)
	root, err := openWorkspaceRoot(run.dir)
	if err != nil {
		return result, err
	}
	defer func() { _ = root.close() }()
	file, err := root.openFile(path, os.O_RDWR, 0)
	if err != nil {
		return result, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return result, err
	}
	if info.Size() > maxPatchBytes {
		return result, errors.New("file exceeds patch size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPatchBytes+1))
	if err != nil {
		return result, err
	}
	if len(data) > maxPatchBytes {
		return result, errors.New("file exceeds patch size limit")
	}
	if !utf8.Valid(data) {
		return result, errors.New("file is not valid UTF-8")
	}
	text := string(data)
	for _, edit := range edits {
		patch, _ := edit.(map[string]any)
		old, _ := patch["old"].(string)
		replacement, _ := patch["new"].(string)
		if old == "" || strings.Count(text, old) != 1 {
			return result, errors.New("patch old text must match exactly once")
		}
		text = strings.Replace(text, old, replacement, 1)
	}
	// 验证通过、写回之前保存原内容，避免不可逆编辑。
	if err = e.snapshotBeforeWriteData(run, path, data); err != nil {
		return result, err
	}
	if err = file.Truncate(0); err != nil {
		return result, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return result, err
	}
	if _, err = file.WriteString(text); err != nil {
		return result, err
	}
	if err = file.Truncate(int64(len(text))); err != nil {
		return result, err
	}
	if err = file.Sync(); err != nil {
		return result, err
	}
	result.ExitCode = 0
	result.Summary = "patched: " + path
	return result, nil
}
