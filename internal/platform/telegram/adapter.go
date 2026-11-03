package telegramplatform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"rua.plus/saber/internal/chat"
)

// editThrottle 把整个平台的出站编辑串成一条节拍。
//
// Telegram 对同一会话的编辑频率敏感（群组约每分钟 20 条），流式回复每个 token
// 都发一次必然触发 429；这里补一道平台自己的下限，超频时等待而不是丢弃
// —— 最终定稿同样走 Edit，丢一次编辑就等于丢掉答案。
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

// Adapter 实现 chat.Adapter：把通用回复翻译成 Telegram Bot API 调用。
type Adapter struct {
	api      *client
	account  string
	throttle *editThrottle
}

// 编译期断言：Telegram 只实现文本能力，没有图片/文件方法，任务投递会自动降级。
var _ chat.Adapter = (*Adapter)(nil)

// newAdapter 构造出站 adapter；throttle 在同一平台实例间共享，才守得住平台级编辑节流。
func newAdapter(api *client, account string, throttle *editThrottle) *Adapter {
	return &Adapter{api: api, account: account, throttle: throttle}
}

// Capabilities 返回 Telegram 的展示能力：支持编辑、输入状态与引用回复。
func (a *Adapter) Capabilities() chat.Capabilities {
	return chat.Capabilities{Edit: true, Typing: true, Reply: true}
}

// Send 发送一条文本消息并返回可用于编辑的消息 ID。
func (a *Adapter) Send(ctx context.Context, reply chat.Reply) (string, error) {
	if err := a.validate(ctx, reply.Session); err != nil {
		return "", err
	}
	text, err := prepareText(reply)
	if err != nil {
		return "", err
	}
	chatID, err := parseChatID(reply.Session.Conversation)
	if err != nil {
		return "", err
	}
	replyTo, err := parseMessageID(reply.ReplyTo)
	if err != nil {
		return "", err
	}
	sent, err := a.api.sendMessage(ctx, chatID, parseThreadID(reply.Session.Thread), text, replyTo)
	if err != nil {
		return "", fmt.Errorf("telegram 发送消息失败: %w", err)
	}
	if sent.MessageID == 0 {
		return "", errors.New("telegram 发送消息未返回消息 ID")
	}
	return fmt.Sprint(sent.MessageID), nil
}

// Edit 原地更新已发送消息的正文；正文与上一帧相同时按成功处理。
func (a *Adapter) Edit(ctx context.Context, messageID string, reply chat.Reply) error {
	if err := a.validate(ctx, reply.Session); err != nil {
		return err
	}
	text, err := prepareText(reply)
	if err != nil {
		return err
	}
	chatID, err := parseChatID(reply.Session.Conversation)
	if err != nil {
		return err
	}
	parsedMessageID, err := parseMessageID(messageID)
	if err != nil || parsedMessageID == 0 {
		return errors.New("telegram 编辑消息缺少合法消息 ID")
	}
	if err := a.throttle.wait(ctx); err != nil {
		return err
	}
	if err := a.api.editMessageText(ctx, chatID, parsedMessageID, text); err != nil {
		if isNotModified(err) {
			return nil
		}
		return fmt.Errorf("telegram 编辑消息失败: %w", err)
	}
	return nil
}

// SetTyping 上报输入状态。active 为 false 时 Telegram 没有对应语义，是空操作。
func (a *Adapter) SetTyping(ctx context.Context, session chat.Session, active bool) error {
	if !active {
		return nil
	}
	if err := a.validate(ctx, session); err != nil {
		return err
	}
	chatID, err := parseChatID(session.Conversation)
	if err != nil {
		return err
	}
	if err := a.api.sendChatAction(ctx, chatID, parseThreadID(session.Thread), "typing"); err != nil {
		return fmt.Errorf("telegram 上报输入状态失败: %w", err)
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
		return fmt.Errorf("回复属于 %q 平台，telegram adapter 不予投递", session.Platform)
	}
	if a.account != "" && session.Account != a.account {
		return fmt.Errorf("回复的 telegram 账号 %q 与当前接入账号 %q 不一致", session.Account, a.account)
	}
	return nil
}

// errEmptyText 表示正文为空，平台会以 400 拒绝。
var errEmptyText = errors.New("telegram 不接受空白正文")

// prepareText 截断并校验出站正文。Telegram 不接受空白正文，提前判掉比换一个 400 更清楚。
func prepareText(reply chat.Reply) (string, error) {
	if reply.Session.Platform != platformName {
		return "", fmt.Errorf("telegram adapter 只接受 telegram 会话，收到 %q", reply.Session.Platform)
	}
	if reply.Text == "" {
		return "", errEmptyText
	}
	text, truncated := truncateContent(reply.Text)
	if truncated {
		slog.Warn("telegram 出站正文超长已截断", "conversation", reply.Session.Conversation, "runes", len([]rune(reply.Text)))
	}
	return text, nil
}
