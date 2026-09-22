// Package violetplatform 是 Violet 博客平台的聊天接入端：以 Bot API 的 bot 虚拟用户身份
// 订阅站内聊天事件，把消息规范化为 chat.Message 交给共享聊天链路，并实现出站 chat.Adapter。
//
// 与 Matrix 接入端的差别集中在三处：入站是 SSE 事件流而非 sync 循环、断线恢复靠消息历史
// 而不是事件补发、以及不接管 ai 的聊天命令入口（!ai/!task 依赖单例且带 Matrix 类型签名的
// ChatEntrypoint，因此 violet 只走「私聊直答 + 群聊被 @ 触发」这条最小链路）。
package violetplatform

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/platform"
)

// 事件流与补拉的节奏常量。取值理由写在各自的用法处。
const (
	// initialReconnectDelay 与 maxReconnectDelay 是重连退避的两端：Violet 重启通常在
	// 秒级完成，退避到 1 分钟封顶既不会打爆对端，也不会在长时间故障时白烧 CPU。
	initialReconnectDelay = time.Second
	maxReconnectDelay     = time.Minute
	// reconnectJitter 给退避加 ±20% 抖动，避免多实例同一时刻齐刷刷重连。
	reconnectJitter = 0.2
	// inboundWorkers 与 inboundQueueSize 限定并发回答数量：一条长回答会占住一个 worker，
	// 队列满时 SSE 读循环会背压等待，而不是把消息丢在内存里无限堆高。
	// 并发处理意味着同一会话内多条消息的回答顺序不保证——与 Matrix 的多事件并发一致，
	// 代价是偶尔两条回答换个先后，换来的是长回答不会让机器人对其他消息「已读不回」。
	inboundWorkers     = 4
	inboundQueueSize   = 64
	backfillPageSize   = 50
	maxBackfillPages   = 2
	aiCommandPrefix    = "!ai"
	inboundMessageType = "text"
)

// inbound 是一条待处理的入站消息与其所属会话。
//
// 会话 ID 单独带：SSE 信封里的 conversation_id 与消息体的 conversation_id 同源，
// 但历史接口的响应只保证消息体带会话 ID，两者取其一才能既不漏也不猜。
type inbound struct {
	// conversationID 是平台原生会话标识。
	conversationID string
	// message 是 Violet 消息快照。
	message messageDTO
}

// Platform 以 bot 身份接入 Violet 站内聊天。
type Platform struct {
	violet   config.VioletConfig
	api      *client
	adapter  *Adapter
	throttle *editThrottle
	states   *conversationStates
	seen     *seenMessages

	// handler 是共享聊天链路，由 Start 注入。
	handler chat.Handler
	// queue 是入站消息的有界缓冲，worker 从中取消息处理。
	queue chan inbound
	// cancel 结束事件流与 worker，Stop 使用。
	cancel context.CancelFunc
	// wg 等在途请求收尾，保证 Stop 之后不再有平台调用。
	wg sync.WaitGroup
	// connected 记录本次进程是否已建立过事件流：首次连接只打基线，之后才补拉。
	connected atomic.Bool
	// startMu 保护 cancel 的读写，使重复 Start 与并发 Stop 都有确定语义。
	startMu sync.Mutex

	identityMu sync.RWMutex
	identity   botProfileDTO
}

// New 绑定 Violet 配置。构造只准备客户端与 adapter，不发起任何请求，
// 因此配置非法时错误留到 Start 才报，装配阶段可以先注册再启动。
func New(cfg *config.Config) *Platform {
	platformValue := &Platform{
		states: newConversationStates(defaultStateCapacity),
		seen:   newSeenMessages(defaultSeenCapacity),
	}
	if cfg == nil {
		return platformValue
	}
	platformValue.violet = cfg.Platforms.Violet
	interval := platformValue.violet.EditIntervalMs
	if interval <= 0 {
		interval = config.DefaultVioletConfig().EditIntervalMs
	}
	platformValue.throttle = &editThrottle{interval: time.Duration(interval) * time.Millisecond}
	platformValue.api = newClient(platformValue.violet.Endpoint, platformValue.violet.BotToken, platformValue.violet.HTTPTimeoutSeconds)
	platformValue.adapter = newAdapter(platformValue.api, platformValue.violet.EffectiveAccount(), platformValue.throttle)
	return platformValue
}

