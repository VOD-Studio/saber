package execution

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

const maxRemoteWorkspaceBytes = 64 << 20

type syncedFile struct {
	Data      []byte
	Mode      int64
	Directory bool
}
type workspaceImage map[string]syncedFile

func sameSyncedFile(a syncedFile, aok bool, b syncedFile, bok bool) bool {
	return aok == bok && (!aok || a.Directory == b.Directory && a.Mode == b.Mode && sha256.Sum256(a.Data) == sha256.Sum256(b.Data))
}

func captureWorkspace(ctx context.Context, dir string) (workspaceImage, error) {
	root, err := openWorkspaceRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.close() }()
	result := workspaceImage{}
	total := 0
	err = fs.WalkDir(root.root.FS(), ".", func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if len(result) >= 20000 {
			return errors.New("remote workspace exceeds 20000 entries")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("remote synchronization refuses symlink %s", p)
		}
		if entry.IsDir() {
			result[p] = syncedFile{Directory: true, Mode: 0700}
			return nil
		}
		file, err := root.openFile(p, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, int64(maxRemoteWorkspaceBytes-total)+1))
		readErr = errors.Join(readErr, file.Close())
		if readErr != nil {
			return readErr
		}
		total += len(data)
		if total > maxRemoteWorkspaceBytes {
			return errors.New("remote workspace exceeds 64 MiB")
		}
		result[p] = syncedFile{Data: data, Mode: int64(info.Mode().Perm())}
		return nil
	})
	return result, err
}

