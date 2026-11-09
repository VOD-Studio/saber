package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/server"
)

func (c *chatClient) request(ctx context.Context, method, path string, input, out any) error {
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readUpstreamError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
func (c *chatClient) history(ctx context.Context, id string) ([]server.Turn, error) {
	var turns []server.Turn
	err := c.request(ctx, "GET", "/v1/sessions/"+url.PathEscape(id)+"/tasks", nil, &turns)
	return turns, err
}
func (c *chatClient) taskStream(ctx context.Context, s *session, text string, attachments []chat.Attachment, emit func(agent.Event) error, watch func(context.Context, int64) error) (server.Turn, error) {
	id, err := newSessionID()
	if err != nil {
		return server.Turn{}, err
	}
	var t server.Turn
	if err = c.request(ctx, "POST", "/v1/runs", map[string]any{"session": s.id, "id": id, "text": text, "model": s.model, "cwd": s.cwd, "mcp_servers": s.mcp, "attachments": attachments}, &t); err != nil {
		return t, err
	}
	return c.observeTask(ctx, s, t, emit, watch)
}

type cancelTaskError struct{ err error }

func (e *cancelTaskError) Error() string { return "服务端任务取消失败: " + e.err.Error() }
func (c *chatClient) stopTask(session string, id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := c.request(ctx, "POST", "/v1/runs/"+strconv.FormatInt(id, 10)+"/cancel?session="+url.QueryEscape(session), nil, nil); err != nil {
		return &cancelTaskError{err}
	}
	return nil
}
func (c *conn) loadSession(raw json.RawMessage) (any, *rpcError) {
	var p struct {
		SessionID  string            `json:"sessionId"`
		CWD        string            `json:"cwd"`
		MCPServers []json.RawMessage `json:"mcpServers"`
	}
	if e := decodeParams(raw, &p); e != nil {
		return nil, e
	}
	turns, err := c.client.history(c.ctx, p.SessionID)
	if err != nil {
		return nil, rpcErrorFromUpstream(err)
	}
	s := &session{id: p.SessionID, cwd: p.CWD}
	if len(turns) > 0 {
		s.model = turns[len(turns)-1].Model
	}
	if err = parseMCP(p.MCPServers, s); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}
	if err = c.restorePendingQuestion(c.ctx, s); err != nil {
		return nil, rpcErrorFromUpstream(err)
	}
	c.sessions.mu.Lock()
	c.sessions.sessions[s.id] = s
	c.sessions.mu.Unlock()
	for _, t := range turns {
		if t.Input != "" {
			c.write(rpcNotification{JSONRPC: jsonrpcVersion, Method: "session/update", Params: map[string]any{"sessionId": s.id, "update": map[string]any{"sessionUpdate": "user_message_chunk", "content": contentBlock{Type: "text", Text: t.Input}}}})
		}
		for _, tool := range t.Tools {
			if err = c.notifyAgentEvent(s.id, agent.Event{Kind: agent.ToolFinished, Tool: tool}); err != nil {
				return nil, rpcErrorFromUpstream(err)
			}
		}
		if t.Content != "" {
			if err = c.notifyChunk(s.id, t.Content); err != nil {
				return nil, rpcErrorFromUpstream(err)
			}
		}
	}
	if s.pendingAnswer != nil {
		if err = c.notifyChunk(s.id, s.pendingAnswer.Question.Question); err != nil {
			return nil, rpcErrorFromUpstream(err)
		}
	}
	return newSessionResult{SessionID: s.id}, nil
}
func parseMCP(raw []json.RawMessage, s *session) error {
	for _, item := range raw {
		var cfg struct {
			Name    string `json:"name"`
			Command string `json:"command"`
			URL     string `json:"url"`
		}
		if json.Unmarshal(item, &cfg) != nil || cfg.Name == "" {
			return fmt.Errorf("MCP 必须引用已配置服务器 name")
		}
		if cfg.Command != "" || cfg.URL != "" {
			return fmt.Errorf("ACP 不接受未授权 MCP command/URL，请先配置可信连接")
		}
		s.mcp = append(s.mcp, cfg.Name)
	}
	return nil
}
func (c *conn) notifyAgentEvent(id string, e agent.Event) error {
	if e.Text != "" && e.Kind == agent.TextDelta {
		return c.notifyChunk(id, e.Text)
	}
	if e.Tool.Call.ID != "" {
		status := "in_progress"
		update := "tool_call"
		if e.Kind == agent.ToolFinished {
			update = "tool_call_update"
			status = "completed"
			if e.Tool.ErrorCode != "" {
				status = "failed"
			}
		}
		kind := "other"
		name := e.Tool.Call.Function.Name
		if strings.Contains(name, "exec") || strings.Contains(name, "terminal") {
			kind = "execute"
		} else if strings.Contains(name, "write") || strings.Contains(name, "edit") || strings.Contains(name, "patch") {
			kind = "edit"
		} else if strings.Contains(name, "read") {
			kind = "read"
		}
		c.write(rpcNotification{JSONRPC: jsonrpcVersion, Method: "session/update", Params: map[string]any{"sessionId": id, "update": map[string]any{"sessionUpdate": update, "toolCallId": e.Tool.Call.ID, "title": name, "kind": kind, "status": status, "rawInput": e.Tool.Call.Function.Arguments, "rawOutput": e.Tool.Content, "content": toolContent(e.Tool)}}})
	}
	return c.firstFailure()
}
