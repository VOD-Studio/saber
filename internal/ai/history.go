package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/memory"
)

// historyTool 让模型按当前可信作用域检索和查阅真实历史对话原文，不接受外部身份参数。
func historyTool() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: "saber_history",
		Description: "检索与查阅当前会话所属空间的真实历史对话原文，回答关于较久以前对话细节、已有约定或结论的问题。" +
			"支持关键词检索（search）、会话与时间浏览（browse/sessions/session）、原文分页（raw）及恢复（restore），历史副作用不会重新执行。" +
			"身份与作用域由系统严格限定，私聊仅能检索本人历史，群聊仅能检索本群历史；不得代其他用户或跨群查询。" +
			"返回内容包含记录 ID、时间、发言人、来源任务与原文片段；若内容过长会截断并提供游标或翻阅提示。",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"search", "context", "browse", "sessions", "session", "raw", "restore"},
				"description": "search 关键词；browse 时间页；sessions 会话目录；session 会话记录；raw 原文分页；restore 原始user/assistant消息；context 前后文",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "search 操作的检索关键词（支持中文、英文及短语）",
			},
			"id": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "context 操作的目标记录编号（由 search 结果获得）",
			},
			"cursor": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "search 操作的分页游标，传入上一页返回的 next_cursor 获取更早的历史",
			},
			"limit": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     8000,
				"description": "browse/search/session 上限20条；raw 按字符分页上限8000字符，默认2000",
			},
			"before": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"maximum":     10,
				"description": "context 操作向前读取的条数（较早的记录），默认 2，最大 10",
			},
			"after": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"maximum":     10,
				"description": "context 操作向后读取的条数（较新的记录），默认 2，最大 10",
			},
			"session_id": map[string]any{"type": "string", "description": "sessions 返回的会话标识，不是身份参数"},
			"since":      map[string]any{"type": "string", "description": "RFC3339、YYYY-MM-DD、7d/24h/2w"},
			"until":      map[string]any{"type": "string", "description": "排除该时间及之后的记录"},
			"role":       map[string]any{"type": "string", "enum": []string{"user", "assistant"}},
			"offset":     map[string]any{"type": "integer", "minimum": 0},
		}, "required": []string{"action"}, "additionalProperties": false},
	}}
}

// executeHistoryTool 执行模型发起的历史回忆与上下文读取，授权只取运行时可信身份。
func (s *Service) executeHistoryTool(ctx context.Context, args map[string]any) (any, error) {
	if isPublicThread(ctx) {
		return nil, errors.New("公开互动回合不得读取历史")
	}
	if s.memory == nil {
		return nil, errors.New("记忆服务未启用")
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return nil, errors.New("历史工具需要可信身份")
	}
	action, _ := args["action"].(string)
	switch action {
	case "browse", "sessions", "session", "raw", "restore":
		return s.executeHistoryBrowse(ctx, args)
	case "search":
		query, _ := args["query"].(string)
		if strings.TrimSpace(query) == "" {
			return nil, errors.New("search 操作需要提供检索关键词 query")
		}
		limit, err := memoryIntArg(args, "limit", false)
		if err != nil {
			return nil, err
		}
		cursor, err := memoryIntArg(args, "cursor", false)
		if err != nil {
			return nil, err
		}
		var res memory.HistorySearchResult
		if args["since"] != nil || args["until"] != nil || args["session_id"] != nil {
			browseArgs := make(map[string]any, len(args))
			for k, v := range args {
				browseArgs[k] = v
			}
			browseArgs["action"] = "browse"
			return s.executeHistoryBrowse(ctx, browseArgs)
		}
		res, err = s.memory.SearchHistory(ctx, identity, memory.HistorySearchOptions{Query: query, Limit: int(limit), Cursor: cursor})
		if err != nil {
			return nil, err
		}
		res.Summary = s.summarizeHistory(ctx, res)
		return formatHistorySearchResult(res), nil

	case "context":
		id, err := memoryIntArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		before, err := memoryIntArg(args, "before", false)
		if err != nil {
			return nil, err
		}
		after, err := memoryIntArg(args, "after", false)
		if err != nil {
			return nil, err
		}
		if before == 0 && args["before"] == nil {
			before = 2
		}
		if after == 0 && args["after"] == nil {
			after = 2
		}
		res, err := s.memory.ReadHistoryContext(ctx, identity, memory.HistoryContextOptions{
			ID:     id,
			Before: int(before),
			After:  int(after),
		})
		if err != nil {
			if errors.Is(err, memory.ErrNotFound) {
				return fmt.Sprintf("未在当前空间找到记录 #%d（可能不存在或属于其他会话）。", id), nil
			}
			return nil, err
		}
		return formatHistoryContextResult(res), nil

	default:
		return nil, fmt.Errorf("未知历史工具操作 %q，必须为 search 或 context", action)
	}
}

