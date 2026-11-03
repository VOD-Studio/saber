package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/model"
)

const summarySystemPrompt = `你是任务上下文压缩器。把给定的历史片段压缩成一份滚动摘要，供同一任务继续执行：
1. 只保留已观察事实、已完成的命令与修改、失败及原因、剩余工作、产物路径与输出句柄。
2. 保留原始目标与约束的要点，但不新增任务、不改变授权范围、不编造未发生的操作。
3. 不输出逐步推理、工具协议或加密推理内容。
4. 使用简洁的中文或原文语言，最多 400 字。`

// summarizer 用当前主模型执行一次无工具、非流式、固定短超时的摘要请求。
func (s *Service) summarizer(modelName string) agent.SummaryFunc {
	return func(ctx context.Context, input agent.SummaryInput) (agent.SummaryOutput, error) {
		ctx, cancel := context.WithTimeout(ctx, auxiliaryRequestTimeout)
		defer cancel()
		var body strings.Builder
		if input.Previous != "" {
			body.WriteString("已有滚动摘要：\n")
			body.WriteString(input.Previous)
			body.WriteString("\n\n")
		}
		body.WriteString("待压缩的历史片段：\n")
		for _, message := range input.Messages {
			encoded, err := json.Marshal(message)
			if err != nil {
				return agent.SummaryOutput{}, err
			}
			if len(encoded) > 16384 {
				encoded = encoded[:16384]
			}
			body.WriteString(string(encoded))
			body.WriteString("\n")
			if ctx.Err() != nil {
				return agent.SummaryOutput{}, ctx.Err()
			}
		}
		request := agent.Request{
			Model:     modelName,
			Messages:  []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: summarySystemPrompt}, {Role: openai.ChatMessageRoleUser, Content: body.String()}},
			Stream:    false,
			MaxTokens: s.auxiliaryMaxTokens(modelName),
			Purpose:   "summary",
		}
		retry := &RetryConfigWrapper{MaxRetries: 0}
		response, err := model.AgentModel(s.getClient, retry)(ctx, request, func(agent.Event) {})
		if err != nil {
			return agent.SummaryOutput{}, err
		}
		text := strings.TrimSpace(response.Content)
		if text == "" {
			return agent.SummaryOutput{}, errors.New("summary model returned empty text")
		}
		return agent.SummaryOutput{Text: text, Usage: response.Usage, Model: response.Model}, nil
	}
}
