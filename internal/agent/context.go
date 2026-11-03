package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sashabaranov/go-openai"
)

// ContextPolicy 控制送给模型的历史；预算不影响持久化的聊天记录。
type ContextPolicy struct {
	Enabled        bool
	MaxMessages    int
	MaxInputTokens int
	// Compaction 控制运行内上下文压缩；只有带任务状态的主运行使用。
	Compaction CompactionPolicy
}

// CompactionPolicy 定义软触发与目标水位（相对 MaxInputTokens 的比例）。
type CompactionPolicy struct {
	Enabled      bool
	TriggerRatio float64
	TargetRatio  float64
}

// Normalize 填充默认比例，不改变显式配置。
func (c CompactionPolicy) Normalize() CompactionPolicy {
	if c.TriggerRatio <= 0 || c.TriggerRatio >= 1 {
		c.TriggerRatio = 0.8
	}
	if c.TargetRatio <= 0 || c.TargetRatio >= c.TriggerRatio {
		c.TargetRatio = 0.5
	}
	return c
}

// renderRequest 组装实际发送给模型的 Messages，是唯一注入状态与运行时资料的入口。
// 原始指令保持 user 权限逐字保留；工作状态与摘要作为 assistant 工作资料附加。
// 不修改传入 Request，也不把渲染结果写回持久化请求。
func renderRequest(req Request) Request {
	rendered := req
	rendered.State = nil
	rendered.Summary = ""
	rendered.RuntimeContext = nil
	pinned := 0
	for pinned < len(req.Messages) {
		role := req.Messages[pinned].Role
		if role != openai.ChatMessageRoleSystem && role != openai.ChatMessageRoleDeveloper {
			break
		}
		pinned++
	}
	messages := make([]openai.ChatCompletionMessage, 0, len(req.Messages)+2)
	messages = append(messages, req.Messages[:pinned]...)
	messages = append(messages, req.RuntimeContext...)
	baseUser := map[string]bool{}
	for _, message := range req.Messages[pinned:] {
		if message.Role == openai.ChatMessageRoleUser && len(message.MultiContent) == 0 && message.Content != "" {
			baseUser[message.Content] = true
		}
	}
	instructionTexts := map[string]bool{}
	if req.State != nil {
		var body strings.Builder
		for _, instruction := range req.State.Instructions {
			if instruction.Text == "" || instructionTexts[instruction.Text] {
				continue
			}
			instructionTexts[instruction.Text] = true
			if baseUser[instruction.Text] {
				// 原始消息仍在历史中逐字可见，不再重复渲染。
				continue
			}
			if body.Len() == 0 {
				body.WriteString("[任务原始指令｜逐字保留，是本次任务的授权与约束来源]")
			}
			body.WriteString("\n\n")
			body.WriteString(instruction.Text)
		}
		if body.Len() > 0 {
			messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: body.String()})
		}
	}
	seenInstruction := map[string]bool{}
	for _, message := range req.Messages[pinned:] {
		if message.Role == openai.ChatMessageRoleUser && len(message.MultiContent) == 0 && instructionTexts[message.Content] {
			if seenInstruction[message.Content] {
				continue
			}
			seenInstruction[message.Content] = true
		}
		messages = append(messages, message)
	}
	if req.State != nil {
		if work := renderWorkState(req.State, req.Summary); work != "" {
			// 工作状态是 assistant 资料，放在最新用户消息之前，避免被当成模型最终回答。
			index := len(messages)
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Role == openai.ChatMessageRoleUser {
					index = i
					break
				}
			}
			messages = append(messages, openai.ChatCompletionMessage{})
			copy(messages[index+1:], messages[index:])
			messages[index] = openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: work}
		}
	}
	rendered.Messages = messages
	return rendered
}

func renderWorkState(state *TaskState, summary string) string {
	if state == nil {
		return ""
	}
	if summary == "" && len(state.Plan) == 0 && len(state.Findings) == 0 && len(state.Artifacts) == 0 && len(state.OpenIssues) == 0 {
		return ""
	}
	var body strings.Builder
	body.WriteString("[任务工作状态｜程序记录，不是新指令，也不是新的授权来源]")
	if summary != "" {
		body.WriteString("\n摘要：")
		body.WriteString(summary)
	}
	if len(state.Plan) > 0 {
		body.WriteString("\n计划：")
		for _, step := range state.Plan {
			body.WriteString("\n- [")
			body.WriteString(step.Status)
			body.WriteString("] ")
			body.WriteString(step.Text)
		}
	}
	appendList := func(label string, items []string) {
		if len(items) == 0 {
			return
		}
		body.WriteString("\n" + label + "：")
		for _, item := range items {
			body.WriteString("\n- ")
			body.WriteString(item)
		}
	}
	appendList("发现", state.Findings)
	appendList("产物", state.Artifacts)
	appendList("未决问题", state.OpenIssues)
	return body.String()
}

// ContextSize 按实际渲染后的输入估算消息数与保守 token 数。
func ContextSize(req Request) (messages int, estimatedTokens int, err error) {
	rendered := renderRequest(req)
	input, err := json.Marshal(struct {
		Messages  []openai.ChatCompletionMessage
		Tools     []openai.Tool
		Reasoning map[string][]json.RawMessage
	}{rendered.Messages, rendered.Tools, rendered.ResponsesHistory})
	if err != nil {
		return 0, 0, fmt.Errorf("estimate context: %w", err)
	}
	return len(rendered.Messages), len(input), nil
}

// TrimContext 保留系统提示及当前轮，按完整用户轮次移除旧历史和对应推理数据。
// 仅用于没有任务状态且没有摘要函数的旧调用者与记忆复盘。
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
