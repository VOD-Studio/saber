package execution

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/config"
)

//go:embed cloud_bridge.py
var cloudBridgeSource string

type remoteTransport interface {
	Execute(context.Context, string, []byte) ([]byte, int, error)
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
	Close(context.Context) error
}

type remoteRecord struct {
	Provider  string `json:"provider"`
	Resource  string `json:"resource"`
	Root      string `json:"root"`
	Workspace string `json:"workspace"`
	TaskID    int64  `json:"task_id"`
	State     string `json:"state"`
}

func randomName() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func writeRemoteRecord(file string, record remoteRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(file), ".remote-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if err = temp.Chmod(0600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	err = errors.Join(err, temp.Close())
	if err != nil {
		return err
	}
	return os.Rename(temp.Name(), file)
}

type sshBackend struct {
	cfg      config.SSHExecutionConfig
	executor *Executor
}

func (b *sshBackend) Open(ctx context.Context, spec sessionSpec) (environment, error) {
	if _, err := exec.LookPath("ssh"); err != nil {
		return nil, fmt.Errorf("SSH backend requires OpenSSH ssh: %w", err)
	}
	if _, err := os.Stat(b.cfg.KnownHosts); err != nil {
		return nil, fmt.Errorf("SSH known_hosts unavailable: %w", err)
	}
	name, err := randomName()
	if err != nil {
		return nil, err
	}
	root := path.Join(b.cfg.RemoteRoot, "saber-"+name)
	transport := &sshTransport{cfg: b.cfg}
	if _, code, err := transport.Execute(ctx, "umask 077; mkdir -m 700 "+quoteShell(root)+" && mkdir -m 700 "+quoteShell(root+"/workspace"), nil); err != nil || code != 0 {
		return nil, fmt.Errorf("create SSH task workspace: %w", errors.Join(err, exitError(code)))
	}
	record := remoteRecord{Provider: "ssh", Root: root, Workspace: spec.Workspace, TaskID: spec.TaskID, State: envActive}
	recordFile := filepath.Join(spec.LogDir, "task-"+strconv.FormatInt(spec.TaskID, 10), "remote.json")
	if err = writeRemoteRecord(recordFile, record); err != nil {
		_, _, _ = transport.Execute(context.Background(), "rm -rf -- "+quoteShell(root), nil)
		return nil, err
	}
	e := &remoteEnvironment{transport: transport, spec: spec, root: root, workspace: root + "/workspace", record: record, recordFile: recordFile, executor: b.executor}
	if err = e.syncWorkspace(ctx); err != nil {
		_ = e.Close(context.Background())
		return nil, err
	}
	return e, nil
}
func (b *sshBackend) Recover(ctx context.Context) error { return b.executor.recoverRemote(ctx, "ssh") }

type sshTransport struct{ cfg config.SSHExecutionConfig }

func (t *sshTransport) Execute(ctx context.Context, command string, input []byte) ([]byte, int, error) {
	args := []string{"-F", "/dev/null", "-T", "-oBatchMode=yes", "-oStrictHostKeyChecking=yes", "-oIdentitiesOnly=yes", "-oIdentityAgent=none", "-oConnectTimeout=10", "-oServerAliveInterval=15", "-oServerAliveCountMax=2", "-oUserKnownHostsFile=" + t.cfg.KnownHosts, "-p", strconv.Itoa(t.cfg.Port)}
	if t.cfg.KeyPath != "" {
		args = append(args, "-i", t.cfg.KeyPath)
	}
	args = append(args, t.cfg.User+"@"+t.cfg.Host, "/bin/sh -c "+quoteShell(command))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	cmd.Stdin = bytes.NewReader(input)
	cmd.WaitDelay = 2 * time.Second
	out := &limitedBuffer{limit: 96 << 20}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, -1, ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if exit.ExitCode() == 255 {
				return nil, -1, fmt.Errorf("SSH transport failed: %s", out.String())
			}
			return out.Bytes(), exit.ExitCode(), nil
		}
		return nil, -1, err
	}
	return out.Bytes(), 0, nil
}
func (t *sshTransport) Put(ctx context.Context, file string, data []byte) error {
	_, code, err := t.Execute(ctx, "umask 077; base64 -d > "+quoteShell(file), []byte(base64.StdEncoding.EncodeToString(data)))
	return errors.Join(err, exitError(code))
}
func (t *sshTransport) Get(ctx context.Context, file string) ([]byte, error) {
	out, code, err := t.Execute(ctx, "base64 "+quoteShell(file), nil)
	if err != nil || code != 0 {
		return nil, errors.Join(err, exitError(code))
	}
	return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(out)), ""))
}
func (t *sshTransport) Close(context.Context) error { return nil }

