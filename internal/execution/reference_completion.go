package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"rua.plus/saber/internal/devtools"
)

// DeveloperComplete 只列出授权目录中匹配输入前缀的真实文件和目录，不跟随链接。
func (e *Executor) DeveloperComplete(ctx context.Context, kind, target string, maxEntries int) ([]devtools.ReferenceCompletion, error) {
	if err := e.Check(ctx, "read_file"); err != nil {
		return nil, err
	}
	if kind != "file" && kind != "folder" {
		return nil, errors.New("unsupported reference completion kind")
	}
	if maxEntries < 1 || maxEntries > 1000 {
		return nil, errors.New("completion limit must be 1..1000")
	}
	if strings.ContainsAny(target, "\\\x00") || strings.HasPrefix(target, "/") {
		return nil, errors.New("completion path must be workspace-relative")
	}
	parent, prefix := path.Split(target)
	parent = strings.TrimSuffix(parent, "/")
	if parent == "" {
		parent = "."
	}
	if err := validatePath(parent, true); err != nil {
		return nil, err
	}
	dir, err := e.WorkspaceForContext(ctx)
	if err != nil {
		return nil, err
	}
	root, err := openWorkspaceRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.close() }()
	folder, err := root.openDir(parent)
	if err != nil {
		return nil, err
	}
	defer func() { _ = folder.Close() }()
	entries, err := folder.ReadDir(listMaxEntries + 1)
	if err != nil {
		return nil, err
	}
	if len(entries) > listMaxEntries {
		return nil, errors.New("completion directory exceeds 10000 entries; use a narrower directory")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]devtools.ReferenceCompletion, 0, min(maxEntries, len(entries)))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if kind == "folder" && !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			continue
		}
		value := path.Join(parent, name)
		if info.IsDir() {
			value += "/"
		}
		result = append(result, devtools.ReferenceCompletion{Value: value, Label: fmt.Sprintf("%s (%d bytes)", name, info.Size()), Directory: info.IsDir(), Size: info.Size()})
		if len(result) >= maxEntries {
			break
		}
	}
	return result, nil
}