func encodeWorkspace(image workspaceImage) ([]byte, error) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	paths := make([]string, 0, len(image))
	for p := range image {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		file := image[p]
		header := &tar.Header{Name: p, Mode: file.Mode, Size: int64(len(file.Data)), Typeflag: tar.TypeReg}
		if file.Directory {
			header.Typeflag = tar.TypeDir
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			return nil, err
		}
		if !file.Directory {
			if _, err := writer.Write(file.Data); err != nil {
				return nil, err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func decodeWorkspace(data []byte) (workspaceImage, error) {
	if len(data) > maxRemoteWorkspaceBytes+(20<<20) {
		return nil, errors.New("remote workspace archive is oversized")
	}
	reader := tar.NewReader(bytes.NewReader(data))
	result := workspaceImage{}
	total := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, err
		}
		p := strings.TrimSuffix(header.Name, "/")
		p = strings.TrimPrefix(p, "./")
		if p == "." || p == "" {
			continue
		}
		if path.Clean(p) != p || strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "\\") {
			return nil, errors.New("remote archive contains an unsafe path")
		}
		if _, exists := result[p]; exists {
			return nil, errors.New("remote archive contains duplicate path")
		}
		if len(result) >= 20000 {
			return nil, errors.New("remote archive exceeds 20000 entries")
		}
		if header.Typeflag == tar.TypeDir {
			result[p] = syncedFile{Directory: true, Mode: 0700}
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("remote archive contains a non-regular entry %s", p)
		}
		if header.Size < 0 || header.Size > int64(maxRemoteWorkspaceBytes-total) {
			return nil, errors.New("remote archive exceeds 64 MiB")
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		total += len(data)
		result[p] = syncedFile{Data: data, Mode: header.Mode & 0777}
	}
}

func mergeWorkspace(base, local, remote workspaceImage) (workspaceImage, error) {
	merged := workspaceImage{}
	paths := map[string]bool{}
	for p := range base {
		paths[p] = true
	}
	for p := range local {
		paths[p] = true
	}
	for p := range remote {
		paths[p] = true
	}
	for p := range paths {
		b, bok := base[p]
		l, lok := local[p]
		r, rok := remote[p]
		lc := !sameSyncedFile(l, lok, b, bok)
		rc := !sameSyncedFile(r, rok, b, bok)
		if lc && rc && !sameSyncedFile(l, lok, r, rok) {
			return nil, fmt.Errorf("remote workspace conflict at %s: host and backend changed the same file", p)
		}
		if rc {
			if rok {
				merged[p] = r
			}
		} else if lok {
			merged[p] = l
		}
	}
	return merged, nil
}

func applyWorkspace(ctx context.Context, dir string, before, after workspaceImage) error {
	root, err := openWorkspaceRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.close() }()
	paths := make([]string, 0, len(after))
	for p := range after {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		file := after[p]
		old, exists := before[p]
		if sameSyncedFile(file, true, old, exists) {
			continue
		}
		if file.Directory {
			if err = root.root.MkdirAll(p, 0700); err != nil {
				return err
			}
			continue
		}
		if err = root.root.MkdirAll(path.Dir(p), 0700); err != nil {
			return err
		}
		if err = writeWorkspaceFile(root, p, file.Data); err != nil {
			return err
		}
		if err = root.root.Chmod(p, os.FileMode(file.Mode)&0777); err != nil {
			return err
		}
	}
	deleted := []string{}
	for p := range before {
		if _, ok := after[p]; !ok {
			deleted = append(deleted, p)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(deleted)))
	for _, p := range deleted {
		if err = root.checkParents(p); err != nil {
			return err
		}
		if err = root.root.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

const remoteExtractScript = `import base64,io,os,sys,tarfile
root=sys.argv[1]
archive=tarfile.open(fileobj=io.BytesIO(base64.b64decode(sys.stdin.buffer.read())),mode="r:")
for member in archive.getmembers():
 p=member.name
 if not p or p.startswith("/") or ".." in p.split("/") or not (member.isdir() or member.isfile()): raise RuntimeError("unsafe archive member")
archive.extractall(root,filter="data")
`

func (e *remoteEnvironment) syncWorkspace(ctx context.Context) error {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()
	local, err := captureWorkspace(ctx, e.spec.Workspace)
	if err != nil {
		return err
	}
	remote := workspaceImage{}
	if e.baseline != nil {
		archive := e.root + "/workspace-export.tar"
		_, code, err := e.transport.Execute(ctx, "tar cf "+quoteShell(archive)+" -C "+quoteShell(e.workspace)+" .", nil)
		if err != nil || code != 0 {
			return errors.Join(err, exitError(code))
		}
		data, err := e.transport.Get(ctx, archive)
		if err != nil {
			return err
		}
		remote, err = decodeWorkspace(data)
		if err != nil {
			return err
		}
	}
	merged := local
	if e.baseline != nil {
		merged, err = mergeWorkspace(e.baseline, local, remote)
		if err != nil {
			return err
		}
	}
	// 读取远端期间宿主若发生变化，拒绝落盘，不能覆盖外部编辑。
	current, err := captureWorkspace(ctx, e.spec.Workspace)
	if err != nil {
		return err
	}
	if len(current) != len(local) {
		return errors.New("host workspace changed during synchronization")
	}
	for p, file := range local {
		now, ok := current[p]
		if !sameSyncedFile(file, true, now, ok) {
			return fmt.Errorf("host file changed during synchronization: %s", p)
		}
	}
	if err = applyWorkspace(ctx, e.spec.Workspace, local, merged); err != nil {
		return err
	}
	data, err := encodeWorkspace(merged)
	if err != nil {
		return err
	}
	if _, code, err := e.transport.Execute(ctx, "python3 -I -c "+quoteShell(remoteExtractScript)+" "+quoteShell(e.workspace), []byte(base64.StdEncoding.EncodeToString(data))); err != nil || code != 0 {
		return errors.Join(err, exitError(code))
	}
	// 远端删除只涉及我们已跟踪的普通文件，绝不扫描或删除沙箱其他路径。
	deleted := []string{}
	for p := range remote {
		if _, ok := merged[p]; !ok {
			deleted = append(deleted, p)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(deleted)))
	for _, p := range deleted {
		if _, code, err := e.transport.Execute(ctx, "rm -d -- "+quoteShell(path.Join(e.workspace, p)), nil); err != nil || code != 0 {
			return errors.Join(err, exitError(code))
		}
	}
	e.baseline = merged
	return nil
}
