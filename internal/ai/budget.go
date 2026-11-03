package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/model"
)

const budgetSystemPrompt = `你是任务预算评估器。判断当前任务是否值得追加少量轮数继续执行：
1. 只依据给出的工作状态、最近工具结果和预算事实，不猜测未观察到的文件或外部状态。
2. 如果仍有明确、互不重复的下一步，可建议继续；如果陷入重复、已无必要或风险过高，建议停止。
3. 只输出严格 JSON：{"continue":true|false,"reason":"简短原因","remaining_steps":["下一步"]}`

// budgetEvaluator 用主模型做一次无工具、非流式、512 token、10 秒的预算评估。
func (s *Service) budgetEvaluator(modelName string) agent.BudgetEvaluator {
	return func(ctx context.Context, progress agent.BudgetProgress) (agent.BudgetDecision, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var body strings.Builder
		fmt.Fprintf(&body, "已用轮次 %d/%d（软限 %d），账号用量 %d，运行中进程 %d，已用时 %s。\n",
			progress.RoundsUsed, progress.HardLimit, progress.SoftLimit, progress.AccountedTokens, progress.RunningProcesses, progress.Elapsed.Truncate(time.Second))
		if work, err := json.Marshal(struct {
			Plan       any      `json:"plan"`
			Findings   []string `json:"findings"`
			Artifacts  []string `json:"artifacts"`
			OpenIssues []string `json:"open_issues"`
		}{progress.State.Plan, progress.State.Findings, progress.State.Artifacts, progress.State.OpenIssues}); err == nil {
			body.WriteString("工作状态：")
			body.Write(work)
			body.WriteString("\n")
		}
		body.WriteString("最近工具结果：\n")
		for _, round := range progress.Recent {
			for _, record := range round.Tools {
				content := record.Content
				if len(content) > 1024 {
					content = content[:1024] + "…"
				}
				fmt.Fprintf(&body, "- %s: %s\n", record.Call.Function.Name, content)
			}
		}
		request := agent.Request{
			Model:     modelName,
			Messages:  []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: budgetSystemPrompt}, {Role: openai.ChatMessageRoleUser, Content: body.String()}},
			Stream:    false,
			MaxTokens: 512,
			Purpose:   "evaluation",
		}
		retry := &RetryConfigWrapper{MaxRetries: 0}
		response, err := model.AgentModel(s.getClient, retry)(ctx, request, func(agent.Event) {})
		if err != nil {
			return agent.BudgetDecision{}, err
		}
		return agent.ParseBudgetDecision(response.Content)
	}
}
