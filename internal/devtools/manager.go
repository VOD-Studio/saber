// Package devtools 提供经过任务后端约束的开发诊断及不自行越权读取的文件辅助能力。
package devtools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

// EditResult 独立报告语法与语义；诊断失败不把已完成的文件写入伪装成失败。
type EditResult struct {
	Path           string       `json:"path"`
	Lint           SyntaxResult `json:"lint"`
	Diagnostics    []Diagnostic `json:"diagnostics,omitempty"`
	LSPDiagnostics string       `json:"lsp_diagnostics,omitempty"`
	Status         string       `json:"status"`
	Error          string       `json:"diagnostic_error,omitempty"`
	Pending        bool         `json:"pending,omitempty"`
}
type clientSlot struct {
	client  *rpcClient
	ready   chan struct{}
	err     error
	cancel  context.CancelFunc
	updated time.Time
}
type editCheck struct {
	done       chan struct{}
	result     EditResult
	before     []byte
	superseded bool
	cancel     context.CancelFunc
}
type brokenServer struct {
	at  time.Time
	err string
}

// Manager 持有任务隔离语言服务、原始读基线与按发起任务交付的延迟诊断。
type Manager struct {
	cfg                   config.DevToolsConfig
	backend               Backend
	servers               []Server
	baselines             baselineStore
	mu                    sync.Mutex
	clients               map[string]*clientSlot
	broken                map[string]brokenServer
	inflight              map[string]*editCheck
	deferred              map[string][]*editCheck
	referenceProviders    func(context.Context) map[string]func(context.Context, string, int) (string, error)
	referenceAutocomplete func(context.Context, string, string, int) ([]string, error)
	changed               chan struct{}
	closed                bool
}

