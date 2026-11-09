package devtools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rua.plus/saber/internal/config"
)

type nativeTestBackend struct{ taskContext context.Context }

func (b nativeTestBackend) DeveloperExists(ctx context.Context, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}
func (b nativeTestBackend) DeveloperContext(context.Context) context.Context { return b.taskContext }
func (b nativeTestBackend) StartLanguageServer(ctx context.Context, root string, command []string, environment map[string]string) (Process, error) {
	if len(command) == 0 {
		return nil, errors.New("missing actual process command")
	}
	life, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(life, command[0], command[1:]...)
	cmd.Dir = root
	if len(environment) > 0 {
		cmd.Env = os.Environ()
		for key, value := range environment {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	p := &nativeTestProcess{cmd: cmd, stdin: stdin, stdout: stdout, cancel: cancel}
	cmd.Stderr = &p.stderr
	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	return p, nil
}

type nativeTestProcess struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	stderr   bytes.Buffer
	cancel   context.CancelFunc
	waitOnce sync.Once
	waitErr  error
}

func (p *nativeTestProcess) Read(data []byte) (int, error)  { return p.stdout.Read(data) }
func (p *nativeTestProcess) Write(data []byte) (int, error) { return p.stdin.Write(data) }
func (p *nativeTestProcess) CloseWrite() error              { return p.stdin.Close() }
func (p *nativeTestProcess) Wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
		if p.waitErr != nil {
			p.waitErr = fmt.Errorf("%w: %s", p.waitErr, p.stderr.String())
		}
	})
	return p.waitErr
}
func (p *nativeTestProcess) Close() error {
	p.cancel()
	_ = p.stdin.Close()
	_ = p.stdout.Close()
	_ = p.Wait()
	return nil
}

