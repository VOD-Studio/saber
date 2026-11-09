package execution

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"time"
)

type remoteEnvironment struct {
	transport  remoteTransport
	spec       sessionSpec
	root       string
	workspace  string
	record     remoteRecord
	recordFile string
	executor   *Executor
	mu         sync.Mutex
	closed     bool
	live       map[*remoteProcess]bool
	syncMu     sync.Mutex
	baseline   workspaceImage
}

func (e *remoteEnvironment) BackendPath(path string) string {
	return mapBackendPath(e.spec.Workspace, e.workspace, path)
}
func (e *remoteEnvironment) Start(ctx context.Context, command string, out io.Writer) (ownedProcess, error) {
	p, err := e.StartInteractive(ctx, launchOptions{Command: command}, out)
	if err == nil {
		err = p.CloseWrite()
	}
	return p, err
}

func (e *remoteEnvironment) StartInteractive(ctx context.Context, options launchOptions, out io.Writer) (interactiveProcess, error) {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return nil, errors.New("remote environment is closed")
	}
	if err := e.syncWorkspace(ctx); err != nil {
		return nil, err
	}
	name, err := randomName()
	if err != nil {
		return nil, err
	}
	dir := e.root + "/p-" + name
	if _, code, err := e.transport.Execute(ctx, "umask 077; mkdir -m 700 "+quoteShell(dir)+"; : > "+quoteShell(dir+"/control")+"; : > "+quoteShell(dir+"/log"), nil); err != nil || code != 0 {
		return nil, errors.Join(err, exitError(code))
	}
	if err = e.transport.Put(ctx, dir+"/supervisor.py", []byte(helper)); err != nil {
		return nil, err
	}
	env := maps.Clone(e.spec.Env)
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
	payload, err := json.Marshal(map[string]any{"command": options.Command, "env": env, "pty": options.PTY, "rows": rows, "cols": cols, "cwd": e.workspace, "control_path": dir + "/control", "pid_path": dir + "/pid", "separate_stderr": options.Stderr != nil})
	if err != nil {
		return nil, err
	}
	if err = e.transport.Put(ctx, dir+"/request", append(payload, '\n')); err != nil {
		return nil, err
	}
	command := "nohup python3 -I " + quoteShell(dir+"/supervisor.py") + " < " + quoteShell(dir+"/request") + " > " + quoteShell(dir+"/log") + " 2>&1 & echo $! > " + quoteShell(dir+"/supervisor_pid")
	if _, code, err := e.transport.Execute(ctx, command, nil); err != nil || code != 0 {
		return nil, errors.Join(err, exitError(code))
	}
	p := &remoteProcess{environment: e, dir: dir, out: out, stderr: options.Stderr, done: make(chan struct{}), started: make(chan struct{})}
	e.mu.Lock()
	if e.live == nil {
		e.live = map[*remoteProcess]bool{}
	}
	e.live[p] = true
	e.mu.Unlock()
	go p.read()
	select {
	case <-p.started:
		p.mu.Lock()
		err = p.err
		p.mu.Unlock()
		if err != nil {
			return nil, err
		}
		go p.watch(ctx)
		return p, nil
	case <-ctx.Done():
		_ = p.Stop(context.Background())
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		_ = p.Stop(context.Background())
		return nil, errors.New("remote supervisor did not acknowledge process start")
	}
}

