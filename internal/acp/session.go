package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
)

// promptParams 是 session/prompt 的参数。
type promptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

// newSessionParams 引用服务端授权目录和已配置的 MCP 服务器。
type newSessionParams struct {
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
	Model      string            `json:"model"`
}

// session 仅保存连接状态；历史和自主运行由服务端持久任务保存。
type session struct {
	id            string
	model         string
	cwd           string
	mcp           []string
	pendingAnswer *pendingAnswerError

	// promptMu 串行化同一会话的提示，避免历史交错。
	promptMu sync.Mutex
	// cancelMu 保护 inFlight，使 session/cancel 能打断正在进行的请求。
	cancelMu sync.Mutex
	inFlight context.CancelFunc
}

// sessionStore 是并发安全的内存会话表。
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*session
}

// newSessionStore 创建空会话表。
func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*session)}
}

// create 生成不可猜测的会话标识并登记新会话。
func (s *sessionStore) create(model string) (*session, error) {
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	item := &session{id: id, model: model}
	s.mu.Lock()
	s.sessions[id] = item
	s.mu.Unlock()
	return item, nil
}

// get 按标识查找会话。
func (s *sessionStore) get(id string) (*session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.sessions[id]
	return item, ok
}

// newSessionID 返回 256 位随机标识，避免会话被猜到。
func newSessionID() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("生成会话标识失败: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// beginPrompt 注册可取消的请求上下文；返回的清理函数清空登记。
func (s *session) beginPrompt(ctx context.Context) (context.Context, func()) {
	pctx, cancel := context.WithCancel(ctx)
	s.cancelMu.Lock()
	s.inFlight = cancel
	s.cancelMu.Unlock()
	return pctx, func() {
		cancel()
		s.cancelMu.Lock()
		s.inFlight = nil
		s.cancelMu.Unlock()
	}
}

// cancelPrompt 取消在途请求；没有在途请求时无副作用。
func (s *session) cancelPrompt() {
	s.cancelMu.Lock()
	cancel := s.inFlight
	s.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// newSession 创建自主任务会话，目录和 MCP 引用在服务端授权后才生效。
func (c *conn) newSession(raw json.RawMessage) (any, *rpcError) {
	var params newSessionParams
	if rerr := decodeParams(raw, &params); rerr != nil {
		return nil, rerr
	}
	s, err := c.sessions.create(params.Model)
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: "无法创建会话"}
	}
	s.cwd = params.CWD
	if err := parseMCP(params.MCPServers, s); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	return newSessionResult{SessionID: s.id}, nil
}

// prompt 通过持久任务运行自主编码并转发真实工具和文本事件。
func (c *conn) prompt(ctx context.Context, params promptParams) (any, *rpcError) {
	s, ok := c.sessions.get(params.SessionID)
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: "会话不存在: " + params.SessionID}
	}
	userText, attachments, rerr := promptContent(params.Prompt)
	if rerr != nil {
		return nil, rerr
	}

	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	if ctx.Err() != nil {
		return promptResult{StopReason: "cancelled"}, nil
	}

	pctx, finish := s.beginPrompt(ctx)
	defer finish()

	turn, err := c.promptTask(pctx, s, userText, attachments)
	if err != nil {
		if _, pending := err.(*pendingAnswerError); pending {
			return promptResult{StopReason: "end_turn"}, nil
		}
		if _, failed := err.(*cancelTaskError); failed {
			return nil, rpcErrorFromUpstream(err)
		}
		if pctx.Err() != nil {
			return promptResult{StopReason: "cancelled"}, nil
		}
		return nil, rpcErrorFromUpstream(err)
	}
	if turn.Status == "cancelled" {
		return promptResult{StopReason: "cancelled"}, nil
	}
	if turn.Status == "budget_exhausted" {
		return promptResult{StopReason: "max_turn_requests"}, nil
	}
	if turn.Status == "blocked" || turn.Status == "review" || turn.Status == "waiting_input" {
		return nil, &rpcError{Code: codeUpstreamError, Message: fmt.Sprintf("任务 #%d 正在等待 %s；通过 /v1/control 回答或审批后恢复", turn.ID, turn.Status)}
	}
	return promptResult{StopReason: "end_turn"}, nil
}

// rpcErrorFromUpstream 把 HTTP 错误映射为 JSON-RPC 错误，不回显令牌。
func rpcErrorFromUpstream(err error) *rpcError {
	var upstream *upstreamError
	if errors.As(err, &upstream) {
		if upstream.Status == http.StatusUnauthorized || upstream.Status == http.StatusForbidden {
			return &rpcError{Code: codeUnauthorized, Message: "Saber 服务鉴权失败，请确认 token_file 与服务端一致"}
		}
		return &rpcError{
			Code:    codeUpstreamError,
			Message: fmt.Sprintf("Saber 服务返回 %d: %s", upstream.Status, upstream.Message),
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &rpcError{Code: codeUpstreamError, Message: "请求 Saber 服务超时"}
	}
	return &rpcError{Code: codeUpstreamError, Message: "请求 Saber 服务失败: " + err.Error()}
}
