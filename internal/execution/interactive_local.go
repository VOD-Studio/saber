package execution

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const cleanupTimeout = 15 * time.Second

func (l *localEnvironment) BackendPath(path string) string { return path }

func (l *localEnvironment) StartInteractive(ctx context.Context, options launchOptions, out io.Writer) (interactiveProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dead {
		return nil, errors.New("local environment is closed")
	}
	cmd := exec.Command("/bin/sh", "-c", options.Command)
	cmd.Dir = l.dir
	cmd.Env = append([]string(nil), l.env...)
	for key, value := range options.Env {
		prefix := key + "="
		for i := range cmd.Env {
			if strings.HasPrefix(cmd.Env[i], prefix) {
				cmd.Env = append(cmd.Env[:i], cmd.Env[i+1:]...)
				break
			}
		}
		cmd.Env = append(cmd.Env, prefix+value)
	}
	p := &localProcess{cmd: cmd, done: make(chan struct{})}
	if options.PTY {
		rows, cols := options.Rows, options.Cols
		if rows == 0 {
			rows = 24
		}
		if cols == 0 {
			cols = 80
		}
		file, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
		if err != nil {
			return nil, err
		}
		p.tty = file
		p.input = file
		p.pumpDone = make(chan struct{})
		go func() {
			_, err := io.Copy(out, file)
			if errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed) {
				err = nil
			}
			p.pumpErr = err
			close(p.pumpDone)
		}()
	} else {
		cmd.Stdout = out
		cmd.Stderr = out
		if options.Stderr != nil {
			cmd.Stderr = options.Stderr
		}
		input, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		p.input = input
		setProcessGroup(cmd)
		if err = cmd.Start(); err != nil {
			_ = input.Close()
			return nil, err
		}
	}
	p.pgid = processGroup(cmd)
	if l.live == nil {
		l.live = make(map[*localProcess]struct{})
	}
	l.live[p] = struct{}{}
	p.onExit = func() { l.mu.Lock(); delete(l.live, p); l.mu.Unlock() }
	go p.watch(ctx)
	return p, nil
}

func (p *localProcess) Write(data []byte) (int, error) {
	p.inputMu.Lock()
	defer p.inputMu.Unlock()
	if p.input == nil {
		return 0, io.ErrClosedPipe
	}
	return p.input.Write(data)
}
func (p *localProcess) CloseWrite() error {
	p.inputMu.Lock()
	defer p.inputMu.Unlock()
	if p.input == nil {
		return nil
	}
	if p.tty != nil {
		_, err := p.tty.Write([]byte{4})
		return err
	}
	err := p.input.Close()
	p.input = nil
	return err
}
func (p *localProcess) Resize(rows, cols int) error {
	if p.tty == nil {
		return errors.New("process has no PTY")
	}
	return pty.Setsize(p.tty, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}
func (p *localProcess) Signal(signal int) error {
	if p.pgid <= 0 {
		return errors.New("process group unavailable")
	}
	return signalGroup(p.pgid, signal)
}
