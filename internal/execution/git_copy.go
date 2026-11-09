package execution

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func gitObjectID(text string) bool {
	return (len(text) == 40 || len(text) == 64) && strings.Trim(text, "0123456789abcdef") == ""
}

func readGitHead(root *workspaceRoot) (string, error) {
	data, exists, err := readCurrentContent(root, ".git/HEAD")
	if err != nil {
		return "", err
	}
	if !exists {
		return "", errors.New("workspace root must contain a regular .git directory")
	}
	head := strings.TrimSpace(string(data))
	for range 16 {
		if gitObjectID(head) {
			return head, nil
		}
		if !strings.HasPrefix(head, "ref: refs/") {
			return "", errors.New("repository HEAD is not a supported object or reference")
		}
		ref := strings.TrimPrefix(head, "ref: ")
		if err := validatePath(ref, false); err != nil {
			return "", err
		}
		data, exists, err = readCurrentContent(root, ".git/"+ref)
		if err != nil {
			return "", err
		}
		if exists {
			head = strings.TrimSpace(string(data))
			continue
		}
		packed, exists, err := readCurrentContent(root, ".git/packed-refs")
		if err != nil {
			return "", err
		}
		if !exists {
			return "", errors.New("git HEAD reference does not exist")
		}
		scanner := bufio.NewScanner(strings.NewReader(string(packed)))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) == 2 && fields[1] == ref && gitObjectID(fields[0]) {
				return fields[0], nil
			}
		}
		if err = scanner.Err(); err != nil {
			return "", err
		}
		return "", errors.New("git HEAD reference was not found in packed refs")
	}
	return "", errors.New("git HEAD symbolic reference depth exceeds 16")
}

type contextFileReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextFileReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func materializeGitCopy(ctx context.Context, source, dir string) error {
	root, err := openWorkspaceRoot(source)
	if err != nil {
		return err
	}
	defer func() { _ = root.close() }()
	gitDir, err := root.openDir(".git")
	if err != nil {
		return fmt.Errorf("isolated Git copy requires .git within the authorized workspace (linked external gitdirs are refused): %w", err)
	}
	_ = gitDir.Close()
	head, err := readGitHead(root)
	if err != nil {
		return err
	}
	alternates, exists, err := readCurrentContent(root, ".git/objects/info/alternates")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if exists && len(strings.TrimSpace(string(alternates))) > 0 {
		return errors.New("isolated Git copy refuses external object alternates")
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	}
	format := "sha1"
	if len(head) == 64 {
		format = "sha256"
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1"}
	init := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "init", "--template=", "--object-format="+format, dir)
	init.Env = env
	output, err := init.CombinedOutput()
	if err != nil {
		return fmt.Errorf("initialize clean isolated repository: %w: %s", err, output)
	}
	buffer := make([]byte, 32<<10)
	err = fs.WalkDir(root.root.FS(), ".git/objects", func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("git object store contains symbolic link %s", p)
		}
		if p == ".git/objects/info/alternates" {
			return nil
		}
		destination := filepath.Join(dir, filepath.FromSlash(p))
		if entry.IsDir() {
			return os.MkdirAll(destination, 0700)
		}
		input, err := root.openFile(p, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		defer func() { _ = input.Close() }()
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0444)
		if err != nil {
			return err
		}
		_, err = io.CopyBuffer(file, contextFileReader{ctx: ctx, reader: input}, buffer)
		return errors.Join(err, file.Close())
	})
	if err != nil {
		return err
	}
	if data, exists, err := readCurrentContent(root, ".git/shallow"); err != nil {
		return err
	} else if exists {
		if err = os.WriteFile(filepath.Join(dir, ".git", "shallow"), data, 0600); err != nil {
			return err
		}
	}
	if err = os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte(head+"\n"), 0600); err != nil {
		return err
	}
	checkout := exec.CommandContext(ctx, "git", "-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.protectHFS=true", "-c", "core.protectNTFS=true", "reset", "--hard", "HEAD")
	checkout.Env = env
	output, err = checkout.CombinedOutput()
	if err != nil {
		return fmt.Errorf("checkout isolated repository without hooks or filters: %w: %s", err, output)
	}
	return nil
}

// PrepareBatchWorkspace 为已持久化的根批任务创建安全副本；任务编号只能来自宿主创建回调。
func (e *Executor) PrepareBatchWorkspace(ctx context.Context, id int64) (string, func(context.Context) error, error) {
	if id <= 0 {
		return "", nil, errors.New("batch workspace requires a persistent task ID")
	}
	dir, err := e.WorkspaceForContext(ctx)
	if err != nil {
		return "", nil, err
	}
	return e.PrepareSubagentWorkspace(WithTask(ctx, id, dir), dir)
}
