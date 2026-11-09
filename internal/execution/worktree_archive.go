package execution

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

type worktreeFile struct {
	SHA256 string `json:"sha256,omitempty"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Link   string `json:"link,omitempty"`
}
type worktreeChange struct {
	Path   string        `json:"path"`
	Before *worktreeFile `json:"before,omitempty"`
	After  *worktreeFile `json:"after,omitempty"`
}

func captureWorktree(ctx context.Context, dir string) (map[string]worktreeFile, error) {
	root, err := openWorkspaceRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.close() }()
	result := map[string]worktreeFile{}
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
		if p == ".git" {
			return fs.SkipDir
		}
		if len(result) >= 100000 {
			return errors.New("worktree exceeds 100000 entries")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		file := worktreeFile{Mode: uint32(info.Mode().Perm())}
		if info.Mode()&os.ModeSymlink != 0 {
			file.Kind = "symlink"
			file.Link, err = root.root.Readlink(p)
			if err != nil {
				return err
			}
		} else if info.IsDir() {
			file.Kind = "directory"
		} else {
			input, err := root.openFile(p, os.O_RDONLY, 0)
			if err != nil {
				return err
			}
			digest := sha256.New()
			_, readErr := io.Copy(digest, contextFileReader{ctx: ctx, reader: input})
			readErr = errors.Join(readErr, input.Close())
			if readErr != nil {
				return readErr
			}
			file.Kind = "file"
			file.SHA256 = hex.EncodeToString(digest.Sum(nil))
		}
		result[p] = file
		return nil
	})
	return result, err
}

func (e *Executor) archiveWorktree(ctx context.Context, d *derivedWorkspace) error {
	if d.initial == nil {
		return nil
	}
	after, err := captureWorktree(ctx, d.path)
	if err != nil {
		return err
	}
	paths := map[string]bool{}
	for p := range d.initial {
		paths[p] = true
	}
	for p := range after {
		paths[p] = true
	}
	ordered := make([]string, 0, len(paths))
	for p := range paths {
		ordered = append(ordered, p)
	}
	sort.Strings(ordered)
	changes := []worktreeChange{}
	for _, p := range ordered {
		before, bok := d.initial[p]
		next, nok := after[p]
		if bok == nok && before == next {
			continue
		}
		change := worktreeChange{Path: p}
		if bok {
			copy := before
			change.Before = &copy
		}
		if nok {
			copy := next
			change.After = &copy
		}
		changes = append(changes, change)
	}
	if len(changes) == 0 {
		return nil
	}
	manifest, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(manifest)
	taskID := d.ownerID
	if taskID <= 0 {
		return errors.New("worktree archive has no persistent owner")
	}
	dir := e.ArtifactDir(taskID)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".worktree-delivery-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if err = temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return err
	}
	archive := zip.NewWriter(temp)
	header, err := archive.Create("manifest.json")
	if err == nil {
		_, err = header.Write(manifest)
	}
	if err != nil {
		_ = archive.Close()
		_ = temp.Close()
		return err
	}
	root, err := openWorkspaceRoot(d.path)
	if err != nil {
		_ = archive.Close()
		_ = temp.Close()
		return err
	}
	defer func() { _ = root.close() }()
	total := len(manifest)
	for _, change := range changes {
		if change.After == nil || change.After.Kind != "file" {
			continue
		}
		file, err := root.openFile(change.Path, os.O_RDONLY, 0)
		if err != nil {
			_ = archive.Close()
			_ = temp.Close()
			return err
		}
		entry, err := archive.Create("files/" + change.Path)
		if err != nil {
			_ = file.Close()
			_ = archive.Close()
			_ = temp.Close()
			return err
		}
		hash := sha256.New()
		written, err := io.Copy(io.MultiWriter(entry, hash), io.LimitReader(contextFileReader{ctx: ctx, reader: file}, int64(maxDeliverBytes-total)+1))
		err = errors.Join(err, file.Close())
		total += int(written)
		if err == nil && total > maxDeliverBytes {
			err = errors.New("isolated workspace changes exceed 16 MiB delivery limit; preserve worktree for manual collection")
		}
		if err == nil && hex.EncodeToString(hash.Sum(nil)) != change.After.SHA256 {
			err = errors.New("worktree changed during delivery; refusing to remove unarchived changes")
		}
		if err != nil {
			_ = archive.Close()
			_ = temp.Close()
			return err
		}
	}
	if err = archive.Close(); err == nil {
		err = temp.Sync()
	}
	err = errors.Join(err, temp.Close())
	if err != nil {
		return err
	}
	name := hex.EncodeToString(digest[:16]) + "-worktree-changes.zip"
	if err = os.Link(temp.Name(), filepath.Join(dir, name)); errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}
