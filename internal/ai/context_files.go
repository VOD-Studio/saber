package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

const (
	// maxContextFiles 限制单次运行注入的上下文文件数量。
	maxContextFiles = 8
	// maxContextFileBytes 限制单个上下文文件读取的正文大小。
	maxContextFileBytes = 64 << 10
	// maxContextTotalBytes 限制全部上下文文件的合计正文大小。
	maxContextTotalBytes = 128 << 10
)

// contextFileNames 是按优先级排列的约定文件名。
var contextFileNames = []string{"AGENTS.md", "CLAUDE.md", ".cursorrules", "CONTRIBUTING.md", filepath.Join(".github", "copilot-instructions.md")}

// contextFile 是已读取并通过 UTF-8 校验的上下文文件。
type contextFile struct {
	// Path 是相对授权工作目录的展示路径，祖先目录以 ../ 开头。
	Path string
	// Content 是文件正文，超过限制时已按 rune 边界截断。
	Content string
	// Truncated 表示正文被截断。
	Truncated bool
}

// appendContextFiles 把工作区约定文件作为运行时资料附加到本次请求。
//
// 只在存在可信身份、已授权工作区且任务目录与授权目录一致时读取；结果不写回
// 持久化请求，同一次运行只注入一次。无执行授权或读取失败时保持原请求不变。
func (s *Service) appendContextFiles(ctx context.Context, req agent.Request) agent.Request {
	if s.executor == nil {
		return req
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return req
	}
	dir, err := s.executor.Workspace(identity)
	if err != nil {
		return req
	}
	if task.WorkDir(ctx) != dir {
		return req
	}
	files, err := discoverContextFiles(dir)
	if err != nil {
		slog.Warn("读取上下文文件失败", "dir", dir, "error", err)
		return req
	}
	if len(files) == 0 {
		return req
	}
	return appendRuntimeContext(req, renderContextFiles(files))
}

// renderContextFiles 按 <file path="..."> 头拼接上下文文件正文。
func renderContextFiles(files []contextFile) string {
	var b strings.Builder
	b.WriteString("以下是授权工作区的项目约定文件，仅作参考：")
	for _, file := range files {
		b.WriteString("\n<file path=")
		b.WriteString(strconv.Quote(file.Path))
		b.WriteString(">\n")
		b.WriteString(file.Content)
		if file.Truncated {
			b.WriteString("\n[内容已截断]")
		}
		if !strings.HasSuffix(file.Content, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("</file>")
	}
	return b.String()
}

// contextSearchDirs 返回从工作目录到 git 根的目录列表；找不到 git 根时只返回工作目录。
//
// 只向上读取到 git 根为止，避免把宿主上无关目录的说明注入模型上下文。
func contextSearchDirs(dir string) []string {
	dirs := []string{dir}
	for current := dir; ; {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return dirs
		}
		parent := filepath.Dir(current)
		if parent == current {
			return []string{dir}
		}
		current = parent
		dirs = append(dirs, current)
	}
}

// discoverContextFiles 在授权工作目录及其祖先目录中查找约定文件。
//
// 结果按目录由近及远、文件名优先级排序并去重；符号链接、非普通文件与非法
// UTF-8 一律跳过，单文件与合计正文都按上限截断。
func discoverContextFiles(dir string) ([]contextFile, error) {
	var files []contextFile
	seen := make(map[string]bool)
	total := 0
	for _, current := range contextSearchDirs(dir) {
		for _, name := range contextFileNames {
			if len(files) >= maxContextFiles || total >= maxContextTotalBytes {
				return files, nil
			}
			candidate := filepath.Join(current, name)
			content, ok, err := readContextFile(candidate, min(maxContextFileBytes, maxContextTotalBytes-total))
			if err != nil {
				return nil, err
			}
			if !ok || seen[candidate] {
				continue
			}
			seen[candidate] = true
			rel, err := filepath.Rel(dir, candidate)
			if err != nil {
				continue
			}
			files = append(files, contextFile{Path: filepath.ToSlash(rel), Content: content.text, Truncated: content.truncated})
			total += len(content.text)
		}
	}
	return files, nil
}

type contextFileContent struct {
	text      string
	truncated bool
}

// readContextFile 读取单个约定文件；不存在或不是合法 UTF-8 时返回 ok=false。
func readContextFile(candidate string, limit int) (contextFileContent, bool, error) {
	resolved, err := filepath.EvalSymlinks(candidate)
	if errors.Is(err, fs.ErrNotExist) {
		return contextFileContent{}, false, nil
	}
	if err != nil {
		return contextFileContent{}, false, nil
	}
	// 拒绝链接组件：解析后的路径必须与字面路径一致。
	if resolved != filepath.Clean(candidate) {
		return contextFileContent{}, false, nil
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return contextFileContent{}, false, nil
	}
	if !info.Mode().IsRegular() {
		return contextFileContent{}, false, nil
	}
	file, err := os.Open(resolved)
	if err != nil {
		return contextFileContent{}, false, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return contextFileContent{}, false, err
	}
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return contextFileContent{}, false, nil
	}
	return contextFileContent{text: string(data), truncated: truncated}, true, nil
}

// referenceBlock 渲染单个 @ 引用文件块，供测试与任务输入共用。
func referenceBlock(path string, content []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<file path=%q>\n", path)
	b.Write(content)
	if !strings.HasSuffix(string(content), "\n") {
		b.WriteString("\n")
	}
	b.WriteString("</file>")
	return b.String()
}