// New 创建实际服务管理器；没有后端时仅允许纯 Go 提取与语法解析，进程操作明确报错。
func New(cfg config.DevToolsConfig, backend Backend) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	d := config.DefaultDevToolsConfig()
	if cfg.WaitMode == "" {
		cfg.WaitMode = d.WaitMode
	}
	if cfg.WaitTimeoutSeconds == 0 {
		cfg.WaitTimeoutSeconds = d.WaitTimeoutSeconds
	}
	if cfg.MaxServers == 0 {
		cfg.MaxServers = d.MaxServers
	}
	if cfg.MaxDocumentBytes == 0 {
		cfg.MaxDocumentBytes = d.MaxDocumentBytes
	}
	if cfg.MaxExtractedBytes == 0 {
		cfg.MaxExtractedBytes = d.MaxExtractedBytes
	}
	if cfg.NotebookOutputCharacters == 0 {
		cfg.NotebookOutputCharacters = d.NotebookOutputCharacters
	}
	if len(cfg.PDFCommand) == 0 {
		cfg.PDFCommand = d.PDFCommand
	}
	if cfg.MaxReferences == 0 {
		cfg.MaxReferences = d.MaxReferences
	}
	if cfg.FolderEntries == 0 {
		cfg.FolderEntries = d.FolderEntries
	}
	if cfg.ReferenceBytes == 0 {
		cfg.ReferenceBytes = d.ReferenceBytes
	}
	return &Manager{cfg: cfg, backend: backend, servers: configuredServers(cfg.Servers), clients: make(map[string]*clientSlot), broken: make(map[string]brokenServer), inflight: make(map[string]*editCheck), deferred: make(map[string][]*editCheck), changed: make(chan struct{})}, nil
}
func inside(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func (m *Manager) trusted(root string) bool {
	for _, p := range m.cfg.TrustedWorkspaces {
		if inside(root, p) {
			return true
		}
	}
	return false
}
func (m *Manager) excluded(root string) bool {
	for _, p := range m.cfg.ExcludeRoots {
		if ok, _ := filepath.Match(p, root); ok || inside(root, p) {
			return true
		}
	}
	return false
}
func (m *Manager) server(path string) (Server, bool) {
	for _, s := range m.servers {
		if matchesServer(s, path) {
			return s, !s.Disabled
		}
	}
	return Server{}, false
}
func (m *Manager) signalLocked()                 { close(m.changed); m.changed = make(chan struct{}) }
func scopeKey(scope, root, server string) string { return scope + "\x00" + root + "\x00" + server }

func (m *Manager) getClient(ctx context.Context, scopeID, root, path string, s Server) (*rpcClient, error) {
	if !m.cfg.Enabled || !m.cfg.LSPEnabled {
		return nil, errors.New("language services are disabled")
	}
	if !filepath.IsAbs(root) || !inside(path, root) {
		return nil, errors.New("language service path is outside authorized workspace")
	}
	if !m.trusted(root) {
		return nil, fmt.Errorf("untrusted workspace %s; add its absolute path to devtools.trusted_workspaces", root)
	}
	if m.excluded(root) {
		return nil, fmt.Errorf("language services excluded for %s", root)
	}
	if m.backend == nil {
		return nil, errors.New("language service backend is unavailable")
	}
	if len(s.Command) == 0 {
		return nil, fmt.Errorf("language server %s requires an explicit installed command (PowerShellEditorServices: configure pwsh and Start-EditorServices.ps1 with -Stdio)", s.ID)
	}
	projectRoot, err := m.projectRoot(ctx, root, path, s)
	if err != nil {
		return nil, err
	}
	if m.excluded(projectRoot) {
		return nil, fmt.Errorf("language services excluded for project %s", projectRoot)
	}
	key := scopeKey(scopeID, projectRoot, s.ID)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("developer tools closed")
	}
	if broken, ok := m.broken[key]; ok {
		if m.cfg.BrokenRetrySeconds == 0 || time.Since(broken.at) < time.Duration(m.cfg.BrokenRetrySeconds)*time.Second {
			m.mu.Unlock()
			return nil, fmt.Errorf("language server %s unavailable until restart/retry: %s", s.ID, broken.err)
		}
		delete(m.broken, key)
	}
	if slot := m.clients[key]; slot != nil {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-slot.ready:
			return slot.client, slot.err
		}
	}
	if len(m.clients) >= m.cfg.MaxServers {
		m.mu.Unlock()
		return nil, errors.New("language server limit reached; close completed task services or restart")
	}
	ownerCtx := ctx
	if owner, ok := m.backend.(interface {
		DeveloperContext(context.Context) context.Context
	}); ok {
		ownerCtx = owner.DeveloperContext(ctx)
	}
	lifeCtx, cancel := context.WithCancel(ownerCtx)
	slot := &clientSlot{ready: make(chan struct{}), cancel: cancel}
	m.clients[key] = slot
	m.mu.Unlock()
	budget := time.Duration(m.cfg.WaitTimeoutSeconds) * time.Second
	if m.cfg.WarmupTimeoutSeconds > 0 {
		budget = time.Duration(m.cfg.WarmupTimeoutSeconds) * time.Second
	}
	startupCtx, stop := context.WithTimeout(ctx, budget)
	defer stop()
	gitRoot, err := m.runProcess(startupCtx, root, []string{"git", "rev-parse", "--show-toplevel"}, nil, 4096)
	if err == nil && strings.TrimSpace(string(gitRoot)) == "" {
		err = errors.New("language services require a Git worktree")
	}
	if err == nil {
		s, err = m.prepareServer(startupCtx, root, projectRoot, s)
	}
	var client *rpcClient
	if err == nil {
		var p Process
		p, err = m.backend.StartLanguageServer(lifeCtx, root, s.Command, nil)
		if err == nil {
			client = newRPCClient(p, s.ID, s.Settings)
			client.cold = true
			stopWatch := client.watchContext(startupCtx)
			err = client.initialize(startupCtx, projectRoot, s.InitializationOptions)
			stopWatch()
			if err != nil {
				err = errors.Join(err, client.close())
				client = nil
			}
		}
	}
	m.mu.Lock()
	slot.client, slot.err, slot.updated = client, err, time.Now()
	if err != nil {
		if m.clients[key] == slot {
			delete(m.clients, key)
			m.broken[key] = brokenServer{time.Now(), err.Error()}
		}
		cancel()
	}
	close(slot.ready)
	m.mu.Unlock()
	return client, err
}