// Name 返回平台标识，与 chat.Session.Platform 和配置键一致。
func (p *Platform) Name() string { return platformName }

// Start 校验配置并接管事件流：连接、重连、补拉与投递都在后台 goroutine 里，
// 只有配置不合法或缺少处理入口才同步报错——那属于「怎么重试都不会好」的问题。
func (p *Platform) Start(ctx context.Context, handler chat.Handler) error {
	if p.api == nil {
		return errors.New("violet 平台缺少配置")
	}
	if err := p.violet.Validate(); err != nil {
		return err
	}
	if handler == nil {
		return errors.New("violet 平台缺少消息处理入口")
	}
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if p.cancel != nil {
		// 重复启动会留下两份读循环与两组 worker，而 p.cancel 只剩最后一份可取消：
		// 前一组 goroutine 会永久阻塞在旧队列上，因此这里直接拒绝。
		return errors.New("violet 平台已启动，重复 Start 被拒绝")
	}
	p.handler = handler
	p.queue = make(chan inbound, inboundQueueSize)
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.wg.Add(1 + inboundWorkers)
	go p.supervise(runCtx)
	for worker := 0; worker < inboundWorkers; worker++ {
		go p.work(runCtx)
	}
	slog.Info("violet 平台已启动", "endpoint", p.violet.Endpoint, "account", p.adapter.account)
	return nil
}

// Stop 断开事件流并等待在途回答收尾。未启动或已停止时是空操作，可重复调用。
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

// running 报告平台当前是否处于已启动状态。
func (p *Platform) running() bool {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	return p.cancel != nil
}

// DeliveryAdapter 返回一次性发送的出站 adapter，用于任务与定时计划的结果投递。
//
// 它复用同一个 adapter 实例：出站本身无状态，编辑节流则按平台全局共享，
// 任务投递与即时回复一起计入同一份配额才守得住服务端限流。
func (p *Platform) DeliveryAdapter() chat.Adapter {
	if p.adapter == nil {
		return nil
	}
	return p.adapter
}

// supervise 维持「连接—读取—退避重连」这条循环，直到 ctx 结束。
func (p *Platform) supervise(ctx context.Context) {
	defer p.wg.Done()
	delay := initialReconnectDelay
	for {
		connected, err := p.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			delay = initialReconnectDelay // 能连上说明服务活着，退避重新从短起步
		}
		if err != nil {
			slog.Warn("violet 事件流中断，准备重连", "error", err, "retry_in", delay.String())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(delay)):
		}
		if delay *= 2; delay > maxReconnectDelay {
			delay = maxReconnectDelay
		}
	}
}

// connectOnce 建立一次事件流。返回的第一值表示是否成功打开过流，
// 供 supervise 决定退避是否归零。
func (p *Platform) connectOnce(ctx context.Context) (bool, error) {
	if err := p.ensureIdentity(ctx); err != nil {
		return false, err
	}
	firstConnect := !p.connected.Swap(true)
	if firstConnect {
		// 首次连接给已知会话打水位基线：没有这一步，重连补拉会把站内历史当新消息回答。
		p.warmConversations(ctx)
	} else {
		p.backfill(ctx)
	}

	activity := &sseActivity{}
	activity.touch() // 建立连接前算作刚有活动，避免首帧未到就被看门狗掐断
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	go watchIdle(streamCtx, activity, cancelStream)

	body, err := p.api.openEvents(streamCtx)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := body.Close(); closeErr != nil {
			slog.Warn("关闭 violet 事件流失败", "error", closeErr)
		}
	}()
	return true, readSSEFrames(bufio.NewReader(body), activity, func(frame sseFrame) error {
		return p.handleFrame(streamCtx, frame)
	})
}

// ensureIdentity 确保已取到 bot 的虚拟用户身份。
//
// 自回声过滤与「被 @ 的是不是我」都拿它比对，身份未知时宁可什么都不做。
func (p *Platform) ensureIdentity(ctx context.Context) error {
	if p.botUserID() != "" {
		return nil
	}
	profile, err := p.api.profile(ctx)
	if err != nil {
		return fmt.Errorf("获取 violet bot 身份失败: %w", err)
	}
	if profile.UserID == "" {
		return errors.New("violet bot 身份缺少 user_id")
	}
	p.setIdentity(profile)
	slog.Info("已获取 violet bot 身份", "bot", profile.Name, "username", profile.Username)
	return nil
}

