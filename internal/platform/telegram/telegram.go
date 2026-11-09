// Package telegramplatform 是 Telegram Bot API 接入端：以 bot 身份长轮询 getUpdates
// 收消息，把消息规范化为 chat.Message 交给共享聊天链路，并实现出站 chat.Adapter。
//
// 入站用长轮询而不是 webhook：不需要公网地址、TLS 证书或端口映射，部署在 NAT 后面
// 也能用。断线靠 update_id 偏移重放，重复投递由偏移推进去重。
package telegramplatform

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/platform"
)

// platformName 是本平台在 chat.Session.Platform 与配置键里的标识。
const platformName = "telegram"

const (
	// initialReconnectDelay 与 maxReconnectDelay 是长轮询失败退避的两端。
	// 网络抖动通常在秒级恢复，退避到 1 分钟封顶既不打爆对端，也不会在故障时白烧 CPU。
	initialReconnectDelay = time.Second
	maxReconnectDelay     = time.Minute
	// reconnectJitter 给退避加 ±20% 抖动，避免多实例同一时刻齐刷刷重连。
	reconnectJitter = 0.2
	// inboundWorkers 与 inboundQueueSize 限定并发回答数量：一条长回答会占住一个 worker，
	// 队列满时轮询循环背压等待，而不是把消息堆在内存里无上限增长。
	inboundWorkers   = 4
	inboundQueueSize = 64
)

// Platform 以 bot 身份接入 Telegram 私聊与群组。
type Platform struct {
	cfg      config.TelegramConfig
	api      *client
	adapter  *Adapter
	throttle *editThrottle

	// handler 是共享聊天链路，由 Start 注入。
	handler chat.Handler
	// queue 是入站消息的有界缓冲，worker 从中取消息处理。
	queue chan message
	// cancel 结束轮询与 worker，Stop 使用。
	cancel context.CancelFunc
	// wg 等在途请求收尾，保证 Stop 之后不再有平台调用。
	wg sync.WaitGroup
	// startMu 保护 cancel 的读写，使重复 Start 与并发 Stop 都有确定语义。
	startMu sync.Mutex
	// offset 是下一条待确认的 update_id。跨重启保留：Telegram 只有在下一轮
	// 带着更大 offset 轮询后才确认旧更新，重启后从 0 开始会把已答过的消息再答一遍。
	offset atomic.Int64

	identityMu  sync.RWMutex
	botID       int64
	botUsername string
	// mentionPattern 是 @botusername 的匹配器，取得身份后编译一次。
	mentionPattern *regexp.Regexp
}

// New 绑定 Telegram 配置。构造只准备客户端与 adapter，不发起任何请求，
// 因此配置非法时错误留到 Start 才报，装配阶段可以先注册再启动。
func New(cfg *config.Config) *Platform {
	platform := &Platform{}
	if cfg == nil {
		return platform
	}
	platform.cfg = cfg.Platforms.Telegram
	interval := platform.cfg.EditIntervalMs
	if interval <= 0 {
		interval = config.DefaultTelegramConfig().EditIntervalMs
	}
	platform.throttle = &editThrottle{interval: time.Duration(interval) * time.Millisecond}
	platform.api = newClient(platform.cfg)
	platform.adapter = newAdapter(platform.api, platform.cfg.EffectiveAccount(), platform.throttle)
	return platform
}

// Name 返回平台标识，与 chat.Session.Platform 和配置键一致。
func (p *Platform) Name() string { return platformName }

// Start 校验配置并在后台开始长轮询。只有「重试也不会好」的配置问题同步报错；
// 网络失败由 supervise 退避重试。重复 Start 是幂等的。
func (p *Platform) Start(ctx context.Context, handler chat.Handler) error {
	if p.api == nil {
		return errors.New("telegram 平台缺少配置")
	}
	if err := p.cfg.Validate(); err != nil {
		return err
	}
	if handler == nil {
		return errors.New("telegram 平台缺少消息处理入口")
	}
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if p.cancel != nil {
		slog.Debug("telegram 平台已在运行，忽略重复启动")
		return nil
	}
	p.handler = handler
	p.queue = make(chan message, inboundQueueSize)
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.wg.Add(1 + inboundWorkers)
	go p.supervise(runCtx)
	for range inboundWorkers {
		go p.work(runCtx)
	}
	slog.Info("telegram 平台已启动", "account", p.adapter.account, "api_base", p.cfg.APIBase)
	return nil
}

// Stop 取消长轮询并等待在途回答收尾。未启动或已停止时是空操作，可重复调用。
func (p *Platform) Stop() {
	p.startMu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.startMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	p.wg.Wait()
}

