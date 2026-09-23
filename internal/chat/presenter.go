package chat

import (
	"context"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/agent"
)

// Display 控制平台无关的流式展示；零值会立即展示增量且不限制编辑次数。
type Display struct {
	// CharThreshold 是开始显示临时消息的文本字节阈值。
	CharThreshold int
	// TimeThreshold 是开始显示临时消息的等待时长。
	TimeThreshold time.Duration
	// EditInterval 限制两次临时编辑的最短间隔，最终回复不受限制。
	EditInterval time.Duration
	// MaxEdits 限制临时编辑次数，零值不限制。
	MaxEdits int
}

// Presenter 将运行事件转为回复；每次运行创建一个实例，内部串行化事件与心跳。
type Presenter struct {
	mu           sync.Mutex
	adapter      Adapter
	reply        Reply
	display      Display
	capabilities Capabilities
	content      strings.Builder
	thinking     strings.Builder
	messageID    string
	started      time.Time
	lastEdit     time.Time
	edits        int
	final        bool
}

// Start 为支持生成状态的平台创建空正文占位消息。
func (p *Presenter) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.capabilities.ReplyState || p.messageID != "" {
		return nil
	}
	p.reply.Status = ReplyPending
	reply := p.reply
	id, err := p.adapter.Send(ctx, reply)
	if err == nil {
		p.messageID = id
	}
	return err
}

// Heartbeat 续期尚未结束的回复，不改变正文和思考内容。
func (p *Presenter) Heartbeat(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.capabilities.ReplyState || p.final || p.messageID == "" {
		return nil
	}
	return p.adapter.Edit(ctx, p.messageID, p.reply)
}

// NewPresenter 绑定入站来源，回复目的地不会来自模型输出。
func NewPresenter(adapter Adapter, message Message, display Display) *Presenter {
	caps := adapter.Capabilities()
	reply := Reply{Session: message.Session}
	if caps.Reply {
		reply.ReplyTo = message.ID
	}
	return &Presenter{adapter: adapter, reply: reply, display: display, capabilities: caps, started: time.Now()}
}

// Event 显示文本增量，临时失败返回给调用方记录，但不会使 Agent 重跑。
// 最终结果通过 Finish 发送，以便先保存历史并独立报告展示错误。
func (p *Presenter) Event(ctx context.Context, event agent.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.capabilities.ReplyState {
		return p.replyStateEvent(ctx, event)
	}
	switch event.Kind {
	case agent.AttemptStarted, agent.ModelStarted:
		p.content.Reset()
		p.started = time.Now()
	case agent.TextDelta:
		if p.final || !p.capabilities.Edit {
			return nil
		}
		p.content.WriteString(event.Text)
		if p.messageID == "" && p.content.Len() < p.display.CharThreshold && time.Since(p.started) < p.display.TimeThreshold {
			return nil
		}
		if p.messageID != "" && (p.display.MaxEdits > 0 && p.edits >= p.display.MaxEdits || time.Since(p.lastEdit) < p.display.EditInterval) {
			return nil
		}
		reply := p.reply
		reply.Text = p.content.String()
		if p.messageID == "" {
			id, err := p.adapter.Send(ctx, reply)
			if err != nil {
				return err
			}
			p.messageID = id
		} else {
			if err := p.adapter.Edit(ctx, p.messageID, reply); err != nil {
				return err
			}
			p.edits++
		}
		p.lastEdit = time.Now()
	}
	return nil
}

func (p *Presenter) replyStateEvent(ctx context.Context, event agent.Event) error {
	if p.final {
		return nil
	}
	switch event.Kind {
	case agent.ModelStarted, agent.AttemptStarted:
		p.content.Reset()
		p.thinking.Reset()
		p.reply.Status = ReplyThinking
	case agent.ThinkingDelta:
		p.thinking.WriteString(event.Text)
		if p.reply.Status != ReplyStreaming {
			p.reply.Status = ReplyThinking
		}
	case agent.TextDelta:
		p.content.WriteString(event.Text)
		p.reply.Status = ReplyStreaming
	default:
		return nil
	}
	p.reply.Text, p.reply.Thinking = p.content.String(), p.thinking.String()
	if p.messageID == "" {
		pending := p.reply
		pending.Status, pending.Text, pending.Thinking = ReplyPending, "", ""
		id, err := p.adapter.Send(ctx, pending)
		if err != nil {
			return err
		}
		p.messageID = id
	}
	if time.Since(p.lastEdit) < p.display.EditInterval {
		return nil
	}
	if err := p.adapter.Edit(ctx, p.messageID, p.reply); err != nil {
		return err
	}
	p.lastEdit = time.Now()
	return nil
}

// Finish 只提交一次最终回复，临时消息失败不阻止本次交付尝试。
func (p *Presenter) Finish(ctx context.Context, content string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.final {
		return nil
	}
	p.final = true
	reply := p.reply
	reply.Text = content
	if p.capabilities.ReplyState {
		reply.Status = ReplyCompleted
		reply.Thinking = p.thinking.String()
		return p.commitReplyState(ctx, reply)
	}
	if p.messageID != "" && p.capabilities.Edit {
		return p.adapter.Edit(ctx, p.messageID, reply)
	}
	_, err := p.adapter.Send(ctx, reply)
	return err
}

// Fail 将错误与部分正文留在同一条消息上，并写入明确的失败状态。
func (p *Presenter) Fail(ctx context.Context, code, detail string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.capabilities.ReplyState || p.final {
		return nil
	}
	p.final = true
	reply := p.reply
	reply.Status, reply.ErrorCode = ReplyFailed, code
	reply.Text, reply.Thinking = p.content.String(), p.thinking.String()
	if detail != "" {
		reply.Text = "⚠️ Saber 未能完成回复：" + detail + "\n\n" + reply.Text
	}
	return p.commitReplyState(ctx, reply)
}

func (p *Presenter) commitReplyState(ctx context.Context, reply Reply) error {
	if p.messageID == "" {
		pending := reply
		pending.Status, pending.Text, pending.Thinking, pending.ErrorCode = ReplyPending, "", "", ""
		id, err := p.adapter.Send(ctx, pending)
		if err != nil {
			return err
		}
		p.messageID = id
	}
	return p.adapter.Edit(ctx, p.messageID, reply)
}
