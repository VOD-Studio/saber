package execution

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/devtools"
)

// ConfigureDevTools 将开发工具管理器绑定到同一执行后端，禁用时不实施新的覆盖基线要求。
func (e *Executor) ConfigureDevTools(cfg config.DevToolsConfig) error {
	if !cfg.Enabled {
		e.devtools = nil
		return nil
	}
	manager, err := devtools.New(cfg, e)
	if err != nil {
		return err
	}
	e.devtools = manager
	return nil
}

// DevTools 返回生命周期由执行器拥有的开发工具提供者。
func (e *Executor) DevTools() *devtools.Manager { return e.devtools }

// DeveloperWorkspace 返回当前真实授权任务目录，包括已绑定的独立 worktree。
func (e *Executor) DeveloperWorkspace(ctx context.Context) (string, error) {
	return e.WorkspaceForContext(ctx)
}

// DeveloperCheck 复用可选工具与后端的统一权限检查。
func (e *Executor) DeveloperCheck(ctx context.Context, name string, external bool) error {
	return e.CheckFeature(ctx, name, external)
}

// DeveloperScope 使用持久任务编号与实际目录隔离基线和语言服务。
func (e *Executor) DeveloperScope(ctx context.Context) string {
	id, dir, ok := TaskScope(ctx)
	if !ok {
		return ""
	}
	return developerScope(id, dir)
}
func developerScope(id int64, dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return "task:" + strconv.FormatInt(id, 10) + ":" + hex.EncodeToString(sum[:])
}

// DeveloperContext 保留真实任务取消，不让单个工具的超时杀死复用语言服务。
func (e *Executor) DeveloperContext(ctx context.Context) context.Context {
	run, ok := ctx.Value(scopeKey{}).(scope)
	if !ok || run.lifetime == nil {
		return ctx
	}
	return context.WithValue(run.lifetime, scopeKey{}, run)
}

// StartLanguageServer 在授权后端启动 stdio 服务，并转换实际后端可见路径。
func (e *Executor) StartLanguageServer(ctx context.Context, workspace string, command []string, env map[string]string) (devtools.Process, error) {
	if err := e.Check(ctx, "read_file"); err != nil {
		return nil, err
	}
	root, err := e.WorkspaceForContext(ctx)
	if err != nil {
		return nil, err
	}
	if root != workspace {
		return nil, errors.New("language server workspace does not match current task")
	}
	return e.StartProgram(ctx, command, env)
}

func (e *Executor) fileLock(dir, path string) *sync.Mutex {
	key := filepath.Join(dir, path)
	e.fileMu.Lock()
	defer e.fileMu.Unlock()
	lock := e.fileLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		e.fileLocks[key] = lock
	}
	return lock
}

// DeveloperRead 只从钉住的授权根读取普通文件，拒绝目录链接与二进制提取旁路。
func (e *Executor) DeveloperRead(ctx context.Context, p string) ([]byte, error) {
	return e.ReadAuthorizedFile(ctx, p, maxSnapshotBytes)
}

