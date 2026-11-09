package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
	"rua.plus/saber/internal/vault"
)

type installation struct {
	Source    string   `json:"source"`
	Revision  string   `json:"revision"`
	Digest    string   `json:"digest"`
	CodeFiles []string `json:"code_files"`
	Enabled   bool     `json:"enabled"`
	Version   string   `json:"version"`
	Error     string   `json:"error,omitempty"`
}

// Manager 是插件进程、安装账本和连接器的唯一运行时所有者。
type Manager struct {
	cfg        config.PluginsConfig
	host       toolkit.Host
	dir        string
	mu         sync.RWMutex
	changeMu   sync.Mutex
	installed  map[string]installation
	manifests  map[string]Manifest
	running    map[string]*process
	reserved   map[string]bool
	closed     bool
	connectors *connectorManager
}

// New 发现用户插件，但只启动配置或账本中明确启用且未被禁用的版本。
func New(cfg config.PluginsConfig, host toolkit.Host) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	dir := cfg.Directory
	if dir == "" {
		dir = filepath.Join(host.StateDir, "plugins")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(host.StateDir, dir)
	}
	if host.StateDir == "" && cfg.Directory == "" {
		return nil, errors.New("plugins require private state directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	m := &Manager{cfg: cfg, host: host, dir: dir, installed: map[string]installation{}, manifests: map[string]Manifest{}, running: map[string]*process{}}
	data, err := os.ReadFile(filepath.Join(dir, "installed.json"))
	if err == nil {
		if json.Unmarshal(data, &m.installed) != nil {
			return nil, errors.New("plugin installation ledger invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if cfg.Enabled {
		if err := m.discover(context.Background()); err != nil {
			_ = m.Close()
			return nil, err
		}
	}
	m.connectors = newConnectorManager(cfg, host)
	return m, nil
}
func (m *Manager) saveLocked() error {
	data, err := json.MarshalIndent(m.installed, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(m.dir, "installed.json"), data)
}
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".plugin-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func (m *Manager) discover(ctx context.Context) error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		name := entry.Name()
		if !slugPattern.MatchString(name) {
			continue
		}
		dir := filepath.Join(m.dir, name)
		manifest, err := readManifest(dir)
		if err != nil {
			m.installed[name] = installation{Error: err.Error()}
			continue
		}
		if manifest.Name != name {
			m.installed[name] = installation{Error: "plugin directory/manifest name mismatch"}
			continue
		}
		if _, err := Scan(dir); err != nil {
			m.installed[name] = installation{Error: err.Error()}
			continue
		}
		if err := m.collision(manifest); err != nil {
			m.installed[name] = installation{Error: err.Error()}
			continue
		}
		record := m.installed[name]
		if len(record.CodeFiles) == 0 {
			record.CodeFiles, err = packageFiles(dir)
			if err != nil {
				return err
			}
		}
		digest, err := digestFiles(dir, record.CodeFiles)
		if err != nil {
			record.Enabled = false
			record.Error = "approved plugin files unavailable: " + err.Error()
		}
		if record.Digest != "" && record.Digest != digest {
			record.Error = "approved code differs from installation digest; review and explicitly enable or reinstall"
			record.Enabled = false
		}
		if record.Digest == "" {
			record.Digest = digest
		}
		record.Version = manifest.Version
		if contains(m.cfg.EnabledPlugins, name) && record.Error == "" {
			record.Enabled = true
		}
		if contains(m.cfg.DisabledPlugins, name) {
			record.Enabled = false
		}
		m.installed[name] = record
		m.manifests[name] = manifest
	}
	names := make([]string, 0, len(m.manifests))
	for name := range m.manifests {
		names = append(names, name)
	}
	sort.Strings(names)
	visiting, loaded := map[string]bool{}, map[string]bool{}
	var load func(string) error
	load = func(name string) error {
		if loaded[name] {
			return nil
		}
		if visiting[name] {
			return errors.New("plugin dependency cycle")
		}
		visiting[name] = true
		manifest := m.manifests[name]
		for dependency, version := range manifest.Dependencies {
			dep, ok := m.manifests[dependency]
			if !ok || dep.Version != version || !m.installed[dependency].Enabled {
				return fmt.Errorf("plugin dependency %s requires enabled exact version %s", dependency, version)
			}
			if err := load(dependency); err != nil {
				return err
			}
		}
		if m.installed[name].Enabled {
			p, err := startProcess(ctx, manifest, filepath.Join(m.dir, name), m.cfg, m.host)
			if err != nil {
				return err
			}
			m.running[name] = p
		}
		visiting[name] = false
		loaded[name] = true
		return nil
	}
	for _, name := range names {
		if !m.installed[name].Enabled {
			continue
		}
		if err := load(name); err != nil {
			record := m.installed[name]
			record.Error = err.Error()
			m.installed[name] = record
		}
	}
	return m.saveLocked()
}
func (m *Manager) authorize(ctx context.Context, name string, external bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return errors.New("plugin requires trusted identity")
	}
	if m.host.Check == nil {
		return errors.New("plugin authorization unavailable")
	}
	if err := m.host.Check(ctx, pluginPermission(name), external); err != nil {
		return err
	}
	return m.host.Check(ctx, "exec", false)
}

