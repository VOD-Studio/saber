package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	searchLimitMax     = 200
	searchLimitDefault = 100
	globLimitMax       = 500
	globLimitDefault   = 200
	maxScanEntries     = 100000
	maxScanDepth       = 32
	maxScanBytes       = 64 << 20
	maxSearchFileBytes = 4 << 20
	maxMatchTextBytes  = 512
	maxEncodedData     = 8192
)

// scanStats 汇总一次遍历的跳过原因与硬限状态。
type scanStats struct {
	entries   int
	skipped   map[string]int
	truncated bool
}

func newScanStats() *scanStats { return &scanStats{skipped: map[string]int{}} }

// walkWorkspace 按 fs.WalkDir 的字典序深度优先顺序遍历，不跟随目录链接。
func walkWorkspace(ctx context.Context, root *os.Root, start string, includeHidden bool, visit func(path string, entry fs.DirEntry) error) (*scanStats, error) {
	stats := newScanStats()
	err := fs.WalkDir(root.FS(), start, func(p string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if p == start {
				return walkErr
			}
			// 单目录权限拒绝计入跳过原因，不终止整次搜索。
			stats.skipped["permission"]++
			return nil
		}
		if p != start {
			name := entry.Name()
			if name == ".git" {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !includeHidden && strings.HasPrefix(name, ".") {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if strings.Count(p, "/")+1 > maxScanDepth {
				if entry.IsDir() {
					stats.skipped["depth_limit"]++
					return fs.SkipDir
				}
				return nil
			}
			stats.entries++
			if stats.entries > maxScanEntries {
				stats.truncated = true
				return fs.SkipAll
			}
		}
		return visit(p, entry)
	})
	if errors.Is(err, fs.SkipAll) {
		err = nil
	}
	return stats, err
}

// matchGlob 支持 * ? [] 与独立路径段的 **；** 匹配零个或多个路径段。
func matchGlob(pattern, name string) (bool, error) {
	patternParts, err := splitGlob(pattern)
	if err != nil {
		return false, err
	}
	return matchSegments(patternParts, strings.Split(name, "/")), nil
}

func splitGlob(pattern string) ([]string, error) {
	if pattern == "" || len(pattern) > 1024 {
		return nil, errors.New("glob pattern must be 1..1024 bytes")
	}
	parts := strings.Split(pattern, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("invalid glob segment %q", part)
		}
		if part != "**" {
			if _, err := path.Match(part, "probe"); err != nil {
				return nil, fmt.Errorf("invalid glob segment %q: %w", part, err)
			}
		}
	}
	return parts, nil
}

func matchSegments(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		if matchSegments(pattern[1:], name) {
			return true
		}
		if len(name) > 0 {
			return matchSegments(pattern, name[1:])
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	matched, err := path.Match(pattern[0], name[0])
	return err == nil && matched && matchSegments(pattern[1:], name[1:])
}

func (e *Executor) searchFiles(ctx context.Context, args map[string]any, run scope) (Result, error) {
	result := Result{ExitCode: -1}
	pattern, _ := args["pattern"].(string)
	if pattern == "" || len(pattern) > 512 {
		return result, errors.New("pattern must be 1..512 bytes")
	}
	caseSensitive := true
	if value, ok := args["case_sensitive"]; ok {
		caseSensitive, _ = value.(bool)
	}
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return result, err
	}
	start := "."
	if value, ok := args["path"]; ok {
		start, _ = value.(string)
	}
	fileGlob := "**"
	if value, ok := args["file_glob"]; ok {
		fileGlob, _ = value.(string)
	}
	if _, err = splitGlob(fileGlob); err != nil {
		return result, err
	}
	offset, limit := 0, searchLimitDefault
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
		if !valid || limit < 1 || limit > searchLimitMax {
			return result, fmt.Errorf("limit must be between 1 and %d", searchLimitMax)
		}
	}
	includeHidden := false
	if value, ok := args["include_hidden"]; ok {
		includeHidden, _ = value.(bool)
	}
	root, err := openWorkspaceRoot(run.dir)
	if err != nil {
		return result, err
	}
	defer func() { _ = root.close() }()
	dir, err := root.openDir(start)
	if err != nil {
		return result, err
	}
	_ = dir.Close()

	var matches []map[string]any
	matched := 0
	more := false
	skipped := map[string]int{}
	scanned := int64(0)
	hardLimit := false
	stats, err := walkWorkspace(ctx, root.root, start, includeHidden, func(p string, entry fs.DirEntry) error {
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			skipped["symlink"]++
			return nil
		}
		ok, err := matchGlob(fileGlob, p)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			skipped["unreadable"]++
			return nil
		}
		if info.Size() > maxSearchFileBytes {
			skipped["too_large"]++
			return nil
		}
		if scanned+info.Size() > maxScanBytes {
			hardLimit = true
			return fs.SkipAll
		}
		file, err := root.openFile(p, os.O_RDONLY, 0)
		if err != nil {
			skipped["unreadable"]++
			return nil
		}
		data, readErr := readBounded(file, maxSearchFileBytes)
		_ = file.Close()
		if readErr != nil {
			skipped["unreadable"]++
			return nil
		}
		scanned += int64(len(data))
		head := data
		if len(head) > 8192 {
			head = head[:8192]
		}
		if bytes.IndexByte(head, 0) >= 0 {
			skipped["binary"]++
			return nil
		}
		for index, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSuffix(line, "\r")
			if !expression.MatchString(line) {
				continue
			}
			if matched < offset {
				matched++
				continue
			}
			if len(matches) >= limit {
				more = true
				return fs.SkipAll
			}
			candidate := map[string]any{"path": p, "line": index + 1, "text": clipText(line)}
			if encodedSearchSize(append(matches, candidate)) > maxEncodedData && len(matches) > 0 {
				more = true
				return fs.SkipAll
			}
			matches = append(matches, candidate)
			matched++
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	if stats != nil {
		hardLimit = hardLimit || stats.truncated
		for reason, count := range stats.skipped {
			skipped[reason] += count
		}
	}
	build := func() map[string]any {
		data := map[string]any{"matches": matches, "skipped": skipped}
		if len(matches) == 0 {
			data["matches"] = []map[string]any{}
		}
		switch {
		case hardLimit:
			data["next_offset"], data["eof"], data["truncated"] = nil, false, true
		case more:
			data["next_offset"], data["eof"], data["truncated"] = offset+len(matches), false, false
		default:
			data["next_offset"], data["eof"], data["truncated"] = nil, true, false
		}
		return data
	}
	// 最终按实际 JSON 编码大小裁减，保证 data 不超过 8 KiB。
	data := build()
	for len(matches) > 0 {
		encoded, marshalErr := json.Marshal(data)
		if marshalErr == nil && len(encoded) <= maxEncodedData {
			break
		}
		matches = matches[:len(matches)-1]
		more = true
		data = build()
	}
	result.ExitCode = 0
	result.Data = data
	result.Summary = fmt.Sprintf("searched %s: %d matches", start, len(matches))
	return result, nil
}

