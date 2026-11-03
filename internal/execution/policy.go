// Package execution 实现由程序强制检查权限的容器命令与文件工具。
package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// Executor 持有只读授权快照；所有本地工具始终通过 Docker 执行。
type Executor struct {
	cfg     config.ExecutionConfig
	grants  map[string]config.ExecutionGrant
	owner   string
	admins  map[string]bool
	blocked sync.Map
	// activeOutputs 记录仍在写入的归档句柄；读取到当前末尾时用 caught_up 区分。
	activeOutputs sync.Map
	outputMu      sync.RWMutex
	outputAuth    func(context.Context, int64) error
	// backends 按名称缓存无状态后端实现；环境生命周期由 envs 管理。
	backends map[string]backend
	envMu    sync.Mutex
	envs     map[int64]*envEntry
	// closedTasks 阻止清理后的任务 ID 重新打开环境。
	closedTasks sync.Map
	// processes 按任务保存后台作业，已结束作业保留状态与日志引用。
	processMu sync.Mutex
	processes map[int64]map[string]*processRecord
}

type scope struct {
	id  int64
	dir string
}
type scopeKey struct{}

// WithTask 注入程序确定的任务身份和目录，工具参数无法覆盖。
func WithTask(ctx context.Context, id int64, dir string) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope{id, dir})
}

func identityKey(i chat.Identity) string {
	data, _ := json.Marshal([]string{i.Session.Platform, i.Session.Account, i.Session.Conversation, i.SenderID})
	return string(data)
}

func (e *Executor) grant(identity chat.Identity) (config.ExecutionGrant, bool) {
	for _, pair := range [][2]string{
		{identity.Session.Conversation, identity.SenderID},
		{identity.Session.Conversation, "*"},
		{"*", identity.SenderID},
		{"*", "*"},
	} {
		identity.Session.Conversation, identity.SenderID = pair[0], pair[1]
		if grant, ok := e.grants[identityKey(identity)]; ok {
			return grant, true
		}
	}
	return config.ExecutionGrant{}, false
}

// LocalTool 判断名称是否属于本地执行工具保留字。
func LocalTool(name string) bool {
	return slices.Contains([]string{"exec", "read_file", "list_files", "write_file", "apply_patch", "read_output", "search_files", "glob_files", "process"}, name)
}

