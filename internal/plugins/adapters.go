package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// BrowserSource 创建真实插件浏览器并返回秘密 CDP 地址，释放回调仅关闭同一 session。
func (m *Manager) BrowserSource(ctx context.Context, provider, session string) (string, func() error, error) {
	result, err := m.ProviderCall(ctx, provider, "browser", "create", map[string]any{"session_id": session})
	if err != nil {
		return "", nil, err
	}
	object, ok := result.(map[string]any)
	if !ok {
		return "", nil, errors.New("browser plugin result must be an object")
	}
	endpoint, _ := object["cdp_url"].(string)
	created, _ := object["session_id"].(string)
	if endpoint == "" || created == "" {
		return "", nil, errors.New("browser plugin must return cdp_url and session_id")
	}
	var once sync.Once
	var closeErr error
	release := func() error {
		once.Do(func() {
			_, closeErr = m.ProviderCall(context.WithoutCancel(ctx), provider, "browser", "close", map[string]any{"session_id": created})
		})
		return closeErr
	}
	return endpoint, release, nil
}

// ReadSkill 只读取启用插件 manifest 明确附属技能的相对资源，路径和软链接仍受包边界保护。
func (m *Manager) ReadSkill(ctx context.Context, plugin, skill, resource string) ([]byte, error) {
	manifest, err := m.authorizeRunningPlugin(ctx, plugin)
	if err != nil {
		return nil, err
	}
	root, ok := manifest.Skills[skill]
	if !ok {
		return nil, errors.New("plugin skill not declared")
	}
	packageDir := filepath.Join(m.dir, plugin)
	if resource == "" {
		resource = "SKILL.md"
	}
	if filepath.IsAbs(resource) {
		return nil, errors.New("plugin skill resource must be relative")
	}
	path := filepath.Join(packageDir, root, resource)
	relative, err := filepath.Rel(packageDir, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("plugin skill path escapes package")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	relative, err = filepath.Rel(packageDir, canonical)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("plugin skill symlink escapes package")
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, errors.New("plugin skill resource exceeds size or type limit")
	}
	return os.ReadFile(canonical)
}

// SkillCatalog 返回已启用插件的技能标识，消费者通过 ReadSkill 读取真实包资源。
func (m *Manager) SkillCatalog(ctx context.Context) map[string][]string {
	m.mu.RLock()
	names := make([]string, 0, len(m.running))
	for name := range m.running {
		names = append(names, name)
	}
	m.mu.RUnlock()
	out := map[string][]string{}
	for _, name := range names {
		if m.authorize(ctx, name, false) != nil {
			continue
		}
		m.mu.RLock()
		for skill := range m.manifests[name].Skills {
			out[name] = append(out[name], skill)
		}
		m.mu.RUnlock()
	}
	return out
}
