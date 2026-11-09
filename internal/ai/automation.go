package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/automation"
	"rua.plus/saber/internal/batch"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/memory"
	"rua.plus/saber/internal/task"
	"rua.plus/saber/internal/toolkit"
)

func (s *Service) prepareAutomation(ctx context.Context, msg chat.Message, skills []string, model string) (agent.Request, error) {
	i, ok := chat.IdentityFromContext(ctx)
	if !ok || i.Session != msg.Session || i.SenderID != msg.SenderID {
		return agent.Request{}, errors.New("automation preparation requires matching trusted identity")
	}
	if model == "" {
		model = s.GetModelRegistry().GetDefault()
	}
	req, err := s.taskRequest(msg, model)
	if err != nil {
		return req, err
	}
	req.Messages = append(req.Messages, taskInput(msg))
	if s.executor != nil {
		dir, err := s.executor.Workspace(i)
		if err != nil {
			return req, err
		}
		ctx = execution.WithTask(ctx, task.ID(ctx), dir)
	}
	req.Tools, _ = s.toolExecutor.PrepareTools(ctx)
	return s.PrepareSkillRequest(ctx, req, skills)
}

func (s *Service) bindAutomation(host toolkit.Host) error {
	if s.automationService != nil {
		bindings := automation.Bindings{Prepare: s.prepareAutomation, Command: s.ExecuteSessionCommand, LoadSkills: s.PrepareSkillRequest}
		if s.memory != nil {
			bindings.Skills = automation.SkillStore{
				List: func(ctx context.Context, i chat.Identity) ([]automation.Skill, error) {
					entries, err := s.memory.ListSkills(ctx, i)
					if err != nil {
						return nil, err
					}
					usage, err := s.memory.SkillUsage(ctx, i)
					if err != nil {
						return nil, err
					}
					uses := map[string]memory.SkillUse{}
					for _, u := range usage {
						uses[u.Name] = u
					}
					var out []automation.Skill
					for _, entry := range entries {
						pkg, err := s.memory.GetSkillPackage(ctx, i, entry.Name)
						if err != nil {
							return nil, err
						}
						u := uses[entry.Name]
						out = append(out, automation.Skill{Package: pkg, Created: entry.CreatedAt, LastUsed: u.LastUsed, Count: u.Count})
					}
					return out, nil
				},
				Get: s.memory.GetSkillPackage,
				Put: func(ctx context.Context, i chat.Identity, pkg memory.SkillPackage, version int64) error {
					mutation, err := s.memory.PutSkillPackage(chat.WithIdentity(ctx, i), i, pkg, version, memory.Source{Creator: i.SenderID, Task: task.ID(ctx)})
					if err != nil {
						return err
					}
					if mutation.Suggested {
						return fmt.Errorf("skill mutation pending approval #%d; not applied", mutation.Change.ID)
					}
					return nil
				},
				Remove: func(ctx context.Context, i chat.Identity, name string, version int64) error {
					mutation, err := s.memory.RemoveSkill(chat.WithIdentity(ctx, i), i, name, version, memory.Source{Creator: i.SenderID, Task: task.ID(ctx)})
					if err != nil {
						return err
					}
					if mutation.Suggested {
						return fmt.Errorf("skill removal pending approval #%d; not applied", mutation.Change.ID)
					}
					return nil
				},
			}
			s.memory.SetSkillGuards(s.automationService.SkillWritable, s.automationService.SkillDeletable)
			s.memory.SetSkillAudit(s.automationService.RecordSkillMutation)
		}
		s.automationService.Configure(bindings)
		s.automationService.AttachTaskManager(s.tasks)
	}
	if s.config.Batch.Enabled {
		if s.tasks == nil {
			return errors.New("batch requires persistent task manager")
		}
		runner, err := batch.New(s.config.Batch, host, s.tasks, func(ctx context.Context, msg chat.Message, model string) (agent.Request, error) {
			req, err := s.prepareAutomation(ctx, msg, nil, model)
			if err != nil {
				return req, err
			}
			req.RunLimits = &agent.Limits{MaxRounds: s.config.Agent.MaxIterations, Timeout: time.Duration(s.config.Agent.TimeoutSeconds) * time.Second, MaxToolOutputBytes: s.config.Agent.MaxToolOutputBytes, MaxTotalTokens: s.config.Agent.Budget.MaxTotalTokens}
			if s.config.Batch.IsolateWorkspace {
				if s.executor == nil {
					return req, errors.New("batch isolation requires execution backend")
				}
				identity, ok := chat.IdentityFromContext(ctx)
				if !ok {
					return req, errors.New("batch isolation requires trusted identity")
				}
				var cleanup func(context.Context) error
				req.IsolateWorkspace = true
				req.PrepareChild = func(setupCtx context.Context, id int64) error {
					dir, closeWorkspace, err := s.executor.PrepareBatchWorkspace(setupCtx, id)
					if err != nil {
						return err
					}
					cleanup = closeWorkspace
					if err = s.tasks.RebindQueuedWorkDir(setupCtx, identity, id, dir); err != nil {
						return errors.Join(err, closeWorkspace(setupCtx))
					}
					return nil
				}
				req.ChildCleanup = func(closeCtx context.Context) error {
					if cleanup == nil {
						return errors.New("batch isolated workspace was not prepared")
					}
					return cleanup(closeCtx)
				}
			}
			return req, nil
		})
		if err != nil {
			return err
		}
		s.batchRunner = runner
	}
	return nil
}

