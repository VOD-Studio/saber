package agent

import (
	"encoding/json"
	"fmt"

	"github.com/sashabaranov/go-openai"
)

// ContextPolicy 控制送给模型的历史；预算不影响持久化的聊天记录。
type ContextPolicy struct {
	Enabled        bool
	MaxMessages    int
	MaxInputTokens int
}

// TrimContext 保留系统提示及当前轮，按完整用户轮次移除旧历史和对应推理数据。
// 使用序列化输入字节数保守估算 token，不依赖提供商特定的 tokenizer。
// 当前轮、工具定义及系统提示仍超预算时返回明确错误，不截断用户输入或工具协议。
func TrimContext(req Request, policy ContextPolicy) (Request, error) {
	if policy.MaxMessages == 0 && policy.MaxInputTokens == 0 {
		return req, nil
	}
	var pinned []openai.ChatCompletionMessage
	var turns [][]openai.ChatCompletionMessage
	for _, message := range req.Messages {
		if message.Role == "system" || message.Role == "developer" {
			pinned = append(pinned, message)
			continue
		}
		if message.Role == "user" || len(turns) == 0 {
			turns = append(turns, nil)
		}
		turns[len(turns)-1] = append(turns[len(turns)-1], message)
	}
	for {
		candidate := req
		candidate.Messages = append([]openai.ChatCompletionMessage(nil), pinned...)
		candidate.ResponsesHistory = make(map[string][]json.RawMessage)
		for _, turn := range turns {
			candidate.Messages = append(candidate.Messages, turn...)
			for _, message := range turn {
				for _, call := range message.ToolCalls {
					if raw, ok := req.ResponsesHistory[call.ID]; ok {
						candidate.ResponsesHistory[call.ID] = raw
					}
				}
			}
		}
		input, err := json.Marshal(struct {
			Messages  []openai.ChatCompletionMessage
			Tools     []openai.Tool
			Reasoning map[string][]json.RawMessage
		}{candidate.Messages, candidate.Tools, candidate.ResponsesHistory})
		if err != nil {
			return req, fmt.Errorf("estimate context: %w", err)
		}
		over := policy.MaxMessages > 0 && len(candidate.Messages) > policy.MaxMessages || policy.MaxInputTokens > 0 && len(input) > policy.MaxInputTokens
		if !over && (policy.Enabled || len(turns) <= 1) {
			return candidate, nil
		}
		if len(turns) <= 1 {
			return req, fmt.Errorf("%w: 当前输入、系统提示或工具定义超过 agent.context 预算（%d 条消息，保守估算 %d tokens），请缩短输入或提高限制", ErrBudgetExhausted, len(candidate.Messages), len(input))
		}
		turns = turns[1:]
	}
}
