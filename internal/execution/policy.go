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
	blocked sync.Map
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

// LocalTool 判断名称是否属于本地执行工具保留字。
func LocalTool(name string) bool {
	return slices.Contains([]string{"exec", "read_file", "list_files", "write_file", "apply_patch"}, name)
}

// New 验证并冻结权限配置，拒绝挂载包含机器人私有数据的工作区。
func New(cfg config.ExecutionConfig, protectedPaths, forbiddenSecrets []string) (*Executor, error) {
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
		return nil, errors.New("execution timeout must be 1..3600 seconds")
	}
	cfg.Workspaces = maps.Clone(cfg.Workspaces)
	cfg.Grants = slices.Clone(cfg.Grants)
	cfg.MCPRequirements = maps.Clone(cfg.MCPRequirements)
	for k, v := range cfg.MCPRequirements {
		cfg.MCPRequirements[k] = slices.Clone(v)
		if !strings.HasPrefix(k, "mcp:") {
			return nil, errors.New("MCP permission keys must use mcp:server:tool")
		}
		for _, capability := range v {
			if !validCapability(capability) {
				return nil, fmt.Errorf("unknown capability %q", capability)
			}
		}
	}
	logDir, err := filepath.Abs(cfg.LogDir)
	if err != nil {
		return nil, err
	}
	if cfg.Enabled {
		if err = os.MkdirAll(logDir, 0700); err != nil {
			return nil, err
		}
		logDir, err = filepath.EvalSymlinks(logDir)
		if err != nil {
			return nil, err
		}
	}
	cfg.LogDir = logDir
	protectedPaths = append(slices.Clone(protectedPaths), logDir, "/var/run/docker.sock")
	for name, w := range cfg.Workspaces {
		if !filepath.IsAbs(w.Path) || w.Path == string(filepath.Separator) {
			return nil, fmt.Errorf("workspace %q requires a specific absolute directory", name)
		}
		path, err := filepath.EvalSymlinks(w.Path)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, errors.New("workspace is not a directory")
		}
		if strings.ContainsAny(path, ",\n\r") {
			return nil, errors.New("workspace path contains unsupported mount characters")
		}
		for _, protected := range protectedPaths {
			if protected == "" {
				continue
			}
			p, err := resolveFuturePath(protected)
			if err != nil {
				return nil, err
			}
			if within(path, p) {
				return nil, fmt.Errorf("workspace %q contains protected Saber data: %s", name, p)
			}
		}
		w.Path = path
		w.Env = maps.Clone(w.Env)
		for key, value := range w.Env {
			if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
				return nil, errors.New("invalid execution environment")
			}
			upper := strings.ToUpper(key)
			if strings.HasPrefix(upper, "SABER_") || strings.HasPrefix(upper, "MATRIX_") || strings.HasPrefix(upper, "OPENAI_") || strings.HasPrefix(upper, "ANTHROPIC_") || strings.HasPrefix(upper, "DOCKER_") {
				return nil, fmt.Errorf("reserved execution environment key %q", key)
			}
			for _, secret := range forbiddenSecrets {
				if secret != "" && strings.Contains(value, secret) {
					return nil, fmt.Errorf("workspace %q environment contains a Saber credential", name)
				}
			}
		}
		cfg.Workspaces[name] = w
	}
	for name, a := range cfg.Workspaces {
		for other, b := range cfg.Workspaces {
			if name != other && a.Path != b.Path && (within(a.Path, b.Path) || within(b.Path, a.Path)) {
				return nil, errors.New("nested workspaces would bypass directory serialization")
			}
		}
	}
	e := &Executor{cfg: cfg, grants: make(map[string]config.ExecutionGrant), owner: fmt.Sprintf("%x", sha256.Sum256([]byte(cfg.LogDir)))}
	for _, grant := range cfg.Grants {
		if _, ok := cfg.Workspaces[grant.Workspace]; !ok {
			return nil, fmt.Errorf("unknown workspace %q", grant.Workspace)
		}
		if grant.Platform == "" || grant.Account == "" || grant.Room == "" || len(grant.Users) == 0 {
			return nil, errors.New("grant requires platform, account, room and users")
		}
		grant.Tools = slices.Clone(grant.Tools)
		grant.Capabilities = slices.Clone(grant.Capabilities)
		for _, capability := range grant.Capabilities {
			if !validCapability(capability) {
				return nil, fmt.Errorf("unknown capability %q", capability)
			}
		}
		for _, tool := range grant.Tools {
			if !LocalTool(tool) {
				if _, ok := cfg.MCPRequirements[tool]; !ok {
					return nil, fmt.Errorf("tool %q has no trusted permission declaration", tool)
				}
			}
		}
		for _, user := range grant.Users {
			if user == "" || strings.Contains(user, "*") {
				return nil, errors.New("grant requires exact member IDs")
			}
			key := identityKey(chat.Identity{Session: chat.Session{Platform: grant.Platform, Account: grant.Account, Conversation: grant.Room}, SenderID: user})
			if _, ok := e.grants[key]; ok {
				return nil, errors.New("member has conflicting workspace grants in one room")
			}
			e.grants[key] = grant
		}
	}
	return e, nil
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
	grant, ok := e.grants[identityKey(identity)]
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
	grant := e.grants[identityKey(identity)]
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

// Timeout 返回工具的程序配置上限。
func (e *Executor) Timeout() time.Duration { return time.Duration(e.cfg.TimeoutSeconds) * time.Second }