// authorizeRunningPlugin 持锁读取运行中插件 manifest，再在不持锁的情况下按声明的 external 能力授权。
// manifest 缺失时仍按外部能力保守校验并返回明确错误，避免撤销 external 后降级为内部调用。
func (m *Manager) authorizeRunningPlugin(ctx context.Context, name string) (Manifest, error) {
	m.mu.RLock()
	manifest, ok := m.manifests[name]
	active := m.running[name] != nil
	m.mu.RUnlock()
	if !ok || !active {
		if err := m.authorize(ctx, name, true); err != nil {
			return Manifest{}, err
		}
		return Manifest{}, errors.New("plugin manifest is unavailable")
	}
	if err := m.authorize(ctx, name, contains(manifest.Capabilities, "external")); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}
func (m *Manager) admin(ctx context.Context) error {
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return errors.New("plugin management requires trusted identity")
	}
	if m.host.IsAdmin == nil || !m.host.IsAdmin(ctx) {
		return errors.New("plugin management requires administrator")
	}
	if m.host.Check == nil {
		return errors.New("plugin management authorization unavailable")
	}
	if err := m.host.Check(ctx, "plugin_manage", false); err != nil {
		return err
	}
	return m.host.Check(ctx, "exec", false)
}

// Definitions 提供动态 schema 快照，权限过滤使用 AuthorizedDefinitions。
func (m *Manager) Definitions() []toolkit.Definition {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed || !m.cfg.Enabled {
		return nil
	}
	out := []toolkit.Definition{managementDefinition("plugin_manage", "Manage explicitly authorized plugin installations and activation; admin-only changes.")}
	if m.connectors != nil {
		out = append(out, m.connectors.definitions()...)
	}
	names := []string{}
	for name := range m.running {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, m.manifests[name].definitions()...)
	}
	return out
}
func managementDefinition(name, description string) toolkit.Definition {
	return toolkit.Definition{
		Tool: openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
			Name: name, Description: description,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"action": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"},
				"source": map[string]any{"type": "string"}, "revision": map[string]any{"type": "string"},
				"operation": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"},
			}},
		}}, Permission: name,
	}
}

// AuthorizedDefinitions 只公开当前调用者获准列举和执行的工具。
func (m *Manager) AuthorizedDefinitions(ctx context.Context) []toolkit.Definition {
	var out []toolkit.Definition
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return out
	}
	for _, d := range m.Definitions() {
		if !agent.ToolAllowed(ctx, d.Tool.Function.Name) || m.host.Check == nil {
			continue
		}
		if m.host.Check(ctx, d.Permission, d.External) != nil {
			continue
		}
		allowed := true
		for _, r := range d.Requires {
			if m.host.Check(ctx, r, false) != nil {
				allowed = false
				break
			}
		}
		if allowed {
			out = append(out, d)
		}
	}
	return out
}

// Tools 将实时授权结果转为模型工具，禁用后不会残留启动时 schema。
func (m *Manager) Tools(ctx context.Context) []openai.Tool {
	var tools []openai.Tool
	for _, d := range m.AuthorizedDefinitions(ctx) {
		tools = append(tools, d.Tool)
	}
	return tools
}

// Contains 判断工具是否由插件管理器拥有，不代表调用已获授权。
func (m *Manager) Contains(name string) bool {
	for _, d := range m.Definitions() {
		if d.Tool.Function.Name == name {
			return true
		}
	}
	return false
}

