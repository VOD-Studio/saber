package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/sashabaranov/go-openai"
)

// SummaryInput 是压缩一个完整批次所需的输入；Previous 为滚动摘要。
type SummaryInput struct {
	Previous string
	Messages []openai.ChatCompletionMessage
}

// SummaryOutput 是一次摘要请求的结果与用量。
type SummaryOutput struct {
	Text  string
	Usage openai.Usage
	Model string
}

// SummaryFunc 用主模型生成压缩摘要；实现必须遵守 ctx 并限制输出长度。
type SummaryFunc func(context.Context, SummaryInput) (SummaryOutput, error)

// contextBatch 是不可拆分的消息区间；带工具调用的 assistant 与全部结果同批。
type contextBatch struct {
	start        int
	end          int
	assistant    bool
	user         bool
	compressible bool
}

func splitBatches(messages []openai.ChatCompletionMessage) []contextBatch {
	var batches []contextBatch
	for i := 0; i < len(messages); {
		message := messages[i]
		batch := contextBatch{start: i, end: i + 1, assistant: message.Role == openai.ChatMessageRoleAssistant, user: message.Role == openai.ChatMessageRoleUser, compressible: true}
		if message.Role == openai.ChatMessageRoleAssistant && len(message.ToolCalls) > 0 {
			pending := make(map[string]bool, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				pending[call.ID] = true
			}
			j := i + 1
			for j < len(messages) && messages[j].Role == openai.ChatMessageRoleTool {
				if !pending[messages[j].ToolCallID] {
					batch.compressible = false
				}
				delete(pending, messages[j].ToolCallID)
				j++
			}
			if len(pending) > 0 {
				batch.compressible = false
			}
			batch.end = j
		} else if message.Role == openai.ChatMessageRoleTool {
			// 孤立工具结果无法安全摘要。
			batch.compressible = false
		}
		batches = append(batches, batch)
		i = batch.end
	}
	return batches
}

// prepareContext 在每轮模型请求前按预算决定是否压缩；仅无状态旧调用者走 TrimContext。
func (r Runtime) prepareContext(ctx context.Context, req Request, round, maxRounds int, emit func(Event), compacted int) (Request, int, *Checkpoint, error) {
	policy := r.Context
	compaction := policy.Compaction.Normalize()
	if req.State == nil {
		updated, err := TrimContext(req, policy)
		return updated, compacted, nil, err
	}
	messages, tokens := ContextSize(req)
	maxTokens := policy.MaxInputTokens
	triggerTokens := 0
	if maxTokens > 0 {
		triggerTokens = int(float64(maxTokens) * compaction.TriggerRatio)
	}
	hard := maxTokens > 0 && tokens > maxTokens
	soft := (triggerTokens > 0 && tokens > triggerTokens) || (policy.MaxMessages > 0 && messages > policy.MaxMessages)
	if !soft && !hard {
		return req, compacted, nil, nil
	}
	if !compaction.Enabled || r.Summarize == nil {
		if hard {
			return req, compacted, nil, fmt.Errorf("%w: 渲染输入超过预算（%d tokens）且上下文压缩未启用", ErrBudgetExhausted, tokens)
		}
		return req, compacted, nil, nil
	}
	if compacted > maxRounds {
		if hard {
			return req, compacted, nil, fmt.Errorf("%w: 上下文压缩次数已用尽，渲染输入仍超过预算", ErrBudgetExhausted)
		}
		return req, compacted, nil, nil
	}
	updated, checkpoint, err := r.compact(ctx, req, round, emit, compaction)
	if err != nil {
		return req, compacted, nil, err
	}
	if checkpoint == nil {
		// 没有可压缩前缀：硬限内继续，超过硬限必须明确失败。
		if hard {
			return req, compacted, nil, fmt.Errorf("%w: 没有可压缩的已完成批次，渲染输入仍超过预算", ErrBudgetExhausted)
		}
		return req, compacted, nil, nil
	}
	if hard {
		if _, tokens := ContextSize(updated); tokens > maxTokens {
			return req, compacted, nil, fmt.Errorf("%w: 压缩后渲染输入仍超过预算（%d tokens）", ErrBudgetExhausted, tokens)
		}
	}
	return updated, compacted + 1, checkpoint, nil
}