// ValidateConfig 归一化执行配置并校验授权、路径与后端约束；不创建目录。
// 返回的 map/slice 都是克隆，调用者的配置不会被修改。
func ValidateConfig(cfg config.ExecutionConfig, protectedPaths, forbiddenSecrets []string) (config.ExecutionConfig, error) {
	if cfg.Image == "" {
		cfg.Image = "python:3.13-slim"
	}
	if cfg.LogDir == "" {
		cfg.LogDir = "data/execution"
	}
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = 60
	}
	if cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 3600 {
		return cfg, errors.New("execution timeout must be 1..3600 seconds")
	}
	cfg.Workspaces = maps.Clone(cfg.Workspaces)
	cfg.Grants = slices.Clone(cfg.Grants)
	cfg.MCPRequirements = maps.Clone(cfg.MCPRequirements)
	for k, v := range cfg.MCPRequirements {
		cfg.MCPRequirements[k] = slices.Clone(v)
		if !strings.HasPrefix(k, "mcp:") {
			return cfg, errors.New("MCP permission keys must use mcp:server:tool")
		}
		for _, capability := range v {
			if !validCapability(capability) {
				return cfg, fmt.Errorf("unknown capability %q", capability)
			}
		}
	}
	logDir, err := filepath.Abs(cfg.LogDir)
	if err != nil {
		return cfg, err
	}
	cfg.LogDir = logDir
	protectedPaths = append(slices.Clone(protectedPaths), logDir, "/var/run/docker.sock")
	for name, w := range cfg.Workspaces {
		if !filepath.IsAbs(w.Path) || w.Path == string(filepath.Separator) {
			return cfg, fmt.Errorf("workspace %q requires a specific absolute directory", name)
		}
		path, err := filepath.EvalSymlinks(w.Path)
		if err != nil {
			return cfg, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return cfg, err
		}
		if !info.IsDir() {
			return cfg, errors.New("workspace is not a directory")
		}
		if strings.ContainsAny(path, ",\n\r") {
			return cfg, errors.New("workspace path contains unsupported mount characters")
		}
		for _, protected := range protectedPaths {
			if protected == "" {
				continue
			}
			p, err := resolveFuturePath(protected)
			if err != nil {
				return cfg, err
			}
			if within(path, p) {
				return cfg, fmt.Errorf("workspace %q contains protected Saber data: %s", name, p)
			}
		}
		w.Path = path
		w.Env = maps.Clone(w.Env)
		if err := ValidateEnvironment(w.Env, forbiddenSecrets); err != nil {
			return cfg, fmt.Errorf("workspace %q: %w", name, err)
		}
		if _, err := cfg.BackendFor(name); err != nil {
			return cfg, fmt.Errorf("workspace %q: %w", name, err)
		}
		cfg.Workspaces[name] = w
	}
	for name, a := range cfg.Workspaces {
		for other, b := range cfg.Workspaces {
			if name != other && a.Path != b.Path && (within(a.Path, b.Path) || within(b.Path, a.Path)) {
				return cfg, errors.New("nested workspaces would bypass directory serialization")
			}
		}
	}
	for _, admin := range cfg.TaskAdmins {
		if admin.Platform == "" || admin.Account == "" || admin.Room == "" || len(admin.Users) == 0 || strings.Contains(admin.Platform+admin.Account+admin.Room, "*") {
			return cfg, errors.New("task admin requires exact platform, account, room and users")
		}
		for _, user := range admin.Users {
			if user == "" || strings.Contains(user, "*") {
				return cfg, errors.New("task admin requires exact member IDs")
			}
		}
	}
	seen := make(map[string]bool)
	active := make(map[string]bool)
	for i := range cfg.Grants {
		grant := cfg.Grants[i]
		if _, ok := cfg.Workspaces[grant.Workspace]; !ok {
			return cfg, fmt.Errorf("unknown workspace %q", grant.Workspace)
		}
		if grant.Platform == "" || strings.Contains(grant.Platform, "*") || grant.Account == "" || strings.Contains(grant.Account, "*") || grant.Room == "" || (grant.Room != "*" && strings.Contains(grant.Room, "*")) || len(grant.Users) == 0 {
			return cfg, errors.New("grant requires exact platform/account and room/users (only full * wildcard allowed)")
		}
		grant.Tools = slices.Clone(grant.Tools)
		grant.Capabilities = slices.Clone(grant.Capabilities)
		for _, capability := range grant.Capabilities {
			if !validCapability(capability) {
				return cfg, fmt.Errorf("unknown capability %q", capability)
			}
		}
		for _, tool := range grant.Tools {
			if !LocalTool(tool) {
				if _, ok := cfg.MCPRequirements[tool]; !ok {
					return cfg, fmt.Errorf("tool %q has no trusted permission declaration", tool)
				}
			}
		}
		for _, user := range grant.Users {
			if user == "" || (user != "*" && strings.Contains(user, "*")) {
				return cfg, errors.New("grant user must be an exact ID or *")
			}
			key := identityKey(chat.Identity{Session: chat.Session{Platform: grant.Platform, Account: grant.Account, Conversation: grant.Room}, SenderID: user})
			if seen[key] {
				return cfg, errors.New("member has conflicting workspace grants in one room")
			}
			seen[key] = true
		}
		active[grant.Workspace] = true
		cfg.Grants[i] = grant
	}
	if cfg.Enabled {
		for name := range active {
			backend, err := cfg.BackendFor(name)
			if err != nil {
				return cfg, err
			}
			if cfg.Backend == "" && cfg.Workspaces[name].Backend == "" {
				return cfg, errors.New("execution backend must be explicit when enabling execution: set backend: local or docker")
			}
			if backend != "local" {
				continue
			}
			if !cfg.IsNetworkEnabled() {
				return cfg, errors.New("local backend cannot enforce network isolation")
			}
			if !workspaceNeedsCommands(cfg.Grants, name) {
				continue
			}
			if !slices.Contains([]string{"darwin", "linux", "freebsd", "openbsd"}, runtime.GOOS) {
				return cfg, errors.New("local execution is unsupported on this platform; configure backend: docker")
			}
			if _, err := os.Stat("/bin/sh"); err != nil {
				return cfg, fmt.Errorf("local execution requires /bin/sh: %w", err)
			}
		}
	}
	return cfg, nil
}