func (e *Executor) globFiles(ctx context.Context, args map[string]any, run scope) (Result, error) {
	result := Result{ExitCode: -1}
	pattern, _ := args["pattern"].(string)
	if _, err := splitGlob(pattern); err != nil {
		return result, err
	}
	start := "."
	if value, ok := args["path"]; ok {
		start, _ = value.(string)
	}
	offset, limit := 0, globLimitDefault
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
		if !valid || limit < 1 || limit > globLimitMax {
			return result, fmt.Errorf("limit must be between 1 and %d", globLimitMax)
		}
	}
	includeHidden := false
	if value, ok := args["include_hidden"]; ok {
		includeHidden, _ = value.(bool)
	}
	root, err := openWorkspaceRoot(run.dir)
	if err != nil {
		return result, err
	}
	defer func() { _ = root.close() }()
	dir, err := root.openDir(start)
	if err != nil {
		return result, err
	}
	_ = dir.Close()

	var paths []string
	matched := 0
	more := false
	hardLimit := false
	stats, err := walkWorkspace(ctx, root.root, start, includeHidden, func(p string, entry fs.DirEntry) error {
		if p == start {
			return nil
		}
		ok, err := matchGlob(pattern, p)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if matched < offset {
			matched++
			return nil
		}
		if len(paths) >= limit {
			more = true
			return fs.SkipAll
		}
		candidate := append(paths, p)
		if encodedGlobSize(candidate) > maxEncodedData && len(paths) > 0 {
			more = true
			return fs.SkipAll
		}
		paths = candidate
		matched++
		return nil
	})
	if err != nil {
		return result, err
	}
	if stats != nil {
		hardLimit = stats.truncated
	}
	build := func() map[string]any {
		data := map[string]any{"paths": paths}
		if len(paths) == 0 {
			data["paths"] = []string{}
		}
		switch {
		case hardLimit:
			data["next_offset"], data["eof"], data["truncated"] = nil, false, true
		case more:
			data["next_offset"], data["eof"], data["truncated"] = offset+len(paths), false, false
		default:
			data["next_offset"], data["eof"], data["truncated"] = nil, true, false
		}
		return data
	}
	data := build()
	for len(paths) > 0 {
		encoded, marshalErr := json.Marshal(data)
		if marshalErr == nil && len(encoded) <= maxEncodedData {
			break
		}
		paths = paths[:len(paths)-1]
		more = true
		data = build()
	}
	result.ExitCode = 0
	result.Data = data
	result.Summary = fmt.Sprintf("globbed %s: %d paths", start, len(paths))
	return result, nil
}

func readBounded(file *os.File, limit int64) ([]byte, error) {
	data := make([]byte, 0, min(limit, 64*1024))
	buffer := make([]byte, 32*1024)
	var total int64
	for total <= limit {
		n, err := file.Read(buffer)
		data = append(data, buffer[:n]...)
		total += int64(n)
		if err != nil {
			if total > limit {
				return nil, errors.New("file exceeds search size limit")
			}
			break
		}
	}
	if total > limit {
		return nil, errors.New("file exceeds search size limit")
	}
	return data, nil
}

func clipText(text string) string {
	if len(text) <= maxMatchTextBytes {
		return strings.ToValidUTF8(text, "\uFFFD")
	}
	end := maxMatchTextBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return strings.ToValidUTF8(text[:end], "\uFFFD")
}

func encodedSearchSize(matches []map[string]any) int {
	data, err := json.Marshal(map[string]any{"matches": matches, "next_offset": 0, "eof": false, "truncated": false, "skipped": map[string]int{}})
	if err != nil {
		return maxEncodedData + 1
	}
	return len(data)
}

func encodedGlobSize(paths []string) int {
	data, err := json.Marshal(map[string]any{"paths": paths, "next_offset": 0, "eof": false, "truncated": false})
	if err != nil {
		return maxEncodedData + 1
	}
	return len(data)
}
