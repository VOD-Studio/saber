package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/task"
)

type runtimeLeaseKey struct{}
type runtimeWorkspaceKey struct{}

func (s *Service) routeSubagent(ctx context.Context, args map[string]any, child *agent.Request) error {
	cfg := s.config.Agent.Subagents
	cfg.ApplyDefaults()
	child.ChildMaxParallel = cfg.MaxParallel
	modelID := cfg.WorkerModel
	workerName, ok := args["worker"].(string)
	if args["worker"] != nil && !ok {
		return errors.New("worker 必须为管理员配置的名称")
	}
	if workerName != "" {
		worker, found := cfg.Workers[workerName]
		if !found {
			return fmt.Errorf("worker %q 未在 agent.subagents.workers 中配置", workerName)
		}
		modelID, child.ReasoningEffort = worker.Model, worker.ReasoningEffort
	}
	if modelID != "" {
		modelCfg, found := s.config.AI.GetModelConfig(modelID)
		if !found {
			return fmt.Errorf("子模型 %q 未配置", modelID)
		}
		child.Model, child.MaxTokens = modelID, modelCfg.MaxTokens
		if modelCfg.Temperature != nil {
			child.Temperature = *modelCfg.Temperature
		}
		if workerName == "" {
			child.ReasoningEffort = modelCfg.ReasoningEffort
		}
	}
	for _, key := range []string{"provider", "base_url", "api_key", "model", "identity", "work_dir"} {
		if args[key] != nil {
			return fmt.Errorf("delegate 不接受模型指定的 %s；请使用管理员配置的 worker", key)
		}
	}
	if raw := args["context"]; raw != nil {
		text, ok := raw.(string)
		if !ok || len(text) > agent.MaxSubagentGoalBytes {
			return errors.New("context 必须为不超过 8192 字节的字符串")
		}
		if text != "" {
			child.Messages[1].Content += "\n\n背景与约束:\n" + text
		}
	}
	if raw := args["images"]; raw != nil {
		var images []string
		switch value := raw.(type) {
		case []string:
			images = value
		case []any:
			for _, image := range value {
				ref, ok := image.(string)
				if !ok {
					return errors.New("images 必须为字符串数组")
				}
				images = append(images, ref)
			}
		default:
			return errors.New("images 必须为字符串数组")
		}
		if len(images) > 8 {
			return errors.New("每个子任务最多 8 张图片")
		}
		if len(images) > 0 {
			parts := []openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: child.Messages[1].Content}}
			for _, image := range images {
				if err := agent.ValidateImageReference(image); err != nil {
					return err
				}
				parts = append(parts, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: image, Detail: openai.ImageURLDetailAuto}})
			}
			child.Messages[1].Content, child.Messages[1].MultiContent = "", parts
		}
	}
	if raw := args["output_schema"]; raw != nil {
		schema, ok := raw.(map[string]any)
		if !ok {
			return errors.New("output_schema 必须为 JSON Schema 对象")
		}
		if _, err := agent.ResolveOutputSchema(schema); err != nil {
			return err
		}
		child.OutputSchema = schema
		data, err := json.Marshal(schema)
		if err != nil {
			return err
		}
		child.Messages[0].Content += "\n最终答复仅返回符合以下 JSON Schema 的 JSON，不使用 Markdown 围栏:\n" + string(data)
	}
	child.RunLimits = &agent.Limits{MaxRounds: cfg.MaxRounds, MaxTotalTokens: cfg.MaxTotalTokens, Timeout: time.Duration(s.config.Agent.TimeoutSeconds) * time.Second, MaxToolOutputBytes: s.config.Agent.MaxToolOutputBytes}
	if cfg.ChildTimeoutSeconds > 0 {
		child.RunLimits.Timeout = time.Duration(cfg.ChildTimeoutSeconds) * time.Second
	}
	return ctx.Err()
}

func (s *Service) prepareSubagentContext(ctx context.Context) (context.Context, func(context.Context) error, error) {
	if s.executor == nil {
		return ctx, nil, errors.New("worktree_isolation 需要已授权执行后端")
	}
	dir := subagentWorkspace(ctx)
	if value, ok := ctx.Value(runtimeWorkspaceKey{}).(string); ok {
		dir = value
	}
	derived, cleanup, err := s.executor.PrepareSubagentWorkspace(ctx, dir)
	if err != nil {
		return ctx, nil, err
	}
	ctx = execution.WithTask(ctx, task.ID(ctx), derived)
	ctx = context.WithValue(ctx, runtimeWorkspaceKey{}, derived)
	return ctx, cleanup, nil
}

