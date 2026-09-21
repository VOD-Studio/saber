// Package memory 提供无需网络的聊天 adapter，用于嵌入式调用和接入验收。
package memory

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// Adapter 模拟一个独立聊天账号，零值不可接收入站消息。
type Adapter struct {
	mu           sync.Mutex
	account      string
	capabilities chat.Capabilities
	handler      chat.Handler
	replies      []chat.Reply
	typing       map[chat.SessionID]bool
}

// New 创建账号实例，所有入站消息交给同一 Handler。
func New(account string, capabilities chat.Capabilities, handler chat.Handler) *Adapter {
	return &Adapter{account: account, capabilities: capabilities, handler: handler, typing: make(map[chat.SessionID]bool)}
}

// Receive 设置可信平台与账号来源，再进入通用聊天链路。
func (a *Adapter) Receive(ctx context.Context, message chat.Message) (agent.Result, error) {
	if a.handler == nil {
		return agent.Result{}, errors.New("memory chat handler is not configured")
	}
	message.Session.Platform, message.Session.Account = "memory", a.account
	return a.handler(ctx, message, a)
}

// Capabilities 返回创建时指定的展示能力。
func (a *Adapter) Capabilities() chat.Capabilities { return a.capabilities }

func (a *Adapter) validate(ctx context.Context, session chat.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.Validate(); err != nil {
		return err
	}
	if session.Platform != "memory" || session.Account != a.account {
		return errors.New("reply does not belong to this memory account")
	}
	return nil
}

// Send 保存回复并返回当前账号内唯一的消息编号。
func (a *Adapter) Send(ctx context.Context, reply chat.Reply) (string, error) {
	if err := a.validate(ctx, reply.Session); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.replies = append(a.replies, reply)
	return strconv.Itoa(len(a.replies)), nil
}

// Edit 更新已有消息，拒绝跨会话编辑。
func (a *Adapter) Edit(ctx context.Context, id string, reply chat.Reply) error {
	if err := a.validate(ctx, reply.Session); err != nil {
		return err
	}
	if !a.capabilities.Edit {
		return errors.New("memory adapter does not support editing")
	}
	n, err := strconv.Atoi(id)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if n < 1 || n > len(a.replies) || a.replies[n-1].Session != reply.Session {
		return errors.New("unknown message in this session")
	}
	a.replies[n-1] = reply
	return nil
}

// SetTyping 保存输入状态，供测试确认清理行为。
func (a *Adapter) SetTyping(ctx context.Context, session chat.Session, active bool) error {
	if err := a.validate(ctx, session); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if active {
		a.typing[session.Key()] = true
	} else {
		delete(a.typing, session.Key())
	}
	return nil
}

// Replies 返回已发送消息的快照，编辑反映为同一条消息的新内容。
func (a *Adapter) Replies() []chat.Reply {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]chat.Reply(nil), a.replies...)
}

// IsTyping 返回指定会话的输入状态。
func (a *Adapter) IsTyping(session chat.Session) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.typing[session.Key()]
}