func exitError(code int) error {
	if code == 0 {
		return nil
	}
	return fmt.Errorf("remote command exited with code %d", code)
}

type cloudBackend struct {
	cfg      config.ExecutionConfig
	name     string
	executor *Executor
}

func (b *cloudBackend) Open(ctx context.Context, spec sessionSpec) (environment, error) {
	name, err := randomName()
	if err != nil {
		return nil, err
	}
	root := "/tmp/saber-" + name
	workspace := root + "/workspace"
	if b.name == "vercel" {
		workspace = "/vercel/sandbox/saber-" + name
	}
	file := filepath.Join(spec.LogDir, "task-"+strconv.FormatInt(spec.TaskID, 10), "remote.json")
	record := remoteRecord{Provider: b.name, Root: root, Workspace: spec.Workspace, TaskID: spec.TaskID, State: envActive}
	if err = writeRemoteRecord(file, record); err != nil {
		return nil, err
	}
	transport, resource, err := b.connect(ctx, spec, "", root, file)
	if err != nil {
		return nil, err
	}
	record.Resource = resource
	if err = writeRemoteRecord(file, record); err != nil {
		_ = transport.Close(context.Background())
		return nil, err
	}
	e := &remoteEnvironment{transport: transport, spec: spec, root: root, workspace: workspace, record: record, recordFile: file, executor: b.executor}
	if _, code, err := transport.Execute(ctx, "umask 077; mkdir -p "+quoteShell(root)+" "+quoteShell(workspace)+"; chmod 700 "+quoteShell(root)+" "+quoteShell(workspace), nil); err != nil || code != 0 {
		_ = e.Close(context.Background())
		return nil, errors.Join(err, exitError(code))
	}
	if err = e.syncWorkspace(ctx); err != nil {
		_ = e.Close(context.Background())
		return nil, err
	}
	return e, nil
}
func (b *cloudBackend) Recover(ctx context.Context) error {
	return b.executor.recoverRemote(ctx, b.name)
}
func (b *cloudBackend) connect(ctx context.Context, spec sessionSpec, resource, root, file string) (remoteTransport, string, error) {
	command := []string{b.cfg.Remote.Python, "-u", "-c", cloudBridgeSource}
	env := maps.Clone(b.cfg.Remote.Credentials)
	if extension, ok := b.cfg.Extensions[b.name]; ok {
		command = extension.Command
		env = extension.Env
	}
	bridge, err := startSDKBridge(command, env)
	if err != nil {
		return nil, "", err
	}
	request := map[string]any{"op": "open", "provider": b.name, "resource": resource, "image": b.cfg.Image, "cpu": b.cfg.Remote.CPU, "memory_mb": b.cfg.Remote.MemoryMB, "disk_mb": b.cfg.Remote.DiskMB, "lifetime_seconds": b.cfg.Remote.LifetimeSeconds, "network": b.cfg.IsNetworkEnabled(), "name": "saber-" + b.executor.owner[:12] + "-" + strconv.FormatInt(spec.TaskID, 10), "owner": b.executor.owner, "runtime": b.cfg.Remote.VercelRuntime, "vercel_image": b.cfg.Remote.VercelImage, "state_file": file, "record": remoteRecord{Provider: b.name, Root: root, Workspace: spec.Workspace, TaskID: spec.TaskID, State: envActive}}
	openCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 300*time.Second)
	defer cancel()
	result, err := bridge.call(openCtx, request)
	if err != nil {
		_ = bridge.stop()
		return nil, "", fmt.Errorf("%s SDK unavailable or sandbox open failed: %w", b.name, err)
	}
	id, _ := result["resource"].(string)
	if id == "" {
		_ = bridge.stop()
		return nil, "", errors.New("execution backend did not return a sandbox resource ID")
	}
	if err = ctx.Err(); err != nil {
		_ = bridge.Close(context.Background())
		return nil, "", err
	}
	return bridge, id, nil
}

type sdkBridge struct {
	cmd         *exec.Cmd
	input       io.WriteCloser
	reader      *bufio.Reader
	mu          sync.Mutex
	diagnostics *limitedBuffer
	secrets     []string
	closed      bool
}