// Execute 每次调用复核精确工具 grant 和依赖权限，然后派发到运行中进程。
func (m *Manager) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	if !agent.ToolAllowed(ctx, name) {
		return nil, errors.New("plugin tool outside child task scope")
	}
	if name == "plugin_manage" {
		action, _ := args["action"].(string)
		plugin, _ := args["name"].(string)
		source, _ := args["source"].(string)
		revision, _ := args["revision"].(string)
		return m.manage(ctx, action, plugin, source, revision)
	}
	if strings.HasPrefix(name, "connector_") || name == "manage_connections" {
		return m.connectors.execute(ctx, name, args)
	}
	m.mu.RLock()
	var p *process
	var tool ToolDefinition
	for plugin, process := range m.running {
		for _, t := range m.manifests[plugin].Tools {
			if namespace(plugin, t.Name) == name {
				p = process
				tool = t
				break
			}
		}
	}
	m.mu.RUnlock()
	if p == nil {
		return nil, errors.New("plugin tool is unavailable")
	}
	if err := m.authorize(ctx, p.manifest.Name, tool.External); err != nil {
		return nil, err
	}
	if err := m.host.Check(ctx, name, tool.External); err != nil {
		return nil, err
	}
	for _, r := range tool.Requires {
		if err := m.host.Check(ctx, r, false); err != nil {
			return nil, err
		}
	}
	who, _ := chat.IdentityFromContext(ctx)
	call, cancel := context.WithTimeout(ctx, time.Duration(m.cfg.CallTimeoutSeconds)*time.Second)
	defer cancel()
	var result any
	if err := p.call(call, "tools.call", map[string]any{"name": tool.Name, "arguments": cleanArguments(args), "session": who.Session.Key(), "user": who.UserKey()}, &result); err != nil {
		return nil, err
	}
	return m.richResult(ctx, result, tool.External)
}
func cleanArguments(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		switch strings.ToLower(k) {
		case "identity", "sender_id", "user", "session", "token", "api_key", "provider_url", "base_url", "credentials":
			continue
		}
		out[k] = v
	}
	return out
}

// ProviderCall 调用同一运行中插件声明的真实 provider 操作，不另外创建未消费 SDK。
func (m *Manager) ProviderCall(ctx context.Context, plugin, kind, operation string, args map[string]any) (any, error) {
	m.mu.RLock()
	p := m.running[plugin]
	manifest := m.manifests[plugin]
	m.mu.RUnlock()
	if p == nil || !contains(manifest.Providers[kind], operation) {
		return nil, errors.New("plugin provider/operation unavailable or not declared")
	}
	if err := m.authorize(ctx, plugin, contains(manifest.Capabilities, "external")); err != nil {
		return nil, err
	}
	who, _ := chat.IdentityFromContext(ctx)
	call, cancel := context.WithTimeout(ctx, time.Duration(m.cfg.CallTimeoutSeconds)*time.Second)
	defer cancel()
	var result any
	err := p.call(call, "provider.call", map[string]any{"kind": kind, "operation": operation, "arguments": cleanArguments(args), "session": who.Session.Key(), "user": who.UserKey()}, &result)
	return result, err
}

