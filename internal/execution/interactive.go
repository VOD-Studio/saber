package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/toolkit"
)

type launchOptions struct {
	Command string
	Env     map[string]string
	PTY     bool
	Rows    int
	Cols    int
	Stderr  io.Writer
}

type interactiveProcess interface {
	ownedProcess
	io.Writer
	CloseWrite() error
	Resize(int, int) error
	Signal(int) error
}

type interactiveEnvironment interface {
	StartInteractive(context.Context, launchOptions, io.Writer) (interactiveProcess, error)
	BackendPath(string) string
}

func quoteShell(text string) string { return "'" + strings.ReplaceAll(text, "'", "'\"'\"'") + "'" }

func argvCommand(argv []string) (string, error) {
	if len(argv) == 0 || argv[0] == "" {
		return "", errors.New("program command is required")
	}
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return "", errors.New("program argument contains NUL")
		}
		quoted[i] = quoteShell(arg)
	}
	return "exec " + strings.Join(quoted, " "), nil
}

func (e *Executor) interactive(ctx context.Context, options launchOptions, out io.Writer) (interactiveProcess, interactiveEnvironment, error) {
	if err := e.Check(ctx, "exec"); err != nil {
		return nil, nil, err
	}
	run, ok := ctx.Value(scopeKey{}).(scope)
	if !ok || run.id <= 0 {
		return nil, nil, errors.New("program requires a persistent task")
	}
	identity, _ := chat.IdentityFromContext(ctx)
	grant, _ := e.grant(identity)
	entry, err := e.openEnvironment(ctx, run, grant.Workspace, e.cfg.Workspaces[grant.Workspace].Env)
	if err != nil {
		return nil, nil, err
	}
	env, ok := entry.environment.(interactiveEnvironment)
	if !ok {
		return nil, nil, errors.New("execution backend has no interactive process transport")
	}
	p, err := env.StartInteractive(ctx, options, out)
	return p, env, err
}

type programStream struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	proc   interactiveProcess
	env    interactiveEnvironment
	code   int
	err    error
	done   chan struct{}
	stderr *stderrBuffer
}

func (p *programStream) Read(data []byte) (int, error)  { return p.reader.Read(data) }
func (p *programStream) Write(data []byte) (int, error) { return p.proc.Write(data) }
func (p *programStream) CloseWrite() error              { return p.proc.CloseWrite() }
func (p *programStream) Wait() error {
	<-p.done
	if p.err != nil {
		return fmt.Errorf("%w: %s", p.err, p.stderr.String())
	}
	if p.code != 0 {
		return fmt.Errorf("program exited with code %d: %s", p.code, p.stderr.String())
	}
	return nil
}
func (p *programStream) Close() error {
	_ = p.reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	return p.proc.Stop(ctx)
}
func (p *programStream) BackendPath(path string) string { return p.env.BackendPath(path) }
func (p *programStream) Stderr() string                 { return p.stderr.String() }

// StartProgram 在当前任务后端启动全双工程序，不继承机器人进程的秘密环境。
func (e *Executor) StartProgram(ctx context.Context, command []string, env map[string]string) (toolkit.Process, error) {
	if err := e.authorizeProgram(ctx, env); err != nil {
		return nil, err
	}
	cmd, err := argvCommand(command)
	if err != nil {
		return nil, err
	}
	reader, writer := io.Pipe()
	stderr := &stderrBuffer{}
	p, backend, err := e.interactive(e.DeveloperContext(ctx), launchOptions{Command: cmd, Env: env, Stderr: stderr}, writer)
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	stream := &programStream{reader: reader, writer: writer, proc: p, env: backend, stderr: stderr, done: make(chan struct{})}
	go func() { stream.code, stream.err = p.Wait(); _ = writer.CloseWithError(stream.err); close(stream.done) }()
	return stream, nil
}

func mapBackendPath(host, remote, path string) string {
	rel, err := filepath.Rel(host, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	if rel == "." {
		return remote
	}
	return strings.TrimRight(remote, "/") + "/" + filepath.ToSlash(rel)
}
