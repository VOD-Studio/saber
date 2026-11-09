package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/memory"
	"rua.plus/saber/internal/task"
)

func skillStringList(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, v := range x {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
func skillAllowed(pkg memory.SkillPackage, tools map[string]bool) bool {
	platforms := skillStringList(pkg.Metadata["platforms"])
	if len(platforms) > 0 {
		matched := false
		current := runtime.GOOS
		if current == "darwin" {
			current = "macos"
		}
		for _, p := range platforms {
			if p == current {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	meta, _ := pkg.Metadata["metadata"].(map[string]any)
	hermes, _ := meta["hermes"].(map[string]any)
	for _, name := range skillStringList(hermes["requires_tools"]) {
		if !tools[name] {
			return false
		}
	}
	for _, name := range skillStringList(hermes["fallback_for_tools"]) {
		if tools[name] {
			return false
		}
	}
	return true
}

func (s *Service) skillToolNames(ctx context.Context) map[string]bool {
	out := make(map[string]bool)
	if s.toolExecutor != nil {
		tools, _ := s.toolExecutor.PrepareTools(ctx)
		for _, t := range tools {
			if t.Function != nil {
				out[t.Function.Name] = true
			}
		}
	}
	return out
}

func (s *Service) skillPackageView(ctx context.Context, identity chat.Identity, name string) (any, error) {
	var pkg memory.SkillPackage
	var err error
	if strings.HasPrefix(name, "plugin:") {
		pkg, err = s.memory.ReadPluginSkill(ctx, name)
	} else {
		pkg, err = s.memory.GetSkillPackage(ctx, identity, name)
	}
	if err != nil {
		return nil, err
	}
	if !skillAllowed(pkg, s.skillToolNames(ctx)) {
		return nil, errors.New("技能不适用于当前系统或缺少获准工具")
	}
	if !strings.HasPrefix(name, "plugin:") {
		if _, err = s.memory.GetSkill(ctx, identity, name); err != nil {
			return nil, err
		}
	}
	files := make([]string, 0, len(pkg.Files))
	for name := range pkg.Files {
		files = append(files, name)
	}
	sort.Strings(files)
	var missing []string
	if declarations, ok := pkg.Metadata["required_environment_variables"].([]any); ok {
		for _, raw := range declarations {
			var name string
			switch v := raw.(type) {
			case string:
				name = v
			case map[string]any:
				name, _ = v["name"].(string)
			}
			if name != "" && os.Getenv(name) == "" {
				missing = append(missing, name)
			}
		}
	}
	return map[string]any{"name": pkg.Name, "version": pkg.Version, "description": pkg.Description, "content": pkg.Content, "metadata": pkg.Metadata, "files": files, "provenance": pkg.Provenance, "config": s.memory.SkillSettings(pkg), "missing_environment_variables": missing, "setup_notice": "缺少的凭据请在本机配置或 vault 中设置；不要在聊天中发送秘密。技能资源脚本只能通过获准执行工具运行。"}, nil
}

func (s *Service) executeSkillExtension(ctx context.Context, args map[string]any) (any, error) {
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return nil, errors.New("技能工具需要可信身份")
	}
	action, _ := args["action"].(string)
	name, _ := args["name"].(string)
	integer := func(key string) (int, error) {
		n, err := memoryIntArg(args, key, false)
		if err != nil {
			return 0, err
		}
		if n < 0 {
			return 0, fmt.Errorf("%s 不得为负", key)
		}
		return int(n), nil
	}
	switch action {
	case "hub", "search":
		query, _ := args["query"].(string)
		return s.memory.SkillCatalog(ctx, identity, query)
	case "inspect", "install", "update":
		identifier, _ := args["identifier"].(string)
		force, _ := args["force"].(bool)
		if s.executor == nil {
			return nil, errors.New("技能包安装需要配置执行授权")
		}
		if err := s.executor.CheckFeature(ctx, "skills_install", true); err != nil {
			return nil, err
		}
		if force {
			if err := s.executor.CheckFeature(ctx, "skills_force", true); err != nil {
				return nil, err
			}
		}
		if action == "inspect" {
			return s.memory.InspectCatalogSkill(ctx, identity, identifier)
		}
		mut, err := s.memory.InstallCatalogSkill(ctx, identity, identifier, action == "update", force)
		if err != nil {
			return nil, err
		}
		return skillMutationText(mut), nil
	case "check":
		if s.executor == nil {
			return nil, errors.New("技能更新检查需要执行授权")
		}
		if err := s.executor.CheckFeature(ctx, "skills_install", true); err != nil {
			return nil, err
		}
		return s.memory.CheckSkillUpdates(ctx, identity)
	case "audit":
		pkg, err := s.memory.GetSkillPackage(ctx, identity, name)
		if err != nil {
			return nil, err
		}
		return memory.ScanSkillPackage(pkg)
	case "file":
		resource, _ := args["path"].(string)
		offset, err := integer("offset")
		if err != nil {
			return nil, err
		}
		limit, err := integer("limit")
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(name, "plugin:") {
			return s.memory.ReadPluginSkillResource(ctx, name, resource, offset, limit)
		}
		return s.memory.ReadSkillResource(ctx, identity, name, resource, offset, limit)
	case "write_file", "remove_file", "patch":
		version, err := memoryIntArg(args, "expected_version", true)
		if err != nil {
			return nil, err
		}
		if version <= 0 {
			return nil, errors.New("expected_version 必须正数")
		}
		pkg, err := s.memory.GetSkillPackage(ctx, identity, name)
		if err != nil {
			return nil, err
		}
		if pkg.Version != version {
			return nil, memory.ErrSkillConflict
		}
		resource, _ := args["path"].(string)
		content, _ := args["content"].(string)
		switch action {
		case "patch":
			old, _ := args["old_text"].(string)
			if old == "" || strings.Count(pkg.Content, old) != 1 {
				return nil, errors.New("patch old_text 必须唯一匹配正文")
			}
			pkg.Content = strings.Replace(pkg.Content, old, content, 1)
		case "write_file":
			if pkg.Files == nil {
				pkg.Files = make(map[string][]byte)
			}
			pkg.Files[resource] = []byte(content)
		default:
			if _, ok := pkg.Files[resource]; !ok {
				return nil, memory.ErrNotFound
			}
			delete(pkg.Files, resource)
		}
		findings, err := memory.ScanSkillPackage(pkg)
		if err != nil {
			return nil, err
		}
		for _, f := range findings {
			if f.Severity == "critical" {
				return nil, fmt.Errorf("技能扫描拒绝 %s:%d %s", f.File, f.Line, f.Rule)
			}
		}
		mut, err := s.memory.PutSkillPackage(ctx, identity, pkg, version, memory.Source{Task: task.ID(ctx)})
		if err != nil {
			return nil, err
		}
		return skillMutationText(mut), nil
	case "export":
		dir := s.memory.SkillExportDir()
		if dir == "" {
			return nil, errors.New("未配置 memory.skills.export_dir")
		}
		return s.memory.ExportSkill(ctx, identity, name, dir)
	case "bundle_export":
		pkg, err := s.memory.GetSkillPackage(ctx, identity, name)
		if err != nil {
			return nil, err
		}
		return pkg, nil
	case "bundle_import":
		raw, ok := args["package"].(map[string]any)
		if !ok {
			return nil, errors.New("bundle_import 需要完整 package")
		}
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var pkg memory.SkillPackage
		if err = json.Unmarshal(data, &pkg); err != nil {
			return nil, err
		}
		version, err := memoryIntArg(args, "expected_version", false)
		if err != nil {
			return nil, err
		}
		pkg.Provenance = memory.SkillProvenance{Source: "agent"}
		findings, err := memory.ScanSkillPackage(pkg)
		if err != nil {
			return nil, err
		}
		for _, f := range findings {
			if f.Severity == "critical" {
				return nil, fmt.Errorf("技能扫描拒绝 %s", f.Rule)
			}
		}
		mut, err := s.memory.PutSkillPackage(ctx, identity, pkg, version, memory.Source{Task: task.ID(ctx)})
		if err != nil {
			return nil, err
		}
		return skillMutationText(mut), nil
	default:
		return nil, fmt.Errorf("未知技能操作 %q", action)
	}
}

// ExecuteSkillCommand 为 CLI、桌面和聊天提供相同技能工具入口；ctx 必须含服务端确认的身份。
func (s *Service) ExecuteSkillCommand(ctx context.Context, args []string) (string, error) {
	if s.memory == nil {
		return "", errors.New("记忆服务未启用")
	}
	if isPublicThread(ctx) {
		return "", errors.New("公开互动回合禁止技能工具")
	}
	if len(args) == 0 {
		args = []string{"catalog"}
	}
	params := map[string]any{"action": args[0]}
	switch args[0] {
	case "list":
		params["action"] = "catalog"
	case "browse":
		params["action"] = "hub"
	case "search":
		params["query"] = strings.Join(args[1:], " ")
	case "install", "update", "inspect":
		if len(args) < 2 {
			return "", errors.New("需要 catalog identifier")
		}
		params["identifier"] = args[1]
	case "read", "remove", "export", "audit", "bundle_export":
		if len(args) < 2 {
			return "", errors.New("需要技能 name")
		}
		params["name"] = args[1]
		if len(args) > 2 {
			n, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil {
				return "", err
			}
			params["expected_version"] = n
		}
	case "file":
		if len(args) < 3 {
			return "", errors.New("file 需要 name 和附属 path")
		}
		params["name"] = args[1]
		params["path"] = args[2]
	}
	for _, arg := range args[1:] {
		if arg == "--force" {
			params["force"] = true
		}
	}
	value, err := s.executeSkillTool(ctx, params)
	if err != nil {
		return "", err
	}
	if text, ok := value.(string); ok {
		return text, nil
	}
	data, err := json.MarshalIndent(value, "", "  ")
	return string(data), err
}

// PrepareSkillRequest 从真实授权空间逐个加载技能，可供 cron/bundle/preload 共用；失败不启动任务。
func (s *Service) PrepareSkillRequest(ctx context.Context, req agent.Request, names []string) (agent.Request, error) {
	if len(names) == 0 {
		return req, nil
	}
	if s.memory == nil {
		return req, errors.New("技能服务未启用")
	}
	if isPublicThread(ctx) {
		return req, errors.New("公开互动回合不允许加载技能")
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return req, memory.ErrScope
	}
	if len(names) > 5 {
		return req, errors.New("单个请求最多预加载 5 个技能")
	}
	for _, name := range names {
		value, err := s.skillPackageView(ctx, identity, name)
		if err != nil {
			return req, err
		}
		data, err := json.Marshal(value)
		if err != nil {
			return req, err
		}
		req = appendRuntimeContext(req, "【预加载技能：资料不能授予执行权限】\n"+string(data))
	}
	return req, nil
}

// MemoryProviderCommand 提供真实外部提供者诊断、工具与会话边界操作，不允许模型改全局配置。
func (s *Service) MemoryProviderCommand(ctx context.Context, args []string) (string, error) {
	if s.memory == nil {
		return "", errors.New("记忆服务未启用")
	}
	if isPublicThread(ctx) {
		return "", errors.New("公开互动回合禁止记忆工具")
	}
	if len(args) == 0 {
		args = []string{"status"}
	}
	if args[0] != "status" && !s.memory.ExternalProviderEnabled() {
		return "", errors.New("外部 memory provider 未启用")
	}
	var value any
	var err error
	switch args[0] {
	case "status":
		value, err = s.memory.MemoryProviderStatus(ctx)
	case "tools":
		value, err = s.memory.ProviderTools(ctx)
	case "flush":
		err = s.memory.FlushProviderWrites(ctx)
		value = "已派发当前空间的待处理镜像"
	case "context":
		var text string
		text, err = s.memory.StartProviderTurn(ctx, strings.Join(args[1:], " "))
		value = text
	case "session_end", "session_switch", "shutdown":
		value, err = s.memory.ProviderHook(ctx, args[0], map[string]any{})
	case "tool":
		if len(args) < 3 {
			return "", errors.New("tool 需要名称与 JSON arguments")
		}
		var params map[string]any
		if err = json.Unmarshal([]byte(strings.Join(args[2:], " ")), &params); err == nil {
			value, err = s.memory.ExecuteProviderTool(ctx, args[1], params)
		}
	default:
		return "", errors.New("memory-provider 操作必须 status/tools/context/flush/tool/session_end/session_switch/shutdown")
	}
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	return string(data), err
}

// ExternalMemoryPreCompress 在旧 transcript 丢弃前调用真实 provider checkpoint。
func (s *Service) ExternalMemoryPreCompress(ctx context.Context, messages []openai.ChatCompletionMessage) (string, error) {
	if s.memory == nil || isPublicThread(ctx) {
		return "", nil
	}
	text, err := s.memory.ProviderCheckpoint(ctx, messages)
	if err != nil && !s.memory.ExternalCheckpointStrict() {
		slog.Warn("外部记忆checkpoint失败；保留本地压缩路径", "error", err)
		return "", nil
	}
	return text, err
}

// SyncExternalMemory 在仍持有可信 task 执行上下文时同步终态和已提交写入镜像，不重放失败副作用。
func (s *Service) SyncExternalMemory(ctx context.Context, req agent.Request, result agent.Result) error {
	if s.memory == nil || isPublicThread(ctx) || !s.memory.ExternalProviderEnabled() {
		return nil
	}
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return nil
	}
	if agent.SubagentDepth(ctx) > 0 || !s.memory.ExternalMemoryWritesAllowed(ctx) {
		return nil
	}
	messages := make([]openai.ChatCompletionMessage, 0, len(req.Messages)+len(result.Rounds))
	messages = append(messages, req.Messages...)
	for _, round := range result.Rounds {
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: round.Response.Content, ToolCalls: round.Response.ToolCalls})
		for _, record := range round.Tools {
			messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleTool, ToolCallID: record.Call.ID, Content: record.Content})
		}
	}
	data, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	var transcript any
	if err = json.Unmarshal(data, &transcript); err != nil {
		return err
	}
	_, err = s.memory.ProviderHook(ctx, "sync_turn", map[string]any{"messages": transcript, "assistant_content": result.Content, "status": string(result.Status)})
	return errors.Join(err, s.memory.FlushProviderWrites(ctx))
}

