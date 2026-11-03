package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/model"
)

const budgetSystemPrompt = `你是任务预算评估器。判断当前任务是否值得追加少量轮数继续执行：
1. 只依据给出的用户指令、工作状态、最近轮次和预算事实，不猜测未观察到的文件或外部状态。
2. 工作状态为空只表示模型还没记录计划、发现或产物，不代表没有进展；以用户指令、最近工具结果和已请求的下一步判断。
3. 如果仍有明确、互不重复的下一步，可建议继续；如果陷入重复、与用户指令无关、已无必要或风险过高，建议停止。
4. 只输出严格 JSON：{"continue":true|false,"reason":"简短原因","remaining_steps":["下一步"]}`

// 评估输入的长度上限：评估请求与任务共享同一账本，证据不能挤占主请求预算。
const (
	budgetInstructionBytes = 2048
	budgetAssistantBytes   = 512
	budgetToolResultBytes  = 1024
	budgetToolArgsBytes    = 256
)

// budgetEvaluator 用主模型做一次无工具、非流式的预算评估。
// 输出上限沿用模型生成预算、时限与摘要一致：推理模型的思考 token 同样计入
// 输出上限，固定小额度会在 JSON 产出前被截断，评估失败即终止任务。
func (s *Service) budgetEvaluator(modelName string) agent.BudgetEvaluator {
	return func(ctx context.Context, progress agent.BudgetProgress) (agent.BudgetDecision, error) {
		ctx, cancel := context.WithTimeout(ctx, s.auxiliaryTimeout(modelName))
		defer cancel()
		request := agent.Request{
			Model: modelName,
			Messages: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleSystem, Content: budgetSystemPrompt},
				{Role: openai.ChatMessageRoleUser, Content: budgetEvaluationBody(progress)},
			},
			Stream:    false,
			MaxTokens: s.auxiliaryMaxTokens(modelName),
			Purpose:   "evaluation",
		}
		response, err := model.AgentModel(s.getClient, s.agentRetry())(ctx, request, func(agent.Event) {})
		if err != nil {
			return agent.BudgetDecision{}, err
		}
		return agent.ParseBudgetDecision(response.Content)
	}
}

// budgetEvaluationBody 汇总评估器可见的事实：预算、原始指令、工作状态与最近轮次。
// 原始指令与模型刚请求的工具调用都必须在场：评估器此前只能看到工作状态和已完成
// 轮次的工具结果，未记录计划的任务会因此被误判为「无进展、无下一步」而终止。
func budgetEvaluationBody(progress agent.BudgetProgress) string {
	var body strings.Builder
	fmt.Fprintf(&body, "已用轮次 %d/%d（软限 %d），账号用量 %d，运行中进程 %d，已用时 %s。\n",
		progress.RoundsUsed, progress.HardLimit, progress.SoftLimit, progress.AccountedTokens, progress.RunningProcesses, progress.Elapsed.Truncate(time.Second))
	if instructions := budgetInstructions(progress.State.Instructions); instructions != "" {
		body.WriteString("用户原始指令（逐字，从早到晚）：\n")
		body.WriteString(instructions)
		body.WriteString("\n")
	}
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
	body.WriteString("最近轮次（从早到晚）：\n")
	for _, round := range progress.Recent {
		if text := strings.TrimSpace(budgetTruncate(round.Response.Content, budgetAssistantBytes)); text != "" {
			body.WriteString("- 助手：")
			body.WriteString(text)
			body.WriteString("\n")
		}
		for _, record := range round.Tools {
			fmt.Fprintf(&body, "- 工具 %s：%s\n", record.Call.Function.Name, budgetTruncate(record.Content, budgetToolResultBytes))
		}
		// 当前轮排在最后且没有工具结果：运行先决定是否追加轮数，再派发本轮调用，
		// 因此这些调用代表模型给出的下一步，不能因为没有结果就算作停滞。
		if len(round.Tools) == 0 {
			for _, call := range round.Response.ToolCalls {
				fmt.Fprintf(&body, "- 已请求、尚未执行：%s(%s)\n", call.Function.Name, budgetTruncate(call.Function.Arguments, budgetToolArgsBytes))
			}
		}
	}
	return body.String()
}

// budgetInstructions 保留任务链上最近的原始指令，超出上限时从最早的开始省略。
func budgetInstructions(instructions []agent.Instruction) string {
	texts := make([]string, 0, len(instructions))
	total := 0
	for index := len(instructions) - 1; index >= 0; index-- {
		text := strings.TrimSpace(instructions[index].Text)
		if text == "" {
			continue
		}
		text = budgetTruncate(text, budgetInstructionBytes)
		if total+len(text) > budgetInstructionBytes && len(texts) > 0 {
			texts = append(texts, "…（更早的指令已省略）")
			break
		}
		texts = append(texts, text)
		total += len(text)
	}
	for left, right := 0, len(texts)-1; left < right; left, right = left+1, right-1 {
		texts[left], texts[right] = texts[right], texts[left]
	}
	return strings.Join(texts, "\n\n")
}

// budgetTruncate 按 UTF-8 边界截断，避免评估输入出现半个字符。
func budgetTruncate(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}
