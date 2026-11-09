package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

func pluginContext() context.Context {
	return chat.WithIdentity(context.Background(), chat.Identity{Session: chat.Session{Platform: "test", Account: "account", Conversation: "dm"}, SenderID: "alice", Direct: true})
}
func testPluginManifest() Manifest {
	return Manifest{ManifestVersion: 1, Name: "filesystem", Version: "1.0.0", Command: []string{os.Args[0], "-test.run=^TestPluginSDKChild$"}, Tools: []ToolDefinition{{Name: "write", Description: "Write the requested file", Parameters: map[string]any{"type": "object"}}, {Name: "host-write", Description: "Use host write authorization", Parameters: map[string]any{"type": "object"}}, {Name: "wait", Description: "Wait until canceled", Parameters: map[string]any{"type": "object"}}}, Commands: []CommandDefinition{{Name: "write", Description: "Write file"}}, Hooks: []string{"pre_tool_call"}, Providers: map[string][]string{"memory": {"sync_turn"}}, Capabilities: []string{"tool_call"}}
}

func TestPluginSDKChild(t *testing.T) {
	if os.Getenv("SABER_PLUGIN_ID") == "" {
		return
	}
	manifest, err := readManifest(".")
	if err != nil {
		os.Exit(2)
	}
	write := func(ctx context.Context, args map[string]any) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			return nil, err
		}
		return map[string]any{"written": len(content)}, nil
	}
	handlers := Handlers{Tools: map[string]Handler{"write": write, "host-write": func(ctx context.Context, args map[string]any) (any, error) {
		runtime, err := RuntimeFromContext(ctx)
		if err != nil {
			return nil, err
		}
		return runtime.CallTool(ctx, "host_write", args)
	}, "wait": func(ctx context.Context, args map[string]any) (any, error) {
		path, _ := args["path"].(string)
		if err := os.WriteFile(path, []byte("started"), 0600); err != nil {
			return nil, err
		}
		<-ctx.Done()
		if err := os.WriteFile(path, []byte("canceled"), 0600); err != nil {
			return nil, err
		}
		return nil, ctx.Err()
	}}, Commands: map[string]Handler{"write": write}, Hooks: map[string]Handler{"pre_tool_call": func(_ context.Context, args map[string]any) (any, error) {
		if path, _ := args["path"].(string); path != "" {
			if err := os.WriteFile(path, []byte("hook"), 0600); err != nil {
				return nil, err
			}
		}
		deny, _ := args["deny"].(bool)
		return map[string]any{"allow": !deny}, nil
	}}, Providers: map[string]map[string]Handler{"memory": {"sync_turn": write}, "context_refs": {"expand": func(_ context.Context, args map[string]any) (any, error) {
		target, _ := args["target"].(string)
		return map[string]any{"text": "expanded " + target}, nil
	}}}}
	if _, err := Serve(context.Background(), manifest, handlers, os.Stdin, os.Stdout); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}
