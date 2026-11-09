// Package slackplatform 是 Slack 接入端：入站走 Socket Mode（WebSocket），
// 出站走 Web API（chat.postMessage / chat.update）。
//
// Socket Mode 用 app-level token 建立连接，不需要公网回调地址；事件必须先 ack
// envelope，否则 Slack 会重投并在超时后断开连接。
package slackplatform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/platform"
)

// platformName 是本平台在 chat.Session.Platform 与配置键里的标识。
const platformName = "slack"

const (
	// initialReconnectDelay 与 maxReconnectDelay 是 Socket Mode 重连退避的两端。
	initialReconnectDelay = time.Second
	maxReconnectDelay     = time.Minute
	// reconnectJitter 给退避加 ±20% 抖动，避免多实例同一时刻齐刷刷重连。
	reconnectJitter = 0.2
	// healthySession 是「连接稳定过」的判定门槛，跑满后再断开则退避重置。
	healthySession = time.Minute
	// inboundWorkers 与 inboundQueueSize 限定并发回答数量，避免长回答堵死事件读取。
	inboundWorkers   = 4
	inboundQueueSize = 64
)

// Platform 以 bot 身份接入 Slack 私聊与频道。
type Platform struct {
	cfg      config.SlackConfig
	api      *client
	adapter  *Adapter
	throttle *editThrottle
	self     *selfMessages

	// handler 是共享聊天链路，由 Start 注入。
	handler chat.Handler
	// queue 是入站消息的有界缓冲，worker 从中取消息处理。
	queue chan slackEvent
	// cancel 结束 Socket Mode 连接与 worker，Stop 使用。
	cancel context.CancelFunc
	// wg 等在途请求收尾，保证 Stop 之后不再有平台调用。
	wg sync.WaitGroup
	// startMu 保护 cancel 的读写，使重复 Start 与并发 Stop 都有确定语义。
	startMu sync.Mutex

	identityMu  sync.RWMutex
	botUserID   string
	mentionExpr *regexp.Regexp
}

// New 绑定 Slack 配置。构造只准备客户端与 adapter，不发起任何请求。
func New(cfg *config.Config) *Platform {
	platform := &Platform{}
	if cfg == nil {
		return platform
	}
	platform.cfg = cfg.Platforms.Slack
	interval := platform.cfg.EditIntervalMs
	if interval <= 0 {
		interval = config.DefaultSlackConfig().EditIntervalMs
	}
	platform.throttle = &editThrottle{interval: time.Duration(interval) * time.Millisecond}
	platform.self = newSelfMessages()
	platform.api = newClient(platform.cfg)
	platform.adapter = newAdapter(platform.api, platform.cfg.EffectiveAccount(), platform.throttle, platform.self)
	return platform
}

// Name 返回平台标识，与 chat.Session.Platform 和配置键一致。
func (p *Platform) Name() string { return platformName }

// Start 校验配置并在后台建立 Socket Mode 连接。重复 Start 是幂等的。
func (p *Platform) Start(ctx context.Context, handler chat.Handler) error {
	if p.api == nil {
		return errors.New("slack 平台缺少配置")
	}
	if err := p.cfg.Validate(); err != nil {
		return err
	}
	if handler == nil {
		return errors.New("slack 平台缺少消息处理入口")
	}
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if p.cancel != nil {
		slog.Debug("slack 平台已在运行，忽略重复启动")
		return nil
	}
	p.handler = handler
	p.queue = make(chan slackEvent, inboundQueueSize)
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.wg.Add(1 + inboundWorkers)
	go p.supervise(runCtx)
	for range inboundWorkers {
		go p.work(runCtx)
	}
	slog.Info("slack 平台已启动", "account", p.adapter.account, "api_base", p.cfg.APIBase)
	return nil
}

// Stop 断开 Socket Mode 并等待在途回答收尾。未启动或已停止时是空操作，可重复调用。
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

