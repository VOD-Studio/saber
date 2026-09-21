package conversation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

// RunFunc 执行一次独立 Agent 运行，不持有平台对象。
type RunFunc func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error)

// Processor 统一消息组装、历史、同会话串行调度及回复交付。
// 不同会话独立运行；创建后不得修改字段。
type Processor struct {
	// Run 是必须注入的 Agent 执行器。
	Run RunFunc
	// History 可为空，表示不跨轮保存会话历史。
	History *ContextManager
	// Display 控制所有接入端共用的展示节流。
	Display chat.Display
	// Timeout 包含排队之后的运行与展示时间，零值默认 120 秒。
	Timeout  time.Duration
	mu       sync.Mutex
	sessions map[chat.SessionID]*sessionGate
}

type sessionGate struct {
	token chan struct{}
	refs  int
}

func (p *Processor) acquire(ctx context.Context, key chat.SessionID) (func(), error) {
	p.mu.Lock()
	if p.sessions == nil {
		p.sessions = make(map[chat.SessionID]*sessionGate)
	}
	gate := p.sessions[key]
	if gate == nil {
		gate = &sessionGate{token: make(chan struct{}, 1)}
		p.sessions[key] = gate
	}
	gate.refs++
	p.mu.Unlock()
	drop := func() {
		p.mu.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(p.sessions, key)
		}
		p.mu.Unlock()
	}
	select {
	case gate.token <- struct{}{}:
		return func() { <-gate.token; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

// Handle 将一条规范化消息追加到历史后运行；req.Messages 仅用于系统提示等前缀。
// 回复失败时仍返回成功的 Agent Result，并保留最终回答；不会重新执行工具。
func (p *Processor) Handle(ctx context.Context, message chat.Message, req agent.Request, adapter chat.Adapter) (agent.Result, error) {
	if err := message.Validate(); err != nil {
		return agent.Result{}, err
	}
	if p.Run == nil || adapter == nil {
		return agent.Result{}, errors.New("chat processor requires runner and adapter")
	}
	if err := ctx.Err(); err != nil {
		return agent.Result{Status: contextStatus(err)}, err
	}
	release, err := p.acquire(ctx, message.Session.Key())
	if err != nil {
		return agent.Result{Status: contextStatus(err)}, err
	}
	defer release()
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return agent.Result{Status: contextStatus(err)}, err
	}
	ctx = chat.WithIdentity(ctx, chat.Identity{Session: message.Session, SenderID: message.SenderID})
	messages := append([]openai.ChatCompletionMessage(nil), req.Messages...)
	if p.History != nil {
		messages = append(messages, p.History.GetContext(message.Session.Key())...)
	}
	current := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: message.Text}
	if len(message.Attachments) > 0 {
		current.Content = ""
		if message.Text != "" {
			current.MultiContent = append(current.MultiContent, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: message.Text})
		}
		for _, attachment := range message.Attachments {
			current.MultiContent = append(current.MultiContent, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: attachment.URL, Detail: openai.ImageURLDetailAuto}})
		}
	}
	req.Messages = append(messages, current)
	if p.History != nil {
		text := message.Text
		for _, a := range message.Attachments {
			text += "\n[图片: " + a.Name + "]"
		}
		p.History.AddMessage(message.Session.Key(), RoleUser, text, message.SenderID)
	}
	result, runErr := Deliver(ctx, p.Run, req, message, adapter, p.Display, func(result agent.Result) {
		if p.History != nil {
			p.History.AddMessage(message.Session.Key(), RoleAssistant, result.Content, "")
		}
	})
	return result, runErr
}

// Deliver 消费运行事件并交付回复；保存回调先于最终发送，保证展示失败不丢回答。
// 调用方须提供有截止时间的 ctx；历史组装及串行调度由 Handle 完成。
func Deliver(ctx context.Context, run RunFunc, req agent.Request, message chat.Message, adapter chat.Adapter, display chat.Display, save func(agent.Result)) (agent.Result, error) {
	if adapter.Capabilities().Typing {
		if err := adapter.SetTyping(ctx, message.Session, true); err != nil {
			slog.Debug("启动输入状态失败", "error", err)
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			if err := adapter.SetTyping(cleanup, message.Session, false); err != nil {
				slog.Debug("停止输入状态失败", "error", err)
			}
		}()
	}
	presenter := chat.NewPresenter(adapter, message, display)
	result, err := run(ctx, req, func(event agent.Event) {
		if displayErr := presenter.Event(ctx, event); displayErr != nil {
			slog.Debug("更新临时回复失败", "error", displayErr)
		}
	})
	if err != nil {
		return result, err
	}
	if result.Status != agent.Completed {
		return result, fmt.Errorf("agent ended without a final answer: %s", result.Status)
	}
	if save != nil {
		save(result)
	}
	if err := presenter.Finish(ctx, result.Content); err != nil {
		return result, fmt.Errorf("发送响应失败：%w", err)
	}
	return result, nil
}

func contextStatus(err error) agent.Status {
	if errors.Is(err, context.DeadlineExceeded) {
		return agent.TimedOut
	}
	return agent.Cancelled
}
