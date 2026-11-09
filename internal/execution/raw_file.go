package execution

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type workspaceSyncedKey struct{}

// ReadAuthorizedFile 读取授权工作区原始字节，用于文档、图像和资源上传，不受平台文件交付能力影响。
func (e *Executor) ReadAuthorizedFile(ctx context.Context, p string, maxBytes int) ([]byte, error) {
	if err := e.Check(ctx, "read_file"); err != nil {
		return nil, err
	}
	if maxBytes < 1 || maxBytes > 64<<20 {
		return nil, errors.New("raw file read limit must be 1..67108864 bytes")
	}
	dir, err := e.WorkspaceForContext(ctx)
	if err != nil {
		return nil, err
	}
	if filepath.IsAbs(p) {
		p, err = filepath.Rel(dir, p)
		if err != nil {
			return nil, err
		}
		p = filepath.ToSlash(p)
	}
	if err = validatePath(p, false); err != nil {
		return nil, err
	}
	id, _, _ := TaskScope(ctx)
	if ctx.Value(workspaceSyncedKey{}) != true {
		if remote := e.currentRemote(scope{dir: dir, id: id}); remote != nil {
			if err = remote.syncWorkspace(ctx); err != nil {
				return nil, err
			}
		}
	}
	root, err := openWorkspaceRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.close() }()
	file, err := root.openFile(p, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > int64(maxBytes) {
		return nil, errors.New("file exceeds requested raw-byte limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, errors.New("file grew beyond requested raw-byte limit")
	}
	return data, nil
}
