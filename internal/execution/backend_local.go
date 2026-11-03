package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// localBackend 在宿主用户权限下运行命令；个人/群隔离只发生在程序层。
type localBackend struct{ executor *Executor }

// Open 为任务创建私有临时目录和冻结环境，不复制机器人凭据。
func (b *localBackend) Open(ctx context.Context, spec sessionSpec) (environment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", fmt.Sprintf("saber-task-%d-", spec.TaskID))
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(tmp, 0700); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	env := []string{}
	values := map[string]string{}
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	for key, value := range spec.Env {
		if key == "TMPDIR" {
			continue
		}
		values[key] = value
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	env = append(env, "TMPDIR="+tmp)
	return &localEnvironment{dir: spec.Workspace, env: env, tmp: tmp}, nil
}

func (b *localBackend) Recover(context.Context) error { return nil }

type localEnvironment struct {
	dir  string
	env  []string
	tmp  string
	mu   sync.Mutex
	live map[*localProcess]struct{}
	dead bool
}

// Start 启动独立进程组的 /bin/sh；输出合流写入任务归档。
func (l *localEnvironment) Start(ctx context.Context, command string, out io.Writer) (ownedProcess, error) {
	l.mu.Lock()
	if l.dead {
		l.mu.Unlock()
		return nil, errors.New("local environment is closed")
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = l.dir
	cmd.Env = append([]string(nil), l.env...)
	cmd.Stdout = out
	cmd.Stderr = out
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		l.mu.Unlock()
		return nil, err
	}
	process := &localProcess{cmd: cmd, pgid: processGroup(cmd), done: make(chan struct{})}
	if l.live == nil {
		l.live = make(map[*localProcess]struct{})
	}
	l.live[process] = struct{}{}
	l.mu.Unlock()
	go process.watch(ctx)
	return process, nil
}

func (l *localEnvironment) Close(context.Context) error {
	l.mu.Lock()
	l.dead = true
	live := make([]*localProcess, 0, len(l.live))
	for process := range l.live {
		live = append(live, process)
	}
	l.mu.Unlock()
	var errs []error
	for _, process := range live {
		if err := process.Stop(context.Background()); err != nil {
			errs = append(errs, err)
		}
	}
	errs = append(errs, os.RemoveAll(l.tmp))
	return errors.Join(errs...)
}

type localProcess struct {
	cmd  *exec.Cmd
	pgid int
	once sync.Once
	done chan struct{}
	code int
	err  error
}

// watch 在调用方取消时终止整组进程，不等待 Wait 主动检查。
func (p *localProcess) watch(ctx context.Context) {
	select {
	case <-ctx.Done():
		_ = p.Stop(context.Background())
	case <-p.done:
	}
}

// Wait 只等待一次并缓存结果；shell 退出后仍清理同组残留子进程。
func (p *localProcess) Wait() (int, error) {
	p.once.Do(func() {
		err := p.cmd.Wait()
		p.code = 0
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				p.code = exit.ExitCode()
				err = nil
			} else {
				p.code = -1
			}
		}
		p.err = err
		if p.pgid > 0 {
			cleanupProcessGroup(p.pgid)
		}
		close(p.done)
	})
	<-p.done
	return p.code, p.err
}

// Stop 先 TERM 整组，最多等待 2 秒后 KILL；可重复调用。
func (p *localProcess) Stop(ctx context.Context) error {
	if p.pgid <= 0 {
		return nil
	}
	_ = terminateGroup(p.pgid)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
	}
	_ = killGroup(p.pgid)
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// cleanupProcessGroup 在正常退出后清理 shell 遗留的同组子进程。
func cleanupProcessGroup(pgid int) {
	if !groupExists(pgid) {
		return
	}
	_ = terminateGroup(pgid)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !groupExists(pgid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = killGroup(pgid)
}
