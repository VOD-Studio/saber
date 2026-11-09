package execution

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"unicode/utf8"

	"rua.plus/saber/internal/chat"
)

const (
	// maxReferenceFileBytes 限制单个 @ 引用读取的正文大小。
	maxReferenceFileBytes = 32 << 10
)

// ErrReferenceNotFound 表示 @ 引用的路径在工作区中不存在；调用方应保持原文不变。
var ErrReferenceNotFound = errors.New("reference does not exist")

// Reference 是工作区内单个 @ 引用文件的只读内容。
type Reference struct {
	// Path 是相对工作区的规范化路径。
	Path string
	// Content 是文件正文，已确认是合法 UTF-8 且不含 NUL。
	Content []byte
}

// ReadReference 读取工作区内单个 @ 引用文件。
//
// 权限与 read_file 使用同一份策略：身份、授权工作区与任务目录都必须匹配；
// 路径不得逃逸工作区，目录、符号链接、二进制与超过 32 KiB 的文件一律拒绝。
func (e *Executor) ReadReference(ctx context.Context, p string) (Reference, error) {
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return Reference{}, errors.New("reference requires trusted identity")
	}
	dir, err := e.Workspace(identity)
	if err != nil {
		return Reference{}, err
	}
	if run, ok := ctx.Value(scopeKey{}).(scope); ok {
		if !e.authorizedDirectory(identity, run, dir) {
			return Reference{}, errors.New("reference workspace does not match current task")
		}
		dir = run.dir
	} else {
		ctx = WithTask(ctx, 0, dir)
	}
	if err = e.Check(ctx, "read_file"); err != nil {
		return Reference{}, err
	}
	clean, err := cleanSnapshotPath(p)
	if err != nil {
		return Reference{}, err
	}
	root, err := openWorkspaceRoot(dir)
	if err != nil {
		return Reference{}, err
	}
	defer func() { _ = root.close() }()
	file, err := root.openFile(clean, os.O_RDONLY, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return Reference{}, ErrReferenceNotFound
	}
	if err != nil {
		return Reference{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return Reference{}, err
	}
	if info.Size() > maxReferenceFileBytes {
		return Reference{}, errors.New("reference exceeds 32 KiB limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxReferenceFileBytes+1))
	if err != nil {
		return Reference{}, err
	}
	if len(data) > maxReferenceFileBytes {
		return Reference{}, errors.New("reference exceeds 32 KiB limit")
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return Reference{}, errors.New("reference is binary")
	}
	if !utf8.Valid(data) {
		return Reference{}, errors.New("reference is not valid UTF-8")
	}
	return Reference{Path: clean, Content: data}, nil
}
