package slackplatform

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/platform"
)

// selfMessageCapacity 是「本 bot 最近发出的消息 ts」的保留数量。
// 只用于判断线程父消息是不是自己发的，不需要长期保留。
const selfMessageCapacity = 512

// editThrottle 把整个平台的出站编辑串成一条节拍。
//
// Slack 对 chat.update 有按频道的速率限制，流式回复每个 token 都发会立刻
// ratelimited；超频时等待而不是丢弃最终定稿。
type editThrottle struct {
	mu       sync.Mutex
	interval time.Duration
	last     time.Time
}

// wait 等到距上次编辑满足最小间隔，期间 ctx 取消则返回其错误。
func (t *editThrottle) wait(ctx context.Context) error {
	if t == nil || t.interval <= 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.last.IsZero() {
		if remaining := t.interval - time.Since(t.last); remaining > 0 {
			timer := time.NewTimer(remaining)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	t.last = time.Now()
	return nil
}

// selfMessages 记录本 bot 最近发出的消息 ts，用于识别「有人在我的线程里回复」。
type selfMessages struct {
	mu    sync.Mutex
	order []string
	seen  map[string]struct{}
}

// newSelfMessages 创建空索引。
func newSelfMessages() *selfMessages {
	return &selfMessages{seen: make(map[string]struct{}, selfMessageCapacity)}
}

// add 记录一个消息 ts，超出容量时丢弃最旧的一条。
func (s *selfMessages) add(ts string) {
	if s == nil || ts == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[ts]; ok {
		return
	}
	if len(s.order) >= selfMessageCapacity {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.seen, oldest)
	}
	s.order = append(s.order, ts)
	s.seen[ts] = struct{}{}
}

// has 判断 ts 是否由本 bot 发出。
func (s *selfMessages) has(ts string) bool {
	if s == nil || ts == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[ts]
	return ok
}

// Adapter 实现 chat.Adapter：把通用回复翻译成 Slack Web API 调用。
type Adapter struct {
	api      *client
	account  string
	throttle *editThrottle
	self     *selfMessages
	pager    platform.ReplyPager
}

// 编译期断言：Slack 支持文本及 external upload 文件协议。
var _ chat.Adapter = (*Adapter)(nil)

// newAdapter 构造出站 adapter；throttle 与 self 在同一平台实例间共享。
func newAdapter(api *client, account string, throttle *editThrottle, self *selfMessages) *Adapter {
	return &Adapter{api: api, account: account, throttle: throttle, self: self}
}

// Capabilities 返回 Slack 的展示能力：支持编辑与线程回复，不支持输入状态。
func (a *Adapter) Capabilities() chat.Capabilities {
	return chat.Capabilities{Edit: true, Reply: true, Audio: true, Video: true, Files: true}
}

// Send 发送一条消息并返回可用于编辑的消息 ts。
func (a *Adapter) Send(ctx context.Context, reply chat.Reply) (string, error) {
	if err := a.validate(ctx, reply.Session); err != nil {
		return "", err
	}
	if strings.TrimSpace(reply.Text) == "" {
		return "", errors.New("slack 不接受空白正文")
	}
	return a.pager.Send(ctx, reply, escapeText(reply.Text), maxContentRunes, func(ctx context.Context, item chat.Reply, text string) (string, error) {
		thread := item.Session.Thread
		if thread == "" {
			thread = item.ReplyTo
		}
		sent, err := a.api.postMessage(ctx, item.Session.Conversation, text, thread)
		if err == nil {
			a.self.add(sent)
		}
		return sent, err
	})
}

// Edit 原地更新已发送消息的正文。
func (a *Adapter) Edit(ctx context.Context, messageID string, reply chat.Reply) error {
	if err := a.validate(ctx, reply.Session); err != nil {
		return err
	}
	if strings.TrimSpace(messageID) == "" {
		return errors.New("slack 编辑消息缺少消息 ID")
	}
	if strings.TrimSpace(reply.Text) == "" {
		return errors.New("slack 不接受空白正文")
	}
	return a.pager.Edit(ctx, messageID, reply, escapeText(reply.Text), maxContentRunes,
		func(ctx context.Context, item chat.Reply, text string) (string, error) {
			id, err := a.api.postMessage(ctx, item.Session.Conversation, text, item.Session.Thread)
			if err == nil {
				a.self.add(id)
			}
			return id, err
		},
		func(ctx context.Context, id string, item chat.Reply, text string) error {
			if err := a.throttle.wait(ctx); err != nil {
				return err
			}
			err := a.api.updateMessage(ctx, item.Session.Conversation, id, text)
			if isNotModified(err) {
				return nil
			}
			return err
		},
		func(ctx context.Context, id string, item chat.Reply) error {
			return a.api.post(ctx, "chat.delete", a.api.botToken, url.Values{"channel": {item.Session.Conversation}, "ts": {id}}, nil)
		})
}

// SetTyping 是空操作：Slack 没有对 bot 开放的输入状态接口。
func (a *Adapter) SetTyping(context.Context, chat.Session, bool) error {
	return nil
}

// validate 确认请求可以落到本平台的会话上。
func (a *Adapter) validate(ctx context.Context, session chat.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.Validate(); err != nil {
		return err
	}
	if session.Platform != platformName {
		return fmt.Errorf("回复属于 %q 平台，slack adapter 不予投递", session.Platform)
	}
	if a.account != "" && session.Account != a.account {
		return fmt.Errorf("回复的 slack 账号 %q 与当前接入账号 %q 不一致", session.Account, a.account)
	}
	return nil
}

// isNotModified 识别 Slack 对「正文没变」的编辑拒绝。
func isNotModified(err error) bool {
	apiErr, ok := err.(*apiError)
	if !ok {
		return false
	}
	return apiErr.Code == "message_not_modified" || apiErr.Code == "no_text"
}
