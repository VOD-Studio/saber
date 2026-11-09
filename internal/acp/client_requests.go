package acp

import (
	"context"
	"encoding/json"
	"fmt"
)

type clientReply struct {
	Result json.RawMessage
	Error  *rpcError
}

func (c *conn) acceptClientReply(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	if fields["method"] != nil {
		return false
	}
	if fields["result"] == nil && fields["error"] == nil {
		return false
	}
	var id string
	if json.Unmarshal(fields["id"], &id) != nil {
		return true
	}
	c.requestMu.Lock()
	ch := c.pendingRequests[id]
	c.requestMu.Unlock()
	if ch == nil {
		return true
	}
	reply := clientReply{Result: fields["result"]}
	if fields["error"] != nil {
		_ = json.Unmarshal(fields["error"], &reply.Error)
	}
	select {
	case ch <- reply:
	default:
	}
	return true
}
func (c *conn) requestClient(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.requestMu.Lock()
	c.requestSequence++
	id := fmt.Sprintf("saber-request-%d", c.requestSequence)
	if c.pendingRequests == nil {
		c.pendingRequests = make(map[string]chan clientReply)
	}
	ch := make(chan clientReply, 1)
	c.pendingRequests[id] = ch
	c.requestMu.Unlock()
	defer func() { c.requestMu.Lock(); delete(c.pendingRequests, id); c.requestMu.Unlock() }()
	c.write(map[string]any{"jsonrpc": jsonrpcVersion, "id": id, "method": method, "params": params})
	if err := c.firstFailure(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	case reply := <-ch:
		if reply.Error != nil {
			return nil, fmt.Errorf("ACP client %s failed (%d): %s", method, reply.Error.Code, reply.Error.Message)
		}
		return reply.Result, nil
	}
}
func (c *conn) requestPermission(ctx context.Context, sessionID, title, toolID string, options []map[string]string) (string, error) {
	raw, err := c.requestClient(ctx, "session/request_permission", map[string]any{"sessionId": sessionID, "toolCall": map[string]any{"toolCallId": toolID, "title": title, "kind": "other", "status": "pending", "content": []any{map[string]any{"type": "content", "content": contentBlock{Type: "text", Text: title}}}}, "options": options})
	if err != nil {
		return "", err
	}
	var response struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return "", fmt.Errorf("invalid ACP permission response")
	}
	if response.Outcome.Outcome == "cancelled" {
		return "cancelled", nil
	}
	if response.Outcome.Outcome != "selected" {
		return "", fmt.Errorf("invalid ACP permission outcome")
	}
	for _, o := range options {
		if o["optionId"] == response.Outcome.OptionID {
			return response.Outcome.OptionID, nil
		}
	}
	return "", fmt.Errorf("client selected unknown permission option")
}
