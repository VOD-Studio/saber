package execution

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/config"
)

// dockerBackend 为每个任务维护一个长期容器；命令通过 docker exec 运行。
type dockerBackend struct {
	cfg      config.ExecutionConfig
	owner    string
	executor *Executor
}

func docker(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	// Docker CLI 只获取连接 daemon 所需变量，不携带机器人凭据。
	for _, key := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if v, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

func (b *dockerBackend) remove(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := docker(ctx, "rm", "--force", name).CombinedOutput()
	if err != nil && !strings.Contains(string(output), "No such container") {
		return fmt.Errorf("cannot terminate task container %s: %w", name, err)
	}
	return nil
}

// Recover 删除属于本执行器的残留容器；失败则禁止启动任务。
func (b *dockerBackend) Recover(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := docker(ctx, "ps", "-aq", "--filter", "label=saber.executor="+b.owner).Output()
	if err != nil {
		return fmt.Errorf("docker unavailable: %w", err)
	}
	for _, id := range strings.Fields(string(out)) {
		if err := b.remove(id); err != nil {
			return err
		}
	}
	return b.executor.finishDockerRecords(ctx)
}

func (b *dockerBackend) containerArgs(name string, spec sessionSpec) []string {
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 1 {
		uid = 65534
	}
	if gid < 1 {
		gid = 65534
	}
	networkMode := "bridge"
	if !b.cfg.IsNetworkEnabled() {
		networkMode = "none"
	}
	return []string{"create", "--pull=never", "--init", "--interactive", "--name", name,
		"--label", "saber.executor=" + b.owner, "--label", "saber.task=" + strconv.FormatInt(spec.TaskID, 10),
		"--network", networkMode, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", "128", "--memory", "512m", "--cpus", "1",
		"--user", fmt.Sprintf("%d:%d", uid, gid),
		"--mount", "type=bind,src=" + spec.Workspace + ",dst=/workspace,bind-recursive=disabled",
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=256m", "--workdir", "/workspace",
		"--entrypoint", "/usr/bin/env", b.cfg.Image, "-i", "PATH=/usr/local/bin:/usr/bin:/bin",
		"python3", "-I", "-c", "import time; time.sleep(10**9)"}
}

// Open 创建并启动任务容器；容器标签用于重启后的残留清理。
func (b *dockerBackend) Open(ctx context.Context, spec sessionSpec) (environment, error) {
	if err := checkMountFiles(ctx, spec.Workspace); err != nil {
		return nil, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	name := "saber-task-" + hex.EncodeToString(random[:])
	createCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	output, err := docker(createCtx, b.containerArgs(name, spec)...).CombinedOutput()
	cancel()
	if err != nil {
		return nil, fmt.Errorf("create task container: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err = ctx.Err(); err != nil {
		_ = b.remove(name)
		return nil, err
	}
	startCtx, startCancel := context.WithTimeout(context.Background(), 10*time.Second)
	output, err = docker(startCtx, "start", name).CombinedOutput()
	startCancel()
	if err != nil {
		_ = b.remove(name)
		return nil, fmt.Errorf("start task container: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return &dockerEnvironment{backend: b, spec: spec, name: name}, nil
}

type dockerEnvironment struct {
	backend *dockerBackend
	spec    sessionSpec
	name    string
	mu      sync.Mutex
	closed  bool
	broken  error
}

// Start 通过 docker exec 运行 helper supervisor，输出解帧后写入归档。
func (d *dockerEnvironment) Start(ctx context.Context, command string, out io.Writer) (ownedProcess, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, errors.New("task container is closed")
	}
	if d.broken != nil {
		err := d.broken
		d.mu.Unlock()
		return nil, err
	}
	d.mu.Unlock()
	if err := checkMountFiles(ctx, d.spec.Workspace); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"command": command, "env": d.spec.Env})
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("docker", "exec", "-i", d.name, "/usr/bin/env", "PATH=/usr/local/bin:/usr/bin:/bin", "python3", "-I", "-c", helper)
	for _, key := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if v, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	cmd.Stdin = bytes.NewReader(append(payload, '\n'))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	diagnostics := &limitedBuffer{limit: 8192}
	cmd.Stderr = diagnostics
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	process := &dockerProcess{environment: d, cmd: cmd, out: out, stdout: stdout, started: make(chan struct{}), done: make(chan struct{})}
	go process.read()
	select {
	case <-process.started:
		go process.watch(ctx)
		return process, nil
	case <-process.done:
		<-process.started
		if err := process.result(); err != nil {
			return nil, err
		}
		return process, nil
	case <-ctx.Done():
		_ = process.Stop(context.Background())
		return nil, ctx.Err()
	}
}

// breakEnvironment 在协议失联或清理失败后销毁容器，禁止继续执行。
func (d *dockerEnvironment) breakEnvironment(cause error) {
	d.mu.Lock()
	if d.broken == nil {
		d.broken = cause
	}
	d.mu.Unlock()
	_ = d.backend.remove(d.name)
	d.backend.executor.blocked.Store(d.spec.Workspace, true)
}

func (d *dockerEnvironment) Close(ctx context.Context) error {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	if err := d.backend.remove(d.name); err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	out, err := docker(checkCtx, "ps", "-aq", "--filter", "label=saber.task="+strconv.FormatInt(d.spec.TaskID, 10), "--filter", "label=saber.executor="+d.backend.owner).Output()
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != "" {
		return errors.New("task container remains after cleanup")
	}
	return nil
}

type dockerProcess struct {
	environment *dockerEnvironment
	cmd         *exec.Cmd
	stdout      io.ReadCloser
	out         io.Writer
	started     chan struct{}
	done        chan struct{}
	startOnce   sync.Once
	mu          sync.Mutex
	pgid        int
	code        int
	exited      bool
	err         error
	outputErr   error
}

func (p *dockerProcess) markStarted(pgid int) {
	p.mu.Lock()
	p.pgid = pgid
	p.mu.Unlock()
	p.startOnce.Do(func() { close(p.started) })
}

// read 解码 NDJSON 帧；EOF 缺少 exit 帧视为不确定执行。
func (p *dockerProcess) read() {
	defer close(p.done)
	defer p.startOnce.Do(func() { close(p.started) })
	reader := bufio.NewReaderSize(p.stdout, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if decodeErr := p.frame(line); decodeErr != nil {
				p.mu.Lock()
				p.err = decodeErr
				p.mu.Unlock()
				break
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				p.mu.Lock()
				p.err = err
				p.mu.Unlock()
			}
			break
		}
	}
	waitErr := p.cmd.Wait()
	p.mu.Lock()
	if p.err == nil && waitErr != nil {
		// docker exec 自身失败属于不确定执行，不当作命令退出码。
		p.err = waitErr
	}
	if p.err == nil && !p.exited {
		p.err = errors.New("output stream ended without exit frame")
	}
	p.mu.Unlock()
}

func (p *dockerProcess) frame(line []byte) error {
	var frame struct {
		Type     string `json:"type"`
		PGID     int    `json:"pgid"`
		Data     string `json:"data"`
		ExitCode *int   `json:"exit_code"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(line), &frame); err != nil {
		return fmt.Errorf("invalid supervisor frame: %w", err)
	}
	switch frame.Type {
	case "started":
		p.markStarted(frame.PGID)
	case "output":
		chunk, err := base64.StdEncoding.DecodeString(frame.Data)
		if err != nil {
			return fmt.Errorf("invalid output frame: %w", err)
		}
		n, err := p.out.Write(chunk)
		if err != nil {
			p.mu.Lock()
			p.outputErr = err
			p.mu.Unlock()
			return err
		}
		if n != len(chunk) {
			return io.ErrShortWrite
		}
	case "exit":
		if frame.ExitCode == nil {
			return errors.New("exit frame without exit_code")
		}
		p.mu.Lock()
		p.code, p.exited = *frame.ExitCode, true
		p.mu.Unlock()
	case "error":
		return fmt.Errorf("supervisor error: %s", frame.Message)
	default:
		return fmt.Errorf("unexpected supervisor frame %q", frame.Type)
	}
	return nil
}

// watch 在调用方取消时终止整组进程，避免后台作业继承前台超时。
func (p *dockerProcess) watch(ctx context.Context) {
	select {
	case <-ctx.Done():
		_ = p.Stop(context.Background())
	case <-p.done:
	}
}

// Wait 等待退出帧与数据进程结束，返回程序退出码。
func (p *dockerProcess) Wait() (int, error) {
	<-p.done
	return p.code, p.result()
}

func (p *dockerProcess) result() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.outputErr != nil {
		return p.outputErr
	}
	if p.err != nil {
		return p.err
	}
	return nil
}

// Stop 通过控制模式先 TERM 整组，最多 2 秒后 KILL；控制失败即销毁容器。
func (p *dockerProcess) Stop(ctx context.Context) error {
	p.mu.Lock()
	pgid := p.pgid
	p.mu.Unlock()
	if pgid <= 0 {
		if err := p.environment.control(0, 0); err != nil {
			return err
		}
		return nil
	}
	if err := p.environment.control(pgid, 15); err != nil {
		p.environment.breakEnvironment(err)
		return err
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
	}
	if err := p.environment.control(pgid, 9); err != nil {
		p.environment.breakEnvironment(err)
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// control 用未取消的短 context 向已登记进程组发信号；模型不能指定 pgid。
func (d *dockerEnvironment) control(pgid, signal int) error {
	payload, err := json.Marshal(map[string]any{"control": true, "pgid": pgid, "signal": signal})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := docker(ctx, "exec", "-i", d.name, "/usr/bin/env", "PATH=/usr/local/bin:/usr/bin:/bin", "python3", "-I", "-c", helper)
	cmd.Stdin = bytes.NewReader(append(payload, '\n'))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cannot signal task process group: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

type limitedBuffer struct {
	mu    sync.Mutex
	data  bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.data.Len() < b.limit {
		b.data.Write(p[:min(len(p), b.limit-b.data.Len())])
	}
	return len(p), nil
}

// probeDocker 启动时验证活动 Docker 工作区依赖：CLI、daemon 与预装镜像。
func probeDocker(ctx context.Context, cfg config.ExecutionConfig) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker CLI is required for backend docker: %w", err)
	}
	infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := docker(infoCtx, "info").Run(); err != nil {
		return fmt.Errorf("docker daemon is not reachable for backend docker: %w", err)
	}
	imageCtx, imageCancel := context.WithTimeout(ctx, 5*time.Second)
	defer imageCancel()
	if err := docker(imageCtx, "image", "inspect", cfg.Image).Run(); err != nil {
		return fmt.Errorf("execution image %q is not installed: %w", cfg.Image, err)
	}
	return nil
}

func checkMountFiles(ctx context.Context, dir string) error {
	// Unix socket 即使断网也可连到宿主服务；拒绝随挂载夹带 IPC/设备节点。
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&(os.ModeSocket|os.ModeDevice|os.ModeNamedPipe) != 0 {
			return fmt.Errorf("workspace contains an IPC or device node: %s", path)
		}
		return nil
	})
}