// DeliveryAdapter 返回一次性发送的出站 adapter，用于任务与定时计划的结果投递。
func (p *Platform) DeliveryAdapter() chat.Adapter {
	if p.adapter == nil {
		return nil
	}
	return p.adapter
}

// supervise 维持「取身份—长轮询—退避重试」循环，直到 ctx 结束。
func (p *Platform) supervise(ctx context.Context) {
	defer p.wg.Done()
	delay := initialReconnectDelay
	for ctx.Err() == nil {
		offset := p.offset.Load()
		if err := p.ensureIdentity(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("获取 telegram bot 身份失败，稍后重试", "error", err, "retry_in", delay.String())
			delay = p.sleep(ctx, delay)
			continue
		}
		updates, err := p.api.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("telegram 长轮询失败，稍后重试", "error", err, "retry_in", delay.String())
			delay = p.sleep(ctx, delay)
			continue
		}
		// 轮询成功说明链路恢复，退避重新从短起步。
		delay = initialReconnectDelay
		for index := range updates {
			item := updates[index]
			if item.UpdateID >= offset {
				offset = item.UpdateID + 1
				p.offset.Store(offset)
			}
			if item.Message == nil {
				slog.Debug("跳过非新消息的 telegram 更新", "update", item.UpdateID)
				continue
			}
			select {
			case p.queue <- *item.Message:
			case <-ctx.Done():
				return
			}
		}
	}
}

// sleep 退避等待并返回下一次的退避时长；ctx 结束时立即返回。
func (p *Platform) sleep(ctx context.Context, delay time.Duration) time.Duration {
	select {
	case <-ctx.Done():
		return delay
	case <-time.After(jitter(delay)):
	}
	if next := delay * 2; next < maxReconnectDelay {
		return next
	}
	return maxReconnectDelay
}

// work 串行消费入站队列，直到 ctx 结束。
func (p *Platform) work(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-p.queue:
			p.handle(ctx, item)
		}
	}
}

// handle 把一条已放行的消息交给共享聊天链路。
func (p *Platform) handle(ctx context.Context, item message) {
	normalized, ok := p.normalize(item)
	if !ok {
		return
	}
	attachments, err := p.receiveMedia(ctx, item)
	if err != nil {
		slog.Warn("telegram 媒体读取失败", "message", normalized.ID, "error", err)
		return
	}
	normalized.Attachments = attachments
	ctx = chat.WithIdentity(ctx, chat.Identity{Session: normalized.Session, SenderID: normalized.SenderID, Direct: normalized.Direct})
	slog.Info("telegram 消息触发 AI 回复",
		"conversation", normalized.Session.Conversation,
		"sender", normalized.SenderID,
		"message", normalized.ID)
	if _, err := p.handler(ctx, normalized, p.adapter); err != nil {
		slog.Warn("telegram 消息处理失败",
			"conversation", normalized.Session.Conversation,
			"message", normalized.ID,
			"error", err)
	}
}

// ensureIdentity 确保已取到机器人身份：自回声过滤与被 @ 判定都依赖它。
func (p *Platform) ensureIdentity(ctx context.Context) error {
	if id, _ := p.identity(); id != 0 {
		return nil
	}
	profile, err := p.api.getMe(ctx)
	if err != nil {
		return err
	}
	if profile.ID == 0 || profile.Username == "" {
		return errors.New("telegram getMe 未返回完整的 bot 身份")
	}
	p.setIdentity(profile)
	slog.Info("已获取 telegram bot 身份", "username", profile.Username, "id", profile.ID)
	return nil
}

// identity 返回已缓存的机器人 ID 与用户名。
func (p *Platform) identity() (int64, string) {
	p.identityMu.RLock()
	defer p.identityMu.RUnlock()
	return p.botID, p.botUsername
}

// setIdentity 缓存机器人身份并编译提及匹配器。
func (p *Platform) setIdentity(profile botUser) {
	p.identityMu.Lock()
	defer p.identityMu.Unlock()
	p.botID = profile.ID
	p.botUsername = profile.Username
	p.mentionPattern = regexp.MustCompile(`(?i)` + regexp.QuoteMeta("@"+profile.Username) + `\b`)
}