// CheckEdit 比较编辑前后真实语义诊断，超过短等待窗口的结果按 scope 延迟交付。
// root/path 由执行器解析为授权宿主路径；原始字节由同一后端文件工具传入。
func (m *Manager) CheckEdit(ctx context.Context, scopeID, root, path string, before, after []byte) (EditResult, error) {
	result := EditResult{Path: path, Status: "skipped", Lint: m.Syntax(ctx, root, path, after)}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if scopeID == "" {
		return result, errors.New("edit diagnostic requires trusted scope")
	}
	if result.Lint.Status == "error" {
		result.Status = "syntax_error"
		return result, nil
	}
	server, ok := m.server(path)
	if !ok || !m.cfg.Enabled || !m.cfg.LSPEnabled {
		return result, nil
	}
	key := scopeID + "\x00" + path
	checkCtx, cancel := context.WithCancel(ctx)
	var pre []byte
	if before != nil {
		pre = append([]byte{}, before...)
	}
	check := &editCheck{done: make(chan struct{}), before: pre, cancel: cancel}
	post := append([]byte(nil), after...)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return result, errors.New("developer tools closed")
	}
	previous := m.inflight[key]
	if previous != nil {
		previous.superseded = true
		check.before = previous.before
	}
	queue := m.deferred[scopeID]
	kept := queue[:0]
	for _, old := range queue {
		if old.result.Path == path {
			if previous == nil {
				check.before = old.before
			}
			old.superseded = true
		} else {
			kept = append(kept, old)
		}
	}
	m.deferred[scopeID] = kept
	m.inflight[key] = check
	m.mu.Unlock()
	go func() {
		defer cancel()
		if previous != nil {
			select {
			case <-previous.done:
			case <-checkCtx.Done():
			}
		}
		m.mu.Lock()
		superseded := check.superseded
		m.mu.Unlock()
		verdict := result
		if superseded {
			verdict.Status = "superseded"
		} else {
			client, err := m.getClient(checkCtx, scopeID, root, path, server)
			if err != nil {
				verdict.Status, verdict.Error = "unavailable", err.Error()
			} else {
				client.opMu.Lock()
				diagnostics, err := m.checkWithClient(checkCtx, client, path, languageID(path, m.cfg.Servers[server.ID]), check.before, post)
				client.opMu.Unlock()
				verdict.Diagnostics = diagnostics
				verdict.LSPDiagnostics = formatDiagnostics(path, diagnostics, false)
				if err != nil {
					verdict.Status, verdict.Error = "unavailable", m.breakClient(scopeID, client, err).Error()
				} else {
					verdict.Status = "ok"
				}
			}
		}
		m.mu.Lock()
		check.result = verdict
		if m.inflight[key] == check {
			delete(m.inflight, key)
		}
		if !check.superseded && (len(verdict.Diagnostics) > 0 || verdict.Error != "") {
			m.deferred[scopeID] = append(m.deferred[scopeID], check)
		}
		close(check.done)
		m.signalLocked()
		m.mu.Unlock()
	}()
	wait := time.NewTimer(time.Duration(m.cfg.InlineWaitMilliseconds) * time.Millisecond)
	defer wait.Stop()
	select {
	case <-ctx.Done():
		cancel()
		return result, ctx.Err()
	case <-check.done:
		m.mu.Lock()
		verdict := check.result
		queue := m.deferred[scopeID]
		for i, queued := range queue {
			if queued == check {
				m.deferred[scopeID] = append(queue[:i], queue[i+1:]...)
				break
			}
		}
		m.mu.Unlock()
		return verdict, nil
	case <-wait.C:
		result.Status, result.Pending = "pending", true
		return result, nil
	}
}
func (m *Manager) checkWithClient(ctx context.Context, c *rpcClient, path, language string, before, after []byte) ([]Diagnostic, error) {
	budget := time.Duration(m.cfg.WaitTimeoutSeconds) * time.Second
	if c.cold && m.cfg.WarmupTimeoutSeconds > 0 {
		budget = time.Duration(m.cfg.WarmupTimeoutSeconds) * time.Second
	}
	operationCtx, cancelOperation := context.WithTimeout(ctx, 2*budget)
	defer cancelOperation()
	stopWatch := c.watchContext(operationCtx)
	defer stopWatch()
	ctx = operationCtx
	version, err := c.syncDocument(path, language, string(after), true)
	if err != nil {
		return nil, err
	}
	postCtx, stop := context.WithTimeout(ctx, budget)
	diagnostics, err := c.verdict(postCtx, path, version, m.cfg.WaitMode)
	stop()
	if err != nil {
		return nil, err
	}
	c.cold = false
	if len(diagnostics) > 0 && before != nil {
		version, err = c.syncDocument(path, language, string(before), false)
		if err != nil {
			return nil, err
		}
		preCtx, stop := context.WithTimeout(ctx, budget)
		baseline, baseErr := c.verdict(preCtx, path, version, m.cfg.WaitMode)
		stop()
		if baseErr != nil {
			return diagnostics, fmt.Errorf("pre-edit baseline diagnostic unavailable; diagnostics may include pre-existing errors: %w", baseErr)
		}
		_, resyncErr := c.syncDocument(path, language, string(after), false)
		if resyncErr != nil {
			return diagnostics, resyncErr
		}
		diagnostics = introducedDiagnosticsForFile(before, after, baseline, diagnostics, fileURI(c.path(path)))
	}
	return diagnostics, nil
}
func (m *Manager) breakClient(scope string, client *rpcClient, cause error) error {
	m.mu.Lock()
	var found *clientSlot
	failedKey := ""
	failedAt := time.Now()
	for key, slot := range m.clients {
		if strings.HasPrefix(key, scope+"\x00") && slot.client == client {
			found, failedKey = slot, key
			delete(m.clients, key)
			m.broken[key] = brokenServer{failedAt, cause.Error()}
			break
		}
	}
	m.mu.Unlock()
	if found != nil {
		found.cancel()
	}
	combined := errors.Join(cause, client.close())
	if failedKey != "" {
		m.mu.Lock()
		if failure, ok := m.broken[failedKey]; ok && failure.at.Equal(failedAt) {
			failure.err = combined.Error()
			m.broken[failedKey] = failure
		}
		m.mu.Unlock()
	}
	return combined
}