// handleFrame 处理一帧 SSE 事件。解析失败只记日志并继续读：坏一帧就断流，
// 会让一条异常数据变成永久性的消息中断。
func (p *Platform) handleFrame(ctx context.Context, frame sseFrame) error {
	name := frame.name
	var event eventFrame
	if err := json.Unmarshal([]byte(frame.data), &event); err != nil {
		slog.Warn("解析 violet 事件失败", "event", name, "error", err)
		return nil
	}
	if name == "" {
		name = event.Type
	}
	switch name {
	case eventMessageCreated:
		conversationID := event.Data.ConversationID
		if conversationID == "" {
			conversationID = event.Data.Message.ConversationID
		}
		if conversationID == "" {
			slog.Warn("violet 消息事件缺少会话标识，已忽略", "message", event.Data.Message.ID)
			return nil
		}
		p.enqueue(ctx, conversationID, event.Data.Message)
	case eventTypingUpdated:
		// 他人的输入状态不影响是否回答，也不需要在出站侧反馈，显式忽略。
	default:
		slog.Debug("忽略未知 violet 事件", "event", name)
	}
	return nil
}

// enqueue 把消息交给处理队列，返回是否入队。
//
// 队列满时这里会背压等待而不是丢弃：慢消费者应该拖慢读取，
// 悄悄丢消息只会让用户看见「已读不回」。
func (p *Platform) enqueue(ctx context.Context, conversationID string, message messageDTO) bool {
	if message.ConversationID == "" {
		message.ConversationID = conversationID
	}
	if !p.admit(conversationID, message) {
		return false
	}
	select {
	case p.queue <- inbound{conversationID: conversationID, message: message}:
		return true
	case <-ctx.Done():
		return false
	}
}

