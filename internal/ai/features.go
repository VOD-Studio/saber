package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/automation"
	"rua.plus/saber/internal/browser"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/codeexec"
	"rua.plus/saber/internal/computer"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/discovery"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/media"
	"rua.plus/saber/internal/memory"
	"rua.plus/saber/internal/memoryproviders"
	"rua.plus/saber/internal/model"
	"rua.plus/saber/internal/plugins"
	"rua.plus/saber/internal/task"
	"rua.plus/saber/internal/toolkit"
	"rua.plus/saber/internal/vault"
)

// Config 返回服务持有的配置，仅供内部装配；HTTP 界面必须另行裁剪所有凭据。
func (s *Service) Config() *config.Config { return s.config }

// Media 返回当前服务的媒体处理器；未装配时返回 nil 而不是伪造可用能力。
func (s *Service) Media() *media.Service { return s.featureMedia }

// Vault 返回只由可信客户端管理的凭据库；模型工具只能读取其受限元数据。
func (s *Service) Vault() *vault.Store { return s.featureVault }

func (s *Service) featureHost() toolkit.Host {
	return toolkit.Host{
		StateDir: s.featureStateDir,
		Call: func(ctx context.Context, name string, args map[string]any) (any, error) {
			if !agent.ToolAllowed(ctx, name) {
				return nil, fmt.Errorf("tool %q is outside the current tool scope", name)
			}
			return s.toolExecutor.ExecuteToolCall(ctx, name, args)
		},
		StartProcess: func(ctx context.Context, argv []string, env map[string]string) (toolkit.Process, error) {
			if s.executor == nil {
				return nil, errors.New("execution is not configured")
			}
			return s.executor.StartProgram(ctx, argv, env)
		},
		SaveOutput: func(ctx context.Context, data []byte) (string, error) {
			if s.executor == nil {
				return "", errors.New("execution is not configured")
			}
			return s.executor.SaveOutput(ctx, data)
		},
		Check: func(ctx context.Context, name string, external bool) error {
			if isPublicThread(ctx) || s.executor == nil {
				return errors.New("tool execution is unavailable in this context")
			}
			return s.executor.CheckFeature(ctx, name, external)
		},
		Workspace: func(ctx context.Context) (string, error) {
			if s.executor == nil {
				return "", errors.New("execution is not configured")
			}
			return s.executor.WorkspaceForContext(ctx)
		},
		Complete: s.completeFeatureRequest,
		IsAdmin: func(ctx context.Context) bool {
			identity, ok := chat.IdentityFromContext(ctx)
			return ok && !isPublicThread(ctx) && s.config.Commands.IsAdmin(identity)
		},
	}
}

func (s *Service) completeFeatureRequest(ctx context.Context, modelName string, messages []openai.ChatCompletionMessage) (string, error) {
	if !s.IsEnabled() || isPublicThread(ctx) {
		return "", errors.New("auxiliary model execution is unavailable")
	}
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return "", errors.New("auxiliary request requires trusted identity")
	}
	if modelName == "" {
		modelName = s.GetModelRegistry().GetDefault()
	}
	cfg, ok := s.config.AI.GetModelConfig(modelName)
	if !ok {
		return "", fmt.Errorf("model %q is not configured", modelName)
	}
	if err := s.core.WaitForRateLimit(ctx); err != nil {
		return "", err
	}
	request := agent.Request{Model: modelName, Messages: messages, MaxTokens: cfg.MaxTokens, ReasoningEffort: cfg.ReasoningEffort}
	if cfg.Temperature != nil {
		request.Temperature = *cfg.Temperature
	}
	response, err := model.AgentModel(s.getClient, s.agentRetry())(ctx, request, nil)
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