// DrainDiagnostics 在后续工具结果或结束任务前交付当前 scope 的诊断，不修改先前模型消息。
func (m *Manager) DrainDiagnostics(ctx context.Context, scopeID string, wait bool) ([]EditResult, error) {
	deadline := time.NewTimer(time.Duration(m.cfg.DeferredWaitSeconds) * time.Second)
	defer deadline.Stop()
	for {
		m.mu.Lock()
		pending := false
		if wait {
			for key := range m.inflight {
				if strings.HasPrefix(key, scopeID+"\x00") {
					pending = true
					break
				}
			}
		}
		if !pending {
			queue := m.deferred[scopeID]
			delete(m.deferred, scopeID)
			results := make([]EditResult, 0, len(queue))
			for _, check := range queue {
				if !check.superseded {
					result := check.result
					result.LSPDiagnostics = formatDiagnostics(result.Path, result.Diagnostics, true)
					results = append(results, result)
				}
			}
			m.mu.Unlock()
			return results, nil
		}
		changed := m.changed
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			results, err := m.DrainDiagnostics(ctx, scopeID, false)
			if err != nil {
				return results, err
			}
			m.mu.Lock()
			for key := range m.inflight {
				if strings.HasPrefix(key, scopeID+"\x00") {
					path := strings.TrimPrefix(key, scopeID+"\x00")
					results = append(results, EditResult{Path: path, Status: "pending", Pending: true, LSPDiagnostics: "Language server still analyzing " + path + "; no fresh semantic verdict is available yet."})
				}
			}
			m.mu.Unlock()
			return results, nil
		case <-changed:
		}
	}
}

// Status 返回全部管理状态，仅供已鉴权的管理员消费者；普通任务应使用 StatusScope。
func (m *Manager) Status() any { return m.status("") }

// StatusScope 只返回当前可信任务拥有的进程与失败记录。
func (m *Manager) StatusScope(scopeID string) any {
	if scopeID == "" {
		return map[string]any{"error": "trusted scope required"}
	}
	return m.status(scopeID)
}
func (m *Manager) status(scopeID string) any {
	m.mu.Lock()
	defer m.mu.Unlock()
	active := make([]map[string]any, 0, len(m.clients))
	failed := make([]map[string]any, 0, len(m.broken))
	for key, slot := range m.clients {
		if scopeID != "" && !strings.HasPrefix(key, scopeID+"\x00") {
			continue
		}
		errText := ""
		if slot.err != nil {
			errText = slot.err.Error()
		}
		active = append(active, map[string]any{"key": strings.ReplaceAll(key, "\x00", " / "), "ready": slot.client != nil, "updated": slot.updated, "error": errText})
	}
	for key, b := range m.broken {
		if scopeID != "" && !strings.HasPrefix(key, scopeID+"\x00") {
			continue
		}
		failed = append(failed, map[string]any{"key": strings.ReplaceAll(key, "\x00", " / "), "error": b.err, "at": b.at})
	}
	return map[string]any{"enabled": m.cfg.Enabled && m.cfg.LSPEnabled, "closed": m.closed, "active": active, "failed": failed, "servers": m.servers}
}

// Restart 由管理员或关闭生命周期调用；普通工具只能重启自己的 scope。
func (m *Manager) Restart() error { return m.restartScope("") }