// admit 是入站事件与断线补拉共用的闸门：先去重，再推进水位。
//
// 顺序不能反：先记账后判重会让重放的消息既被计数又被丢掉。
func (p *Platform) admit(conversationID string, message messageDTO) bool {
	if message.ID == "" {
		return false
	}
	if !p.seen.add(message.ID) {
		slog.Debug("重复的 violet 消息事件已跳过", "message", message.ID)
		return false
	}
	if created, ok := parseTimestamp(message.CreatedAt); ok {
		p.states.get(conversationID).noteSeen(created)
	}
	return true
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
func (p *Platform) handle(ctx context.Context, item inbound) {
	message, ok := p.normalizeMessage(ctx, item)
	if !ok {
		return
	}
	slog.Info("violet 消息触发 AI 回复", "conversation", message.Session.Conversation, "sender", message.SenderID, "message", message.ID)
	result, err := p.handler(ctx, message, p.adapter)
	if err != nil {
		slog.Warn("violet 消息处理失败", "conversation", message.Session.Conversation, "message", message.ID, "error", err)
		return
	}
	slog.Debug("violet 消息处理完成", "message", message.ID, "status", string(result.Status), "rounds", len(result.Rounds))
}

// normalizeMessage 把 Violet 消息快照翻译为 chat.Message，并按配置判定是否应当回答。
func (p *Platform) normalizeMessage(ctx context.Context, item inbound) (chat.Message, bool) {
	message := item.message
	botUserID := p.botUserID()
	switch {
	case botUserID == "":
		slog.Debug("violet bot 身份未知，暂不处理消息", "message", message.ID)
		return chat.Message{}, false
	case message.IsDeleted:
		slog.Debug("violet 消息已被删除，跳过", "message", message.ID)
		return chat.Message{}, false
	case message.Type != "" && message.Type != inboundMessageType:
		// Bot API 只开放文本：图片消息既没有上传通道也不该被当成问题回答。
		slog.Debug("跳过非文本 violet 消息", "message", message.ID, "type", message.Type)
		return chat.Message{}, false
	case message.Sender.ID == botUserID:
		// 服务端已不向 bot 投递 bot 的消息，这里再挡一道，防的是协议放宽或自身回声。
		slog.Debug("跳过 violet 自回声", "message", message.ID)
		return chat.Message{}, false
	case message.Sender.ID == "":
		slog.Warn("violet 消息缺少发送者标识，跳过", "message", message.ID)
		return chat.Message{}, false
	}

	// 提及判定看原文（token 还在），交给模型的正文看剥离后。
	mentionsSelf := p.mentionsSelf(message.Content)
	if allowed, reason := p.shouldAnswer(ctx, item.conversationID, mentionsSelf); !allowed {
		slog.Debug("violet 消息未触发回复", "conversation", item.conversationID, "message", message.ID, "reason", reason)
		return chat.Message{}, false
	}
	if mentionResidue(message.Content) == "" {
		slog.Debug("violet 消息只有点名没有内容，跳过", "message", message.ID)
		return chat.Message{}, false
	}
	text, ok := commandText(stripMentions(message.Content))
	if !ok {
		slog.Debug("violet 侧不支持该命令，已跳过", "message", message.ID)
		return chat.Message{}, false
	}
	if text == "" {
		slog.Debug("violet 消息剥离后为空，跳过", "message", message.ID)
		return chat.Message{}, false
	}

	replyTo := ""
	if message.ReplyTo != nil {
		replyTo = message.ReplyTo.ID
	}
	return chat.Message{
		Session: chat.Session{
			Platform:     platformName,
			Account:      p.adapter.account,
			Conversation: item.conversationID,
		},
		ID:       message.ID,
		SenderID: message.Sender.ID,
		Text:     text,
		ReplyTo:  replyTo,
	}, true
}

// shouldAnswer 按会话形态与配置开关决定是否回答。
//
// 未知形态一律按群聊从严处理：宁可漏答一条私聊，也不要在没被 @ 的房间里插话。
func (p *Platform) shouldAnswer(ctx context.Context, conversationID string, mentionsSelf bool) (bool, string) {
	kind, err := p.conversationKind(ctx, conversationID)
	if err != nil {
		slog.Debug("查询 violet 会话形态失败", "conversation", conversationID, "error", err)
	}
	switch kind {
	case kindDirect:
		if !p.violet.DirectChatAutoReply {
			return false, "私聊自动回复已关闭"
		}
		return true, ""
	default:
		if !p.violet.GroupChatMentionReply {
			return false, "群聊提及回复已关闭"
		}
		if mentionsSelf {
			return true, ""
		}
		return false, "群聊消息未点名本 bot"
	}
}

// conversationKind 读取会话形态，带 TTL 缓存。
//
// 形态判定几乎不变但每次入站都要用，不缓存就会把消息处理压在一次 HTTP 往返上；
// 缓存过期后查询失败仍回退旧值，网络抖动不该让机器人开始误判房间。
func (p *Platform) conversationKind(ctx context.Context, conversationID string) (string, error) {
	memory := p.states.get(conversationID)
	now := p.states.now()
	if kind, fresh := memory.cachedKind(now); fresh {
		return kind, nil
	}
	conversation, err := p.api.conversation(ctx, conversationID)
	if err != nil {
		if stored := memory.storedKind(); stored != "" {
			return stored, nil
		}
		return "", err
	}
	if conversation.Kind == "" {
		if stored := memory.storedKind(); stored != "" {
			return stored, nil
		}
		return "", fmt.Errorf("violet 会话 %s 未返回形态", conversationID)
	}
	memory.rememberKind(conversation.Kind, now.Add(conversationKindTTL))
	return conversation.Kind, nil
}

// mentionsSelf 判断正文是否点名本 bot，用户名与虚拟用户 ID 任一命中即可。
func (p *Platform) mentionsSelf(content string) bool {
	identity := p.currentIdentity()
	return mentionsUser(content, identity.Username, identity.UserID)
}

// warmConversations 为已知会话打水位基线并顺带预热会话形态缓存。
//
// 拉不到列表不算故障：新会话的消息到达时会各自识别形态并就地打基线。
func (p *Platform) warmConversations(ctx context.Context) {
	conversations, err := p.api.conversations(ctx)
	if err != nil {
		slog.Warn("预热 violet 会话列表失败，将在消息到达时逐个识别会话形态", "error", err)
		return
	}
	now := p.states.now()
	for _, conversation := range conversations {
		if conversation.ID == "" {
			continue
		}
		memory := p.states.get(conversation.ID)
		if conversation.Kind != "" {
			memory.rememberKind(conversation.Kind, now.Add(conversationKindTTL))
		}
		memory.since(now) // 只用它的副作用：首次调用即把基线水位设为当前时刻
	}
}

// backfill 在重连后按会话补齐断线期间漏掉的消息。
//
// Violet 的 bot 事件流不写 id: 行、不支持 Last-Event-ID 补发，重拉消息历史是唯一
// 的恢复通道；按时间正序入队，模型才不会被倒着回答。
func (p *Platform) backfill(ctx context.Context) {
	for _, conversationID := range p.states.known() {
		if ctx.Err() != nil {
			return
		}
		memory := p.states.get(conversationID)
		since, baselined := memory.since(p.states.now())
		if !baselined {
			continue // 只打过基线的新会话没有缺口要补
		}
		messages := p.pullSince(ctx, conversationID, since)
		for index := len(messages) - 1; index >= 0; index-- {
			if !p.enqueue(ctx, conversationID, messages[index]) {
				continue
			}
			slog.Info("补拉 violet 断线期间的消息", "conversation", conversationID, "message", messages[index].ID)
		}
	}
}

// pullSince 从新到旧翻页收集晚于 since 的消息，最多翻 maxBackfillPages 页。
//
// 上限是有意为之：停机很久之后一次性把几百条旧消息灌给模型，等于用历史把自己刷爆。
func (p *Platform) pullSince(ctx context.Context, conversationID string, since time.Time) []messageDTO {
	var collected []messageDTO
	cursor := ""
	for page := 0; page < maxBackfillPages; page++ {
		messages, next, hasMore, err := p.api.messages(ctx, conversationID, cursor, backfillPageSize)
		if err != nil {
			slog.Warn("补拉 violet 会话历史失败", "conversation", conversationID, "error", err)
			return collected
		}
		reachedWatermark := false
		for _, message := range messages {
			created, parsed := parseTimestamp(message.CreatedAt)
			if parsed && created.Before(since) {
				reachedWatermark = true
				continue
			}
			collected = append(collected, message)
		}
		if reachedWatermark || !hasMore || next == "" {
			return collected
		}
		cursor = next
	}
	slog.Warn("violet 断线期间消息过多，仅补拉最近部分", "conversation", conversationID, "pages", maxBackfillPages)
	return collected
}

// commandText 处理命令前缀。Violet 没有聊天命令入口，只认 !ai：
// 其余 !xxx 命令（!task、!schedule、上下文命令等）在本平台无落点，跳过而不是
// 把命令原文当问题丢给模型。
func commandText(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	switch {
	case trimmed == "":
		return "", true
	case strings.HasPrefix(trimmed, aiCommandPrefix):
		rest := trimmed[len(aiCommandPrefix):]
		// 前缀后必须是空白才算命令：!ai-switch 这类别名命令在 violet 没有落点，
		// 把 "-switch gpt" 当正文回答只会答非所问。
		if rest != "" && !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t") {
			return "", false
		}
		body := strings.TrimSpace(rest)
		if body == "" {
			return "", false // 光秃秃的 !ai 没有提问内容
		}
		return body, true
	case strings.HasPrefix(trimmed, "!"):
		return "", false
	default:
		return trimmed, true
	}
}

// botUserID 返回本 bot 的虚拟用户 ID，未取到身份时为空串。
func (p *Platform) botUserID() string {
	return p.currentIdentity().UserID
}

// currentIdentity 读取 bot 身份快照。
func (p *Platform) currentIdentity() botProfileDTO {
	p.identityMu.RLock()
	defer p.identityMu.RUnlock()
	return p.identity
}

// setIdentity 记录 bot 身份。
func (p *Platform) setIdentity(profile botProfileDTO) {
	p.identityMu.Lock()
	defer p.identityMu.Unlock()
	p.identity = profile
}

// jitter 给退避时长加 ±reconnectJitter 的随机抖动，避免多实例同一时刻齐刷刷重连。
//
// 抖动只用于打散节奏，不需要密码学强度，因此用 math/rand/v2 的全局源。
func jitter(base time.Duration) time.Duration {
	if base <= 0 {
		return base
	}
	spread := float64(base) * reconnectJitter
	return base + time.Duration(rand.Float64()*2*spread-spread)
}

// 确保实现满足平台接口与可选的任务投递端口。
var (
	_ platform.Platform     = (*Platform)(nil)
	_ platform.TaskDelivery = (*Platform)(nil)
)