// Commands 返回当前运行中插件命令；命令名强制使用插件前缀。
func (m *Manager) Commands() []CommandDefinition {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []CommandDefinition{}
	for name, p := range m.running {
		for _, cmd := range p.manifest.Commands {
			cmd.Name = name + ":" + cmd.Name
			cmd.Plugin = name
			out = append(out, cmd)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ExecuteCommand 保留可信身份和授权，命令内工具回调仍走宿主 ToolExecutor。
func (m *Manager) ExecuteCommand(ctx context.Context, name string, args map[string]any) (any, error) {
	parts := strings.SplitN(name, ":", 2)
	if len(parts) != 2 {
		return nil, errors.New("plugin command requires plugin:name")
	}
	m.mu.RLock()
	p := m.running[parts[0]]
	m.mu.RUnlock()
	if p == nil {
		return nil, errors.New("plugin command unavailable")
	}
	found := false
	for _, cmd := range p.manifest.Commands {
		if cmd.Name == parts[1] {
			found = true
		}
	}
	if !found {
		return nil, errors.New("plugin command not declared")
	}
	if _, err := m.authorizeRunningPlugin(ctx, parts[0]); err != nil {
		return nil, err
	}
	who, _ := chat.IdentityFromContext(ctx)
	call, cancel := context.WithTimeout(ctx, time.Duration(m.cfg.CallTimeoutSeconds)*time.Second)
	defer cancel()
	var result any
	err := p.call(call, "commands.call", map[string]any{"name": parts[1], "arguments": cleanArguments(args), "session": who.Session.Key(), "user": who.UserKey()}, &result)
	return result, err
}

// Hook 在宿主生命周期调用；pre_tool_call 的错误、授权失败或明确拒绝都失败关闭。
func (m *Manager) Hook(ctx context.Context, event string, args map[string]any) error {
	m.mu.RLock()
	ps := []*process{}
	for _, p := range m.running {
		if contains(p.manifest.Hooks, event) {
			ps = append(ps, p)
		}
	}
	m.mu.RUnlock()
	sort.Slice(ps, func(i, j int) bool { return ps[i].manifest.Name < ps[j].manifest.Name })
	for _, p := range ps {
		if _, err := m.authorizeRunningPlugin(ctx, p.manifest.Name); err != nil {
			return err
		}
		call, cancel := context.WithTimeout(ctx, time.Duration(m.cfg.CallTimeoutSeconds)*time.Second)
		var result struct {
			Allow *bool `json:"allow"`
		}
		err := p.call(call, "hooks.call", map[string]any{"event": event, "arguments": cleanArguments(args)}, &result)
		cancel()
		if err != nil {
			return err
		}
		if event == "pre_tool_call" && result.Allow != nil && !*result.Allow {
			return errors.New("plugin hook denied tool call")
		}
	}
	return nil
}

// AttachVault 使用同一加密 store 保存 OAuth 凭据，不让模型读取 token。
func (m *Manager) AttachVault(store *vault.Store) { m.connectors.attachVault(store) }

// Command 提供统一插件及连接器管理入口，不修改共享命令注册表。
func (m *Manager) Command(ctx context.Context, args []string) (any, error) {
	if len(args) == 0 {
		args = []string{"list"}
	}
	if args[0] == "connectors" {
		params := map[string]any{"action": "status"}
		if len(args) > 1 {
			params["action"] = args[1]
		}
		if len(args) > 2 {
			params["name"] = args[2]
		}
		if len(args) > 3 {
			params["connector"] = args[3]
		}
		return m.connectors.execute(ctx, "manage_connections", params)
	}
	if args[0] == "command" {
		if len(args) < 2 {
			return nil, errors.New("plugin command needs plugin:name")
		}
		return m.ExecuteCommand(ctx, args[1], map[string]any{"text": strings.Join(args[2:], " ")})
	}
	name, source, revision := "", "", ""
	if len(args) > 1 {
		name = args[1]
	}
	if len(args) > 2 {
		source = args[2]
	}
	if len(args) > 3 {
		revision = args[3]
	}
	if args[0] == "inspect_files" {
		return m.InspectFiles(ctx, name)
	}
	return m.manage(ctx, args[0], name, source, revision)
}
func (m *Manager) manage(ctx context.Context, action, name, source, revision string) (any, error) {
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return nil, errors.New("trusted identity required")
	}
	if !m.cfg.Enabled {
		return nil, errors.New("plugins disabled")
	}
	if action == "list" || action == "inspect" || action == "catalog" {
		m.mu.RLock()
		defer m.mu.RUnlock()
		if action == "catalog" {
			return m.cfg.Sources, nil
		}
		if action == "inspect" {
			manifest, ok := m.manifests[name]
			if !ok {
				return nil, errors.New("plugin not found")
			}
			return map[string]any{"manifest": manifest, "installation": m.installed[name]}, nil
		}
		out := map[string]installation{}
		for key, value := range m.installed {
			out[key] = value
		}
		return out, nil
	}
	if err := m.admin(ctx); err != nil {
		return nil, err
	}
	if !slugPattern.MatchString(name) {
		return nil, errors.New("plugin name invalid")
	}
	m.changeMu.Lock()
	defer m.changeMu.Unlock()
	if action == "scan" {
		return Scan(filepath.Join(m.dir, name))
	}
	if action == "install" || action == "update" {
		return m.install(ctx, name, source, revision)
	}
	if action != "enable" && action != "disable" && action != "remove" {
		return nil, errors.New("plugin action must be list/inspect/catalog/scan/install/update/enable/disable/remove")
	}
	m.mu.RLock()
	p := m.running[name]
	record, ok := m.installed[name]
	m.mu.RUnlock()
	if !ok {
		return nil, errors.New("plugin not found")
	}
	if action == "enable" {
		if contains(m.cfg.DisabledPlugins, name) {
			return nil, errors.New("plugin explicitly disabled by configuration")
		}
		manifest, err := readManifest(filepath.Join(m.dir, name))
		if err != nil {
			return nil, err
		}
		if manifest.Name != name {
			return nil, errors.New("plugin name changed")
		}
		if err := m.collision(manifest); err != nil {
			return nil, err
		}
		if err := m.authorize(ctx, name, contains(manifest.Capabilities, "external")); err != nil {
			return nil, err
		}
		if _, err := Scan(filepath.Join(m.dir, name)); err != nil {
			return nil, err
		}
		if len(record.CodeFiles) == 0 {
			record.CodeFiles, err = packageFiles(filepath.Join(m.dir, name))
			if err != nil {
				return nil, err
			}
		}
		record.Digest, err = digestFiles(filepath.Join(m.dir, name), record.CodeFiles)
		if err != nil {
			return nil, err
		}
		for dep, version := range manifest.Dependencies {
			m.mu.RLock()
			depRecord := m.installed[dep]
			active := m.running[dep] != nil
			m.mu.RUnlock()
			if !active || depRecord.Version != version {
				return nil, errors.New("plugin dependency exact enabled version missing")
			}
		}
		if p != nil {
			if err := p.close(); err != nil {
				return nil, err
			}
		}
		p, err = startProcess(ctx, manifest, filepath.Join(m.dir, name), m.cfg, m.host)
		if err != nil {
			record.Enabled = false
			record.Error = err.Error()
			m.mu.Lock()
			delete(m.running, name)
			m.installed[name] = record
			saveErr := m.saveLocked()
			m.mu.Unlock()
			return nil, errors.Join(err, saveErr)
		}
		record.Enabled = true
		record.Error = ""
		record.Version = manifest.Version
		m.mu.Lock()
		m.running[name] = p
		m.manifests[name] = manifest
		m.installed[name] = record
		err = m.saveLocked()
		m.mu.Unlock()
		if err != nil {
			_ = p.close()
			m.mu.Lock()
			delete(m.running, name)
			record.Enabled = false
			m.installed[name] = record
			m.mu.Unlock()
			return nil, err
		}
		return record, nil
	}
	m.mu.Lock()
	for other, manifest := range m.manifests {
		if other != name && m.installed[other].Enabled && manifest.Dependencies[name] != "" {
			m.mu.Unlock()
			return nil, fmt.Errorf("plugin %s depends on this plugin", other)
		}
	}
	delete(m.running, name)
	m.mu.Unlock()
	if p != nil {
		if err := p.close(); err != nil {
			return nil, err
		}
	}
	record.Enabled = false
	if action == "remove" {
		backup := filepath.Join(m.dir, ".removed-"+name+"-"+fmt.Sprint(time.Now().UnixNano()))
		if err := os.Rename(filepath.Join(m.dir, name), backup); err != nil {
			return nil, err
		}
		m.mu.Lock()
		delete(m.installed, name)
		delete(m.manifests, name)
		err := m.saveLocked()
		m.mu.Unlock()
		return map[string]any{"removed": true, "backup": backup}, err
	}
	m.mu.Lock()
	m.installed[name] = record
	err := m.saveLocked()
	m.mu.Unlock()
	return record, err
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return runGit(ctx, dir, args...)
}
func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if d.Name() == ".git" && d.IsDir() {
			return filepath.SkipDir
		}
		dest := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("plugin copy rejects symlinks")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("plugin copy rejects nonregular files")
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0755)
		if err != nil {
			_ = in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		return errors.Join(err, in.Close(), out.Close())
	})
}
func (m *Manager) install(ctx context.Context, name, source, revision string) (any, error) {
	m.mu.RLock()
	old, exists := m.installed[name]
	m.mu.RUnlock()
	if source == "" {
		source = old.Source
	}
	if configured, ok := m.cfg.Sources[source]; ok {
		source = configured
	}
	allowed := false
	for _, configured := range m.cfg.Sources {
		if configured == source {
			allowed = true
		}
	}
	if !allowed {
		return nil, errors.New("plugin source must be administrator-configured in plugins.sources")
	}
	local := filepath.IsAbs(source)
	if !local && !revisionPattern.MatchString(revision) {
		return nil, errors.New("git install/update requires full immutable 40-character commit SHA; pinned versions never auto-update")
	}
	if err := m.host.Check(ctx, "plugin_manage", !local); err != nil {
		return nil, err
	}
	clone, cancel := context.WithTimeout(ctx, time.Duration(m.cfg.CloneTimeoutSeconds)*time.Second)
	defer cancel()
	stage, err := os.MkdirTemp(m.dir, ".install-"+name+"-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if local {
		if err := copyTree(source, stage); err != nil {
			return nil, err
		}
	} else {
		if _, err := git(clone, m.dir, "clone", "--no-checkout", "--", source, stage); err != nil {
			return nil, err
		}
		if _, err := git(clone, stage, "fetch", "origin", revision); err != nil {
			return nil, err
		}
		if _, err := git(clone, stage, "-c", "core.hooksPath=/dev/null", "checkout", "--detach", revision); err != nil {
			return nil, err
		}
		head, err := git(clone, stage, "rev-parse", "HEAD")
		if err != nil || !strings.EqualFold(strings.TrimSpace(string(head)), revision) {
			return nil, errors.New("plugin checkout does not match requested exact revision")
		}
	}
	manifest, err := readManifest(stage)
	if err != nil {
		return nil, err
	}
	if manifest.Name != name {
		return nil, errors.New("installed manifest name differs from target")
	}
	if err := m.collision(manifest); err != nil {
		return nil, err
	}
	scan, err := Scan(stage)
	if err != nil {
		return nil, err
	}
	codeFiles, err := packageFiles(stage)
	if err != nil {
		return nil, err
	}
	if exists && old.Source == source {
		if err := preserveLocalFiles(filepath.Join(m.dir, name), stage, old.CodeFiles); err != nil {
			return nil, err
		}
	}
	if err := m.admin(ctx); err != nil {
		return nil, err
	}
	m.mu.RLock()
	for other, otherManifest := range m.manifests {
		if other != name && m.installed[other].Enabled {
			if version := otherManifest.Dependencies[name]; version != "" && version != manifest.Version {
				m.mu.RUnlock()
				return nil, fmt.Errorf("enabled dependent %s requires version %s", other, version)
			}
		}
	}
	m.mu.RUnlock()
	m.mu.Lock()
	p := m.running[name]
	delete(m.running, name)
	m.mu.Unlock()
	if p != nil {
		if err := p.close(); err != nil {
			return nil, err
		}
	}
	backup := ""
	target := filepath.Join(m.dir, name)
	if exists {
		backup = filepath.Join(m.dir, ".backup-"+name+"-"+fmt.Sprint(time.Now().UnixNano()))
		if err := os.Rename(target, backup); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(stage, target); err != nil {
		if backup != "" {
			err = errors.Join(err, os.Rename(backup, target))
		}
		return nil, err
	}
	record := installation{Source: source, Revision: strings.ToLower(revision), Digest: scan.Digest, CodeFiles: codeFiles, Version: manifest.Version}
	if old.Enabled && old.Source == source && !contains(m.cfg.DisabledPlugins, name) {
		if err := m.authorize(ctx, name, contains(manifest.Capabilities, "external")); err == nil {
			p, startErr := startProcess(ctx, manifest, target, m.cfg, m.host)
			if startErr == nil {
				m.mu.Lock()
				m.running[name] = p
				m.mu.Unlock()
				record.Enabled = true
			} else {
				record.Error = "updated plugin failed to initialize: " + startErr.Error()
			}
		} else {
			record.Error = "updated plugin activation permission denied"
		}
	}
	m.mu.Lock()
	m.installed[name] = record
	m.manifests[name] = manifest
	err = m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if record.Error != "" {
		return nil, errors.New(record.Error + "; previous code and local edits retained at " + backup)
	}
	return map[string]any{"installation": record, "scan": scan, "backup": backup}, nil
}

// Close 禁止新调用并关闭所有插件进程和连接；不会自动重放中断的副作用。
func (m *Manager) Close() error {
	m.changeMu.Lock()
	defer m.changeMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	ps := m.running
	m.running = map[string]*process{}
	m.mu.Unlock()
	var errs []error
	for _, p := range ps {
		errs = append(errs, p.close())
	}
	if m.connectors != nil {
		errs = append(errs, m.connectors.close())
	}
	return errors.Join(errs...)
}