// ConfigureFeatures 在开放任务队列前装配能力，任何初始化失败都不会留下可调用的半套工具。
func (s *Service) ConfigureFeatures(stateDir string) (err error) {
	s.featuresMu.Lock()
	defer s.featuresMu.Unlock()
	if s.featuresReady {
		return nil
	}
	dir, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	s.featureStateDir = dir
	host := s.featureHost()
	registry := toolkit.New(host)
	s.extensions = registry
	defer func() {
		if err != nil {
			err = errors.Join(err, registry.Close())
			if s.extensionPlugins != nil {
				err = errors.Join(err, s.extensionPlugins.Close())
			}
		}
	}()
	register := func(provider toolkit.Provider) error {
		for _, definition := range provider.Definitions() {
			if definition.Tool.Function == nil {
				return errors.New("feature tool has no function")
			}
			name := definition.Tool.Function.Name
			if execution.LocalTool(name) || builtinToolName(name) {
				return fmt.Errorf("feature tool %q conflicts with a built-in tool", name)
			}
		}
		return registry.Register(provider)
	}
	if s.executor != nil && s.executor.DevTools() != nil {
		if err = register(s.executor.DevTools()); err != nil {
			return err
		}
	}
	browserProvider, err := browser.New(s.config.Browser, host)
	if err != nil {
		return err
	}
	if err = register(browserProvider); err != nil {
		return err
	}
	if b, ok := browserProvider.(*browser.Service); ok {
		s.featureBrowser = b
	}
	computerProvider, err := computer.New(s.config.Computer, host)
	if err != nil {
		return err
	}
	if err = register(computerProvider); err != nil {
		return err
	}
	s.featureMedia, err = media.New(s.config.MediaTools, host)
	if err != nil {
		return err
	}
	if err = register(s.featureMedia); err != nil {
		return err
	}
	codeProvider, err := codeexec.New(s.config.CodeExecution, host)
	if err != nil {
		return err
	}
	if err = register(codeProvider); err != nil {
		return err
	}
	s.featureVault, err = vault.New(s.config.Vault, host)
	if err != nil {
		return err
	}
	if s.featureBrowser != nil {
		s.featureVault.AttachBrowser(s.featureBrowser)
		s.featureBrowser.SetRedactor(s.featureVault.Redact)
	}
	if err = register(s.featureVault); err != nil {
		return err
	}
	s.extensionPlugins, err = plugins.New(s.config.Plugins, host)
	if err != nil {
		return err
	}
	s.extensionPlugins.AttachVault(s.featureVault)
	s.core.SetPlugins(s.extensionPlugins)
	if s.featureBrowser != nil {
		s.featureBrowser.SetSource(s.extensionPlugins.BrowserSource)
	}
	if s.executor != nil && s.executor.DevTools() != nil {
		s.executor.DevTools().SetReferenceProviders(s.extensionPlugins.ContextReferences)
		s.executor.DevTools().SetReferenceAutocomplete(s.extensionPlugins.ContextReferenceAutocomplete)
	}
	s.nativeMemory, err = memoryproviders.New(s.config.MemoryBackends, host, func(id string, cfg config.MemoryBackendConfig, h toolkit.Host) (memoryproviders.Backend, error) {
		if s.memory == nil {
			return nil, errors.New("native memory backend requires memory.enabled")
		}
		backend, err := s.memory.NewNativeBackend(id, cfg, h)
		if err == nil && s.executor != nil {
			backend.SetFileReader(s.executor.ReadAuthorizedFile)
		}
		return backend, err
	})
	if err != nil {
		return err
	}
	if s.memory != nil {
		if err = s.memory.ConfigureProviders(s.config.MemoryProviders, memoryProviderCaller{service: s}); err != nil {
			return err
		}
	}
	discoveryProvider, err := discovery.New(s.config.AgentTools, host, s.combinedDefinitions)
	if err != nil {
		return err
	}
	if err = register(discoveryProvider); err != nil {
		return err
	}
	moaProvider, err := NewMoA(s.config.ModelRuntime.MoA, host)
	if err != nil {
		return err
	}
	if err = register(moaProvider); err != nil {
		return err
	}
	s.automationService, err = automation.New(s.config.Automation, host)
	if err != nil {
		return err
	}
	if err = register(s.automationService); err != nil {
		return err
	}
	if err = s.bindAutomation(host); err != nil {
		return err
	}
	var reserved []string
	for _, t := range s.executor.Tools(context.Background()) {
		if t.Function != nil {
			reserved = append(reserved, t.Function.Name)
		}
	}
	for _, d := range registry.Definitions(context.Background()) {
		if d.Tool.Function != nil {
			reserved = append(reserved, d.Tool.Function.Name)
		}
	}
	if s.mcpManager != nil {
		for _, t := range s.mcpManager.ListTools() {
			reserved = append(reserved, t.Name)
		}
	}
	if err = s.extensionPlugins.SetReservedTools(reserved); err != nil {
		return err
	}
	s.featuresReady = true
	return nil
}