func observeFilePage(manager *devtools.Manager, scopeID, path string, file *os.File, lineOffset, byteOffset int, text string) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	reader := bufio.NewReader(io.NewSectionReader(file, 0, info.Size()))
	digest := sha256.New()
	line := 0
	position := int64(0)
	start := int64(0)
	for {
		chunk, readErr := reader.ReadSlice('\n')
		if line == lineOffset && position == 0 {
			start = 0
		}
		if _, err := digest.Write(chunk); err != nil {
			return err
		}
		position += int64(len(chunk))
		if readErr == nil {
			line++
			if line == lineOffset {
				start = position
			}
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if lineOffset > line {
		start = info.Size()
	}
	start += int64(byteOffset)
	if start > info.Size() {
		start = info.Size()
	}
	end := start + int64(len(text))
	if end > info.Size() {
		end = info.Size()
	}
	manager.ObserveRead(scopeID, path, hex.EncodeToString(digest.Sum(nil)), start, end, info.Size(), false)
	return nil
}

func (e *Executor) editDiagnostics(ctx context.Context, run scope, p string, before, after []byte, created bool, result *Result) {
	if e.devtools == nil {
		return
	}
	scopeID := developerScope(run.id, run.dir)
	e.devtools.Written(scopeID, p, before, after, created)
	if !created && before == nil {
		before = []byte{}
	}
	diagnostics, err := e.devtools.CheckEdit(ctx, scopeID, run.dir, filepath.Join(run.dir, p), before, after)
	data, ok := result.Data.(map[string]any)
	if !ok {
		data = map[string]any{}
		result.Data = data
	}
	data["diagnostics"] = diagnostics
	if err != nil {
		data["diagnostic_error"] = err.Error()
	}
}

// DrainTaskDiagnostics 仅交付当前任务 scope 的已完成或有界等待的语义诊断。
func (e *Executor) DrainTaskDiagnostics(ctx context.Context, wait bool) ([]devtools.EditResult, error) {
	if e.devtools == nil {
		return nil, nil
	}
	if err := e.Check(ctx, "read_file"); err != nil {
		return nil, err
	}
	return e.devtools.DrainDiagnostics(ctx, e.DeveloperScope(ctx), wait)
}

// ValidateTaskWorkspace 让任务领取与恢复只接受原授权目录或已绑定的派生工作副本。
func (e *Executor) ValidateTaskWorkspace(ctx context.Context, id int64, dir string) error {
	if id <= 0 {
		return errors.New("workspace requires persistent task")
	}
	_, err := e.WorkspaceForContext(WithTask(ctx, id, dir))
	return err
}

// SaveOutput 将程序化工具的完整输出归档为同一任务 ACL 保护的 output_id。
func (e *Executor) SaveOutput(ctx context.Context, data []byte) (string, error) {
	ctx = context.WithoutCancel(ctx)
	if err := e.Check(ctx, "exec"); err != nil {
		return "", err
	}
	id, _, ok := TaskScope(ctx)
	if !ok {
		return "", errors.New("output requires persistent task")
	}
	if len(data) > 16<<20 {
		return "", errors.New("output exceeds 16 MiB")
	}
	archive, err := e.openOutput(id)
	if err != nil {
		return "", err
	}
	_, writeErr := archive.file.Write(data)
	err = errors.Join(writeErr, archive.close(e))
	return archive.ID, err
}

func (e *Executor) readDocument(ctx context.Context, args map[string]any, run scope) (Result, error) {
	p, _ := args["path"].(string)
	data, err := e.DeveloperRead(ctx, p)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	extraction, err := e.devtools.Extract(ctx, run.dir, p, data)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	offset, limit := 0, readLimitDef
	if value, ok := args["offset"]; ok {
		offset, _ = intArgument(value)
	}
	if value, ok := args["limit"]; ok {
		limit, _ = intArgument(value)
	}
	lines := strings.SplitAfter(extraction.Content, "\n")
	if offset > len(lines) {
		offset = len(lines)
	}
	end := min(len(lines), offset+limit)
	text := strings.Join(lines[offset:end], "")
	byteOffset := 0
	if value, ok := args["byte_offset"]; ok {
		byteOffset, _ = intArgument(value)
	}
	if byteOffset > len(text) {
		return Result{ExitCode: -1}, errors.New("document byte_offset exceeds extracted page")
	}
	text = text[byteOffset:]
	nextByte := 0
	if len(text) > pageBytes {
		cut := pageBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
		nextByte = byteOffset + cut
		end = offset
	}
	return Result{ExitCode: 0, Summary: fmt.Sprintf("extracted %s (%s)", p, extraction.Format), Data: map[string]any{"path": p, "text": text, "offset": offset, "next_offset": end, "next_byte_offset": nextByte, "eof": end >= len(lines), "extracted": true, "format": extraction.Format, "source_bytes": extraction.SourceBytes, "warnings": extraction.Warnings}}, nil
}