func startSDKBridge(command []string, env map[string]string) (*sdkBridge, error) {
	if len(command) == 0 {
		return nil, errors.New("cloud SDK adapter command is empty")
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "VERCEL_TELEMETRY_DISABLED=1"}
	bridge := &sdkBridge{cmd: cmd, diagnostics: &limitedBuffer{limit: 8192}}
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
		if value != "" {
			bridge.secrets = append(bridge.secrets, value)
		}
	}
	cmd.Stderr = bridge.diagnostics
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	bridge.input = input
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	bridge.reader = bufio.NewReaderSize(output, 64<<10)
	if err = cmd.Start(); err != nil {
		_ = input.Close()
		return nil, err
	}
	return bridge, nil
}
func (b *sdkBridge) call(ctx context.Context, request map[string]any) (map[string]any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errors.New("SDK bridge is closed")
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err = b.input.Write(append(data, '\n')); err != nil {
		return nil, err
	}
	type response struct {
		line []byte
		err  error
	}
	ch := make(chan response, 1)
	go func() { line, err := b.reader.ReadBytes('\n'); ch <- response{line, err} }()
	select {
	case <-ctx.Done():
		b.closed = true
		_ = b.cmd.Process.Kill()
		_ = b.cmd.Wait()
		return nil, ctx.Err()
	case reply := <-ch:
		if reply.err != nil {
			return nil, fmt.Errorf("SDK transport ended: %w: %s", reply.err, b.redact(b.diagnostics.String()))
		}
		if len(reply.line) > 96<<20 {
			return nil, errors.New("SDK response exceeds 96 MiB")
		}
		var body struct {
			Result map[string]any `json:"result"`
			Error  string         `json:"error"`
		}
		if err = json.Unmarshal(reply.line, &body); err != nil {
			return nil, fmt.Errorf("invalid SDK response: %w", err)
		}
		if body.Error != "" {
			return nil, errors.New(b.redact(body.Error))
		}
		return body.Result, nil
	}
}
func (b *sdkBridge) redact(text string) string {
	for _, secret := range b.secrets {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}
func (b *sdkBridge) Execute(ctx context.Context, command string, input []byte) ([]byte, int, error) {
	request := map[string]any{"op": "exec", "command": command}
	if input != nil {
		request["stdin"] = base64.StdEncoding.EncodeToString(input)
	}
	result, err := b.call(ctx, request)
	if err != nil {
		return nil, -1, err
	}
	output, _ := result["output"].(string)
	data, err := base64.StdEncoding.DecodeString(output)
	code, valid := intArgument(result["exit_code"])
	if !valid {
		return nil, -1, errors.New("SDK response has no exit_code")
	}
	return data, code, err
}
func (b *sdkBridge) Put(ctx context.Context, file string, data []byte) error {
	_, err := b.call(ctx, map[string]any{"op": "put", "path": file, "data": base64.StdEncoding.EncodeToString(data)})
	return err
}
func (b *sdkBridge) Get(ctx context.Context, file string) ([]byte, error) {
	result, err := b.call(ctx, map[string]any{"op": "get", "path": file})
	if err != nil {
		return nil, err
	}
	data, _ := result["data"].(string)
	return base64.StdEncoding.DecodeString(data)
}
func (b *sdkBridge) stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	_ = b.input.Close()
	return b.cmd.Wait()
}
func (b *sdkBridge) Close(ctx context.Context) error {
	_, err := b.call(ctx, map[string]any{"op": "close"})
	return errors.Join(err, b.stop())
}

func (e *Executor) recoverRemote(ctx context.Context, provider string) error {
	entries, err := os.ReadDir(e.cfg.LogDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "task-") {
			continue
		}
		file := filepath.Join(e.cfg.LogDir, entry.Name(), "remote.json")
		data, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var record remoteRecord
		if err = json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.Provider != provider || record.State != envActive {
			continue
		}
		var transport remoteTransport
		if provider == "ssh" {
			transport = &sshTransport{cfg: e.cfg.SSH}
		} else {
			if record.Resource == "" {
				return errors.New("remote creation was interrupted before its resource was recorded; inspect provider sandbox inventory before clearing quarantine")
			}
			b := e.backend(provider).(*cloudBackend)
			transport, _, err = b.connect(ctx, sessionSpec{TaskID: record.TaskID, Workspace: record.Workspace}, record.Resource, record.Root, file)
			if err != nil {
				return err
			}
		}
		if provider == "ssh" {
			if record.Root == "" || !strings.HasPrefix(record.Root, path.Join(e.cfg.SSH.RemoteRoot, "saber-")) {
				return errors.New("invalid recorded SSH cleanup root")
			}
			_, code, runErr := transport.Execute(ctx, "python3 -I -c "+quoteShell(sshRecoverySource)+" "+quoteShell(record.Root), nil)
			if runErr != nil || code != 0 {
				return errors.Join(runErr, exitError(code))
			}
		}
		if err = transport.Close(ctx); err != nil {
			return err
		}
		record.State = envClosed
		if err = writeRemoteRecord(file, record); err != nil {
			return err
		}
		if err = e.markEnvironmentClosed(environmentRecord{TaskID: record.TaskID, Backend: provider, Workspace: record.Workspace}); err != nil {
			return err
		}
		e.blocked.Delete(record.Workspace)
	}
	return nil
}