func workspaceNeedsCommands(grants []config.ExecutionGrant, workspace string) bool {
	for _, grant := range grants {
		if grant.Workspace != workspace {
			continue
		}
		if slices.Contains(grant.Tools, "exec") || slices.Contains(grant.Tools, "process") {
			return true
		}
	}
	return false
}

// New 验证并冻结权限配置，拒绝挂载包含机器人私有数据的工作区。
func New(cfg config.ExecutionConfig, protectedPaths, forbiddenSecrets []string) (*Executor, error) {
	var err error
	cfg, err = ValidateConfig(cfg, protectedPaths, forbiddenSecrets)
	if err != nil {
		return nil, err
	}
	if cfg.Enabled {
		if err = os.MkdirAll(cfg.LogDir, 0700); err != nil {
			return nil, err
		}
		resolved, err := filepath.EvalSymlinks(cfg.LogDir)
		if err != nil {
			return nil, err
		}
		if resolved != cfg.LogDir {
			cfg.LogDir = resolved
			if cfg, err = ValidateConfig(cfg, protectedPaths, forbiddenSecrets); err != nil {
				return nil, err
			}
		}
	}
	e := &Executor{
		cfg:       cfg,
		grants:    make(map[string]config.ExecutionGrant),
		admins:    make(map[string]bool),
		owner:     fmt.Sprintf("%x", sha256.Sum256([]byte(cfg.LogDir))),
		backends:  make(map[string]backend),
		envs:      make(map[int64]*envEntry),
		processes: make(map[int64]map[string]*processRecord),
	}
	for _, admin := range cfg.TaskAdmins {
		for _, user := range admin.Users {
			e.admins[identityKey(chat.Identity{Session: chat.Session{Platform: admin.Platform, Account: admin.Account, Conversation: admin.Room}, SenderID: user})] = true
		}
	}
	for _, grant := range cfg.Grants {
		for _, user := range grant.Users {
			key := identityKey(chat.Identity{Session: chat.Session{Platform: grant.Platform, Account: grant.Account, Conversation: grant.Room}, SenderID: user})
			e.grants[key] = grant
		}
	}
	if err = e.loadEnvironmentRecords(); err != nil {
		return nil, err
	}
	if cfg.Enabled {
		for name := range e.activeWorkspaces() {
			if backend, backendErr := cfg.BackendFor(name); backendErr == nil && backend == "docker" {
				if err = probeDocker(context.Background(), cfg); err != nil {
					return nil, err
				}
				break
			}
		}
	}
	return e, nil
}

// ValidateEnvironment 阻止执行工具与 stdio MCP 显式注入机器人自身凭据。
func ValidateEnvironment(env map[string]string, forbiddenSecrets []string) error {
	for key, value := range env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return errors.New("invalid execution environment")
		}
		upper := strings.ToUpper(key)
		for _, prefix := range []string{"SABER_", "MATRIX_", "OPENAI_", "ANTHROPIC_", "DOCKER_"} {
			if strings.HasPrefix(upper, prefix) {
				return fmt.Errorf("reserved execution environment key %q", key)
			}
		}
		for _, secret := range forbiddenSecrets {
			if secret != "" && strings.Contains(value, secret) {
				return errors.New("environment contains a Saber credential")
			}
		}
	}
	return nil
}

