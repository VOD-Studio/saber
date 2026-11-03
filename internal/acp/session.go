package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// promptParams 是 session/prompt 的参数。
type promptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

// newSessionParams 是 session/new 的参数；cwd 与 mcpServers 只记录，不参与运行。
type newSessionParams struct {
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
	Model      string            `json:"model"`
}

// session 是一次编辑器会话的内存状态，进程退出即丢失。
type session struct {
	id    string
	model string

	// promptMu 串行化同一会话的提示，避免历史交错。
	promptMu sync.Mutex
	// historyMu 保护 history，允许取消通知与提示并发读取。
	historyMu sync.Mutex
	history   []chatMessage
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

// historySnapshot 复制当前历史，避免上游请求期间被改写。
func (s *session) historySnapshot() []chatMessage {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	return append([]chatMessage(nil), s.history...)
}

// commit 在请求成功后一次性写入本轮用户与助手消息。
func (s *session) commit(user, assistant string) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	s.history = append(s.history,
		chatMessage{Role: "user", Content: user},
		chatMessage{Role: "assistant", Content: assistant},
	)
}

// newSession 创建会话；cwd 与 mcpServers 本版本不支持，但会明确记录。
func (c *conn) newSession(raw json.RawMessage) (any, *rpcError) {
	var params newSessionParams
	if rerr := decodeParams(raw, &params); rerr != nil {
		return nil, rerr
	}
	s, err := c.sessions.create(params.Model)
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: "无法创建会话"}
	}
	c.log.Info("ACP 创建会话",
		"session", s.id,
		"cwd", params.CWD,
		"mcp_servers", len(params.MCPServers),
		"ignored_cwd", params.CWD != "",
		"ignored_mcp_servers", len(params.MCPServers) > 0,
	)
	return newSessionResult{SessionID: s.id}, nil
}

// prompt 把会话历史与本轮文本转成 OpenAI 消息，流式转发文本增量。
func (c *conn) prompt(ctx context.Context, params promptParams) (any, *rpcError) {
	s, ok := c.sessions.get(params.SessionID)
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: "会话不存在: " + params.SessionID}
	}
	userText, rerr := promptText(params.Prompt)
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

	history := s.historySnapshot()
	messages := make([]chatMessage, 0, len(history)+1)
	messages = append(messages, history...)
	messages = append(messages, chatMessage{Role: "user", Content: userText})

	var assistant strings.Builder
	err := c.client.stream(pctx, s.model, messages, func(delta string) error {
		assistant.WriteString(delta)
		return c.notifyChunk(s.id, delta)
	})
	if err != nil {
		if pctx.Err() != nil {
			return promptResult{StopReason: "cancelled"}, nil
		}
		return nil, rpcErrorFromUpstream(err)
	}
	s.commit(userText, assistant.String())
	return promptResult{StopReason: "end_turn"}, nil
}

// promptText 拼接文本内容块；非文本类型明确拒绝而不是静默丢弃。
func promptText(blocks []contentBlock) (string, *rpcError) {
	if len(blocks) == 0 {
		return "", &rpcError{Code: codeInvalidParams, Message: "prompt 不能为空"}
	}
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != "text" {
			return "", &rpcError{Code: codeInvalidParams, Message: "仅支持 type=text 的提示内容"}
		}
		texts = append(texts, block.Text)
	}
	text := strings.Join(texts, "\n")
	if strings.TrimSpace(text) == "" {
		return "", &rpcError{Code: codeInvalidParams, Message: "提示内容为空"}
	}
	return text, nil
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