func TestActualGoplsReportsOnlyIntroducedSemanticErrors(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("install gopls in the test backend to exercise actual LSP")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for real workspace detection")
	}
	root := t.TempDir()
	if output, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/lspdiagnostics\n\ngo 1.22\n"), 0600); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, "sample.go")
	before := []byte("package sample\nfunc Old() { _ = preexisting }\nfunc Changed() { var x int = 1; _ = x }\n")
	after := []byte("package sample\nfunc Old() { _ = preexisting }\nfunc Changed() { var x int = newlyUndefined; _ = x }\n")
	if err := os.WriteFile(filename, after, 0600); err != nil {
		t.Fatal(err)
	}
	taskCtx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	cfg := config.DefaultDevToolsConfig()
	cfg.TrustedWorkspaces = []string{root}
	cfg.WaitTimeoutSeconds = 20
	cfg.WarmupTimeoutSeconds = 30
	cfg.DeferredWaitSeconds = 60
	cfg.InlineWaitMilliseconds = 1
	m, err := New(cfg, nativeTestBackend{taskCtx})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	result, err := m.CheckEdit(taskCtx, "real-task", root, filename, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if result.Pending {
		results, err := m.DrainDiagnostics(taskCtx, "real-task", true)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 1 {
			t.Fatalf("missing real semantic verdict: %#v", results)
		}
		result = results[0]
	}
	if result.Error != "" {
		t.Fatalf("real language server failed: %s", result.Error)
	}
	found := false
	for _, d := range result.Diagnostics {
		if strings.Contains(d.Message, "preexisting") {
			t.Fatalf("pre-existing error leaked into delta: %#v", d)
		}
		if strings.Contains(d.Message, "newlyUndefined") {
			found = true
		}
	}
	if !found {
		t.Fatalf("introduced semantic error absent: %#v", result)
	}
	clientCount := len(m.clients)
	if clientCount != 1 {
		t.Fatalf("warm server not retained: %d", clientCount)
	}
	if err := os.WriteFile(filename, before, 0600); err != nil {
		t.Fatal(err)
	}
	next, err := m.CheckEdit(taskCtx, "real-task", root, filename, after, before)
	if err != nil {
		t.Fatal(err)
	}
	if next.Pending {
		results, err := m.DrainDiagnostics(taskCtx, "real-task", true)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range results {
			if len(r.Diagnostics) > 0 || r.Error != "" {
				t.Fatalf("fix produced phantom diagnostics: %#v", r)
			}
		}
	} else if len(next.Diagnostics) > 0 || next.Error != "" {
		t.Fatalf("fix produced phantom diagnostics: %#v", next)
	}
	if err := os.WriteFile(filename, after, 0600); err != nil {
		t.Fatal(err)
	}
	pendingEdit, err := m.CheckEdit(taskCtx, "real-task", root, filename, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if pendingEdit.Pending {
		if err := os.WriteFile(filename, before, 0600); err != nil {
			t.Fatal(err)
		}
		fixed, err := m.CheckEdit(taskCtx, "real-task", root, filename, after, before)
		if err != nil {
			t.Fatal(err)
		}
		if !fixed.Pending && (len(fixed.Diagnostics) > 0 || fixed.Error != "") {
			t.Fatalf("superseding fix reported old errors: %#v", fixed)
		}
		late, err := m.DrainDiagnostics(taskCtx, "real-task", true)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range late {
			if len(r.Diagnostics) > 0 || r.Error != "" {
				t.Fatalf("late fixed error escaped: %#v", r)
			}
		}
	}
	if err := m.CloseScope("real-task"); err != nil {
		t.Fatal(err)
	}
	if len(m.clients) != 0 {
		t.Fatal("task cleanup leaked language server")
	}
}
func TestActualDeveloperProcessCancellation(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep binary required")
	}
	taskCtx, stop := context.WithCancel(context.Background())
	defer stop()
	m, err := New(config.DefaultDevToolsConfig(), nativeTestBackend{taskCtx})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	ctx, cancel := context.WithTimeout(taskCtx, 50*time.Millisecond)
	defer cancel()
	_, err = m.runProcess(ctx, t.TempDir(), []string{"sleep", "30"}, nil, 1024)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("actual process cancellation hidden: %v", err)
	}
}
func TestUntrustedWorkspaceCannotLaunchLanguageServer(t *testing.T) {
	cfg := config.DefaultDevToolsConfig()
	cfg.InlineWaitMilliseconds = 1000
	m, err := New(cfg, nativeTestBackend{context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	root := t.TempDir()
	result, err := m.CheckEdit(context.Background(), "untrusted", root, filepath.Join(root, "sample.go"), nil, []byte("package sample\n"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Pending {
		results, err := m.DrainDiagnostics(context.Background(), "untrusted", true)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 1 {
			t.Fatal("trust denial lost")
		}
		result = results[0]
	}
	if !strings.Contains(result.Error, "untrusted workspace") || len(m.clients) != 0 {
		t.Fatalf("untrusted service was launched: %#v", result)
	}
}

func TestActualPythonSyntaxParserDoesNotExecuteFile(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python parser requires an installed python3")
	}
	root := t.TempDir()
	taskCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := config.DefaultDevToolsConfig()
	cfg.TrustedWorkspaces = []string{root}
	m, err := New(cfg, nativeTestBackend{taskCtx})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	target := filepath.Join(root, "should-not-exist")
	source := []byte("open(" + strconv.Quote(target) + ", 'w').write('unsafe')\n")
	result := m.Syntax(taskCtx, root, filepath.Join(root, "test.py"), source)
	if result.Status != "ok" {
		t.Fatal("actual Python parser failed", result)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("syntax check executed project code")
	}
	invalid := m.Syntax(taskCtx, root, filepath.Join(root, "test.py"), []byte("def broken(:\n"))
	if invalid.Status != "error" || invalid.Output == "" {
		t.Fatal("real Python parse error hidden", invalid)
	}
}

func TestActualBlockedLanguageServerWriteIsCancelled(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep binary required for the actual pipe-blocking boundary")
	}
	taskCtx, stop := context.WithCancel(context.Background())
	defer stop()
	backend := nativeTestBackend{taskCtx}
	process, err := backend.StartLanguageServer(taskCtx, t.TempDir(), []string{"sleep", "30"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := newRPCClient(process, "blocked-reader", nil)
	defer func() { _ = client.close() }()
	ctx, cancel := context.WithTimeout(taskCtx, 50*time.Millisecond)
	defer cancel()
	var result any
	err = client.request(ctx, "initialize", map[string]any{"content": strings.Repeat("A", 1<<20)}, &result)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked real stdin ignored cancellation: %v", err)
	}
}

type completionWorkspaceKey struct{}

func (b nativeTestBackend) DeveloperComplete(ctx context.Context, kind, target string, max int) ([]ReferenceCompletion, error) {
	root, ok := ctx.Value(completionWorkspaceKey{}).(string)
	if !ok {
		return nil, errors.New("test completion requires a bound filesystem root")
	}
	directory, prefix := filepath.Split(target)
	if directory == "" {
		directory = "."
	}
	if filepath.IsAbs(directory) {
		return nil, errors.New("completion outside root")
	}
	opened, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = opened.Close() }()
	file, err := opened.Open(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var result []ReferenceCompletion
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) || entry.Type()&os.ModeSymlink != 0 || kind == "folder" && !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		result = append(result, ReferenceCompletion{Value: filepath.Join(directory, entry.Name()), Label: entry.Name(), Directory: entry.IsDir(), Size: info.Size()})
		if len(result) == max {
			break
		}
	}
	return result, nil
}
func TestActualFilesystemReferenceCompletionPreservesQuotedPathsAndMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file name.go"), []byte("package sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), completionWorkspaceKey{}, root)
	m, err := New(config.DefaultDevToolsConfig(), nativeTestBackend{ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	items, err := m.ContextCompletions(ctx, "@file:fi", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Size != int64(len("package sample\n")) {
		t.Fatalf("actual file metadata missing: %#v", items)
	}
	parsed := ParseReferences(items[0].Value)
	if len(parsed) != 1 || parsed[0].Target != "file name.go" {
		t.Fatalf("completed path cannot be parsed back: %#v", items)
	}
	directories, err := m.ContextCompletions(ctx, "@folder:fo", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(directories) != 1 || !directories[0].Directory || directories[0].Value != "@folder:folder/" {
		t.Fatalf("wrong actual folder completion: %#v", directories)
	}
	if _, err := m.ContextCompletions(ctx, "@file:../", 20); err == nil {
		t.Fatal("filesystem autocomplete escaped its bound root")
	}
}
