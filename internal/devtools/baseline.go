package devtools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ErrStaleOverwrite 表示未读取完整当前文件或文件已改变；调用方必须在任何写入前拒绝。
var ErrStaleOverwrite = errors.New("stale overwrite blocked")

// Digest 返回原始文件字节的版本，不使用可能被保留的修改时间。
func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

type byteRange struct{ start, end int64 }
type readBaseline struct {
	digest   string
	total    int64
	ranges   []byteRange
	redacted bool
	complete bool
}
type baselineStore struct {
	mu     sync.Mutex
	scopes map[string]map[string]*readBaseline
}

// ObserveRead 将同一字节版本的分页覆盖合并；偏移为从零开始的半开区间。
// 提取结果、截断的长行和脱敏视图不能被当作原文件完整基线。
func (m *Manager) ObserveRead(scopeID, path, digest string, startByte, endByte, totalBytes int64, redacted bool) {
	if scopeID == "" || path == "" || len(digest) != 64 || startByte < 0 || endByte < startByte || endByte > totalBytes {
		return
	}
	for _, c := range digest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return
		}
	}
	m.baselines.mu.Lock()
	defer m.baselines.mu.Unlock()
	if m.baselines.scopes == nil {
		m.baselines.scopes = make(map[string]map[string]*readBaseline)
	}
	paths := m.baselines.scopes[scopeID]
	if paths == nil {
		paths = make(map[string]*readBaseline)
		m.baselines.scopes[scopeID] = paths
	}
	b := paths[path]
	if b == nil || b.digest != digest || b.total != totalBytes {
		b = &readBaseline{digest: digest, total: totalBytes}
		paths[path] = b
	}
	b.redacted = b.redacted || redacted
	b.ranges = append(b.ranges, byteRange{startByte, endByte})
	sort.Slice(b.ranges, func(i, j int) bool { return b.ranges[i].start < b.ranges[j].start })
	merged := b.ranges[:0]
	for _, r := range b.ranges {
		if len(merged) > 0 && r.start <= merged[len(merged)-1].end {
			if r.end > merged[len(merged)-1].end {
				merged[len(merged)-1].end = r.end
			}
		} else {
			merged = append(merged, r)
		}
	}
	b.ranges = merged
	b.complete = !b.redacted && len(merged) == 1 && merged[0].start == 0 && merged[0].end == totalBytes
	if len(paths) > 1000 {
		for k := range paths {
			if k != path {
				delete(paths, k)
				break
			}
		}
	}
}

// Snapshot 只登记实际返回给消费者的完整、未脱敏原始字节。
func (m *Manager) Snapshot(ctx context.Context, scopeID, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if scopeID == "" || path == "" {
		return errors.New("read baseline requires trusted scope and path")
	}
	m.baselines.mu.Lock()
	if paths := m.baselines.scopes[scopeID]; paths != nil {
		delete(paths, path)
	}
	m.baselines.mu.Unlock()
	m.ObserveRead(scopeID, path, Digest(data), 0, int64(len(data)), int64(len(data)), false)
	return nil
}

// CheckOverwrite 只验证基线，不打开路径；文件工具须在持有路径锁时重读 current 并立即写入。
func (m *Manager) CheckOverwrite(scopeID, path string, current []byte, exists bool) error {
	if err := CheckTextWrite(path, exists, current); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	m.baselines.mu.Lock()
	defer m.baselines.mu.Unlock()
	b := m.baselines.scopes[scopeID][path]
	if b == nil || !b.complete {
		return fmt.Errorf("%w: %s requires a complete unredacted read before replacement", ErrStaleOverwrite, path)
	}
	if b.digest != Digest(current) {
		return fmt.Errorf("%w: %s changed after the read; reload and merge current bytes", ErrStaleOverwrite, path)
	}
	return nil
}

// Written 只传递来自完整已知字节的基线；盲目局部补丁不会使整文件覆盖变得合法。
func (m *Manager) Written(scopeID, path string, before, after []byte, created bool) {
	m.baselines.mu.Lock()
	paths := m.baselines.scopes[scopeID]
	b := paths[path]
	known := created || b != nil && b.complete && b.digest == Digest(before)
	if paths != nil {
		delete(paths, path)
	}
	m.baselines.mu.Unlock()
	if known {
		m.ObserveRead(scopeID, path, Digest(after), 0, int64(len(after)), int64(len(after)), false)
	}
}

// ForgetScope 清理结束任务或上下文原文被压缩后的覆盖授权与待交付诊断。
func (m *Manager) ForgetScope(scopeID string) {
	m.baselines.mu.Lock()
	delete(m.baselines.scopes, scopeID)
	m.baselines.mu.Unlock()
	m.mu.Lock()
	delete(m.deferred, scopeID)
	m.mu.Unlock()
}

// CheckTextWrite 拒绝会损坏二进制容器及 SQLite 旁路日志的纯文本写入。
func CheckTextWrite(path string, exists bool, current []byte) error {
	lower := strings.ToLower(path)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if strings.HasSuffix(lower, suffix) {
			base := strings.TrimSuffix(lower, suffix)
			if sqliteExtension(filepath.Ext(base)) {
				return fmt.Errorf("refusing text write to SQLite sidecar %s", path)
			}
		}
	}
	ext := filepath.Ext(lower)
	if opaqueExtensions[ext] {
		return fmt.Errorf("refusing text write to binary document %s: extraction is not its original bytes", path)
	}
	if exists && (binaryExtensions[ext] || bytes.IndexByte(current, 0) >= 0) {
		return fmt.Errorf("refusing text overwrite of binary file %s; use a binary-aware editor", path)
	}
	return nil
}

var opaqueExtensions = map[string]bool{
	".doc": true, ".docx": true, ".docm": true, ".xls": true, ".xlsx": true, ".xlsm": true, ".xlsb": true,
	".ppt": true, ".pps": true, ".pot": true, ".pptx": true, ".pptm": true, ".ppsx": true, ".ppsm": true,
	".odt": true, ".ods": true, ".odp": true, ".rtf": true, ".epub": true,
}
var binaryExtensions = map[string]bool{
	".pdf": true, ".db": true, ".sqlite": true, ".sqlite3": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".zip": true, ".gz": true, ".tar": true, ".7z": true, ".rar": true, ".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".mp3": true, ".mp4": true, ".wav": true, ".ogg": true, ".webm": true, ".woff": true, ".woff2": true, ".ttf": true, ".bin": true,
}

// ConflictBlocks 只统计成对且含分隔符的 Git 冲突块，孤立字符串标记不计数。
func ConflictBlocks(text string) int {
	count, phase := 0, 0
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "<<<<<<< "):
			phase = 1
		case phase == 1 && strings.HasPrefix(line, "||||||| "):
		case phase == 1 && line == "=======":
			phase = 2
		case phase == 2 && strings.HasPrefix(line, ">>>>>>> "):
			count++
			phase = 0
		}
	}
	return count
}

// InvalidateReadScope 在上下文压缩丢失原始文件视图时撤销整文件覆盖基线，不丢失延迟诊断。
func (m *Manager) InvalidateReadScope(scopeID string) {
	m.baselines.mu.Lock()
	delete(m.baselines.scopes, scopeID)
	m.baselines.mu.Unlock()
}
