package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/memory"
	"rua.plus/saber/internal/model"
)

const reviewSystemPrompt = `你是一个专业的长期记忆与程序性经验（技能）提炼助手。
你的职责是复盘最近的对话记录，提炼需要长期保留的关键事实或可复用技能：
1. 事实记忆（saber_memory）：
   - 个人空间：提炼该用户的稳定偏好、习惯与反复纠正的要求（例如语言偏好、编码风格、表达习惯）。
   - 群聊空间：提炼全群共识、团队约定、已确认的发布流程或决定。严禁在群聊空间提炼某具体成员的个人档案或隐私信息。
2. 程序性技能（saber_skill）：
   - 当对话中展现出成功解决复杂故障的排错经验（Troubleshooting Playbook）、或成功执行的多步骤复杂工作流程时，使用 saber_skill 工具的 add 或 replace 操作沉淀为可复用技能。
   - 技能必须具备规范名称（kebab-case Slug）、清晰的适用场景描述与 Markdown 步骤/排错指引。
   - 严禁包含敏感凭证、密码或特定临时路径，需泛化为参数。
3. 忽略单次临时任务、闲聊调侃、猜测假设、未证实的事实或敏感密码密钥。
4. 你可以使用 saber_memory、saber_history 和 saber_skill 工具查看当前空间记忆或技能，沉淀新增内容或修改过时内容。
5. 若最近对话没有值得长期保留的新增或修改内容，请直接完成，无需调用工具。`

// triggerBackgroundReview 异步启动一次后台待复盘记录扫描。
func (s *Service) triggerBackgroundReview() {
	if s.memory == nil || s.config == nil || !s.config.Memory.Enabled || !s.config.Memory.AutoLearn {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := s.ReviewPending(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Debug("后台待复盘扫描结束", "error", err)
		}
	}()
}

// ReviewPending 扫描所有达到复盘轮次阈值且未暂停的记忆空间，并发起轻量复盘。
func (s *Service) ReviewPending(ctx context.Context) error {
	if s.memory == nil || s.config == nil || !s.config.Memory.Enabled || !s.config.Memory.AutoLearn {
		return nil
	}
	minTurns := s.config.Memory.ReviewInterval
	if minTurns <= 0 {
		minTurns = 5
	}
	scopes, err := s.memory.PendingReviewScopes(ctx, minTurns)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if err := s.ReviewScope(ctx, scope); err != nil {
			slog.Warn("空间复盘失败", "scope", scope.Key(), "error", err)
		}
	}
	return nil
}

