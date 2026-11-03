package execution

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// outputIDPattern 固定日志句柄形状，避免用模型提供的任意路径读取宿主文件。
var outputIDPattern = regexp.MustCompile(`^t([1-9][0-9]*)-([0-9a-f]{32})$`)

const (
	outputPageDefault = 4096
	outputPageMin     = 4
	outputPageMax     = 4096
)

// outputArchive 表示一次命令或后台作业的私有输出归档。
type outputArchive struct {
	ID   string
	Path string
	file *os.File
}

// openOutput 创建新的私有归档并返回模型可见句柄。
func (e *Executor) openOutput(taskID int64) (*outputArchive, error) {
	dir := filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(taskID, 10))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	hexID := hex.EncodeToString(random[:])
	path := filepath.Join(dir, "output-"+hexID+".log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	archive := &outputArchive{ID: fmt.Sprintf("t%d-%s", taskID, hexID), Path: path, file: file}
	e.activeOutputs.Store(archive.ID, struct{}{})
	return archive, nil
}

// close 停止归档并把句柄标记为终态，之后读取到末尾即可返回 eof。
func (a *outputArchive) close(e *Executor) error {
	err := a.file.Close()
	e.activeOutputs.Delete(a.ID)
	return err
}

// parseOutputID 只接受程序生成的 t<taskID>-<32hex> 句柄。
func parseOutputID(id string) (int64, string, error) {
	match := outputIDPattern.FindStringSubmatch(id)
	if match == nil {
		return 0, "", errors.New("invalid output_id; use the value returned by a tool")
	}
	taskID, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || taskID <= 0 {
		return 0, "", errors.New("invalid output_id task")
	}
	return taskID, match[2], nil
}

// outputPath 由句柄推导私有归档路径；不会接受宿主任意路径。
func (e *Executor) outputPath(taskID int64, hexID string) string {
	return filepath.Join(e.cfg.LogDir, "task-"+strconv.FormatInt(taskID, 10), "output-"+hexID+".log")
}

// ReadOutput 分页读取程序归档的原始字节，不按房间推断访问权。
func (e *Executor) ReadOutput(ctx context.Context, args map[string]any) (Result, error) {
	result := Result{ExitCode: -1}
	run, ok := ctx.Value(scopeKey{}).(scope)
	if !ok || run.id <= 0 {
		return result, errors.New("read_output requires a persistent task")
	}
	if err := validateArgs("read_output", args); err != nil {
		return result, err
	}
	id, _ := args["output_id"].(string)
	taskID, hexID, err := parseOutputID(id)
	if err != nil {
		return result, err
	}
	authorize := e.outputAuthorizer()
	if authorize == nil {
		return result, errors.New("output access is not configured")
	}
	if err := authorize(ctx, taskID); err != nil {
		return result, err
	}
	offset, limit := 0, outputPageDefault
	if value, ok := args["offset"]; ok {
		var valid bool
		offset, valid = intArgument(value)
		if !valid || offset < 0 {
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
	if err := ctx.Err(); err != nil {
		return result, err
	}
	_, active := e.activeOutputs.Load(id)
	data, err := readOutputPage(e.outputPath(taskID, hexID), id, offset, limit, active)
	if err != nil {
		return result, err
	}
	result.ExitCode = 0
	result.Data = data
	result.Summary = fmt.Sprintf("read output %s bytes %d-%d", id, data["offset"], data["next_offset"])
	return result, nil
}

// readOutputPage 按原始字节游标分页读取归档；active 表示 writer 尚未关闭。
func readOutputPage(path, id string, offset, limit int, active bool) (map[string]any, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("output archive does not exist")
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("output archive is not a regular file")
	}
	size := info.Size()
	if int64(offset) > size {
		offset = int(size)
	}
	if offset > 0 && offset < int(size) {
		// 游标落在 UTF-8 续字节上时，必须显式拒绝而不是返回半个字符。
		back := min(3, offset)
		probe := make([]byte, back+1)
		n, probeErr := file.ReadAt(probe, int64(offset-back))
		if probeErr != nil && !errors.Is(probeErr, io.EOF) {
			return nil, probeErr
		}
		if !utf8Boundary(probe[:n], back) {
			return nil, errors.New("offset must be a UTF-8 boundary")
		}
	}
	buffer := make([]byte, limit+utf8.UTFMax)
	n, err := file.ReadAt(buffer, int64(offset))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	buffer = buffer[:n]
	end := len(buffer)
	if end > limit {
		end = limit
		for end > 0 && offset+end < int(size) && !utf8.RuneStart(buffer[end]) {
			end--
		}
	}
	text := strings.ToValidUTF8(string(buffer[:end]), "\uFFFD")
	next := offset + end
	atEnd := next >= int(size)
	data := map[string]any{
		"output_id":   id,
		"offset":      offset,
		"next_offset": next,
		"eof":         atEnd && !active,
		"text":        text,
		"encoding":    "utf-8",
	}
	if active && atEnd {
		data["caught_up"] = true
	}
	return data, nil
}

// intArgument 接受工具 JSON 解码后的数字类型，拒绝小数与负数。
func intArgument(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, v >= 0
	case int64:
		return int(v), v >= 0 && v <= math.MaxInt32
	case float64:
		if v < 0 || v > math.MaxInt32 || v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	case json.Number:
		n, err := strconv.ParseInt(string(v), 10, 32)
		return int(n), err == nil && n >= 0
	default:
		return 0, false
	}
}

// utf8Boundary 判断原始字节游标是否落在完整字符之后。
func utf8Boundary(b []byte, offset int) bool {
	if offset <= 0 || offset >= len(b) {
		return true
	}
	if utf8.RuneStart(b[offset]) {
		return true
	}
	for i := 1; i <= utf8.UTFMax && offset-i >= 0; i++ {
		if !utf8.RuneStart(b[offset-i]) {
			continue
		}
		_, size := utf8.DecodeRune(b[offset-i:])
		return size <= i
	}
	return true
}

// SetOutputAuthorizer 在接受任务前注入输出访问校验，避免工作线程先于授权运行。
func (e *Executor) SetOutputAuthorizer(fn func(context.Context, int64) error) {
	e.outputMu.Lock()
	defer e.outputMu.Unlock()
	e.outputAuth = fn
}

func (e *Executor) outputAuthorizer() func(context.Context, int64) error {
	e.outputMu.RLock()
	defer e.outputMu.RUnlock()
	return e.outputAuth
}
