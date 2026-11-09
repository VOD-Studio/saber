// Package computer 使用真实 cua-driver MCP 传输控制桌面，不使用模拟 shell 输出。
package computer

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rua.plus/saber/internal/config"
)

type driver struct {
	cfg        config.ComputerConfig
	session    *mcp.ClientSession
	tools      map[string]*mcp.Tool
	label      string
	binary     string
	socket     string
	daemon     *exec.Cmd
	daemonDone chan struct{}
}

func randomID() string { return rand.Text() }
func sanitizedEnv(cfg config.ComputerConfig) []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TMP", "TEMP", "DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "XDG_SESSION_TYPE", "DBUS_SESSION_BUS_ADDRESS", "SYSTEMROOT", "WINDIR", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	telemetry := "0"
	if cfg.Telemetry {
		telemetry = "1"
	}
	return append(env, "CUA_DRIVER_RS_TELEMETRY_ENABLED="+telemetry)
}
func noOverlay(cfg config.ComputerConfig) bool {
	if cfg.NoOverlay != nil {
		return *cfg.NoOverlay
	}
	return runtime.GOOS == "darwin" || (runtime.GOOS == "linux" && os.Getenv("XDG_SESSION_TYPE") != "wayland")
}
func probe(ctx context.Context, binary string, args []string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	cmd.Stderr = io.Discard
	return cmd.Output()
}

var versionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$`)

func manifestInvocation(raw []byte, binary string) (string, []string, error) {
	var manifest struct {
		BinaryVersion string `json:"binary_version"`
		MCPInvocation struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcp_invocation"`
		Subcommands []struct {
			Name string `json:"name"`
			Args []struct {
				Name string `json:"name"`
			} `json:"args"`
		} `json:"subcommands"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", nil, fmt.Errorf("cua-driver manifest invalid: %w", err)
	}
	version := versionPattern.FindStringSubmatch(manifest.BinaryVersion)
	if len(version) != 4 {
		return "", nil, errors.New("cua-driver manifest does not report a semantic binary_version")
	}
	major, _ := strconv.Atoi(version[1])
	minor, _ := strconv.Atoi(version[2])
	if major == 0 && minor < 20 {
		return "", nil, errors.New("cua-driver >= 0.20.0 is required; update the configured binary")
	}
	if manifest.MCPInvocation.Args == nil {
		return "", nil, errors.New("cua-driver manifest does not provide mcp_invocation.args")
	}
	if filepath.IsAbs(manifest.MCPInvocation.Command) {
		binary = manifest.MCPInvocation.Command
	}
	return binary, manifest.MCPInvocation.Args, nil
}

type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	for key, value := range t.headers {
		clone.Header.Set(key, value)
	}
	return t.base.RoundTrip(clone)
}

func startDriver(ctx context.Context, cfg config.ComputerConfig, stateDir string) (*driver, error) {
	d := &driver{cfg: cfg, label: "saber-" + randomID(), tools: make(map[string]*mcp.Tool)}
	var transport mcp.Transport
	if cfg.Endpoint != "" {
		transport = &mcp.StreamableClientTransport{Endpoint: cfg.Endpoint, HTTPClient: &http.Client{Transport: headerTransport{headers: cfg.Headers, base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, MaxRetries: -1}
	} else {
		if !cfg.AllowHostProcess {
			return nil, errors.New("host computer driver denied; use trusted sandbox MCP endpoint or allow_host_process")
		}
		binary := cfg.Command
		if binary == "" {
			binary = "cua-driver"
		}
		path, err := exec.LookPath(binary)
		if err != nil {
			return nil, fmt.Errorf("cua-driver is not installed; install cua-driver >= 0.20.0 and grant Accessibility/Screen Recording/display access: %w", err)
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, err := probe(probeCtx, path, []string{"manifest"}, sanitizedEnv(cfg))
		cancel()
		if err != nil {
			return nil, fmt.Errorf("cua-driver manifest probe failed; repair configured driver: %w", err)
		}
		binary, args, err := manifestInvocation(raw, path)
		if err != nil {
			return nil, err
		}
		d.binary = binary
		if len(cfg.Args) > 0 {
			args = append([]string(nil), cfg.Args...)
		}
		if cfg.PermissionMode != "standard" {
			if err = d.startDaemon(ctx, stateDir); err != nil {
				return nil, err
			}
			args = append(args, "--embedded", "--socket", d.socket)
		}
		if noOverlay(cfg) {
			args = append(args, "--no-overlay")
		}
		cmd := exec.Command(binary, args...)
		cmd.Env = sanitizedEnv(cfg)
		cmd.Stderr = io.Discard
		transport = &mcp.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "saber-computer", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("cua-driver MCP connection failed: %w", err), d.close())
	}
	d.session = session
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, errors.Join(err, d.close())
		}
		d.tools[tool.Name] = tool
	}
	for _, name := range []string{"start_session", "end_session", "list_windows", "get_window_state"} {
		if d.tools[name] == nil {
			return nil, errors.Join(fmt.Errorf("cua-driver live MCP contract is missing %s; repair/update driver", name), d.close())
		}
	}
	if _, err = d.call(ctx, "start_session", map[string]any{}); err != nil {
		return nil, errors.Join(err, d.close())
	}
	if d.tools["set_config"] != nil && cfg.MaxImageDimension > 0 {
		if err = d.setConfig(ctx, "max_image_dimension", cfg.MaxImageDimension); err != nil {
			return nil, errors.Join(err, d.close())
		}
	}
	if d.tools["set_agent_cursor_enabled"] != nil && noOverlay(cfg) {
		if _, err = d.call(ctx, "set_agent_cursor_enabled", map[string]any{"enabled": false, "cursor_id": d.label}); err != nil {
			return nil, errors.Join(err, d.close())
		}
	}
	return d, nil
}
func (d *driver) property(tool, property string) bool {
	t := d.tools[tool]
	if t == nil {
		return false
	}
	schema, ok := t.InputSchema.(map[string]any)
	if !ok {
		return false
	}
	properties, _ := schema["properties"].(map[string]any)
	_, ok = properties[property]
	return ok
}
func (d *driver) call(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	if d.session == nil {
		return nil, errors.New("computer driver disconnected; capture fresh state after reconnect")
	}
	if d.tools[name] == nil {
		return nil, fmt.Errorf("connected cua-driver does not advertise %s", name)
	}
	payload := make(map[string]any, len(args)+1)
	for k, v := range args {
		if k == "session" {
			return nil, errors.New("driver session is owned by trusted host")
		}
		payload[k] = v
	}
	if d.property(name, "session") {
		payload["session"] = d.label
	}
	out, err := d.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: payload})
	if err != nil {
		return nil, fmt.Errorf("cua-driver %s dispatched outcome unknown; not replayed, capture fresh state: %w", name, err)
	}
	if out.IsError {
		return out, fmt.Errorf("cua-driver refused %s: %s", name, resultText(out))
	}
	return out, nil
}
func resultText(out *mcp.CallToolResult) string {
	var parts []string
	if out == nil {
		return ""
	}
	for _, part := range out.Content {
		if text, ok := part.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	if len(parts) == 0 && out.StructuredContent != nil {
		raw, _ := json.Marshal(out.StructuredContent)
		parts = append(parts, string(raw))
	}
	return strings.Join(parts, "\n")
}
func resultObject(out *mcp.CallToolResult) map[string]any {
	if out == nil {
		return nil
	}
	if data, ok := out.StructuredContent.(map[string]any); ok {
		return data
	}
	raw, _ := json.Marshal(out.StructuredContent)
	var data map[string]any
	if json.Unmarshal(raw, &data) == nil && data != nil {
		return data
	}
	for _, part := range out.Content {
		if text, ok := part.(*mcp.TextContent); ok {
			if json.Unmarshal([]byte(text.Text), &data) == nil && data != nil {
				return data
			}
		}
	}
	return nil
}

func (d *driver) startDaemon(ctx context.Context, stateDir string) error {
	if err := os.MkdirAll(filepath.Join(stateDir, "computer"), 0700); err != nil {
		return err
	}
	d.socket = filepath.Join(os.TempDir(), "sc-"+randomID()[:12]+".sock")
	if runtime.GOOS == "windows" {
		d.socket = `\\.\pipe\saber-cua-` + randomID()[:12]
	}
	args := []string{"serve", "--embedded", "--socket", d.socket, "--no-permissions-gate", "--permission-mode", d.cfg.PermissionMode}
	if d.cfg.PermissionMode == "unrestricted" {
		args = append(args, "--dangerously-bypass-approvals")
	}
	if d.cfg.CapabilityManifest != "" {
		path, err := filepath.Abs(d.cfg.CapabilityManifest)
		if err != nil {
			return err
		}
		if st, e := os.Stat(path); e != nil || !st.Mode().IsRegular() {
			return errors.New("computer capability manifest must be a readable regular file")
		}
		if _, e := os.ReadFile(path); e != nil {
			return fmt.Errorf("capability manifest unreadable: %w", e)
		}
		args = append(args, "--capability-manifest", path, "--approve-capability-manifest")
	}
	if noOverlay(d.cfg) {
		args = append(args, "--no-overlay")
	}
	binary := d.binary
	if runtime.GOOS == "darwin" {
		resolved, e := filepath.EvalSymlinks(binary)
		if e != nil {
			return e
		}
		at := strings.Index(resolved, ".app/")
		if at < 0 {
			return errors.New("private macOS cua-driver runtime must use the installed CuaDriver.app bundle for stable Accessibility/Screen Recording identity")
		}
		app := resolved[:at+4]
		if !d.cfg.AllowUnsignedDriver {
			verifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			cmd := exec.CommandContext(verifyCtx, "/usr/bin/codesign", "--verify", "--deep", "--strict", app)
			cmd.Stderr = io.Discard
			err := cmd.Run()
			cancel()
			if err != nil {
				return errors.New("CuaDriver.app signature verification failed; allow_unsigned_driver only for explicitly trusted development builds")
			}
			infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			cmd = exec.CommandContext(infoCtx, "/usr/bin/codesign", "-d", "--verbose=4", app)
			info, err := cmd.CombinedOutput()
			cancel()
			if err != nil {
				return err
			}
			text := string(info)
			if !strings.Contains(text, "Identifier=com.trycua.driver\n") || (!strings.Contains(text, "TeamIdentifier=4YEC26S9KF\n") && !strings.Contains(text, "TeamIdentifier=YCK386LBJ7\n")) {
				return errors.New("CuaDriver.app has an untrusted bundle identifier or signing team")
			}
		}
		binary = "/usr/bin/open"
		args = append([]string{"-n", "-g", "-a", app, "--args"}, args...)
	}
	cmd := exec.Command(binary, args...)
	cmd.Env = append(sanitizedEnv(d.cfg), "CUA_DRIVER_PERMISSION_MODE="+d.cfg.PermissionMode)
	if d.cfg.PermissionMode == "unrestricted" {
		cmd.Env = append(cmd.Env, "CUA_DRIVER_DANGEROUSLY_BYPASS_APPROVALS=1")
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	d.daemon = cmd
	d.daemonDone = make(chan struct{})
	go func() { _ = cmd.Wait(); close(d.daemonDone) }()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		_, err := probe(probeCtx, d.binary, []string{"status", "--socket", d.socket}, sanitizedEnv(d.cfg))
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), d.close())
		case <-deadline.C:
			return errors.Join(errors.New("private cua-driver daemon did not become ready; run computer_use doctor"), d.close())
		case <-d.daemonDone:
			if runtime.GOOS != "darwin" {
				return errors.Join(errors.New("cua-driver daemon exited during launch"), d.close())
			}
			select {
			case <-ctx.Done():
				return errors.Join(ctx.Err(), d.close())
			case <-tick.C:
			}
		case <-tick.C:
		}
	}
}
func (d *driver) close() error {
	var err error
	if d.session != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if d.tools["end_session"] != nil {
			_, e := d.call(ctx, "end_session", map[string]any{})
			err = errors.Join(err, e)
		}
		cancel()
		err = errors.Join(err, d.session.Close())
		d.session = nil
	}
	if d.socket != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, e := probe(ctx, d.binary, []string{"stop", "--socket", d.socket}, sanitizedEnv(d.cfg))
		cancel()
		err = errors.Join(err, e)
		if d.daemon != nil {
			select {
			case <-d.daemonDone:
			default:
				if e = d.daemon.Process.Kill(); e != nil && !errors.Is(e, os.ErrProcessDone) {
					err = errors.Join(err, e)
				}
				select {
				case <-d.daemonDone:
				case <-time.After(3 * time.Second):
					err = errors.Join(err, errors.New("private cua-driver process did not stop"))
				}
			}
		}
		if runtime.GOOS != "windows" {
			if e = os.Remove(d.socket); e != nil && !errors.Is(e, os.ErrNotExist) {
				err = errors.Join(err, e)
			}
		}
		d.socket = ""
	}
	return err
}