// ReviewScope 在受限工具沙箱中对指定空间的未复盘轮次执行后台复盘任务。
// 该过程使用独立的调用标记，不触发递归复盘，仅允许记忆与历史工具，
// 并受到超时、最大轮数、配额与防重复学习机制严格约束。
func (s *Service) ReviewScope(ctx context.Context, scope memory.Scope) error {
	if s.memory == nil || s.config == nil || !s.config.Memory.Enabled || !s.config.Memory.AutoLearn {
		return nil
	}
	if _, loaded := s.runningReviews.LoadOrStore(scope.Key(), struct{}{}); loaded {
		return nil
	}
	defer s.runningReviews.Delete(scope.Key())

	paused, err := s.memory.IsPaused(ctx, scope)
	if err != nil {
		return err
	}
	if paused {
		return nil
	}

	// 空间配额检查
	settings, err := s.memory.ScopeSettings(ctx, scope)
	if err != nil {
		return err
	}
	if s.config.Memory.MaxScopeReviews > 0 && settings.ReviewCount >= s.config.Memory.MaxScopeReviews {
		slog.Info("跳过后台记忆复盘：已达到空间复盘次数上限", "scope", scope.Key(), "reviews", settings.ReviewCount)
		return nil
	}
	if s.config.Memory.MaxScopeTokens > 0 && settings.TokenCount >= s.config.Memory.MaxScopeTokens {
		slog.Info("跳过后台记忆复盘：已达到空间复盘 token 上限", "scope", scope.Key(), "tokens", settings.TokenCount)
		return nil
	}

	// 全局配额检查
	gReviews, gTokens, err := s.memory.GlobalUsage(ctx)
	if err != nil {
		return err
	}
	if s.config.Memory.MaxGlobalReviews > 0 && gReviews >= s.config.Memory.MaxGlobalReviews {
		slog.Info("跳过后台记忆复盘：已达到全局复盘次数上限", "reviews", gReviews)
		return nil
	}
	if s.config.Memory.MaxGlobalTokens > 0 && gTokens >= s.config.Memory.MaxGlobalTokens {
		slog.Info("跳过后台记忆复盘：已达到全局复盘 token 上限", "tokens", gTokens)
		return nil
	}

	// 查询未复盘对话投影
	records, err := s.memory.UnreviewedProjections(ctx, scope, 20)
	if err != nil || len(records) == 0 {
		return err
	}
	taskIDs := make([]int64, len(records))
	for i, r := range records {
		taskIDs[i] = r.TaskID
	}

	// 构造运行时身份
	var identity chat.Identity
	if scope.Kind == memory.ScopeUser {
		identity = chat.Identity{
			Session:  chat.Session{Platform: scope.Platform, Account: scope.Account, Conversation: "direct"},
			SenderID: scope.ID,
			Direct:   true,
		}
	} else {
		identity = chat.Identity{
			Session:  chat.Session{Platform: scope.Platform, Account: scope.Account, Conversation: scope.ID},
			SenderID: "system:memory_review",
			Direct:   false,
		}
	}

	// 构造复盘消息
	var userPrompt strings.Builder
	fmt.Fprintf(&userPrompt, "以下是【%s】最近未复盘的 %d 轮对话记录：\n", scope, len(records))
	for _, r := range records {
		timeStr := r.CreatedAt.Format("2006-01-02 15:04:05")
		fmt.Fprintf(&userPrompt, "\n[轮次任务 #%d, 发送人: %s, 时间: %s]\n用户: %s\nSaber: %s\n",
			r.TaskID, r.SenderID, timeStr, r.UserText, r.AssistantText)
	}
	userPrompt.WriteString("\n请复盘上述对话，提炼需要沉淀为长期记忆的事实、偏好或共识。如无需更新记忆，无需调用工具。")

	modelName := s.config.Memory.ReviewModel
	if modelName == "" {
		modelName = s.config.AI.DefaultModel
	}

	req := agent.Request{
		Model: modelName,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: reviewSystemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: userPrompt.String()},
		},
		Tools: []openai.Tool{memoryTool(), historyTool(), skillTool()},
	}

	retry := &RetryConfigWrapper{
		MaxRetries:    1,
		InitialDelay:  time.Second,
		MaxDelay:      3 * time.Second,
		BackoffFactor: 2,
	}
	retry.CircuitBreaker = s.circuitBreaker

	// 沙箱运行时：限制最多 2 轮模型请求、30 秒超时、受限工具集
	runtime := agent.Runtime{
		Context: s.contextPolicy(),
		Limits: agent.Limits{
			MaxRounds:          2,
			Timeout:            30 * time.Second,
			MaxToolOutputBytes: 8192,
		},
		Model: model.AgentModel(func(m string) (*Client, error) {
			return s.getClient(m)
		}, retry),
		Execute: func(execCtx context.Context, name string, args map[string]any) (agent.ToolOutput, error) {
			if name != "saber_memory" && name != "saber_history" && name != "saber_skill" {
				return agent.ToolOutput{IsError: true}, fmt.Errorf("复盘沙箱仅允许使用记忆、历史与技能工具，禁止使用 %q", name)
			}
			var val any
			var toolErr error
			switch name {
			case "saber_memory":
				val, toolErr = s.executeMemoryTool(execCtx, args)
			case "saber_history":
				val, toolErr = s.executeHistoryTool(execCtx, args)
			case "saber_skill":
				val, toolErr = s.executeSkillTool(execCtx, args)
			}
			return agent.ToolOutput{Value: val}, toolErr
		},
	}

	execCtx := chat.WithIdentity(ctx, identity)
	execCtx = withReviewContext(execCtx)

	result, runErr := runtime.Run(execCtx, req, nil)

	// 无论模型是否有提炼结果，标记该批任务已完成复盘，记录消耗的 token
	_ = s.memory.MarkTasksReviewed(ctx, scope, taskIDs)
	if result.Usage.TotalTokens > 0 {
		_ = s.memory.RecordReviewUsage(ctx, scope, result.Usage.TotalTokens)
	}

	if runErr != nil && !errors.Is(runErr, agent.ErrBudgetExhausted) {
		slog.Warn("后台记忆复盘运行异常", "scope", scope.Key(), "error", runErr)
		return runErr
	}
	return nil
}