func createPluginSource(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := json.Marshal(testPluginManifest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func testPluginManager(t *testing.T) (*Manager, *atomic.Bool) {
	t.Helper()
	source := createPluginSource(t)
	cfg := config.DefaultPluginsConfig()
	cfg.Enabled = true
	cfg.Sources = map[string]string{"local": source}
	allowed := &atomic.Bool{}
	allowed.Store(true)
	host := toolkit.Host{StateDir: t.TempDir(), Check: func(context.Context, string, bool) error {
		if !allowed.Load() {
			return errors.New("permission revoked")
		}
		return nil
	}, IsAdmin: func(context.Context) bool { return true }, Call: func(ctx context.Context, name string, args map[string]any) (any, error) {
		if name != "host_write" || !allowed.Load() {
			return nil, errors.New("host write denied")
		}
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		return nil, os.WriteFile(path, []byte(content), 0600)
	}}
	manager, err := New(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := manager.Command(pluginContext(), []string{"install", "filesystem", "local"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Command(pluginContext(), []string{"enable", "filesystem"}); err != nil {
		t.Fatal(err)
	}
	return manager, allowed
}

func TestPluginLifecycleRealExecutionDisableAndProvider(t *testing.T) {
	manager, _ := testPluginManager(t)
	path := filepath.Join(t.TempDir(), "output.txt")
	if _, err := manager.Execute(pluginContext(), namespace("filesystem", "write"), map[string]any{"path": path, "content": "tool content"}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "tool content" {
		t.Fatalf("real tool write: %s %v", raw, err)
	}
	if _, err := manager.ProviderCall(pluginContext(), "filesystem", "memory", "sync_turn", map[string]any{"path": path, "content": "provider content"}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "provider content" {
		t.Fatalf("provider write: %s %v", raw, err)
	}
	if _, err := manager.ExecuteCommand(pluginContext(), "filesystem:write", map[string]any{"path": path, "content": "command content"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Hook(pluginContext(), "pre_tool_call", map[string]any{"deny": true}); err == nil {
		t.Fatal("pre-tool denial was not enforced")
	}
	if _, err := manager.Command(pluginContext(), []string{"disable", "filesystem"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Execute(pluginContext(), namespace("filesystem", "write"), map[string]any{"path": path, "content": "after disable"}); err == nil {
		t.Fatal("disabled plugin dispatched")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "command content" {
		t.Fatal("disabled plugin changed file")
	}
}

// capabilityPluginManifest 声明四个入口共用的命令、钩子、技能和上下文 provider。
func capabilityPluginManifest(name string, capabilities ...string) Manifest {
	return Manifest{
		ManifestVersion: 1,
		Name:            name,
		Version:         "1.0.0",
		Command:         []string{os.Args[0], "-test.run=^TestPluginSDKChild$"},
		Commands:        []CommandDefinition{{Name: "write", Description: "Write file"}},
		Hooks:           []string{"pre_tool_call"},
		Skills:          map[string]string{"echo": "."},
		Providers:       map[string][]string{"context_refs": {"expand"}},
		Capabilities:    capabilities,
	}
}
func createCapabilityPluginSource(t *testing.T, manifest Manifest) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# "+manifest.Name), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func resolveTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
func newTestPluginManager(t *testing.T, cfg config.PluginsConfig, host toolkit.Host) *Manager {
	t.Helper()
	manager, err := New(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	return manager
}

func TestPluginHostCallbackCannotBypassRevokedGrant(t *testing.T) {
	manager, allowed := testPluginManager(t)
	path := filepath.Join(t.TempDir(), "host.txt")
	if _, err := manager.Execute(pluginContext(), namespace("filesystem", "host-write"), map[string]any{"path": path, "content": "authorized"}); err != nil {
		t.Fatal(err)
	}
	allowed.Store(false)
	if _, err := manager.Execute(pluginContext(), namespace("filesystem", "host-write"), map[string]any{"path": path, "content": "denied"}); err == nil {
		t.Fatal("revoked plugin was executed")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "authorized" {
		t.Fatal("revoked host callback altered file")
	}
}

// TestPluginExternalCapabilityRevocationBlocksCommandHookSkillAndContextReference 证明撤销 external 后
// 命令、钩子、技能和上下文引用都按 manifest 声明的外部能力失败，且未声明 external 的插件不受影响。
func TestPluginExternalCapabilityRevocationBlocksCommandHookSkillAndContextReference(t *testing.T) {
	ctx := pluginContext()
	cfg := config.DefaultPluginsConfig()
	cfg.Enabled = true
	cfg.Sources = map[string]string{
		"external": createCapabilityPluginSource(t, capabilityPluginManifest("external-plugin", "tool_call", "external")),
		"internal": createCapabilityPluginSource(t, capabilityPluginManifest("internal-plugin", "tool_call")),
	}
	var revoked atomic.Bool
	check := func(_ context.Context, permission string, external bool) error {
		if external && revoked.Load() && permission == pluginPermission("external-plugin") {
			return errors.New("external capability revoked")
		}
		return nil
	}
	externalCfg, internalCfg := cfg, cfg
	externalCfg.Directory = resolveTempDir(t)
	internalCfg.Directory = resolveTempDir(t)
	external := newTestPluginManager(t, externalCfg, toolkit.Host{StateDir: t.TempDir(), IsAdmin: func(context.Context) bool { return true }, Check: check})
	internal := newTestPluginManager(t, internalCfg, toolkit.Host{StateDir: t.TempDir(), IsAdmin: func(context.Context) bool { return true }, Check: check})
	for _, setup := range []struct {
		manager *Manager
		name    string
		source  string
	}{{external, "external-plugin", "external"}, {internal, "internal-plugin", "internal"}} {
		if _, err := setup.manager.Command(ctx, []string{"install", setup.name, setup.source}); err != nil {
			t.Fatal(err)
		}
		if _, err := setup.manager.Command(ctx, []string{"enable", setup.name}); err != nil {
			t.Fatal(err)
		}
	}
	// 撤销前四个入口都可用，证明后续失败来自 external 授权而非插件未运行。
	probePath := filepath.Join(t.TempDir(), "before.txt")
	if _, err := external.ExecuteCommand(ctx, "external-plugin:write", map[string]any{"path": probePath, "content": "before"}); err != nil {
		t.Fatal(err)
	}
	if err := external.Hook(ctx, "pre_tool_call", map[string]any{"deny": false}); err != nil {
		t.Fatal(err)
	}
	if _, err := external.ReadSkill(ctx, "external-plugin", "echo", "SKILL.md"); err != nil {
		t.Fatal(err)
	}
	if _, ok := external.ContextReferences(ctx)["plugin-external-plugin"]; !ok {
		t.Fatal("external context reference unavailable before revocation")
	}
	revoked.Store(true)

	externalDir := t.TempDir()
	commandPath := filepath.Join(externalDir, "command.txt")
	if _, err := external.ExecuteCommand(ctx, "external-plugin:write", map[string]any{"path": commandPath, "content": "command"}); err == nil || !strings.Contains(err.Error(), "external capability revoked") {
		t.Fatalf("revoked external command was not denied by external grant: %v", err)
	}
	if _, err := os.Stat(commandPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("revoked external command wrote file: %v", err)
	}
	hookPath := filepath.Join(externalDir, "hook.txt")
	if err := external.Hook(ctx, "pre_tool_call", map[string]any{"path": hookPath, "deny": false}); err == nil || !strings.Contains(err.Error(), "external capability revoked") {
		t.Fatalf("revoked external hook was not denied by external grant: %v", err)
	}
	if _, err := os.Stat(hookPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("revoked external hook wrote file: %v", err)
	}
	if _, err := external.ReadSkill(ctx, "external-plugin", "echo", "SKILL.md"); err == nil {
		t.Fatal("revoked external skill was readable")
	}
	if _, ok := external.ContextReferences(ctx)["plugin-external-plugin"]; ok {
		t.Fatal("revoked external context reference remained available")
	}

	// 同一授权策略下，未声明 external 的插件四条入口仍可用。
	internalPath := filepath.Join(t.TempDir(), "command.txt")
	if _, err := internal.ExecuteCommand(ctx, "internal-plugin:write", map[string]any{"path": internalPath, "content": "command"}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(internalPath); err != nil || string(raw) != "command" {
		t.Fatalf("internal command: %s %v", raw, err)
	}
	internalHookPath := filepath.Join(t.TempDir(), "hook.txt")
	if err := internal.Hook(ctx, "pre_tool_call", map[string]any{"path": internalHookPath, "deny": false}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(internalHookPath); err != nil || string(raw) != "hook" {
		t.Fatalf("internal hook: %s %v", raw, err)
	}
	if raw, err := internal.ReadSkill(ctx, "internal-plugin", "echo", "SKILL.md"); err != nil || string(raw) != "# internal-plugin" {
		t.Fatalf("internal skill: %s %v", raw, err)
	}
	expand, ok := internal.ContextReferences(ctx)["plugin-internal-plugin"]
	if !ok {
		t.Fatal("internal context reference unavailable")
	}
	if text, err := expand(ctx, "docs", 64); err != nil || text != "expanded docs" {
		t.Fatalf("internal context reference: %q %v", text, err)
	}
}

func TestPluginRPCPropagatesCancellationIntoHandler(t *testing.T) {
	manager, _ := testPluginManager(t)
	path := filepath.Join(t.TempDir(), "state.txt")
	ctx, cancel := context.WithTimeout(pluginContext(), 100*time.Millisecond)
	defer cancel()
	if _, err := manager.Execute(ctx, namespace("filesystem", "wait"), map[string]any{"path": path}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		raw, _ := os.ReadFile(path)
		if string(raw) == "canceled" {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("SDK handler did not observe cancellation")
		case <-tick.C:
		}
	}
}

func TestPluginInstallRejectsMutableGitRevisionAndSymlink(t *testing.T) {
	cfg := config.DefaultPluginsConfig()
	cfg.Enabled = true
	cfg.Sources = map[string]string{"remote": "https://example.invalid/plugin.git"}
	host := toolkit.Host{StateDir: t.TempDir(), IsAdmin: func(context.Context) bool { return true }, Check: func(context.Context, string, bool) error { return nil }}
	manager, err := New(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Close() }()
	if _, err := manager.Command(pluginContext(), []string{"install", "filesystem", "remote", "main"}); err == nil {
		t.Fatal("mutable ref accepted")
	}
	source := createPluginSource(t)
	if err := os.Symlink("/etc/passwd", filepath.Join(source, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Scan(source); err == nil {
		t.Fatal("symlink plugin package accepted")
	}
}
