// Package discordplatform 是 Discord Bot 接入端：出站走 REST，入站走 Gateway WebSocket。
//
// 用 Gateway 而不是 HTTP 回调：不需要公网地址，也不需要 slash command 注册；
// 断线按 1s..60s 指数退避重建会话，单条消息处理失败不影响后续事件。
package discordplatform

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/platform"
)

// platformName 是本平台在 chat.Session.Platform 与配置键里的标识。
const platformName = "discord"

const (
	// initialReconnectDelay 与 maxReconnectDelay 是 Gateway 重连退避的两端。
	initialReconnectDelay = time.Second
	maxReconnectDelay     = time.Minute
	// reconnectJitter 给退避加 ±20% 抖动，避免多实例同一时刻齐刷刷重连。
	reconnectJitter = 0.2
	// healthySession 是「连接稳定过」的判定门槛：跑满这个时长后再断开，
	// 退避从 1s 重新起步，而不是继承上一次故障的长退避。
	healthySession = time.Minute
	// inboundWorkers 与 inboundQueueSize 限定并发回答数量，避免长回答堵死事件读取。
	inboundWorkers   = 4
	inboundQueueSize = 64
)

// messageTypeDefault 与 messageTypeReply 是允许进入聊天链路的两种消息类型；
// 其余类型是加入、置顶等系统事件。
const (
	messageTypeDefault = 0
	messageTypeReply   = 19
)

// Platform 以 bot 身份接入 Discord 私聊与服务器频道。
type Platform struct {
	cfg      config.DiscordConfig
	api      *client
	adapter  *Adapter
	throttle *editThrottle

	// handler 是共享聊天链路，由 Start 注入。
	handler chat.Handler
	// queue 是入站消息的有界缓冲，worker 从中取消息处理。
	queue chan gatewayMessage
	// cancel 结束 Gateway 连接与 worker，Stop 使用。
	cancel context.CancelFunc
	// wg 等在途请求收尾，保证 Stop 之后不再有平台调用。
	wg sync.WaitGroup
	// startMu 保护 cancel 的读写，使重复 Start 与并发 Stop 都有确定语义。
	startMu sync.Mutex

	identityMu  sync.RWMutex
	botID       string
	botUsername string
	// mentionPattern 匹配 <@id> 或 <@!id>，取得身份后编译一次。
	mentionPattern *regexp.Regexp
}

// New 绑定 Discord 配置。构造只准备客户端与 adapter，不发起任何请求。
func New(cfg *config.Config) *Platform {
	platform := &Platform{}
	if cfg == nil {
		return platform
	}
	platform.cfg = cfg.Platforms.Discord
	interval := platform.cfg.EditIntervalMs
	if interval <= 0 {
		interval = config.DefaultDiscordConfig().EditIntervalMs
	}
	platform.throttle = &editThrottle{interval: time.Duration(interval) * time.Millisecond}
	platform.api = newClient(platform.cfg)
	platform.adapter = newAdapter(platform.api, platform.cfg.EffectiveAccount(), platform.throttle)
	return platform
}

// Name 返回平台标识，与 chat.Session.Platform 和配置键一致。
func (p *Platform) Name() string { return platformName }

// Start 校验配置并在后台建立 Gateway 会话。重复 Start 是幂等的。
func (p *Platform) Start(ctx context.Context, handler chat.Handler) error {
	if p.api == nil {
		return errors.New("discord 平台缺少配置")
	}
	if err := p.cfg.Validate(); err != nil {
		return err
	}
	if handler == nil {
		return errors.New("discord 平台缺少消息处理入口")
	}
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if p.cancel != nil {
		slog.Debug("discord 平台已在运行，忽略重复启动")
		return nil
	}
	p.handler = handler
	p.queue = make(chan gatewayMessage, inboundQueueSize)
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.wg.Add(1 + inboundWorkers)
	go p.supervise(runCtx)
	for range inboundWorkers {
		go p.work(runCtx)
	}
	slog.Info("discord 平台已启动", "account", p.adapter.account, "api_base", p.cfg.APIBase)
	return nil
}

// Stop 断开 Gateway 并等待在途回答收尾。未启动或已停止时是空操作，可重复调用。
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