// RestartScope 取消并回收指定任务的服务，不干扰其他成员的任务。
func (m *Manager) RestartScope(scopeID string) error {
	if scopeID == "" {
		return errors.New("restart requires trusted scope")
	}
	return m.restartScope(scopeID)
}
func (m *Manager) restartScope(scopeID string) error {
	m.mu.Lock()
	slots := make([]*clientSlot, 0)
	checks := make([]*editCheck, 0)
	for key, slot := range m.clients {
		if scopeID == "" || strings.HasPrefix(key, scopeID+"\x00") {
			slots = append(slots, slot)
			delete(m.clients, key)
			slot.cancel()
		}
	}
	for key := range m.broken {
		if scopeID == "" || strings.HasPrefix(key, scopeID+"\x00") {
			delete(m.broken, key)
		}
	}
	for key, check := range m.inflight {
		if scopeID == "" || strings.HasPrefix(key, scopeID+"\x00") {
			check.cancel()
			checks = append(checks, check)
		}
	}
	m.mu.Unlock()
	var errs []error
	for _, slot := range slots {
		<-slot.ready
		if slot.client != nil {
			errs = append(errs, slot.client.close())
		}
	}
	for _, check := range checks {
		<-check.done
	}
	return errors.Join(errs...)
}

// CloseScope 在任务环境关闭前回收诊断进程与读基线。
func (m *Manager) CloseScope(scopeID string) error {
	err := m.RestartScope(scopeID)
	m.ForgetScope(scopeID)
	return err
}

// Close 终止所有拥有的后端进程并禁止后续启动。
func (m *Manager) Close() error { m.mu.Lock(); m.closed = true; m.mu.Unlock(); return m.Restart() }

// Definitions 将真实 LSP 消费入口暴露给统一工具权限注册表。
func (m *Manager) Definitions() []toolkit.Definition {
	if !m.cfg.Enabled {
		return nil
	}
	return []toolkit.Definition{{Permission: "lsp", Requires: []string{"exec", "read_file"}, Tool: openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "lsp", Description: "Real task-backend language services: status/list, diagnose a workspace file, restart, which, explicitly install a supported server, or complete an @ context reference. Installation additionally requires external permission.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"status", "list", "diagnose", "restart", "which", "install", "complete_reference"}}, "path": map[string]any{"type": "string", "description": "Workspace file for diagnose, or partially typed @ reference for complete_reference."}, "server": map[string]any{"type": "string"}}, "required": []string{"action"}, "additionalProperties": false}}}}}
}

// Execute 只从可信后端取得工作区与原文，不允许模型指定宿主根目录。
func (m *Manager) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	if name != "lsp" {
		return nil, fmt.Errorf("unknown developer tool %s", name)
	}
	if !m.cfg.Enabled {
		return nil, errors.New("developer tools disabled")
	}
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return nil, errors.New("lsp requires trusted identity")
	}
	access, ok := m.backend.(interface {
		DeveloperWorkspace(context.Context) (string, error)
		DeveloperRead(context.Context, string) ([]byte, error)
		DeveloperCheck(context.Context, string, bool) error
		DeveloperScope(context.Context) string
	})
	if !ok {
		return nil, errors.New("developer tool backend consumer interface unavailable")
	}
	for _, permission := range []string{"lsp", "exec", "read_file"} {
		if err := access.DeveloperCheck(ctx, permission, false); err != nil {
			return nil, err
		}
	}
	root, err := access.DeveloperWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	scopeID := access.DeveloperScope(ctx)
	if scopeID == "" {
		return nil, errors.New("lsp requires persistent task scope")
	}
	action, _ := args["action"].(string)
	switch action {
	case "status":
		return m.StatusScope(scopeID), nil
	case "list":
		return m.servers, nil
	case "complete_reference":
		input, _ := args["path"].(string)
		return m.ContextCompletions(ctx, input, 20)
	case "restart":
		err := m.RestartScope(scopeID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"restarted": true}, nil
	case "diagnose":
		path, _ := args["path"].(string)
		if path == "" {
			return nil, errors.New("diagnose requires path")
		}
		data, err := access.DeveloperRead(ctx, path)
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		s, ok := m.server(path)
		if !ok {
			return nil, errors.New("no enabled language server for file")
		}
		c, err := m.getClient(ctx, scopeID, root, path, s)
		if err != nil {
			return nil, err
		}
		c.opMu.Lock()
		defer c.opMu.Unlock()
		budget := time.Duration(m.cfg.WaitTimeoutSeconds) * time.Second
		if c.cold && m.cfg.WarmupTimeoutSeconds > 0 {
			budget = time.Duration(m.cfg.WarmupTimeoutSeconds) * time.Second
		}
		timeout, stop := context.WithTimeout(ctx, budget)
		defer stop()
		stopWatch := c.watchContext(timeout)
		defer stopWatch()
		version, err := c.syncDocument(path, languageID(path, m.cfg.Servers[s.ID]), string(data), true)
		if err != nil {
			return nil, m.breakClient(scopeID, c, err)
		}
		diagnostics, err := c.verdict(timeout, path, version, m.cfg.WaitMode)
		if err == nil {
			c.cold = false
			return diagnostics, nil
		}
		return diagnostics, m.breakClient(scopeID, c, err)
	case "which", "install":
		id, _ := args["server"].(string)
		var server *Server
		for i := range m.servers {
			if m.servers[i].ID == id {
				server = &m.servers[i]
				break
			}
		}
		if server == nil {
			return nil, errors.New("unknown language server")
		}
		if action == "install" {
			if err := access.DeveloperCheck(ctx, "lsp", true); err != nil {
				return nil, err
			}
			if !m.trusted(root) {
				return nil, errors.New("installation requires explicitly trusted workspace")
			}
			if len(server.InstallCommand) == 0 {
				return nil, fmt.Errorf("%s requires manual toolchain installation", id)
			}
			output, err := m.runProcess(ctx, root, server.InstallCommand, nil, 1<<20)
			return map[string]any{"server": id, "output": string(output)}, err
		}
		if len(server.Command) == 0 {
			return nil, errors.New("server command is not configured")
		}
		output, err := m.runProcess(ctx, root, []string{"which", server.Command[0]}, nil, 4096)
		return map[string]any{"server": id, "binary": strings.TrimSpace(string(output))}, err
	default:
		return nil, errors.New("unknown lsp action")
	}
}
func formatDiagnostics(path string, diagnostics []Diagnostic, earlier bool) string {
	if len(diagnostics) == 0 {
		return ""
	}
	var out strings.Builder
	if earlier {
		fmt.Fprintf(&out, "LSP diagnostics introduced by your earlier edit of %s:\n", path)
	} else {
		out.WriteString("LSP diagnostics introduced by this edit:\n")
	}
	for _, d := range diagnostics {
		sourcePath := d.File
		if sourcePath == "" {
			sourcePath = path
		}
		fmt.Fprintf(&out, "%s [%d:%d] %s [%v] (%s)\n", sourcePath, d.Range.Start.Line+1, d.Range.Start.Character+1, d.Message, d.Code, d.Source)
	}
	return out.String()
}