// compact 摘要最早的可压缩批次，保留最近两个完整批次与最新用户消息。
func (r Runtime) compact(ctx context.Context, req Request, round int, emit func(Event), compaction CompactionPolicy) (Request, *Checkpoint, error) {
	_, oldTokens := ContextSize(req)
	pinned := 0
	for pinned < len(req.Messages) {
		role := req.Messages[pinned].Role
		if role != openai.ChatMessageRoleSystem && role != openai.ChatMessageRoleDeveloper {
			break
		}
		pinned++
	}
	batches := splitBatches(req.Messages[pinned:])
	preserveFrom := len(batches)
	assistantKept := 0
	for i := len(batches) - 1; i >= 0; i-- {
		if batches[i].assistant {
			assistantKept++
			if assistantKept == 2 {
				preserveFrom = i
				break
			}
		}
	}
	lastUser := -1
	for i, batch := range batches {
		if batch.user {
			lastUser = i
		}
	}
	if lastUser >= 0 && lastUser < preserveFrom {
		preserveFrom = lastUser
	}
	// 保留区自身已超过硬限时只保留最近一个完整批次。
	if r.Context.MaxInputTokens > 0 && preserveFrom < len(batches) {
		probe := req
		probe.Messages = append([]openai.ChatCompletionMessage(nil), req.Messages[:pinned]...)
		probe.Messages = append(probe.Messages, req.Messages[pinned+batches[preserveFrom].start:]...)
		probe.Summary = ""
		if _, tokens := ContextSize(probe); tokens > r.Context.MaxInputTokens {
			for i := len(batches) - 1; i >= 0; i-- {
				if batches[i].assistant {
					preserveFrom = i
					break
				}
			}
		}
	}
	candidates := batches[:preserveFrom]
	cutoff := 0
	for cutoff < len(candidates) && candidates[cutoff].compressible {
		cutoff++
	}
	candidates = candidates[:cutoff]
	if len(candidates) == 0 {
		return req, nil, nil
	}
	// 摘要分批同样按 token 估算，与 MaxInputTokens 保持同一单位。
	chunkBudget := 0
	if r.Context.MaxInputTokens > 0 {
		chunkBudget = int(float64(r.Context.MaxInputTokens) * 0.7)
	}
	type chunk struct {
		messages []openai.ChatCompletionMessage
		end      int
	}
	var chunks []chunk
	current := chunk{}
	size := 0
	for _, batch := range candidates {
		part := req.Messages[pinned+batch.start : pinned+batch.end]
		partTokens := estimateMessagesTokens(part)
		if chunkBudget > 0 && size > 0 && size+partTokens > chunkBudget {
			chunks = append(chunks, current)
			current, size = chunk{}, 0
		}
		if chunkBudget > 0 && size == 0 && partTokens > chunkBudget {
			return req, nil, errors.New("context_compaction_failed: irreplaceable batch exceeds summary budget")
		}
		current.messages = append(current.messages, part...)
		current.end = batch.end
		size += partTokens
	}
	if len(current.messages) > 0 {
		chunks = append(chunks, current)
	}
	if len(chunks) > 8 {
		chunks = chunks[:8]
	}
	summary := req.Summary
	for _, item := range chunks {
		if err := ctx.Err(); err != nil {
			return req, nil, err
		}
		output, summaryErr := r.Summarize(ctx, SummaryInput{Previous: summary, Messages: item.messages})
		if summaryErr != nil {
			return req, nil, fmt.Errorf("context_compaction_failed: %w", summaryErr)
		}
		summary = strings.TrimSpace(output.Text)
		if summary == "" {
			return req, nil, errors.New("context_compaction_failed: summary is empty")
		}
	}
	compressedEnd := chunks[len(chunks)-1].end
	newReq := req
	newReq.Messages = append([]openai.ChatCompletionMessage(nil), req.Messages[:pinned]...)
	newReq.Messages = append(newReq.Messages, req.Messages[pinned+compressedEnd:]...)
	newReq.Summary = summary
	newReq.ResponsesHistory = pruneResponses(req.ResponsesHistory, newReq.Messages)
	_, newTokens := ContextSize(newReq)
	if newTokens >= oldTokens {
		return req, nil, errors.New("context_compaction_failed: context did not shrink")
	}
	if r.Context.MaxInputTokens > 0 {
		target := int(float64(r.Context.MaxInputTokens) * compaction.TargetRatio)
		if newTokens > target {
			withoutSummary := newReq
			withoutSummary.Summary = ""
			_, preservedTokens := ContextSize(withoutSummary)
			if preservedTokens <= target {
				return req, nil, errors.New("context_compaction_failed: context did not fall to target")
			}
		}
	}
	if r.OnCompaction != nil {
		if err := r.OnCompaction(ctx); err != nil {
			return req, nil, fmt.Errorf("context_compaction_failed: %w", err)
		}
	}
	checkpoint := &Checkpoint{Request: cloneRequest(newReq), ThroughRound: round - 1}
	emit(Event{Kind: ContextCompacted, Round: round, State: CloneTaskState(req.State), Checkpoint: checkpoint})
	return newReq, checkpoint, nil
}

func pruneResponses(history map[string][]json.RawMessage, messages []openai.ChatCompletionMessage) map[string][]json.RawMessage {
	if len(history) == 0 {
		return history
	}
	kept := make(map[string][]json.RawMessage)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if raw, ok := history[call.ID]; ok {
				kept[call.ID] = raw
			}
		}
	}
	return kept
}

func cloneRequest(req Request) Request {
	cloned := req
	cloned.Messages = append([]openai.ChatCompletionMessage(nil), req.Messages...)
	cloned.State = CloneTaskState(req.State)
	cloned.ResponsesHistory = maps.Clone(req.ResponsesHistory)
	cloned.RuntimeContext = nil
	return cloned
}
