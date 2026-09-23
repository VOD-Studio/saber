package violetplatform

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/chat"
)

// platformName 是本平台在 chat.Session.Platform 与配置键里的标识，全包唯一来源。
const platformName = "violet"

// maxIdempotencyKeyBytes 对齐 Violet chat_messages.idempotency_key 的 VARCHAR(128) 上限。
const maxIdempotencyKeyBytes = 128

// editThrottle 把整个平台的出站编辑串成一条节拍。
//
// Violet 的写端点按 bot 用户维度限流，编辑配额约 300 次/分钟，而 Presenter 的
// 编辑节奏取自全局展示配置；这里补一道平台自己的下限，超频时**等待**而不是丢弃
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

// Adapter 实现 chat.Adapter：把通用回复翻译成 Violet Bot API 调用。
type Adapter struct {
	api       *client
	account   string
	throttle  *editThrottle
	mu        sync.Mutex
	revisions map[string]replyRevision
	// platformName 用于校验回复确实属于本接入端，避免拿别的平台的会话往 Violet 发。
	platformName string
}

type replyRevision struct {
	value  int64
	status string
}

// newAdapter 构造出站 adapter。throttle 应在同一平台实例间共享，才能守住全局编辑配额。
func newAdapter(api *client, account string, throttle *editThrottle) *Adapter {
	return &Adapter{api: api, account: account, throttle: throttle, platformName: platformName}
}

// Capabilities 返回 Violet 的编辑、输入提示、引用及生成状态能力。
func (a *Adapter) Capabilities() chat.Capabilities {
	return chat.Capabilities{Edit: true, Typing: true, Reply: true, ReplyState: true}
}

// Send 创建一条文本消息并返回可供编辑的消息 ID。
func (a *Adapter) Send(ctx context.Context, reply chat.Reply) (string, error) {
	if reply.Status != "" && reply.Status != chat.ReplyPending {
		return "", errors.New("violet 创建生成回复只能使用 pending 状态")
	}
	content, err := a.prepare(ctx, reply)
	if err != nil {
		return "", err
	}
	created, err := a.api.send(ctx, reply.Session.Conversation, outgoing(reply, content), idempotencyKey(reply))
	if err != nil {
		return "", fmt.Errorf("violet 发送消息失败: %w", err)
	}
	if created.ID == "" {
		return "", errors.New("violet 发送消息未返回消息 ID")
	}
	if created.BotReply != nil {
		a.mu.Lock()
		if a.revisions == nil {
			a.revisions = make(map[string]replyRevision)
		}
		if created.BotReply.Revision >= a.revisions[created.ID].value {
			a.revisions[created.ID] = replyRevision{created.BotReply.Revision, created.BotReply.Status}
		}
		a.mu.Unlock()
	}
	return created.ID, nil
}

// Edit 整体替换此前发出消息的正文。
func (a *Adapter) Edit(ctx context.Context, messageID string, reply chat.Reply) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if messageID == "" {
		return errors.New("violet 编辑消息缺少消息 ID")
	}
	content, err := a.prepare(ctx, reply)
	if err != nil {
		return err
	}
	if previous := a.revisions[messageID]; (previous.status == string(chat.ReplyCompleted) || previous.status == string(chat.ReplyFailed)) && previous.status == string(reply.Status) {
		delete(a.revisions, messageID)
		return nil
	}
	if err := a.throttle.wait(ctx); err != nil {
		return err
	}
	body := outgoing(reply, content)
	body.ReplyToID = ""
	if reply.Status != "" {
		body.Revision = a.revisions[messageID].value + 1
	}
	updated, err := a.api.edit(ctx, reply.Session.Conversation, messageID, body)
	if err != nil {
		return fmt.Errorf("violet 编辑消息失败: %w", err)
	}
	if updated.BotReply != nil {
		if updated.BotReply.Status == string(chat.ReplyCompleted) || updated.BotReply.Status == string(chat.ReplyFailed) {
			delete(a.revisions, messageID)
		} else {
			a.revisions[messageID] = replyRevision{updated.BotReply.Revision, updated.BotReply.Status}
		}
	}
	return nil
}