func validCapability(capability string) bool {
	return slices.Contains([]string{"external", "publish", "deploy", "cross_directory"}, capability)
}
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolveFuturePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent, err := resolveFuturePath(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

// Workspace 只根据真实接入身份选择工作区，忽略模型或文件中的目录声明。
func (e *Executor) Workspace(identity chat.Identity) (string, error) {
	grant, ok := e.grant(identity)
	if !e.cfg.Enabled || !ok {
		return "", errors.New("该成员尚未获得本群工作区授权")
	}
	return e.cfg.Workspaces[grant.Workspace].Path, nil
}

// Check 在工具列举和每次真实调用时检查同一份策略，默认拒绝。
func (e *Executor) Check(ctx context.Context, tool string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return errors.New("tool permission requires trusted identity")
	}
	dir, err := e.Workspace(identity)
	if err != nil {
		return err
	}
	run, ok := ctx.Value(scopeKey{}).(scope)
	if !ok || run.dir != dir {
		return errors.New("tool workspace does not match current authorization")
	}
	if _, blocked := e.blocked.Load(dir); blocked {
		return errors.New("workspace quarantined after container cleanup failure")
	}
	grant, _ := e.grant(identity)
	if !slices.Contains(grant.Tools, tool) {
		return fmt.Errorf("tool %q is not authorized", tool)
	}
	if !LocalTool(tool) {
		required, ok := e.cfg.MCPRequirements[tool]
		if !ok {
			return errors.New("MCP tool has no trusted permission declaration")
		}
		for _, capability := range append(slices.Clone(required), "external") {
			if !slices.Contains(grant.Capabilities, capability) {
				return fmt.Errorf("tool requires separately granted %s capability", capability)
			}
		}
	}
	return nil
}

// CheckMCP 是所有 MCP 实际调用共用的权限入口。
func (e *Executor) CheckMCP(ctx context.Context, server, tool string) error {
	return e.Check(ctx, "mcp:"+server+":"+tool)
}

// AllowedTools 返回当前身份在程序层获准的工具名；不要求任务目录。
func (e *Executor) AllowedTools(identity chat.Identity) []string {
	grant, ok := e.grant(identity)
	if !e.cfg.Enabled || !ok {
		return nil
	}
	tools := make([]string, 0, len(grant.Tools))
	for _, tool := range grant.Tools {
		if LocalTool(tool) {
			tools = append(tools, tool)
			continue
		}
		required, declared := e.cfg.MCPRequirements[tool]
		if !declared {
			continue
		}
		allowed := true
		for _, capability := range append(slices.Clone(required), "external") {
			if !slices.Contains(grant.Capabilities, capability) {
				allowed = false
				break
			}
		}
		if allowed {
			tools = append(tools, tool)
		}
	}
	slices.Sort(tools)
	return tools
}

// IdentityBackend 返回当前身份有效后端及是否处于沙箱。
func (e *Executor) IdentityBackend(identity chat.Identity) (string, bool, error) {
	grant, ok := e.grant(identity)
	if !e.cfg.Enabled || !ok {
		return "", false, errors.New("该成员尚未获得本群工作区授权")
	}
	backend, err := e.cfg.BackendFor(grant.Workspace)
	if err != nil {
		return "", false, err
	}
	return backend, backend == "docker", nil
}

// WorkspaceBlocked 报告当前身份工作区是否因清理失败被隔离。
func (e *Executor) WorkspaceBlocked(identity chat.Identity) bool {
	grant, ok := e.grant(identity)
	if !ok {
		return false
	}
	_, blocked := e.blocked.Load(e.cfg.Workspaces[grant.Workspace].Path)
	return blocked
}

// Timeout 返回工具的程序配置上限。
func (e *Executor) Timeout() time.Duration { return time.Duration(e.cfg.TimeoutSeconds) * time.Second }

// IsTaskAdmin 查询配置中的精确管理授权；管理员身份不开放任何执行工具。
func (e *Executor) IsTaskAdmin(identity chat.Identity) bool { return e.admins[identityKey(identity)] }