func formatHistorySearchResult(res memory.HistorySearchResult) string {
	if len(res.Records) == 0 {
		return fmt.Sprintf("【历史检索｜%s】未找到与 %q 相关的历史记录。", res.Scope, res.Query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "【历史检索｜%s】关键词: %q，找到 %d 条记录：", res.Scope, res.Query, len(res.Records))
	for _, r := range res.Records {
		timeStr := r.CreatedAt.Format("2006-01-02 15:04:05")
		userText := truncateSnippet(r.UserText, 400)
		assistantText := truncateSnippet(r.AssistantText, 400)
		fmt.Fprintf(&b, "\n- 记录 #%d (%s, 发送人: %s, 来源任务: #%d, 状态: %s)：\n  用户: %s\n  Saber: %s",
			r.ID, timeStr, r.SenderID, r.TaskID, r.TaskStatus, userText, assistantText)
	}
	if res.HasMore {
		fmt.Fprintf(&b, "\n[还有更多历史记录，可使用 action=\"search\", cursor=%d 继续翻页；使用 action=\"context\", id=<记录ID> 可查看完整对话原文与前后文]", res.NextCursor)
	} else {
		fmt.Fprintf(&b, "\n[使用 action=\"context\", id=<记录ID> 可查看完整对话原文与前后文]")
	}
	if res.Summary != "" {
		fmt.Fprintf(&b, "\n【摘要】%s", res.Summary)
	}
	return b.String()
}

// historySummaryPrompt 约束摘要只复述本页已授权命中的记录。
const historySummaryPrompt = `你是历史对话摘要助手。
请只根据用户给出的检索命中记录，概括与检索关键词相关的信息：
1. 说明相关事件、发生时间与仍未完成的待办，用中文，不超过 600 字。
2. 只使用给定记录中的事实，不得推测、补充或引用其他来源。
3. 保留关键结论、时间点与待办事项；记录之间没有关联时简短说明即可。
4. 若记录不足以回答，直接说明命中的信息有限。`

// summarizeHistory 为一次已授权检索生成摘要，命中缓存时不重复请求模型。
//
// 输入只包含本次检索到的记录，每条截断且总输入不超过 12 KiB；
// 模型不可用、失败或返回空内容时返回空串，调用方降级为仅返回原文。
func (s *Service) summarizeHistory(ctx context.Context, res memory.HistorySearchResult) string {
	if len(res.Records) == 0 || s.memory == nil || s.config == nil || !s.config.Memory.Enabled || !s.IsEnabled() {
		return ""
	}
	lastRecordID := res.Records[0].ID
	if cached, ok, err := s.memory.CachedHistorySummary(ctx, res.Scope, res.QueryHash, lastRecordID); err != nil {
		slog.Warn("读取历史摘要缓存失败", "error", err)
	} else if ok {
		return cached.Summary
	}
	input := buildHistorySummaryInput(res.Records)
	if input == "" {
		return ""
	}
	modelName := ""
	if s.config != nil {
		modelName = s.config.Memory.ReviewModel
	}
	callCtx, cancel := context.WithTimeout(ctx, profileModelTimeout)
	defer cancel()
	text, err := s.GenerateSimpleResponseWithModel(callCtx, modelName, profileTemperature, historySummaryPrompt, input)
	if err != nil {
		slog.Debug("生成历史摘要失败，降级为仅返回原文", "error", err)
		return ""
	}
	text = truncateProfileRunes(strings.TrimSpace(text), memory.MaxHistorySummaryRunes)
	if text == "" {
		return ""
	}
	if err := s.memory.SaveHistorySummary(ctx, res.Scope, res.QueryHash, lastRecordID, text); err != nil {
		slog.Warn("写入历史摘要缓存失败", "error", err)
	}
	return text
}

// buildHistorySummaryInput 拼接摘要输入，只使用本次命中的记录并限制总字节数。
func buildHistorySummaryInput(records []memory.HistoryRecord) string {
	var b strings.Builder
	b.WriteString("检索命中记录（按时间倒序）：\n")
	for _, r := range records {
		entry := fmt.Sprintf("\n#%d 时间 %s 发送人 %s\n用户: %s\nSaber: %s\n",
			r.ID, r.CreatedAt.Format("2006-01-02 15:04"), r.SenderID,
			truncateSnippet(r.UserText, 450), truncateSnippet(r.AssistantText, 450))
		if b.Len()+len(entry) > memory.HistorySummaryInputBytes {
			break
		}
		b.WriteString(entry)
	}
	return b.String()
}

func formatHistoryContextResult(res memory.HistoryContextResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【历史原文查阅｜%s】目标记录 #%d：", res.Scope, res.Target.ID)
	if len(res.Before) > 0 {
		fmt.Fprintf(&b, "\n--- 前文（%d 条）---", len(res.Before))
		for _, r := range res.Before {
			timeStr := r.CreatedAt.Format("2006-01-02 15:04:05")
			fmt.Fprintf(&b, "\n- 记录 #%d (%s, 发送人: %s, 来源任务: #%d)：\n  用户: %s\n  Saber: %s",
				r.ID, timeStr, r.SenderID, r.TaskID, truncateSnippet(r.UserText, 1000), truncateSnippet(r.AssistantText, 1000))
		}
	}
	targetTime := res.Target.CreatedAt.Format("2006-01-02 15:04:05")
	fmt.Fprintf(&b, "\n--- 目标记录 #%d (%s, 发送人: %s, 来源任务: #%d, 状态: %s) ---\n  用户: %s\n  Saber: %s",
		res.Target.ID, targetTime, res.Target.SenderID, res.Target.TaskID, res.Target.TaskStatus, truncateSnippet(res.Target.UserText, 2000), truncateSnippet(res.Target.AssistantText, 2000))
	if len(res.Target.UserText) > 2000 || len(res.Target.AssistantText) > 2000 {
		b.WriteString("\n[完整原文可用 action=raw, id=<记录>, role=user/assistant, offset=<字符偏移> 分页恢复]")
	}
	if len(res.After) > 0 {
		fmt.Fprintf(&b, "\n--- 后文（%d 条）---", len(res.After))
		for _, r := range res.After {
			timeStr := r.CreatedAt.Format("2006-01-02 15:04:05")
			fmt.Fprintf(&b, "\n- 记录 #%d (%s, 发送人: %s, 来源任务: #%d)：\n  用户: %s\n  Saber: %s",
				r.ID, timeStr, r.SenderID, r.TaskID, truncateSnippet(r.UserText, 1000), truncateSnippet(r.AssistantText, 1000))
		}
	}
	return b.String()
}

func truncateSnippet(text string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = 400
	}
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:maxRunes]) + "...[已截断]"
}

