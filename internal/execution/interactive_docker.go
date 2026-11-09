package execution

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
)

func (d *dockerEnvironment) BackendPath(path string) string {
	return mapBackendPath(d.spec.Workspace, "/workspace", path)
}

func (d *dockerEnvironment) StartInteractive(ctx context.Context, options launchOptions, out io.Writer) (interactiveProcess, error) {
	d.mu.Lock()
	closed, broken := d.closed, d.broken
	d.mu.Unlock()
	if closed {
		return nil, errors.New("task container is closed")
	}
	if broken != nil {
		return nil, broken
	}
	if err := checkMountFiles(ctx, d.spec.Workspace); err != nil {
		return nil, err
	}
	env := maps.Clone(d.spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, options.Env)
	rows, cols := options.Rows, options.Cols
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}
	payload, err := json.Marshal(map[string]any{"command": options.Command, "env": env, "pty": options.PTY, "rows": rows, "cols": cols, "cwd": "/workspace", "separate_stderr": options.Stderr != nil})
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("docker", "exec", "-i", d.name, "/usr/bin/env", "PATH=/usr/local/bin:/usr/bin:/bin", "python3", "-I", "-c", helper)
	for _, key := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	diagnostics := &limitedBuffer{limit: 8192}
	cmd.Stderr = diagnostics
	if err = cmd.Start(); err != nil {
		_ = input.Close()
		return nil, err
	}
	p := &dockerProcess{environment: d, cmd: cmd, input: input, out: out, stderr: options.Stderr, stdout: stdout, started: make(chan struct{}), done: make(chan struct{})}
	go p.read()
	if _, err = input.Write(append(payload, '\n')); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	select {
	case <-p.started:
		if err = p.result(); err != nil {
			return nil, err
		}
		go p.watch(ctx)
		return p, nil
	case <-ctx.Done():
		_ = p.Stop(context.Background())
		return nil, ctx.Err()
	}
}

func (p *dockerProcess) send(frame map[string]any) error {
	p.inputMu.Lock()
	defer p.inputMu.Unlock()
	select {
	case <-p.done:
		return io.ErrClosedPipe
	default:
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	_, err = p.input.Write(append(payload, '\n'))
	return err
}
func (p *dockerProcess) Write(data []byte) (int, error) {
	err := p.send(map[string]any{"type": "stdin", "data": base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		return 0, err
	}
	return len(data), nil
}
func (p *dockerProcess) CloseWrite() error { return p.send(map[string]any{"type": "eof"}) }
func (p *dockerProcess) Resize(rows, cols int) error {
	return p.send(map[string]any{"type": "resize", "rows": rows, "cols": cols})
}
func (p *dockerProcess) Signal(signal int) error {
	p.mu.Lock()
	pgid := p.pgid
	p.mu.Unlock()
	if pgid <= 0 {
		return errors.New("process has not started")
	}
	return p.environment.control(pgid, signal)
}