// SetTyping 上报输入状态。错误一律上抛，由调用方（Presenter 的清理路径）决定降级方式。
func (a *Adapter) SetTyping(ctx context.Context, session chat.Session, active bool) error {
	if err := a.validate(ctx, session); err != nil {
		return err
	}
	if err := a.api.setTyping(ctx, session.Conversation, active); err != nil {
		return fmt.Errorf("violet 上报输入状态失败: %w", err)
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
	if session.Platform != a.platformName {
		return fmt.Errorf("回复属于 %q 平台，violet adapter 不予投递", session.Platform)
	}
	if a.account != "" && session.Account != a.account {
		return fmt.Errorf("回复的 violet 账号 %q 与当前接入账号 %q 不一致", session.Account, a.account)
	}
	return nil
}

// prepare 校验并归一化出站正文：先验会话，再拒绝空白正文，最后按字符上限截断。
//
// Violet 的文本消息在库层面要求正文去掉空白后非空，空正文只会换一个 500 回来，
// 因此提前判掉；截断记 warn，运维看日志能知道答案被压短了。
func (a *Adapter) prepare(ctx context.Context, reply chat.Reply) (string, error) {
	if err := a.validate(ctx, reply.Session); err != nil {
		return "", err
	}
	if strings.TrimSpace(reply.Text) == "" && reply.Status != chat.ReplyPending && reply.Status != chat.ReplyThinking && reply.Status != chat.ReplyFailed {
		return "", errors.New("violet 不接受空白正文")
	}
	if reply.Status == chat.ReplyPending && strings.TrimSpace(reply.Text) != "" {
		return "", errors.New("violet pending 回复不能携带正文")
	}
	if reply.Status == "" && (reply.Thinking != "" || reply.ErrorCode != "") {
		return "", errors.New("violet 生成信息缺少回复状态")
	}
	switch reply.Status {
	case "", chat.ReplyPending, chat.ReplyThinking, chat.ReplyStreaming, chat.ReplyCompleted, chat.ReplyFailed:
	default:
		return "", fmt.Errorf("violet 不支持回复状态 %q", reply.Status)
	}
	content, truncated := truncateContent(reply.Text)
	if truncated {
		slog.Warn("violet 出站正文超长已截断", "platform", a.platformName, "conversation", reply.Session.Conversation, "runes", len([]rune(reply.Text)))
	}
	return content, nil
}

func outgoing(reply chat.Reply, content string) outgoingMessage {
	return outgoingMessage{Content: content, ReplyToID: reply.ReplyTo, Status: string(reply.Status), Thinking: reply.Thinking}
}

// idempotencyKey 取上层给定的事务 ID，缺省时生成一个随机键。
//
// 上层（任务投递）给的键必须原样带上：Violet 按 (会话, 发送者, 幂等键) 唯一，
// 同键重试会返回同一条消息，这正是「重试不该刷两遍屏」的语义。
// 超过列宽上限时退化为 SHA-256，稳定可重放且不会撑爆字段。
func idempotencyKey(reply chat.Reply) string {
	transaction := strings.TrimSpace(reply.TransactionID)
	if transaction == "" {
		return randomIdempotencyKey()
	}
	if len(transaction) <= maxIdempotencyKeyBytes {
		return transaction
	}
	sum := sha256.Sum256([]byte(transaction))
	return "saber-" + hex.EncodeToString(sum[:])
}

// randomIdempotencyKey 生成一次性幂等键，用于没有事务 ID 的即时回复。
func randomIdempotencyKey() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		// 随机源不可用时退回时间戳：键只需要唯一，不需要不可预测。
		return fmt.Sprintf("saber-%d", time.Now().UnixNano())
	}
	return "saber-" + hex.EncodeToString(buffer)
}