// syncHistoryProjections 将 tasks.db 中的待处理终态任务同步为 memory.db 历史投影。
func (s *Service) syncHistoryProjections(ctx context.Context) error {
	if s.memory == nil || s.tasks == nil {
		return nil
	}
	for {
		pending, err := s.tasks.PendingProjections(ctx, 20)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		for _, t := range pending {
			identity := chat.Identity{
				Session:  t.Message.Session,
				SenderID: t.Message.SenderID,
				Direct:   t.Message.Direct,
			}
			scope, ok := memory.Space(identity)
			if ok {
				_, err = s.memory.RecordProjection(ctx, memory.ProjectionInput{
					TaskID:        t.ID,
					Scope:         scope,
					Conversation:  t.Message.Session.Conversation,
					Thread:        t.Message.Session.Thread,
					SenderID:      t.Message.SenderID,
					UserMessageID: t.Message.ID,
					UserText:      t.Message.Text,
					AssistantText: t.Result.Content,
					TaskStatus:    t.Status,
					CreatedAt:     t.CreatedAt,
				})
				if err != nil {
					slog.Warn("写入历史投影失败", "task", t.ID, "error", err)
					return err
				}
			}
			// 无论是否写入投影，均确认出队，避免无法推导作用域的任务造成死循环
			if err := s.tasks.AcknowledgeProjection(ctx, t.ID); err != nil {
				slog.Warn("确认历史投影待处理记录失败", "task", t.ID, "error", err)
				return err
			}
		}
	}
}

// SyncHistory 主动触发历史对话投影同步。
func (s *Service) SyncHistory(ctx context.Context) error {
	return s.syncHistoryProjections(ctx)
}

// BackfillHistory 将历史任务批量回填至记忆数据库全文索引中。
func (s *Service) BackfillHistory(ctx context.Context, batchSize int) (int, error) {
	if s.memory == nil || s.tasks == nil {
		return 0, nil
	}
	if batchSize <= 0 {
		batchSize = 50
	}
	var afterID int64
	total := 0
	for {
		batch, err := s.tasks.BackfillTasks(ctx, batchSize, afterID)
		if err != nil {
			return total, err
		}
		if len(batch) == 0 {
			break
		}
		for _, t := range batch {
			afterID = t.ID
			identity := chat.Identity{
				Session:  t.Message.Session,
				SenderID: t.Message.SenderID,
				Direct:   t.Message.Direct,
			}
			scope, ok := memory.Space(identity)
			if !ok {
				continue
			}
			_, err = s.memory.RecordProjection(ctx, memory.ProjectionInput{
				TaskID:        t.ID,
				Scope:         scope,
				Conversation:  t.Message.Session.Conversation,
				Thread:        t.Message.Session.Thread,
				SenderID:      t.Message.SenderID,
				UserMessageID: t.Message.ID,
				UserText:      t.Message.Text,
				AssistantText: t.Result.Content,
				TaskStatus:    t.Status,
				CreatedAt:     t.CreatedAt,
			})
			if err != nil {
				return total, err
			}
			total++
		}
	}
	return total, nil
}
