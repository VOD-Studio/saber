package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/memory"
	"rua.plus/saber/internal/task"
)

// memoryTool 让模型按当前可信作用域维护长期记忆，不接受作用域参数。
func memoryTool() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: "saber_memory",
		Description: "维护当前会话所属空间的长期记忆：私聊保存本人资料与偏好，群聊保存本群公共约定。" +
			"身份与作用域由系统提供，不得代其他用户或群操作。只有工具返回成功才是已保存；返回待确认建议表示尚未生效。" +
			"用户明确要求记住、纠正或忘记时使用；普通闲聊、猜测与临时任务状态不要保存。",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"action":           map[string]any{"type": "string", "enum": []string{"list", "add", "replace", "remove"}},
			"content":          map[string]any{"type": "string", "description": "add 与 replace 的条目正文"},
			"id":               map[string]any{"type": "integer", "minimum": 1, "description": "replace 与 remove 的目标条目编号"},
			"expected_version": map[string]any{"type": "integer", "minimum": 1, "description": "replace 与 remove 必须提供列表看到的当前版本，避免覆盖他人修改"},
		}, "required": []string{"action"}, "additionalProperties": false},
	}}
}

type reviewContextKey struct{}

func withReviewContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, reviewContextKey{}, true)
}

func isReviewContext(ctx context.Context) bool {
	val, _ := ctx.Value(reviewContextKey{}).(bool)
	return val
}

// executeMemoryTool 执行模型发起的记忆操作，授权只取运行时可信身份。
func (s *Service) executeMemoryTool(ctx context.Context, args map[string]any) (any, error) {
	if s.memory == nil {
		return nil, errors.New("记忆服务未启用")
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return nil, errors.New("记忆工具需要可信身份")
	}
	action, _ := args["action"].(string)
	content, _ := args["content"].(string)
	source := memory.Source{Task: task.ID(ctx)}
	isReview := isReviewContext(ctx)
	switch action {
	case "list":
		return s.memoryListText(ctx, identity)
	case "add":
		var mutation memory.Mutation
		var err error
		if isReview {
			mutation, err = s.memory.AutoAdd(ctx, identity, content, source)
		} else {
			mutation, err = s.memory.Add(ctx, identity, content, source, false)
		}
		if err != nil {
			return nil, err
		}
		return memoryMutationText(mutation), nil
	case "replace", "remove":
		id, err := memoryIntArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		version, err := memoryIntArg(args, "expected_version", !isReview)
		if err != nil {
			return nil, err
		}
		var mutation memory.Mutation
		if isReview {
			if action == "replace" {
				mutation, err = s.memory.AutoReplace(ctx, identity, id, version, content, source)
			} else {
				mutation, err = s.memory.AutoRemove(ctx, identity, id, version, source)
			}
		} else {
			if action == "replace" {
				mutation, err = s.memory.Replace(ctx, identity, id, version, content, source)
			} else {
				mutation, err = s.memory.Remove(ctx, identity, id, version, source)
			}
		}
		if err != nil {
			return nil, err
		}
		return memoryMutationText(mutation), nil
	default:
		return nil, fmt.Errorf("未知记忆操作 %q", action)
	}
}

func (s *Service) memoryListText(ctx context.Context, identity chat.Identity) (string, error) {
	entries, err := s.memory.List(ctx, identity)
	if err != nil {
		return "", err
	}
	usage, err := s.memory.Usage(ctx, identity)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return fmt.Sprintf("%s暂无记忆条目（容量 %d 字符）", usage.Scope, usage.MaxChars), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s（%d/%d 字符，%d 条）：", usage.Scope, usage.Chars, usage.MaxChars, usage.Count)
	for _, entry := range entries {
		marker := ""
		if entry.Explicit {
			marker = "，用户明确保存"
		}
		fmt.Fprintf(&b, "\n- #%d v%d%s：%s", entry.ID, entry.Version, marker, entry.Content)
	}
	return b.String(), nil
}

// memoryMutationText 只描述真实结果：直接生效为已保存，否则说明仍是待确认建议。
func memoryMutationText(mutation memory.Mutation) string {
	if mutation.Suggested {
		return fmt.Sprintf("已提交待确认建议 #%d；需本群记忆管理员确认后才会生效，当前尚未写入。", mutation.Change.ID)
	}
	if mutation.Duplicate {
		return fmt.Sprintf("该内容已存在（#%d），未重复保存。", mutation.Entry.ID)
	}
	return fmt.Sprintf("已保存为记忆 #%d（v%d）。", mutation.Entry.ID, mutation.Entry.Version)
}

// memoryIntArg 解析工具参数中的正整数；JSON 数字到达时是 float64。
func memoryIntArg(args map[string]any, name string, required bool) (int64, error) {
	raw, ok := args[name]
	if !ok || raw == nil {
		if required {
			return 0, fmt.Errorf("缺少参数 %s", name)
		}
		return 0, nil
	}
	switch value := raw.(type) {
	case float64:
		if value != float64(int64(value)) {
			return 0, fmt.Errorf("参数 %s 必须是整数", name)
		}
		return int64(value), nil
	case int64:
		return value, nil
	case string:
		return strconv.ParseInt(value, 10, 64)
	default:
		return 0, fmt.Errorf("参数 %s 必须是整数", name)
	}
}

// memoryBudget 计算单次记忆注入的字节上限：不超过配置上限，也不超过总输入预算的四分之一。
func (s *Service) memoryBudget() int {
	if s.memory == nil {
		return 0
	}
	budget := s.memory.InjectBytes()
	if limit := s.config.Agent.Context.MaxTokens; limit > 0 && limit/4 < budget {
		budget = limit / 4
	}
	return budget
}

// skillBudget 计算单次技能目录注入的字节上限：不超过 4096 字节，且不超过总输入预算的八分之一。
func (s *Service) skillBudget() int {
	if s.memory == nil {
		return 0
	}
	budget := 4096
	if limit := s.config.Agent.Context.MaxTokens; limit > 0 && limit/8 < budget {
		budget = limit / 8
	}
	return budget
}

// augmentMemory 在任务续接恢复之后、Agent 运行之前，把当前作用域的快照追加为运行时上下文。
//
// 快照只附加在本次运行的请求上，不写回持久化请求，因此下一次顶层任务会重新读取新版本。
// 无法证明可信作用域或没有可注入条目时保持原请求不变。
func (s *Service) augmentMemory(ctx context.Context, req agent.Request) agent.Request {
	if s.memory == nil {
		return req
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return req
	}
	snapshot, err := s.memory.Snapshot(ctx, identity, s.memoryBudget())
	if err != nil {
		if !errors.Is(err, memory.ErrScope) {
			slog.Warn("读取记忆快照失败", "error", err)
		}
	} else if snapshot.Text != "" {
		req = appendRuntimeContext(req, snapshot.Text)
	}

	catalogSnap, err := s.memory.SkillCatalogSnapshot(ctx, identity, s.skillBudget())
	if err != nil {
		if !errors.Is(err, memory.ErrScope) {
			slog.Warn("读取技能目录快照失败", "error", err)
		}
	} else if catalogSnap != "" {
		req = appendRuntimeContext(req, catalogSnap)
	}

	return req
}

// appendRuntimeContext 在系统提示之后追加一条带来源的资料消息；TrimContext 会保留它。
func appendRuntimeContext(req agent.Request, text string) agent.Request {
	req.Messages = append(req.Messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: text})
	return req
}