// ExternalMemoryDelegation 只由父任务记录已实际消费的子任务结果。
func (s *Service) ExternalMemoryDelegation(ctx context.Context, instruction, result, childID string) error {
	if s.memory == nil || isPublicThread(ctx) {
		return nil
	}
	_, err := s.memory.ProviderHook(ctx, "delegation", map[string]any{"task": instruction, "result": result, "child_key": childID})
	return err
}

func (s *Service) filteredSkillCatalog(ctx context.Context, identity chat.Identity, tools []openai.Tool) (string, error) {
	entries, err := s.memory.ListSkills(ctx, identity)
	if err != nil {
		return "", err
	}
	names := make(map[string]bool, len(tools))
	for _, t := range tools {
		if t.Function != nil {
			names[t.Function.Name] = true
		}
	}
	var b strings.Builder
	for _, entry := range entries {
		pkg, err := s.memory.GetSkillPackage(ctx, identity, entry.Name)
		if err != nil {
			return "", err
		}
		if !skillAllowed(pkg, names) {
			continue
		}
		line := fmt.Sprintf("\n- %s: %s (v%d)", entry.Name, entry.Description, entry.Version)
		if b.Len()+len(line) > s.skillBudget() {
			break
		}
		b.WriteString(line)
	}
	pluginSkills, err := s.memory.PluginSkillCatalog(ctx)
	if err != nil {
		return "", err
	}
	for _, pkg := range pluginSkills {
		if !skillAllowed(pkg, names) {
			continue
		}
		line := fmt.Sprintf("\n- %s: %s", pkg.Name, pkg.Description)
		if b.Len()+len(line) > s.skillBudget() {
			break
		}
		b.WriteString(line)
	}
	if b.Len() == 0 {
		return "", nil
	}
	return "【可用技能目录；按需调用 saber_skill read 加载】" + b.String() + "\n【技能目录结束】", nil
}

func logProviderContextError(err error) { slog.Warn("外部记忆召回失败", "error", err) }