func isAutomationTool(name string) bool {
	return name == "automation_loop" || name == "automation_goal" || name == "automation_heartbeat" || name == "cronjob_manage" || name == "curator"
}
func (s *Service) executeAutomationTool(ctx context.Context, name string, args map[string]any) (any, error) {
	if s.automationService == nil {
		return nil, errors.New("automation disabled")
	}
	if isPublicThread(ctx) {
		return nil, errors.New("public rounds cannot use automation")
	}
	return s.automationService.Execute(ctx, name, args)
}

// ExecuteAutomationCommand 让 CLI、平台和桌面控制共享工具实际授权路径。
func (s *Service) ExecuteAutomationCommand(ctx context.Context, name string, args map[string]any) (any, error) {
	return s.executeAutomationTool(ctx, name, args)
}

// Batch 将可信请求交给持久批量任务并返回真实轨迹统计。
func (s *Service) Batch(ctx context.Context, opts batch.Options) (batch.Summary, error) {
	if isPublicThread(ctx) {
		return batch.Summary{}, errors.New("public rounds cannot run batches")
	}
	if s.batchRunner == nil {
		return batch.Summary{}, errors.New("batch disabled or task manager unavailable")
	}
	if opts.Model == "" {
		opts.Model = s.GetModelRegistry().GetDefault()
	}
	return s.batchRunner.Run(ctx, opts)
}

// PrepareBatchTask 把创建时采样工具与当前授权求交集，绝不放宽被撤销的权限。
func (s *Service) PrepareBatchTask(ctx context.Context, req agent.Request) (agent.Request, error) {
	if task.ID(ctx) <= 0 {
		return req, nil
	}
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return req, errors.New("batch task requires trusted identity")
	}
	t, err := s.tasks.Get(ctx, i.Session, task.ID(ctx))
	if err != nil {
		return req, err
	}
	if !strings.HasPrefix(t.EventKey, "saber-batch:") {
		return req, nil
	}
	if len(t.Request.ToolScope) == 0 {
		return req, errors.New("batch sampled tool scope missing")
	}
	allowed := map[string]bool{}
	for _, name := range t.Request.ToolScope {
		allowed[name] = true
	}
	req.ToolScope = append([]string(nil), t.Request.ToolScope...)
	var filtered []openai.Tool
	for _, tool := range req.Tools {
		if tool.Function != nil && allowed[tool.Function.Name] {
			filtered = append(filtered, tool)
		}
	}
	req.Tools = filtered
	return req, nil
}

// PrepareAutomationTask 在持久任务真正执行前重载附属技能并复核自动化发生。
func (s *Service) PrepareAutomationTask(ctx context.Context, req agent.Request) (agent.Request, error) {
	if s.automationService == nil {
		return req, nil
	}
	return s.automationService.PrepareTask(ctx, req)
}

// IsBatchTask 只根据已持久接收的来源事件判断批量执行，不从模型正文读取控制字段。
func (s *Service) IsBatchTask(ctx context.Context) bool {
	if s.tasks == nil || task.ID(ctx) <= 0 {
		return false
	}
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return false
	}
	t, err := s.tasks.Get(ctx, i.Session, task.ID(ctx))
	return err == nil && strings.HasPrefix(t.EventKey, "saber-batch:")
}

// ExecuteSessionCommand 在共享命令注册表上执行一条会话指令，
// 供循环自动化复用平台命令的真实授权、回执与一次性语义。
func (s *Service) ExecuteSessionCommand(ctx context.Context, command string) (string, error) {
	if s.sessionCommandDispatcher == nil {
		return "", errors.New("session command dispatcher unavailable")
	}
	text := strings.TrimSpace(command)
	if !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "!") {
		return "", errors.New("session command must start with a / or ! prefix")
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return "", errors.New("session command requires trusted identity")
	}
	message := chat.Message{Session: identity.Session, SenderID: identity.SenderID, Direct: identity.Direct, Text: text}
	if id := task.ID(ctx); id > 0 {
		// 稳定消息 ID 让一次性命令的持久回执在任务重放时仍然生效。
		message.ID = fmt.Sprintf("automation-%d", id)
	}
	return s.sessionCommandDispatcher(ctx, message)
}