func (s *Service) combinedDefinitions(ctx context.Context) []toolkit.Definition {
	if isPublicThread(ctx) {
		return nil
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok || identity.Session.Validate() != nil || identity.SenderID == "" {
		return nil
	}
	var definitions []toolkit.Definition
	seen := make(map[string]bool)
	appendDefinition := func(d toolkit.Definition) {
		if d.Tool.Function == nil {
			return
		}
		name := d.Tool.Function.Name
		if name == "tool_search" || name == "tool_describe" || name == "tool_call" || seen[name] || !agent.ToolAllowed(ctx, name) {
			return
		}
		seen[name] = true
		definitions = append(definitions, d)
	}
	if s.executor != nil {
		for _, tool := range s.executor.Tools(ctx) {
			appendDefinition(toolkit.Definition{Tool: tool, Permission: tool.Function.Name})
		}
	}
	if s.tasks != nil {
		for _, tool := range []openai.Tool{taskTool(), scheduleTool(), traceTool(), batchTool()} {
			appendDefinition(toolkit.Definition{Tool: tool})
		}
		for _, tool := range s.controlDefinitions(ctx) {
			appendDefinition(toolkit.Definition{Tool: tool})
		}
	}
	if s.memory != nil {
		if _, ok := memory.Space(identity); ok {
			for _, tool := range []openai.Tool{memoryTool(), historyTool(), skillTool()} {
				appendDefinition(toolkit.Definition{Tool: tool})
			}
		}
	}
	if s.tweetToolAvailable(ctx) {
		appendDefinition(toolkit.Definition{Tool: tweetTool()})
	}
	if s.emojiToolAvailable(ctx) {
		appendDefinition(toolkit.Definition{Tool: emojiTool()})
	}
	if s.avatarAdapter(ctx) != nil {
		appendDefinition(toolkit.Definition{Tool: avatarTool()})
	}
	if s.extensions != nil {
		for _, d := range s.extensions.Definitions(ctx) {
			appendDefinition(d)
		}
	}
	if s.extensionPlugins != nil {
		for _, d := range s.extensionPlugins.AuthorizedDefinitions(ctx) {
			appendDefinition(d)
		}
	}
	if s.executor != nil && s.mcpManager != nil && s.mcpManager.IsEnabled() {
		for _, tool := range s.mcpManager.ListTools() {
			server := s.mcpManager.GetServerForTool(tool.Name)
			if s.executor.CheckMCP(ctx, server, tool.Name) != nil {
				continue
			}
			appendDefinition(toolkit.Definition{Tool: openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema}}, Permission: "mcp:" + server + ":" + tool.Name, External: true})
		}
	}
	return definitions
}

func (s *Service) filterDeferredTools(tools []openai.Tool) []openai.Tool {
	if s.config == nil {
		return tools
	}
	cfg := s.config.AgentTools
	if s.extensions == nil || !s.extensions.Contains("tool_search") {
		return tools
	}
	if cfg.ToolSearch == "off" {
		return tools
	}
	bytes := 0
	for _, tool := range tools {
		if data, err := json.Marshal(tool); err == nil {
			bytes += len(data)
		}
	}
	budget := cfg.ListingMaxTokens * 4
	if cfg.ToolSearch == "auto" && len(cfg.Deferred) == 0 && (budget == 0 || bytes <= budget) {
		return tools
	}
	always := make(map[string]bool, len(cfg.AlwaysVisible)+6)
	for _, name := range cfg.AlwaysVisible {
		always[name] = true
	}
	for _, name := range []string{"tool_search", "tool_describe", "tool_call", agent.DelegateTool, "saber_task", "saber_schedule", "saber_task_state", "clarify"} {
		always[name] = true
	}
	visible := tools[:0]
	for _, tool := range tools {
		if tool.Function == nil {
			continue
		}
		name := tool.Function.Name
		deferTool := cfg.ToolSearch == "on" || (cfg.ToolSearch == "auto" && budget > 0 && bytes > budget)
		for _, pattern := range cfg.Deferred {
			if match, err := path.Match(pattern, name); err == nil && match {
				deferTool = true
			}
		}
		if !deferTool || always[name] {
			visible = append(visible, tool)
		}
	}
	return visible
}

type toolLeaseHeldKey struct{}