// supervise 维持「取身份—连接—读取—退避重连」循环，直到 ctx 结束。
func (p *Platform) supervise(ctx context.Context) {
	defer p.wg.Done()
	delay := initialReconnectDelay
	for ctx.Err() == nil {
		if err := p.ensureIdentity(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("获取 slack bot 身份失败，稍后重试", "error", err, "retry_in", delay.String())
			delay = p.sleep(ctx, delay)
			continue
		}
		started := time.Now()
		err := p.runSocket(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= healthySession {
			delay = initialReconnectDelay
		}
		if err != nil {
			slog.Warn("slack socket mode 断开，稍后重连", "error", err, "retry_in", delay.String())
		}
		delay = p.sleep(ctx, delay)
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

// runSocket 建立一次 Socket Mode 会话并读到断开为止。
func (p *Platform) runSocket(ctx context.Context) error {
	endpoint, err := p.api.connectionsOpen(ctx)
	if err != nil {
		return fmt.Errorf("打开 slack socket mode 连接失败: %w", err)
	}
	config, err := websocket.NewConfig(endpoint, "https://slack.com")
	if err != nil {
		return fmt.Errorf("构造 slack socket 连接配置失败: %w", err)
	}
	// x/net 的默认拨号没有超时；DNS 或 TCP 挂起会卡住整个重连循环。
	config.Dialer = &net.Dialer{Timeout: 15 * time.Second}
	conn, err := websocket.DialConfig(config)
	if err != nil {
		return fmt.Errorf("连接 slack socket mode 失败: %w", err)
	}
	// 每次连接单独派生 ctx：连接断开重连时这个监听 goroutine 能及时退出，
	// 而不是一直挂到平台整体停止。
	connCtx, closeConn := context.WithCancel(ctx)
	defer closeConn()
	// ctx 结束时主动关闭连接，让阻塞中的 Receive 立刻返回。
	go func() {
		<-connCtx.Done()
		if closeErr := conn.Close(); closeErr != nil {
			slog.Debug("关闭 slack socket 连接失败", "error", closeErr)
		}
	}()
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			slog.Debug("关闭 slack socket 连接失败", "error", closeErr)
		}
	}()

	var writeMu sync.Mutex
	ack := func(envelopeID string) error {
		if envelopeID == "" {
			return nil
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		return websocket.JSON.Send(conn, map[string]any{"envelope_id": envelopeID})
	}
	for {
		var envelope socketEnvelope
		if err := websocket.JSON.Receive(conn, &envelope); err != nil {
			return fmt.Errorf("读取 slack socket 事件失败: %w", err)
		}
		switch envelope.Type {
		case "hello":
			slog.Debug("slack socket mode 握手完成")
		case "disconnect":
			return fmt.Errorf("slack socket mode 被服务端断开: %s", envelope.Reason)
		case "events_api":
			// ack 必须在处理之前发出：先回答 Slack 再入队，处理慢不会触发重投。
			if err := ack(envelope.EnvelopeID); err != nil {
				return fmt.Errorf("确认 slack 事件失败: %w", err)
			}
			select {
			case p.queue <- envelope.Payload.Event:
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			if err := ack(envelope.EnvelopeID); err != nil {
				return fmt.Errorf("确认 slack 事件失败: %w", err)
			}
			slog.Debug("忽略未知 slack socket 事件", "type", envelope.Type)
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
func (p *Platform) handle(ctx context.Context, item slackEvent) {
	normalized, ok := p.normalize(item)
	if !ok {
		return
	}
	attachments, err := p.receiveMedia(ctx, item)
	if err != nil {
		slog.Warn("slack 媒体读取失败", "message", normalized.ID, "error", err)
		return
	}
	normalized.Attachments = attachments
	ctx = chat.WithIdentity(ctx, chat.Identity{Session: normalized.Session, SenderID: normalized.SenderID, Direct: normalized.Direct})
	slog.Info("slack 消息触发 AI 回复",
		"conversation", normalized.Session.Conversation,
		"sender", normalized.SenderID,
		"message", normalized.ID)
	if _, err := p.handler(ctx, normalized, p.adapter); err != nil {
		slog.Warn("slack 消息处理失败",
			"conversation", normalized.Session.Conversation,
			"message", normalized.ID,
			"error", err)
	}
}

// ensureIdentity 确保已取到 bot 的 user_id：@ 检测与自回声过滤都依赖它。
func (p *Platform) ensureIdentity(ctx context.Context) error {
	if p.botUserIDValue() != "" {
		return nil
	}
	identity, err := p.api.authTest(ctx)
	if err != nil {
		return err
	}
	if identity.UserID == "" {
		return errors.New("slack auth.test 未返回 user_id")
	}
	p.setIdentity(identity.UserID)
	slog.Info("已获取 slack bot 身份", "user", identity.User, "team", identity.Team)
	return nil
}

// setIdentity 缓存 bot 的 user_id 并编译提及匹配器。
func (p *Platform) setIdentity(userID string) {
	p.identityMu.Lock()
	defer p.identityMu.Unlock()
	p.botUserID = userID
	p.mentionExpr = regexp.MustCompile(`<@` + regexp.QuoteMeta(userID) + `(?:\|[^>]*)?>`)
}

// botUserIDValue 返回已缓存的 bot user_id。
func (p *Platform) botUserIDValue() string {
	p.identityMu.RLock()
	defer p.identityMu.RUnlock()
	return p.botUserID
}

// normalize 把 Slack 消息事件翻译为 chat.Message，并按配置判定是否应当回答。
func (p *Platform) normalize(item slackEvent) (chat.Message, bool) {
	botID := p.botUserIDValue()
	if botID == "" {
		slog.Debug("slack bot 身份未知，暂不处理消息", "ts", item.TS)
		return chat.Message{}, false
	}
	if item.Type != "" && item.Type != "message" && item.Type != "app_mention" {
		slog.Debug("跳过非 message 的 slack 事件", "type", item.Type)
		return chat.Message{}, false
	}
	if item.Subtype != "" && item.Subtype != "file_share" {
		// message_changed/message_deleted/bot_message 等系统事件不是新提问。
		slog.Debug("跳过 slack 消息子类型", "subtype", item.Subtype, "ts", item.TS)
		return chat.Message{}, false
	}
	if item.BotID != "" || item.User == "" || item.User == botID {
		slog.Debug("跳过 slack bot 自身或其他机器人的消息", "ts", item.TS)
		return chat.Message{}, false
	}
	if item.Edited != nil {
		slog.Debug("跳过 slack 编辑事件", "ts", item.TS)
		return chat.Message{}, false
	}
	if item.Channel == "" || (strings.TrimSpace(item.Text) == "" && len(item.Files) == 0) {
		slog.Debug("跳过 slack 空正文或缺少频道的消息", "ts", item.TS)
		return chat.Message{}, false
	}
	direct := item.ChannelType == "im" || (item.ChannelType == "" && strings.HasPrefix(item.Channel, "D"))
	replyToBot := item.ThreadTS != "" && p.self.has(item.ThreadTS)
	if direct {
		if !p.cfg.DirectChatAutoReply {
			slog.Debug("slack 私聊自动回复已关闭", "ts", item.TS)
			return chat.Message{}, false
		}
	} else {
		if !p.cfg.GroupChatMentionReply {
			slog.Debug("slack 频道提及回复已关闭", "ts", item.TS)
			return chat.Message{}, false
		}
		if !p.mentionsSelf(item.Text) && !replyToBot {
			slog.Debug("slack 频道消息未点名或回复本 bot", "ts", item.TS)
			return chat.Message{}, false
		}
	}
	text := p.stripMentions(item.Text)
	if strings.TrimSpace(text) == "" && len(item.Files) == 0 {
		return chat.Message{}, false
	}
	return chat.Message{
		Session: chat.Session{
			Platform:     platformName,
			Account:      p.cfg.EffectiveAccount(),
			Conversation: item.Channel,
			Thread:       item.ThreadTS,
		},
		ID:       item.TS,
		SenderID: item.User,
		Direct:   direct,
		Text:     text,
		ReplyTo:  item.ThreadTS,
	}, true
}

// mentionsSelf 判断正文是否点名本 bot。
func (p *Platform) mentionsSelf(text string) bool {
	p.identityMu.RLock()
	expr := p.mentionExpr
	p.identityMu.RUnlock()
	return expr != nil && expr.MatchString(text)
}

// stripMentions 从正文里移除对本 bot 的提及，保留其余文本。
func (p *Platform) stripMentions(text string) string {
	p.identityMu.RLock()
	expr := p.mentionExpr
	p.identityMu.RUnlock()
	if expr == nil {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(expr.ReplaceAllString(text, ""))
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
