package ai

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

type taskStreamConfig struct {
	adapter chat.Adapter
	display chat.Display
}

// taskStream 在后台合并文本增量；终态由持久化投递器可靠定稿。
type taskStream struct {
	task    task.Task
	adapter chat.Adapter
	display chat.Display

	mu       sync.Mutex
	content  strings.Builder
	thinking strings.Builder
	status   chat.ReplyStatus
	started  time.Time
	version  uint64
	updates  chan struct{}
	stop     chan struct{}
	done     chan struct{}
}

func (s *Service) newTaskStream(ctx context.Context) *taskStream {
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return nil
	}
	value, ok := s.taskStreams.Load(identity.Session.Platform)
	if !ok {
		return nil
	}
	config := value.(taskStreamConfig)
	t, err := s.tasks.Get(ctx, identity.Session, task.ID(ctx))
	if err != nil {
		slog.Warn("读取任务流式回复来源失败", "task", task.ID(ctx), "error", err)
		return nil
	}
	p := &taskStream{
		task: t, adapter: config.adapter, display: config.display,
		started: time.Now(), status: chat.ReplyPending, updates: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go p.run(ctx)
	return p
}

func (p *taskStream) event(event agent.Event) {
	p.mu.Lock()
	switch event.Kind {
	case agent.ModelStarted, agent.AttemptStarted:
		p.content.Reset()
		p.thinking.Reset()
		p.status = chat.ReplyThinking
		p.started = time.Now()
		p.version++
		select {
		case p.updates <- struct{}{}:
		default:
		}
	case agent.ThinkingDelta:
		p.thinking.WriteString(event.Text)
		if p.status != chat.ReplyStreaming {
			p.status = chat.ReplyThinking
		}
		p.version++
		select {
		case p.updates <- struct{}{}:
		default:
		}
	case agent.TextDelta:
		p.content.WriteString(event.Text)
		p.status = chat.ReplyStreaming
		p.version++
		select {
		case p.updates <- struct{}{}:
		default:
		}
	}
	p.mu.Unlock()
}

func (p *taskStream) close() {
	close(p.stop)
	<-p.done
}

func (p *taskStream) run(ctx context.Context) {
	defer close(p.done)
	if p.adapter.Capabilities().ReplyState {
		p.runReplyState(ctx)
		return
	}
	interval := p.display.EditInterval
	if interval <= 0 {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var messageID string
	var lastAttempt time.Time
	var shown uint64
	var edits int
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stop:
			return
		case <-p.updates:
		case <-ticker.C:
		}
		p.mu.Lock()
		text, started, version := strings.Clone(p.content.String()), p.started, p.version
		p.mu.Unlock()
		if version == shown || strings.TrimSpace(text) == "" {
			continue
		}
		if messageID == "" && len(text) < p.display.CharThreshold && time.Since(started) < p.display.TimeThreshold {
			continue
		}
		if messageID != "" && (p.display.MaxEdits > 0 && edits >= p.display.MaxEdits || time.Since(lastAttempt) < p.display.EditInterval) {
			continue
		}
		lastAttempt = time.Now()
		updateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		reply := taskReply(p.task, "result", text)
		var err error
		if messageID == "" {
			messageID, err = p.adapter.Send(updateCtx, reply)
		} else {
			err = p.adapter.Edit(updateCtx, messageID, reply)
			if err == nil {
				edits++
			}
		}
		cancel()
		shown = version
		if err != nil {
			slog.Debug("更新任务临时回复失败，终态仍会重试投递", "task", p.task.ID, "error", err)
		}
	}
}

// runReplyState 在同一条幂等回复上更新状态和累计内容；无增量时续期生成租约。
func (p *taskStream) runReplyState(ctx context.Context) {
	interval := p.display.EditInterval
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var messageID string
	var shown uint64
	var lastUpdate time.Time
	var retryAt time.Time
	var backoff time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stop:
			return
		case <-p.updates:
		case <-ticker.C:
		}
		if time.Now().Before(retryAt) {
			continue
		}
		p.mu.Lock()
		text, thinking, status, version := strings.Clone(p.content.String()), strings.Clone(p.thinking.String()), p.status, p.version
		p.mu.Unlock()
		if messageID == "" {
			pending := taskReply(p.task, "result", "")
			pending.Status = chat.ReplyPending
			updateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			id, err := p.adapter.Send(updateCtx, pending)
			cancel()
			if err != nil {
				slog.Debug("取得任务回复占位消息失败", "task", p.task.ID, "error", err)
				backoff = replyRetryDelay(err, backoff)
				retryAt = time.Now().Add(backoff)
				continue
			}
			messageID = id
			lastUpdate = time.Now()
			backoff = 0
		}
		if version == shown && time.Since(lastUpdate) < 15*time.Second {
			continue
		}
		reply := taskReply(p.task, "result", text)
		reply.Status, reply.Thinking = status, thinking
		updateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := p.adapter.Edit(updateCtx, messageID, reply)
		cancel()
		if err != nil {
			slog.Debug("更新任务回复状态失败，终态仍会重试投递", "task", p.task.ID, "error", err)
			backoff = replyRetryDelay(err, backoff)
			retryAt = time.Now().Add(backoff)
			continue
		}
		shown, lastUpdate = version, time.Now()
		backoff = 0
	}
}

func replyRetryDelay(err error, previous time.Duration) time.Duration {
	delay := time.Second
	if previous > 0 {
		delay = min(previous*2, 30*time.Second)
	}
	var advised interface{ RetryDelay() time.Duration }
	if errors.As(err, &advised) {
		delay = max(delay, advised.RetryDelay())
	}
	return delay
}
