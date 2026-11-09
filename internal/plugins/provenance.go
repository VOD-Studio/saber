package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func packageFiles(dir string) ([]string, error) {
	out := []string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("plugin provenance rejects symlinks")
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}
func digestFiles(dir string, files []string) (string, error) {
	h := sha256.New()
	for _, rel := range files {
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(filepath.Clean(rel), ".."+string(filepath.Separator)) {
			return "", errors.New("plugin provenance path escapes package")
		}
		path := filepath.Join(dir, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("plugin provenance file not regular")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		_, _ = h.Write([]byte(rel + "\x00"))
		_, _ = h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func preserveLocalFiles(oldDir, newDir string, codeFiles []string) error {
	code := map[string]bool{}
	for _, file := range codeFiles {
		code[filepath.Clean(file)] = true
	}
	return filepath.WalkDir(oldDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(oldDir, path)
		if err != nil {
			return err
		}
		if code[rel] {
			return nil
		}
		target := filepath.Join(newDir, rel)
		if _, err := os.Lstat(target); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("plugin local state contains symlink; review it before updating")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("plugin local state is not a regular file")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm()&0755)
	})
}
func (m *Manager) collision(manifest Manifest) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for name, other := range m.manifests {
		if name != manifest.Name && pluginPermission(name) == pluginPermission(manifest.Name) {
			return errors.New("plugin namespace collides after safe-name normalization")
		}
		if name == manifest.Name {
			continue
		}
		for _, candidate := range other.definitions() {
			for _, tool := range manifest.definitions() {
				if tool.Tool.Function.Name == candidate.Tool.Function.Name {
					return errors.New("plugin tool collides with another installed plugin")
				}
			}
		}
	}
	for _, tool := range manifest.definitions() {
		if m.reserved[tool.Tool.Function.Name] {
			return fmt.Errorf("plugin tool %s collides with a builtin", tool.Tool.Function.Name)
		}
	}
	return nil
}

// SetReservedTools 拒绝已安装及后续更新的插件覆盖宿主内置工具；失败不会悄悄改派发顺序。
func (m *Manager) SetReservedTools(names []string) error {
	m.changeMu.Lock()
	defer m.changeMu.Unlock()
	reserved := map[string]bool{}
	for _, name := range names {
		reserved[name] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, manifest := range m.manifests {
		for _, tool := range manifest.definitions() {
			if reserved[tool.Tool.Function.Name] {
				return fmt.Errorf("plugin tool %s collides with a builtin", tool.Tool.Function.Name)
			}
		}
	}
	for _, name := range []string{"plugin_manage", "manage_connections", "connector_search", "connector_describe", "connector_call"} {
		if reserved[name] {
			return fmt.Errorf("plugin management tool %s collides with builtin", name)
		}
	}
	m.reserved = reserved
	return nil
}

// InspectFiles 给可信管理员展示当前批准文件和修改状态，不执行插件代码。
func (m *Manager) InspectFiles(ctx context.Context, name string) (map[string]any, error) {
	if err := m.admin(ctx); err != nil {
		return nil, err
	}
	m.mu.RLock()
	record, ok := m.installed[name]
	m.mu.RUnlock()
	if !ok {
		return nil, errors.New("plugin not found")
	}
	digest, err := digestFiles(filepath.Join(m.dir, name), record.CodeFiles)
	if err != nil {
		return nil, err
	}
	return map[string]any{"approved_files": record.CodeFiles, "approved_digest": record.Digest, "current_digest": digest, "modified": digest != record.Digest}, nil
}
