package chat

import (
	"context"
	"strings"
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

// Presenter 将运行事件转为回复；每次运行创建一个实例，串行使用。
type Presenter struct {
	adapter      Adapter
	reply        Reply
	display      Display
	capabilities Capabilities
	content      strings.Builder
	messageID    string
	started      time.Time
	lastEdit     time.Time
	edits        int
	final        bool
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

// Finish 只提交一次最终回复，临时消息失败不阻止本次交付尝试。
func (p *Presenter) Finish(ctx context.Context, content string) error {
	if p.final {
		return nil
	}
	p.final = true
	reply := p.reply
	reply.Text = content
	if p.messageID != "" && p.capabilities.Edit {
		return p.adapter.Edit(ctx, p.messageID, reply)
	}
	_, err := p.adapter.Send(ctx, reply)
	return err
}
