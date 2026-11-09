package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/memory"
)

func (s *Service) executeHistoryBrowse(ctx context.Context, args map[string]any) (any, error) {
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return nil, memory.ErrScope
	}
	action, _ := args["action"].(string)
	sessionID, _ := args["session_id"].(string)
	since, _ := args["since"].(string)
	until, _ := args["until"].(string)
	now := time.Now()
	after, err := memory.ParseHistoryTime(since, now)
	if err != nil {
		return nil, err
	}
	before, err := memory.ParseHistoryTime(until, now)
	if err != nil {
		return nil, err
	}
	limit, err := memoryIntArg(args, "limit", false)
	if err != nil {
		return nil, err
	}
	cursor, err := memoryIntArg(args, "cursor", false)
	if err != nil {
		return nil, err
	}
	query, _ := args["query"].(string)
	opts := memory.HistoryBrowseOptions{SessionID: sessionID, Since: after, Until: before, Limit: int(limit), Cursor: cursor, Query: query}
	switch action {
	case "sessions":
		sessions, next, err := s.memory.BrowseHistorySessions(ctx, identity, opts)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessions": sessions, "next_cursor": next}, nil
	case "browse", "session":
		if action == "session" && sessionID == "" {
			return nil, errors.New("session 需要 session_id")
		}
		res, err := s.memory.BrowseHistory(ctx, identity, opts)
		if err != nil {
			return nil, err
		}
		return formatHistorySearchResult(res), nil
	case "raw":
		id, err := memoryIntArg(args, "id", true)
		if err != nil {
			return nil, err
		}
		offset, err := memoryIntArg(args, "offset", false)
		if err != nil {
			return nil, err
		}
		role, _ := args["role"].(string)
		return s.memory.ReadHistoryOriginal(ctx, identity, id, role, int(offset), int(limit))
	case "restore":
		req, err := s.RestoreHistoryRequest(ctx, sessionID, agent.Request{})
		if err != nil {
			return nil, err
		}
		return map[string]any{"session_id": sessionID, "messages": req.Messages, "restored": true, "replayed_tools": false}, nil
	default:
		return nil, fmt.Errorf("未知历史浏览操作 %q", action)
	}
}

// RestoreHistoryRequest 将原始历史放回新请求，不恢复旧身份、工具权限或重新执行旧副作用。
func (s *Service) RestoreHistoryRequest(ctx context.Context, sessionID string, req agent.Request) (agent.Request, error) {
	if s.memory == nil {
		return req, errors.New("历史服务未启用")
	}
	if isPublicThread(ctx) {
		return req, errors.New("公开互动回合不得恢复私有历史")
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return req, memory.ErrScope
	}
	records, err := s.memory.RestoreHistoryMessages(ctx, identity, sessionID)
	if err != nil {
		return req, err
	}
	messages := make([]openai.ChatCompletionMessage, 0, len(records)*2+len(req.Messages))
	for _, record := range records {
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: record.UserText})
		if record.AssistantText != "" {
			messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: record.AssistantText})
		}
	}
	messages = append(messages, req.Messages...)
	req.Messages = messages
	return req, nil
}

// ExecuteHistoryCommand 提供 CLI、桌面和平台统一的授权历史 browse/session/raw/restore 入口。
func (s *Service) ExecuteHistoryCommand(ctx context.Context, args []string) (string, error) {
	if len(args) == 0 {
		args = []string{"sessions"}
	}
	params := map[string]any{"action": args[0]}
	switch args[0] {
	case "search":
		params["query"] = strings.Join(args[1:], " ")
	case "session", "restore":
		if len(args) < 2 {
			return "", errors.New("需要 sessions 返回的 session_id JSON")
		}
		params["session_id"] = args[1]
	case "raw":
		if len(args) < 3 {
			return "", errors.New("raw 需要记录 ID 与 user/assistant role")
		}
		params["id"] = args[1]
		params["role"] = args[2]
	}
	value, err := s.executeHistoryTool(ctx, params)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(value), nil
}