func (s *Service) acquireToolLease(ctx context.Context) (context.Context, func(), error) {
	if held, _ := ctx.Value(toolLeaseHeldKey{}).(bool); held {
		return ctx, func() {}, nil
	}
	dir := task.WorkDir(ctx)
	if dir == "" && s.executor != nil {
		if identity, ok := chat.IdentityFromContext(ctx); ok {
			dir, _ = s.executor.Workspace(identity)
		}
	}
	if dir == "" {
		return ctx, func() {}, nil
	}
	unlock := s.toolLease.Lock(dir)
	if err := ctx.Err(); err != nil {
		unlock()
		return ctx, nil, err
	}
	return context.WithValue(ctx, toolLeaseHeldKey{}, true), unlock, nil
}

// CallTaskTool 只在已认证且仍运行的任务内执行工具，并把派发及结果写入任务事件。
func (s *Service) CallTaskTool(ctx context.Context, identity chat.Identity, taskID int64, name, arguments string) (any, error) {
	if s.tasks == nil || s.executor == nil {
		return nil, errors.New("task execution is unavailable")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil || args == nil {
		return nil, errors.New("tool arguments must be a JSON object")
	}
	bound, err := s.tasks.BindToolContext(ctx, identity, taskID)
	if err != nil {
		return nil, err
	}
	bound = execution.WithTask(bound, taskID, task.WorkDir(bound))
	record := agent.ToolRecord{Call: openai.ToolCall{ID: "external_" + uuid.NewString(), Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: name, Arguments: arguments}}}
	if err = s.tasks.RecordExternalTool(bound, identity, taskID, agent.Event{Kind: agent.ToolStarted, Tool: record}); err != nil {
		return nil, err
	}
	started := time.Now()
	value, callErr := s.toolExecutor.ExecuteToolCall(bound, name, args)
	record.Duration = time.Since(started)
	if callErr != nil {
		record.ErrorCode = "tool_execution_failed"
		record.Content = callErr.Error()
	} else {
		prepared := runtimeToolOutput(value)
		record.Images, record.Files, record.OutputID = prepared.Images, prepared.Files, prepared.OutputID
		if prepared.IsError {
			record.ErrorCode = "tool_failed"
		}
		data, encodeErr := json.Marshal(prepared.Value)
		if encodeErr != nil {
			callErr = encodeErr
			record.ErrorCode = "tool_result_invalid"
			record.Content = encodeErr.Error()
		} else {
			record.OriginalBytes = len(data)
			limit := s.config.Agent.MaxToolOutputBytes
			if limit == 0 {
				limit = 32768
			}
			if len(data) > limit {
				end := limit
				for end > 0 && !utf8.Valid(data[:end]) {
					end--
				}
				record.Content = string(data[:end]) + "\n[tool output truncated]"
				record.Truncated = true
			} else {
				record.Content = string(data)
			}
		}
	}
	if result, ok := value.(toolkit.Result); ok {
		record.Images = result.Images
		record.OutputID = result.OutputID
		if result.IsError {
			record.ErrorCode = "tool_failed"
		}
	}
	if result, ok := value.(execution.Result); ok {
		record.OutputID = result.OutputID
		if result.ExitCode != 0 || result.Error != "" {
			record.ErrorCode = "tool_failed"
		}
	}
	endErr := s.tasks.RecordExternalTool(context.WithoutCancel(bound), identity, taskID, agent.Event{Kind: agent.ToolFinished, Tool: record})
	return value, errors.Join(callErr, endErr)
}

