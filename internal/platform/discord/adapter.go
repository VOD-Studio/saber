package discordplatform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/platform"
)

// nonceBytes 是 Discord nonce 字段的字符上限（25）。
const nonceBytes = 25

// editThrottle 把整个平台的出站编辑串成一条节拍。
//
// Discord 按频道限流（约每 5 秒 5 条），流式回复每帧都发必然触发 429；
// 超频时等待而不是丢弃，最终定稿同样走 Edit。
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

// Adapter 实现 chat.Adapter：把通用回复翻译成 Discord REST 调用。
type Adapter struct {
	api      *client
	account  string
	throttle *editThrottle
	pager    platform.ReplyPager
}

// 编译期断言：Discord 支持文本与真实附件上传。
var _ chat.Adapter = (*Adapter)(nil)

// newAdapter 构造出站 adapter；throttle 在同一平台实例间共享。
func newAdapter(api *client, account string, throttle *editThrottle) *Adapter {
	return &Adapter{api: api, account: account, throttle: throttle}
}

// Capabilities 返回 Discord 的展示能力：支持编辑、输入状态与引用回复。
func (a *Adapter) Capabilities() chat.Capabilities {
	return chat.Capabilities{Edit: true, Typing: true, Reply: true, Audio: true, Video: true, Files: true}
}

// Send 发送一条文本消息并返回可用于编辑的消息 ID。
func (a *Adapter) Send(ctx context.Context, reply chat.Reply) (string, error) {
	if err := a.validate(ctx, reply.Session); err != nil {
		return "", err
	}
	if strings.TrimSpace(reply.Text) == "" {
		return "", errors.New("discord 不接受空白正文")
	}
	return a.pager.Send(ctx, reply, reply.Text, maxContentRunes, func(ctx context.Context, item chat.Reply, text string) (string, error) {
		return a.api.sendMessage(ctx, item.Session.Conversation, text, item.ReplyTo, nonce(item.TransactionID))
	})
}

// Edit 原地更新已发送消息的正文。
func (a *Adapter) Edit(ctx context.Context, messageID string, reply chat.Reply) error {
	if err := a.validate(ctx, reply.Session); err != nil {
		return err
	}
	if strings.TrimSpace(messageID) == "" {
		return errors.New("discord 编辑消息缺少消息 ID")
	}
	if strings.TrimSpace(reply.Text) == "" {
		return errors.New("discord 不接受空白正文")
	}
	return a.pager.Edit(ctx, messageID, reply, reply.Text, maxContentRunes,
		func(ctx context.Context, item chat.Reply, text string) (string, error) {
			return a.api.sendMessage(ctx, item.Session.Conversation, text, "", nonce(item.TransactionID))
		},
		func(ctx context.Context, id string, item chat.Reply, text string) error {
			if err := a.throttle.wait(ctx); err != nil {
				return err
			}
			return a.api.editMessage(ctx, item.Session.Conversation, id, text)
		},
		func(ctx context.Context, id string, item chat.Reply) error {
			return a.api.do(ctx, "DELETE", "/channels/"+url.PathEscape(item.Session.Conversation)+"/messages/"+url.PathEscape(id), nil, nil)
		})
}

// SetTyping 上报输入状态。active 为 false 时 Discord 没有对应语义，是空操作。
func (a *Adapter) SetTyping(ctx context.Context, session chat.Session, active bool) error {
	if !active {
		return nil
	}
	if err := a.validate(ctx, session); err != nil {
		return err
	}
	if err := a.api.sendTyping(ctx, session.Conversation); err != nil {
		return fmt.Errorf("discord 上报输入状态失败: %w", err)
	}
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
		return fmt.Errorf("回复属于 %q 平台，discord adapter 不予投递", session.Platform)
	}
	if a.account != "" && session.Account != a.account {
		return fmt.Errorf("回复的 discord 账号 %q 与当前接入账号 %q 不一致", session.Account, a.account)
	}
	return nil
}

// nonce 把事务 ID 压成 Discord 接受的 25 字符 nonce。
//
// 任务重试带同一个 TransactionID 时，enforce_nonce 会让 Discord 返回同一条消息，
// 这就是「重试不刷两遍屏」；没有事务 ID 时返回空串，走普通发送。
func nonce(transaction string) string {
	value := strings.TrimSpace(transaction)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:nonceBytes]
}