// supervise 维持「连接—读取—退避重连」循环，直到 ctx 结束。
func (p *Platform) supervise(ctx context.Context) {
	defer p.wg.Done()
	delay := initialReconnectDelay
	for ctx.Err() == nil {
		started := time.Now()
		err := p.runGateway(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= healthySession {
			delay = initialReconnectDelay
		}
		if err != nil {
			slog.Warn("discord gateway 断开，稍后重连", "error", err, "retry_in", delay.String())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(delay)):
		}
		if next := delay * 2; next < maxReconnectDelay {
			delay = next
		} else {
			delay = maxReconnectDelay
		}
	}
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
func (p *Platform) handle(ctx context.Context, item gatewayMessage) {
	normalized, ok := p.normalize(item)
	if !ok {
		return
	}
	slog.Info("discord 消息触发 AI 回复",
		"conversation", normalized.Session.Conversation,
		"sender", normalized.SenderID,
		"message", normalized.ID)
	if _, err := p.handler(ctx, normalized, p.adapter); err != nil {
		slog.Warn("discord 消息处理失败",
			"conversation", normalized.Session.Conversation,
			"message", normalized.ID,
			"error", err)
	}
}

// setBotUser 缓存 READY 里的机器人身份并编译提及匹配器。
func (p *Platform) setBotUser(id, username string) {
	p.identityMu.Lock()
	defer p.identityMu.Unlock()
	p.botID = id
	p.botUsername = username
	p.mentionPattern = regexp.MustCompile(`<@!?` + regexp.QuoteMeta(id) + `>`)
}

// botUserID 返回已缓存的机器人用户 ID。
func (p *Platform) botUserID() string {
	p.identityMu.RLock()
	defer p.identityMu.RUnlock()
	return p.botID
}

// normalize 把 Discord 消息翻译为 chat.Message，并按配置判定是否应当回答。
func (p *Platform) normalize(item gatewayMessage) (chat.Message, bool) {
	botID := p.botUserID()
	if botID == "" {
		slog.Debug("discord bot 身份未知，暂不处理消息", "message", item.ID)
		return chat.Message{}, false
	}
	if item.Author.ID == "" {
		slog.Debug("discord 消息缺少发送者，跳过", "message", item.ID)
		return chat.Message{}, false
	}
	if item.Author.ID == botID || item.Author.Bot {
		slog.Debug("跳过 discord bot 自身或其他机器人的消息", "message", item.ID)
		return chat.Message{}, false
	}
	if item.ChannelID == "" {
		slog.Debug("discord 消息缺少频道标识，跳过", "message", item.ID)
		return chat.Message{}, false
	}
	if item.Type != messageTypeDefault && item.Type != messageTypeReply {
		slog.Debug("跳过 discord 系统事件消息", "message", item.ID, "type", item.Type)
		return chat.Message{}, false
	}
	if strings.TrimSpace(item.Content) == "" {
		// 目前不实现图片/文件能力，纯附件消息没有可交给模型的正文。
		slog.Debug("跳过 discord 非文本消息", "message", item.ID)
		return chat.Message{}, false
	}
	direct := item.GuildID == ""
	replyToBot := item.ReferencedMessage != nil && item.ReferencedMessage.Author.ID == botID
	if direct {
		if !p.cfg.DirectChatAutoReply {
			slog.Debug("discord 私聊自动回复已关闭", "message", item.ID)
			return chat.Message{}, false
		}
	} else {
		if !p.cfg.GroupChatMentionReply {
			slog.Debug("discord 频道提及回复已关闭", "message", item.ID)
			return chat.Message{}, false
		}
		if !p.mentionsSelf(item) && !replyToBot {
			slog.Debug("discord 频道消息未点名或回复本 bot", "message", item.ID)
			return chat.Message{}, false
		}
	}
	text := stripMentionTokens(item.Content, botID)
	if strings.TrimSpace(text) == "" {
		slog.Debug("discord 消息剥离提及后为空，跳过", "message", item.ID)
		return chat.Message{}, false
	}
	replyTo := ""
	if item.MessageReference != nil {
		replyTo = item.MessageReference.MessageID
	}
	return chat.Message{
		Session: chat.Session{
			Platform:     platformName,
			Account:      p.cfg.EffectiveAccount(),
			Conversation: item.ChannelID,
		},
		ID:       item.ID,
		SenderID: item.Author.ID,
		Direct:   direct,
		Text:     text,
		ReplyTo:  replyTo,
	}, true
}

// mentionsSelf 判断消息是否点名本 bot：mentions 列表与正文 token 任一命中即可。
func (p *Platform) mentionsSelf(item gatewayMessage) bool {
	botID := p.botUserID()
	for _, mention := range item.Mentions {
		if mention.ID == botID {
			return true
		}
	}
	p.identityMu.RLock()
	pattern := p.mentionPattern
	p.identityMu.RUnlock()
	return pattern != nil && pattern.MatchString(item.Content)
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