// SubmitAPIChatTask 校验编辑器声明的目录和 MCP 名称，仍走同一持久会话续接链。
func (s *Service) SubmitAPIChatTask(ctx context.Context, message chat.Message, modelName, effort, cwd string, mcpNames []string) (task.Task, error) {
	identity := chat.Identity{Session: message.Session, SenderID: message.SenderID, Direct: message.Direct}
	if err := identity.Session.Validate(); err != nil || identity.SenderID == "" {
		return task.Task{}, errors.New("API task requires trusted identity")
	}
	if supplied, ok := chat.IdentityFromContext(ctx); ok && (supplied.Session != identity.Session || supplied.SenderID != identity.SenderID) {
		return task.Task{}, errors.New("API task identity mismatch")
	}
	ctx = chat.WithIdentity(ctx, identity)
	dir := s.taskDir
	granted := ""
	if s.executor != nil {
		granted, _ = s.executor.Workspace(identity)
	}
	if granted != "" {
		dir = granted
	}
	if strings.TrimSpace(cwd) != "" {
		requested, err := filepath.Abs(cwd)
		if err != nil {
			return task.Task{}, err
		}
		requested, err = filepath.EvalSymlinks(requested)
		if err != nil {
			return task.Task{}, err
		}
		if granted == "" || requested != granted {
			return task.Task{}, errors.New("editor cwd is not the currently authorized workspace")
		}
	}
	if len(mcpNames) == 0 {
		return s.SubmitChatTask(ctx, message, modelName, effort)
	}
	if s.mcpManager == nil || !s.mcpManager.IsEnabled() || s.executor == nil || granted == "" {
		return task.Task{}, errors.New("configured MCP access is unavailable")
	}
	toolContext := execution.WithTask(ctx, 0, dir)
	requested := make(map[string]bool, len(mcpNames))
	for _, name := range mcpNames {
		if name == "" || requested[name] {
			return task.Task{}, errors.New("invalid or repeated MCP server name")
		}
		found := false
		for _, tool := range s.mcpManager.ListTools() {
			if s.mcpManager.GetServerForTool(tool.Name) == name && s.executor.CheckMCP(toolContext, name, tool.Name) == nil {
				found = true
				break
			}
		}
		if !found {
			return task.Task{}, fmt.Errorf("MCP server %q is not configured and authorized", name)
		}
		requested[name] = true
	}
	if modelName == "" {
		modelName = s.GetModelRegistry().GetDefault()
	}
	if _, ok := s.GetModelRegistry().GetModelInfo(modelName); !ok {
		return task.Task{}, fmt.Errorf("model %q is not configured", modelName)
	}
	if len(effort) > 64 || strings.ContainsAny(effort, "\r\n\x00") {
		return task.Task{}, errors.New("invalid reasoning effort")
	}
	if err := message.Validate(); err != nil {
		return task.Task{}, err
	}
	req, err := s.taskRequest(message, modelName)
	if err != nil {
		return task.Task{}, err
	}
	req.ReasoningEffort = effort
	req.Messages = append(req.Messages, taskInput(message))
	for _, d := range s.combinedDefinitions(toolContext) {
		name := d.Tool.Function.Name
		server := s.mcpManager.GetServerForTool(name)
		if server == "" || requested[server] {
			req.ToolScope = append(req.ToolScope, name)
		}
	}
	req.ToolScope = append(req.ToolScope, "tool_search", "tool_describe", "tool_call", agent.DelegateTool)
	return s.tasks.SubmitTurn(ctx, message, dir, req)
}

func (s *Service) callMemoryProvider(ctx context.Context, name, kind, operation string, args map[string]any) (any, error) {
	if strings.HasPrefix(name, "native:") {
		if s.nativeMemory == nil {
			return nil, errors.New("native memory service is unavailable")
		}
		return s.nativeMemory.ProviderCall(ctx, name, kind, operation, args)
	}
	if s.extensionPlugins == nil {
		return nil, errors.New("plugin memory service is unavailable")
	}
	return s.extensionPlugins.ProviderCall(ctx, name, kind, operation, args)
}

// memoryProviderCaller 把插件与原生记忆提供方合成一个授权范式：
// 调用仍走宿主校验，技能读取只来自已安装并授权的插件包。
type memoryProviderCaller struct{ service *Service }

func (c memoryProviderCaller) ProviderCall(ctx context.Context, name, kind, operation string, args map[string]any) (any, error) {
	return c.service.callMemoryProvider(ctx, name, kind, operation, args)
}

// ReadSkill 读取插件 manifest 声明并每次重新授权的技能文件。
func (c memoryProviderCaller) ReadSkill(ctx context.Context, plugin, skill, resource string) ([]byte, error) {
	if c.service.extensionPlugins == nil {
		return nil, errors.New("plugin skill store is unavailable")
	}
	return c.service.extensionPlugins.ReadSkill(ctx, plugin, skill, resource)
}

// SkillCatalog 只返回当前运行插件声明的技能，未安装插件时为空。
func (c memoryProviderCaller) SkillCatalog(ctx context.Context) map[string][]string {
	if c.service.extensionPlugins == nil {
		return nil
	}
	return c.service.extensionPlugins.SkillCatalog(ctx)
}