func (e *remoteEnvironment) Close(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	live := make([]*remoteProcess, 0, len(e.live))
	for p := range e.live {
		live = append(live, p)
	}
	e.mu.Unlock()
	var errs []error
	for _, p := range live {
		if err := p.Stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if err := e.syncWorkspace(ctx); err != nil {
		errs = append(errs, err)
	}
	if e.record.Provider == "ssh" {
		_, code, err := e.transport.Execute(ctx, "rm -rf -- "+quoteShell(e.root), nil)
		if err != nil || code != 0 {
			errs = append(errs, errors.Join(err, exitError(code)))
		}
	}
	if err := e.transport.Close(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	e.record.State = envClosed
	return writeRemoteRecord(e.recordFile, e.record)
}

type remoteProcess struct {
	environment *remoteEnvironment
	dir         string
	out         io.Writer
	stderr      io.Writer
	mu          sync.Mutex
	done        chan struct{}
	started     chan struct{}
	startOnce   sync.Once
	pgid        int
	code        int
	err         error
	exited      bool
}

func (p *remoteProcess) read() {
	defer close(p.done)
	defer p.startOnce.Do(func() { close(p.started) })
	defer func() { p.environment.mu.Lock(); delete(p.environment.live, p); p.environment.mu.Unlock() }()
	offset := 0
	pending := []byte{}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		script := `import base64,sys;f=open(sys.argv[1],"rb");f.seek(int(sys.argv[2]));sys.stdout.write(base64.b64encode(f.read(65536)).decode())`
		data, code, err := p.environment.transport.Execute(ctx, "python3 -I -c "+quoteShell(script)+" "+quoteShell(p.dir+"/log")+" "+fmt.Sprint(offset), nil)
		cancel()
		if err != nil || code != 0 {
			p.fail(errors.Join(err, exitError(code)))
			return
		}
		chunk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			p.fail(fmt.Errorf("remote log transport is invalid: %w", err))
			return
		}
		offset += len(chunk)
		pending = append(pending, chunk...)
		if len(pending) > 1<<20 {
			p.fail(errors.New("remote supervisor frame exceeds 1 MiB"))
			return
		}
		for {
			index := bytes.IndexByte(pending, '\n')
			if index < 0 {
				break
			}
			line := pending[:index]
			pending = pending[index+1:]
			if err := p.frame(line); err != nil {
				p.fail(err)
				return
			}
		}
		p.mu.Lock()
		exited := p.exited
		p.mu.Unlock()
		if exited {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
func (p *remoteProcess) fail(err error) {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
	p.environment.executor.blocked.Store(p.environment.spec.Workspace, true)
}
func (p *remoteProcess) frame(line []byte) error {
	var frame struct {
		Type     string `json:"type"`
		PGID     int    `json:"pgid"`
		Data     string `json:"data"`
		ExitCode *int   `json:"exit_code"`
		Message  string `json:"message"`
		Stream   string `json:"stream"`
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		return fmt.Errorf("invalid remote supervisor frame: %w", err)
	}
	switch frame.Type {
	case "started":
		if frame.PGID <= 0 {
			return errors.New("remote supervisor returned invalid process group")
		}
		p.mu.Lock()
		p.pgid = frame.PGID
		p.mu.Unlock()
		p.startOnce.Do(func() { close(p.started) })
	case "output":
		data, err := base64.StdEncoding.DecodeString(frame.Data)
		if err != nil {
			return err
		}
		target := p.out
		if frame.Stream == "stderr" && p.stderr != nil {
			target = p.stderr
		}
		n, err := target.Write(data)
		if err != nil {
			return err
		}
		if n != len(data) {
			return io.ErrShortWrite
		}
	case "exit":
		if frame.ExitCode == nil {
			return errors.New("remote exit frame has no exit code")
		}
		p.mu.Lock()
		p.code = *frame.ExitCode
		p.exited = true
		p.mu.Unlock()
	case "error":
		return errors.New(frame.Message)
	default:
		return fmt.Errorf("unknown remote supervisor frame %q", frame.Type)
	}
	return nil
}
func (p *remoteProcess) Wait() (int, error) {
	<-p.done
	p.mu.Lock()
	code, err := p.code, p.err
	exited := p.exited
	p.mu.Unlock()
	if err != nil {
		return -1, err
	}
	if !exited {
		return -1, errors.New("remote process state is unknown")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return code, p.environment.syncWorkspace(ctx)
}
func (p *remoteProcess) watch(ctx context.Context) {
	select {
	case <-ctx.Done():
		_ = p.Stop(context.Background())
	case <-p.done:
	}
}
func (p *remoteProcess) send(frame map[string]any) error {
	select {
	case <-p.done:
		return io.ErrClosedPipe
	default:
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, code, err := p.environment.transport.Execute(ctx, "base64 -d >> "+quoteShell(p.dir+"/control"), []byte(base64.StdEncoding.EncodeToString(append(payload, '\n'))))
	return errors.Join(err, exitError(code))
}
func (p *remoteProcess) Write(data []byte) (int, error) {
	if err := p.send(map[string]any{"type": "stdin", "data": base64.StdEncoding.EncodeToString(data)}); err != nil {
		return 0, err
	}
	return len(data), nil
}
func (p *remoteProcess) CloseWrite() error { return p.send(map[string]any{"type": "eof"}) }
func (p *remoteProcess) Resize(rows, cols int) error {
	return p.send(map[string]any{"type": "resize", "rows": rows, "cols": cols})
}
func (p *remoteProcess) Signal(signal int) error {
	p.mu.Lock()
	pgid := p.pgid
	p.mu.Unlock()
	if pgid <= 0 {
		return errors.New("remote process group has not been acknowledged")
	}
	request, _ := json.Marshal(map[string]any{"control": true, "pgid": pgid, "signal": signal})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, code, err := p.environment.transport.Execute(ctx, "python3 -I -c "+quoteShell(helper), append(request, '\n'))
	return errors.Join(err, exitError(code))
}
func (p *remoteProcess) Stop(ctx context.Context) error {
	select {
	case <-p.done:
		p.mu.Lock()
		err := p.err
		p.mu.Unlock()
		return err
	default:
	}
	select {
	case <-p.started:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return errors.New("cannot safely stop unacknowledged remote process")
	}
	if err := p.Signal(15); err != nil {
		return err
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	if err := p.Signal(9); err != nil {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(15 * time.Second):
		return errors.New("remote process did not acknowledge termination")
	}
}