// normalize 把 Telegram 消息翻译为 chat.Message，并按配置判定是否应当回答。
func (p *Platform) normalize(item message) (chat.Message, bool) {
	if item.Text == "" {
		item.Text = item.Caption
		item.Entities = item.CaptionEntities
	}
	botID, _ := p.identity()
	if botID == 0 {
		slog.Debug("telegram bot 身份未知，暂不处理消息", "message", item.MessageID)
		return chat.Message{}, false
	}
	if item.From == nil || item.From.ID == 0 {
		slog.Debug("telegram 消息缺少发送者，跳过", "message", item.MessageID)
		return chat.Message{}, false
	}
	if item.From.ID == botID || item.From.IsBot {
		slog.Debug("跳过 telegram bot 自身或其他机器人的消息", "message", item.MessageID)
		return chat.Message{}, false
	}
	if item.Chat.ID == 0 || item.Chat.Type == "channel" {
		slog.Debug("跳过 telegram 频道消息", "message", item.MessageID)
		return chat.Message{}, false
	}
	if strings.TrimSpace(item.Text) == "" && !item.hasMedia() {
		return chat.Message{}, false
	}
	direct := item.Chat.Type == "private"
	replyToBot := item.ReplyToMessage != nil && item.ReplyToMessage.From != nil && item.ReplyToMessage.From.ID == botID
	mentionsSelf := p.mentionsSelf(item)
	if direct {
		if !p.cfg.DirectChatAutoReply {
			slog.Debug("telegram 私聊自动回复已关闭", "message", item.MessageID)
			return chat.Message{}, false
		}
	} else {
		if !p.cfg.GroupChatMentionReply {
			slog.Debug("telegram 群聊提及回复已关闭", "message", item.MessageID)
			return chat.Message{}, false
		}
		if !mentionsSelf && !replyToBot {
			slog.Debug("telegram 群聊消息未点名或回复本 bot", "message", item.MessageID)
			return chat.Message{}, false
		}
	}
	text := p.stripMentions(item.Text)
	if strings.TrimSpace(text) == "" && !item.hasMedia() {
		return chat.Message{}, false
	}
	replyTo := ""
	if item.ReplyToMessage != nil && item.ReplyToMessage.MessageID > 0 {
		replyTo = strconv.FormatInt(item.ReplyToMessage.MessageID, 10)
	}
	thread := ""
	if item.MessageThreadID > 0 {
		thread = strconv.Itoa(item.MessageThreadID)
	}
	return chat.Message{
		Session: chat.Session{
			Platform:     platformName,
			Account:      p.cfg.EffectiveAccount(),
			Conversation: strconv.FormatInt(item.Chat.ID, 10),
			Thread:       thread,
		},
		ID:       strconv.FormatInt(item.MessageID, 10),
		SenderID: strconv.FormatInt(item.From.ID, 10),
		Direct:   direct,
		Text:     text,
		ReplyTo:  replyTo,
	}, true
}

// mentionsSelf 判断正文是否点名本 bot：优先用实体（text_mention 不依赖用户名），
// 再按 @username 文本兜底。
func (p *Platform) mentionsSelf(item message) bool {
	botID, username := p.identity()
	for _, entity := range item.Entities {
		switch entity.Type {
		case "text_mention":
			if entity.User != nil && entity.User.ID == botID {
				return true
			}
		case "mention":
			token := utf16Slice(item.Text, entity.Offset, entity.Length)
			if strings.EqualFold(token, "@"+username) {
				return true
			}
		}
	}
	p.identityMu.RLock()
	pattern := p.mentionPattern
	p.identityMu.RUnlock()
	return pattern != nil && pattern.MatchString(item.Text)
}

// stripMentions 从正文里移除 @botusername，保留其余文本。
func (p *Platform) stripMentions(text string) string {
	p.identityMu.RLock()
	pattern := p.mentionPattern
	p.identityMu.RUnlock()
	if pattern == nil {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(pattern.ReplaceAllString(text, ""))
}

// utf16Slice 按 Telegram 实体的 UTF-16 偏移切出正文片段。
//
// Telegram 的实体偏移单位是 UTF-16 code unit，不是字节也不是 rune：
// 直接按 rune 切会在 emoji 与中文混排时错位。
func utf16Slice(text string, offset, length int) string {
	if offset < 0 || length <= 0 {
		return ""
	}
	units := utf16.Encode([]rune(text))
	if offset >= len(units) {
		return ""
	}
	end := offset + length
	if end > len(units) {
		end = len(units)
	}
	return string(utf16.Decode(units[offset:end]))
}

// jitter 给退避加 ±reconnectJitter 的抖动，避免多实例同一时刻重连。
func jitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	factor := 1 + (rand.Float64()*2-1)*reconnectJitter
	return time.Duration(float64(delay) * factor)
}

// 确保实现满足平台接口与可选的任务投递端口。
var (
	_ platform.Platform     = (*Platform)(nil)
	_ platform.TaskDelivery = (*Platform)(nil)
)