func (s *Service) spawnSubagent(ctx context.Context, child agent.Request) (agent.SubagentResult, bool, error) {
	cfg := s.config.Agent.Subagents
	cfg.ApplyDefaults()
	if cfg.Mode == "inline" {
		return agent.SubagentResult{}, false, nil
	}
	identity, trusted := chat.IdentityFromContext(ctx)
	parentID := task.ID(ctx)
	if !trusted || s.tasks == nil || parentID <= 0 || !s.tasks.HasChildResultSink(ctx, identity, parentID) {
		if cfg.Mode == "background" {
			return agent.SubagentResult{}, false, errors.New("后台 delegate 需要可信持久父任务与可领取的结果出口")
		}
		return agent.SubagentResult{}, false, nil
	}
	dir := task.WorkDir(ctx)
	if value, ok := ctx.Value(runtimeWorkspaceKey{}).(string); ok {
		dir = value
	}
	if !cfg.WorktreeIsolation && dir != "" {
		if cfg.Mode == "background" {
			return agent.SubagentResult{}, false, errors.New("后台 delegate 与父任务共享工作目录，请启用 worktree_isolation 或使用 inline")
		}
		return agent.SubagentResult{}, false, nil
	}
	var cleanup func(context.Context) error
	if cfg.WorktreeIsolation {
		if s.executor == nil {
			return agent.SubagentResult{}, false, errors.New("worktree_isolation 需要已授权执行后端")
		}
		derived, release, err := s.executor.PrepareSubagentWorkspace(ctx, dir)
		if err != nil {
			return agent.SubagentResult{}, false, err
		}
		dir, cleanup = derived, release
		child.IsolateWorkspace = true
		child.PrepareChild = func(_ context.Context, id int64) error { return s.executor.BindSubagentWorkspace(ctx, id, derived) }
		child.ChildCleanup = release
	}
	var eventID [16]byte
	if _, err := rand.Read(eventID[:]); err != nil {
		if cleanup != nil {
			err = errors.Join(err, cleanup(context.WithoutCancel(ctx)))
		}
		return agent.SubagentResult{}, false, err
	}
	goal := child.Messages[1].Content
	if goal == "" && len(child.Messages[1].MultiContent) > 0 {
		goal = child.Messages[1].MultiContent[0].Text
	}
	parent, parentErr := s.tasks.Get(ctx, identity.Session, parentID)
	if parentErr != nil {
		if cleanup != nil {
			parentErr = errors.Join(parentErr, cleanup(context.WithoutCancel(ctx)))
		}
		return agent.SubagentResult{}, false, parentErr
	}
	message := chat.Message{ID: "delegate:" + hex.EncodeToString(eventID[:]), Session: identity.Session, SenderID: identity.SenderID, Direct: parent.Message.Direct, Text: goal}
	spawned, err := s.tasks.SpawnDetached(ctx, identity, parentID, message, dir, child)
	if err != nil {
		if cleanup != nil {
			err = errors.Join(err, cleanup(context.WithoutCancel(ctx)))
		}
		return agent.SubagentResult{}, false, err
	}
	return agent.SubagentResult{ID: spawned.ID, Status: agent.Status(spawned.Status), Detached: true, Model: child.Model, WorkDir: dir}, true, nil
}

func (s *Service) consumeChildResults(ctx context.Context) ([]agent.ChildCompletion, error) {
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok || s.tasks == nil || task.ID(ctx) <= 0 {
		return nil, nil
	}
	results, err := s.tasks.DrainChildCompletions(ctx, identity, task.ID(ctx))
	if err != nil {
		return nil, err
	}
	for _, result := range results {
		if err := s.ExternalMemoryDelegation(ctx, result.Goal, result.Content, fmt.Sprint(result.TaskID)); err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (s *Service) runtimeSummarizer(modelID string) agent.SummaryFunc {
	summary := s.summarizer(modelID)
	return func(ctx context.Context, input agent.SummaryInput) (agent.SummaryOutput, error) {
		if !isPublicThread(ctx) {
			notes, err := s.ExternalMemoryPreCompress(ctx, input.Messages)
			if err != nil {
				return agent.SummaryOutput{}, err
			}
			if strings.TrimSpace(notes) != "" {
				input.Messages = append(append([]openai.ChatCompletionMessage(nil), input.Messages...), openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: notes})
			}
		}
		return summary(ctx, input)
	}
}