type processRunError struct {
	stage string
	err   error
}

func (e *processRunError) Error() string { return e.stage + ": " + e.err.Error() }
func (e *processRunError) Unwrap() error { return e.err }
func (m *Manager) runProcess(ctx context.Context, root string, command []string, input []byte, limit int64) (data []byte, resultErr error) {
	if m.backend == nil {
		return nil, errors.New("authorized process backend unavailable")
	}
	p, err := m.backend.StartLanguageServer(ctx, root, command, nil)
	if err != nil {
		return nil, &processRunError{stage: "start", err: err}
	}
	defer func() { resultErr = errors.Join(resultErr, p.Close()) }()
	writeDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(p, bytes.NewReader(input))
		closeErr := p.CloseWrite()
		writeDone <- errors.Join(err, closeErr)
	}()
	data, readErr := io.ReadAll(io.LimitReader(p, limit+1))
	if int64(len(data)) > limit {
		return nil, errors.New("process output exceeded developer tool limit")
	}
	writeErr := <-writeDone
	waitErr := p.Wait()
	if err := ctx.Err(); err != nil {
		return data, err
	}
	if waitErr != nil {
		if diagnostics, ok := p.(interface{ Stderr() string }); ok {
			stderr := diagnostics.Stderr()
			if len(stderr) > 64<<10 {
				stderr = stderr[len(stderr)-(64<<10):]
			}
			if stderr != "" && !strings.Contains(waitErr.Error(), stderr) {
				waitErr = fmt.Errorf("%w: %s", waitErr, stderr)
			}
		}
	}
	if err := errors.Join(readErr, writeErr, waitErr); err != nil {
		stage := "transport"
		if waitErr != nil {
			stage = "exit"
		}
		return data, &processRunError{stage: stage, err: err}
	}
	return data, nil
}
