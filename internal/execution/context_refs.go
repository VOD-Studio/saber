package execution

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"rua.plus/saber/internal/devtools"
)

// DeveloperExists 只在钉住的授权根内检查普通文件或目录，不跟随符号链接。
func (e *Executor) DeveloperExists(ctx context.Context, p string) (bool, error) {
	if err := e.Check(ctx, "read_file"); err != nil {
		return false, err
	}
	dir, err := e.WorkspaceForContext(ctx)
	if err != nil {
		return false, err
	}
	if filepath.IsAbs(p) {
		p, err = filepath.Rel(dir, p)
		if err != nil {
			return false, err
		}
		p = filepath.ToSlash(p)
	}
	if p == "." {
		return true, nil
	}
	if err = validatePath(p, false); err != nil {
		return false, err
	}
	root, err := openWorkspaceRoot(dir)
	if err != nil {
		return false, err
	}
	defer func() { _ = root.close() }()
	if err = root.checkParents(p); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	info, err := root.root.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("reference marker is a symbolic link")
	}
	return info.IsDir() || info.Mode().IsRegular(), nil
}

// ExpandReferences 接入纯 Go 解析、授权文件/目录/固定 Git 参数以及宿主安全 URL 抓取。
func (e *Executor) ExpandReferences(ctx context.Context, message string, contextTokens int, urlReader func(context.Context, string, int) (string, error)) (devtools.ReferenceResult, error) {
	if e.devtools == nil {
		return devtools.ReferenceResult{Message: message}, nil
	}
	return e.devtools.ExpandReferences(ctx, message, devtools.ReferenceSource{Read: e.referenceRead, Folder: e.referenceFolder, Git: e.referenceGit, URL: urlReader}, contextTokens)
}

func (e *Executor) referenceRead(ctx context.Context, p string, start, end, maxBytes int) (string, error) {
	if err := e.Check(ctx, "read_file"); err != nil {
		return "", err
	}
	data, err := e.DeveloperRead(ctx, p)
	if err != nil {
		return "", err
	}
	if e.devtools != nil && e.devtools.IsDocument(p) {
		dir, _ := e.WorkspaceForContext(ctx)
		extraction, err := e.devtools.Extract(ctx, dir, p, data)
		if err != nil {
			return "", err
		}
		data = []byte(extraction.Content)
	} else if err = validateText(data); err != nil {
		return "", err
	}
	if start > 0 {
		reader := bufio.NewReader(strings.NewReader(string(data)))
		var output strings.Builder
		line := 1
		for {
			chunk, readErr := reader.ReadString('\n')
			if line >= start && (end == 0 || line <= end) {
				output.WriteString(chunk)
				if output.Len() > maxBytes {
					return "", errors.New("file reference exceeds context byte limit")
				}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					return "", readErr
				}
				break
			}
			line++
			if end > 0 && line > end {
				break
			}
		}
		return output.String(), nil
	}
	if len(data) > maxBytes {
		return "", errors.New("file reference exceeds context byte limit; select a line range")
	}
	return string(data), nil
}

func (e *Executor) referenceFolder(ctx context.Context, p string, maxEntries, maxBytes int) (string, error) {
	if err := e.Check(ctx, "read_file"); err != nil {
		return "", err
	}
	if err := validatePath(p, true); err != nil {
		return "", err
	}
	dir, err := e.WorkspaceForContext(ctx)
	if err != nil {
		return "", err
	}
	root, err := openWorkspaceRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.close() }()
	folder, err := root.openDir(p)
	if err != nil {
		return "", err
	}
	_ = folder.Close()
	var output strings.Builder
	count := 0
	err = fs.WalkDir(root.root.FS(), p, func(target string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if target == p {
			return nil
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("folder reference refuses symbolic link %s", target)
		}
		count++
		if count > maxEntries {
			return errors.New("folder reference exceeds entry limit; select a subdirectory")
		}
		fmt.Fprintln(&output, target)
		if !entry.IsDir() {
			text, err := e.referenceRead(ctx, target, 0, 0, maxBytes-output.Len())
			if err != nil {
				return err
			}
			output.WriteString(text)
			output.WriteByte('\n')
		}
		if output.Len() > maxBytes {
			return errors.New("folder reference exceeds context byte limit; select a subdirectory")
		}
		return nil
	})
	return output.String(), err
}

func (e *Executor) referenceGit(ctx context.Context, args []string, maxBytes int) (string, error) {
	if err := e.Check(ctx, "read_file"); err != nil {
		return "", err
	}
	command := append([]string{"git", "-c", "core.hooksPath=/dev/null", "-c", "color.ui=false"}, args...)
	process, err := e.StartProgram(ctx, command, map[string]string{"GIT_PAGER": "cat", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_TERMINAL_PROMPT": "0"})
	if err != nil {
		return "", err
	}
	defer func() { _ = process.Close() }()
	if err = process.CloseWrite(); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(process, int64(maxBytes)+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxBytes {
		return "", errors.New("git reference exceeds context byte limit; narrow the diff or commit count")
	}
	if err = process.Wait(); err != nil {
		return "", err
	}
	return string(data), nil
}
