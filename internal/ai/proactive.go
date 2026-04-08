// Package ai 提供与 AI 相关的功能，包括主动聊天管理。
package ai

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// ProactiveManager 管理 AI 主动聊天功能。
//
// 它负责：
// - 监控房间活动并检测静默时段
// - 根据配置定时触发 AI 消息
// - 欢迎新成员加入
// - 使用决策模型判断何时发送主动消息
type ProactiveManager struct {
	config         *config.ProactiveConfig
	aiService      *Service
	roomService    *matrix.RoomService
	stateTracker   *StateTracker
	triggerCoord   *TriggerCoordinator
	decisionEngine *DecisionEngine
	globalAIConfig *config.AIConfig
	decisionCache  *DecisionCache

	stopChan chan struct{}
	wg       sync.WaitGroup
}

// NewProactiveManager 创建并返回一个新的主动聊天管理器实例。
//
// 参数:
//   - cfg: 主动聊天配置（必须非 nil）
//   - aiService: AI 服务实例（必须非 nil）
//   - roomService: Matrix 房间服务实例（必须非 nil）
//   - stateTracker: 状态跟踪器实例（可选，为 nil 时使用默认值）
//   - globalAIConfig: 全局 AI 配置（必须非 nil）
//
// 返回值:
//   - *ProactiveManager: 创建的主动聊天管理器
//   - error: 初始化过程中的错误
func NewProactiveManager(
	cfg *config.ProactiveConfig,
	aiService *Service,
	roomService *matrix.RoomService,
	stateTracker *StateTracker,
	globalAIConfig *config.AIConfig,
) (*ProactiveManager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("主动聊天配置不能为空")
	}

	if aiService == nil {
		return nil, fmt.Errorf("AI 服务不能为空")
	}

	if roomService == nil {
		return nil, fmt.Errorf("matrix 房间服务不能为空")
	}

	if globalAIConfig == nil {
		return nil, fmt.Errorf("全局 AI 配置不能为空")
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("主动聊天配置验证失败：%w", err)
	}

	// 使用默认状态跟踪器（如果未提供）
	if stateTracker == nil {
		stateTracker = NewStateTracker()
	}

	// 创建触发器
	silenceTrigger, err := NewSilenceTrigger(&cfg.Silence, stateTracker, roomService)
	if err != nil {
		return nil, fmt.Errorf("创建静默触发器失败：%w", err)
	}

	scheduleTrigger, err := NewScheduleTrigger(&cfg.Schedule)
	if err != nil {
		return nil, fmt.Errorf("创建定时触发器失败：%w", err)
	}

	rateLimiter, err := NewRateLimiter(cfg, stateTracker)
	if err != nil {
		return nil, fmt.Errorf("创建速率限制器失败：%w", err)
	}

	// 创建触发协调器
	triggerCoord, err := NewTriggerCoordinator(cfg, silenceTrigger, scheduleTrigger, rateLimiter, stateTracker, roomService)
	if err != nil {
		return nil, fmt.Errorf("创建触发协调器失败：%w", err)
	}

	// 创建决策引擎
	decisionEngine, err := NewDecisionEngine(aiService, &cfg.Decision, globalAIConfig)
	if err != nil {
		return nil, fmt.Errorf("创建决策引擎失败：%w", err)
	}

	// 创建决策缓存（默认 TTL 5 分钟）
	decisionCache := NewDecisionCache(5 * time.Minute)

	manager := &ProactiveManager{
		config:         cfg,
		aiService:      aiService,
		roomService:    roomService,
		stateTracker:   stateTracker,
		triggerCoord:   triggerCoord,
		decisionEngine: decisionEngine,
		globalAIConfig: globalAIConfig,
		decisionCache:  decisionCache,
		stopChan:       make(chan struct{}),
	}

	slog.Info("主动聊天管理器初始化完成",
		"enabled", cfg.Enabled,
		"max_messages_per_day", cfg.MaxMessagesPerDay,
		"min_interval_minutes", cfg.MinIntervalMinutes,
		"silence_enabled", cfg.Silence.Enabled,
		"schedule_enabled", cfg.Schedule.Enabled,
		"new_member_enabled", cfg.NewMember.Enabled)

	return manager, nil
}

// Start 启动主动聊天管理器。
//
// 它启动以下后台 goroutine（如果相应功能已启用）：
// - 静默检测：定期检查长时间无活动的房间
// - 定时聊天：在配置的时间点发送消息
//
// 参数:
//   - ctx: 上下文，用于取消操作
func (m *ProactiveManager) Start(ctx context.Context) {
	if !m.config.Enabled {
		slog.Debug("主动聊天功能未启用，跳过启动")
		return
	}

	slog.Info("启动主动聊天管理器")

	// 如果启用了状态持久化，尝试加载状态
	if m.config.PersistState && m.config.StatePath != "" {
		if err := m.stateTracker.Load(m.config.StatePath); err != nil {
			// 加载失败不阻止启动，仅记录错误
			slog.Warn("加载主动聊天状态失败，使用新状态", "path", m.config.StatePath, "error", err)
		} else {
			slog.Info("已加载主动聊天状态", "path", m.config.StatePath)
		}
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.runBackgroundTasks(ctx)
	}()
}

// Stop 停止主动聊天管理器。
//
// 它会：
// 1. 发送停止信号到所有后台 goroutine
// 2. 等待所有 goroutine 完成清理
// 3. 如果启用了状态持久化，保存状态到文件
//
// 该方法会阻塞直到所有后台任务完全停止。
func (m *ProactiveManager) Stop() {
	slog.Info("停止主动聊天管理器")

	close(m.stopChan)
	m.wg.Wait()

	// 如果启用了状态持久化，保存状态
	if m.config.PersistState && m.config.StatePath != "" {
		if err := m.stateTracker.Save(m.config.StatePath); err != nil {
			// 保存失败不影响关闭流程，仅记录错误
			slog.Error("保存主动聊天状态失败", "path", m.config.StatePath, "error", err)
		} else {
			slog.Info("已保存主动聊天状态", "path", m.config.StatePath)
		}
	}

	slog.Debug("主动聊天管理器已停止")
}

// runBackgroundTasks 运行后台任务循环。
//
// 它监听停止信号并定期执行以下任务：
// - 静默检测（如果启用）
// - 定时聊天（如果启用）
//
// 参数:
//   - ctx: 上下文，用于取消操作
func (m *ProactiveManager) runBackgroundTasks(ctx context.Context) {
	slog.Debug("主动聊天后台任务启动")

	// 创建内部停止通道，用于优雅关闭
	done := make(chan struct{})
	defer close(done)

	// 设置静默检测定时器
	var silenceTicker *time.Ticker
	if m.config.Silence.Enabled {
		interval := time.Duration(m.config.Silence.CheckIntervalMinutes) * time.Minute
		silenceTicker = time.NewTicker(interval)
		defer silenceTicker.Stop()
		slog.Debug("静默检测定时器已启动", "interval", interval)
	}

	// 设置定时检查定时器（每分钟检查一次是否到达配置的时间点）
	scheduleTicker := time.NewTicker(1 * time.Minute)
	defer scheduleTicker.Stop()

	// 创建 silence ticker 的 channel（如果启用）
	var silenceChan <-chan time.Time
	if silenceTicker != nil {
		silenceChan = silenceTicker.C
	}

	for {
		select {
		case <-ctx.Done():
			slog.Debug("主动聊天后台任务因上下文取消而停止", "reason", ctx.Err())
			return
		case <-m.stopChan:
			slog.Debug("主动聊天后台任务因收到停止信号而停止")
			return
		case <-silenceChan:
			m.handleSilenceTrigger(ctx)
		case <-scheduleTicker.C:
			m.handleScheduleTrigger(ctx)
		case <-done:
			return
		}
	}
}

// handleSilenceTrigger 处理静默触发。
//
// 它执行以下步骤：
// 1. 检查静默触发器并获取需要触发的房间列表
// 2. 对每个房间收集决策上下文
// 3. 调用 AI 决策引擎判断是否发送消息
// 4. 如果决定发送，则生成并发送消息
// 5. 更新房间状态
func (m *ProactiveManager) handleSilenceTrigger(ctx context.Context) {
	if !m.config.Silence.Enabled {
		return
	}

	results := m.triggerCoord.checkSilenceTrigger(ctx)
	for _, result := range results {
		if !result.ShouldTrigger {
			slog.Debug("静默触发器触发但被速率限制",
				"room_id", result.RoomID,
				"reason", result.Reason)
			continue
		}

		slog.Info("静默触发器触发",
			"room_id", result.RoomID,
			"reason", result.Reason)

		// 处理单个房间的触发
		if err := m.handleProactiveTrigger(ctx, result.RoomID, TriggerInactivity); err != nil {
			slog.Warn("处理静默触发失败",
				"room_id", result.RoomID,
				"error", err)
		}
	}
}

// handleScheduleTrigger 处理定时触发。
func (m *ProactiveManager) handleScheduleTrigger(ctx context.Context) {
	if !m.config.Schedule.Enabled {
		return
	}

	results := m.triggerCoord.checkScheduleTrigger(ctx)
	for _, result := range results {
		if !result.ShouldTrigger {
			slog.Debug("定时触发器触发但被速率限制",
				"room_id", result.RoomID,
				"reason", result.Reason)
			continue
		}

		slog.Info("定时触发器触发",
			"room_id", result.RoomID,
			"reason", result.Reason)

		if err := m.handleProactiveTrigger(ctx, result.RoomID, TriggerScheduled); err != nil {
			slog.Warn("处理定时触发失败",
				"room_id", result.RoomID,
				"error", err)
		}
	}
}

// handleProactiveTrigger 处理单个房间的主动聊天触发。
//
// 它执行以下步骤：
// 1. 收集房间决策上下文
// 2. 调用 AI 决策引擎判断是否发送消息
// 3. 如果决定发送，则发送消息
// 4. 更新房间状态
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - roomID: 目标房间 ID
//   - triggerType: 触发类型
//
// 返回值:
//   - error: 处理过程中的错误
func (m *ProactiveManager) handleProactiveTrigger(ctx context.Context, roomID id.RoomID, triggerType TriggerType) error {
	logger := slog.With("room_id", roomID, "trigger_type", triggerType)

	// 收集决策上下文
	silenceThreshold := m.config.Silence.ThresholdMinutes
	decisionCtx, err := GatherDecisionContext(ctx, roomID, m.stateTracker, m.roomService, triggerType, silenceThreshold)
	if err != nil {
		return fmt.Errorf("收集决策上下文失败: %w", err)
	}

	logger.Debug("决策上下文收集完成",
		"room_name", decisionCtx.RoomName,
		"activity_level", decisionCtx.ActivityLevel,
		"minutes_since_last", decisionCtx.MinutesSinceLast,
		"silence_threshold", decisionCtx.SilenceThresholdMinutes,
		"messages_today", decisionCtx.MessagesToday)

	// 检查缓存中是否有有效的决策
	if cached, ok := m.decisionCache.Get(roomID, decisionCtx); ok {
		logger.Debug("使用缓存的决策", "should_speak", cached.ShouldSpeak, "reason", cached.Reason)
		if cached.ShouldSpeak && cached.Content != "" {
			if err := m.SendMessage(ctx, roomID, cached.Content, false); err != nil {
				return fmt.Errorf("发送缓存决策消息失败: %w", err)
			}
		}
		return nil
	}

	// 调用决策引擎
	shouldSpeak, content, err := m.decisionEngine.Decide(ctx, decisionCtx)
	if err != nil {
		return fmt.Errorf("AI 决策失败: %w", err)
	}

	// 缓存决策结果
	m.decisionCache.Set(roomID, decisionCtx, &DecisionResponse{
		ShouldSpeak: shouldSpeak,
		Reason:      "AI 决策",
		Content:     content,
	})

	if !shouldSpeak {
		logger.Debug("AI 决定不发送消息", "reason", "AI 判断当前不适合发送消息")
		return nil
	}

	// 如果有内容，直接发送
	if content != "" {
		logger.Info("AI 决定发送主动消息", "content_length", len(content))
		if err := m.SendMessage(ctx, roomID, content, false); err != nil {
			return fmt.Errorf("发送主动消息失败: %w", err)
		}
		return nil
	}

	// 如果没有内容，生成默认消息
	defaultMsg := m.generateDefaultProactiveMessage(triggerType, decisionCtx)
	logger.Info("AI 决定发送消息，使用默认内容")
	if err := m.SendMessage(ctx, roomID, defaultMsg, false); err != nil {
		return fmt.Errorf("发送默认消息失败: %w", err)
	}

	return nil
}

// generateDefaultProactiveMessage 生成默认的主动聊天消息。
//
// 当 AI 决定发送消息但未提供具体内容时使用此方法。
// 根据房间类型（私聊/群聊）使用不同的语气：
//   - 私聊：使用亲密、个人化的语气
//   - 群聊：使用面向群体的语气
//
// 参数:
//   - triggerType: 触发类型
//   - decisionCtx: 决策上下文
//
// 返回值:
//   - string: 生成的默认消息
func (m *ProactiveManager) generateDefaultProactiveMessage(triggerType TriggerType, decisionCtx *DecisionContext) string {
	isDirect := decisionCtx.IsDirect

	switch triggerType {
	case TriggerInactivity:
		if decisionCtx.MinutesSinceLast > 120 {
			if isDirect {
				return "好久没聊天了，最近怎么样？有什么想分享的吗？"
			}
			return "大家好！房间静默有一段时间了，有什么有趣的话题想聊聊吗？"
		}
		if isDirect {
			return "在忙什么呢？有什么新鲜事吗？"
		}
		return "大家好，有什么新鲜事吗？"
	case TriggerScheduled:
		if isDirect {
			return "又是新的一天，希望你今天一切顺利！有什么想聊的吗？"
		}
		return "大家好！又到了定时问候的时间，希望大家一切顺利！"
	case TriggerNewUser:
		return "欢迎新朋友加入！"
	default:
		if isDirect {
			return "你好！"
		}
		return "大家好！"
	}
}

// OnNewMember 处理新成员加入房间事件。
//
// 当检测到新成员加入时，该方法会：
// 1. 检查新成员欢迎功能是否启用
// 2. 检查房间是否满足发送条件（频率限制）
// 3. 获取房间类型（私聊/群聊）
// 4. 生成欢迎消息并发送
// 5. 更新房间的主动消息状态
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - roomID: 新成员加入的房间 ID
//   - userID: 新加入成员的用户 ID
//
// 返回值:
//   - error: 处理过程中发生的错误
func (m *ProactiveManager) OnNewMember(ctx context.Context, roomID id.RoomID, userID id.UserID) error {
	logger := slog.With("room_id", roomID, "user_id", userID, "trigger", "new_member")

	// 检查主动聊天功能是否启用
	if !m.config.Enabled {
		logger.Debug("主动聊天功能未启用，跳过新成员欢迎")
		return nil
	}

	// 检查新成员欢迎功能是否启用
	if !m.config.NewMember.Enabled {
		logger.Debug("新成员欢迎功能未启用")
		return nil
	}

	// 检查频率限制
	if !m.canSendMessage(roomID) {
		logger.Debug("新成员欢迎因频率限制被跳过")
		return nil
	}

	logger.Info("处理新成员欢迎")

	// 获取房间信息以判断房间类型
	roomInfo, err := m.roomService.GetRoomInfo(ctx, roomID.String())
	if err != nil {
		logger.Debug("获取房间信息失败，使用默认群聊语气", "error", err)
		roomInfo = &matrix.RoomInfo{ID: roomID, MemberCount: 0}
	}

	// 判断是否为私聊房间（成员数为2）
	isDirect := roomInfo.MemberCount == 2
	logger.Debug("房间类型判断", "is_direct", isDirect, "member_count", roomInfo.MemberCount)

	// 生成欢迎消息
	welcomeMsg, err := m.generateWelcomeMessage(ctx, userID, isDirect)
	if err != nil {
		return fmt.Errorf("生成欢迎消息失败: %w", err)
	}

	// 发送欢迎消息
	if err := m.sendWelcomeMessage(ctx, roomID, welcomeMsg); err != nil {
		return fmt.Errorf("发送欢迎消息失败: %w", err)
	}

	// 记录主动消息
	m.stateTracker.RecordProactiveMessage(roomID)
	logger.Info("新成员欢迎消息已发送")

	return nil
}

// canSendMessage 检查是否可以向指定房间发送主动消息。
//
// 它会检查：
// - 每日最大消息数限制
// - 最小消息间隔限制
//
// 参数:
//   - roomID: 要检查的房间 ID
//
// 返回值:
//   - bool: 如果可以发送消息则返回 true
func (m *ProactiveManager) canSendMessage(roomID id.RoomID) bool {
	state := m.stateTracker.GetState(roomID)

	// 检查每日消息上限
	if m.config.MaxMessagesPerDay > 0 && state.MessagesToday >= m.config.MaxMessagesPerDay {
		slog.Debug("已达到每日消息上限",
			"room_id", roomID,
			"messages_today", state.MessagesToday,
			"max_per_day", m.config.MaxMessagesPerDay)
		return false
	}

	// 检查最小间隔
	if m.config.MinIntervalMinutes > 0 && !state.LastProactiveTime.IsZero() {
		minInterval := time.Duration(m.config.MinIntervalMinutes) * time.Minute
		elapsed := time.Since(state.LastProactiveTime)
		if elapsed < minInterval {
			slog.Debug("距离上次主动消息时间过短",
				"room_id", roomID,
				"elapsed", elapsed,
				"min_interval", minInterval)
			return false
		}
	}

	return true
}

// generateWelcomeMessage 生成新成员欢迎消息。
//
// 它使用 AI 服务根据配置的欢迎提示词生成个性化欢迎消息。
// 根据房间类型（私聊/群聊）使用不同的语气：
//   - 私聊：使用亲密、个人化的语气
//   - 群聊：使用面向群体的语气
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - userID: 新成员的用户 ID
//   - isDirect: 是否为私聊房间
//
// 返回值:
//   - string: 生成的欢迎消息
//   - error: 生成过程中发生的错误
func (m *ProactiveManager) generateWelcomeMessage(ctx context.Context, userID id.UserID, isDirect bool) (string, error) {
	// 构建欢迎提示词
	welcomePrompt := m.config.NewMember.WelcomePrompt
	if welcomePrompt == "" {
		welcomePrompt = "欢迎新成员加入"
	}

	// 如果 AI 服务未启用，返回简单欢迎消息
	if !m.aiService.IsEnabled() {
		if isDirect {
			return fmt.Sprintf("很高兴认识你，%s！", userID), nil
		}
		return fmt.Sprintf("欢迎 %s 加入！", userID), nil
	}

	// 根据房间类型调整系统提示词
	var roomTypeHint string
	if isDirect {
		roomTypeHint = "这是私聊场景，请使用亲密、个人化的语气，像朋友一样交谈。"
	} else {
		roomTypeHint = "这是群聊场景，请使用面向群体的语气。"
	}

	// 构建系统提示词
	systemPrompt := fmt.Sprintf("你是一个友好的聊天机器人。%s。%s 请用简短友好的方式回复，不要超过两句话。", welcomePrompt, roomTypeHint)

	// 构建用户消息
	userMsg := fmt.Sprintf("新成员 %s 刚刚加入了这个房间。请生成一条简短的欢迎消息。", userID)

	// 使用 AI 生成欢迎消息
	response, err := m.aiService.GenerateSimpleResponse(ctx, systemPrompt, userMsg)
	if err != nil {
		slog.Warn("AI 生成欢迎消息失败，使用默认消息", "error", err)
		if isDirect {
			return fmt.Sprintf("很高兴认识你，%s！", userID), nil
		}
		return fmt.Sprintf("欢迎 %s 加入！", userID), nil
	}

	return response, nil
}

// sendWelcomeMessage 发送欢迎消息到指定房间。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - roomID: 目标房间 ID
//   - message: 要发送的消息内容
//
// 返回值:
//   - error: 发送过程中发生的错误
func (m *ProactiveManager) sendWelcomeMessage(ctx context.Context, roomID id.RoomID, message string) error {
	_, err := m.roomService.SendMessage(ctx, roomID.String(), message)
	if err != nil {
		return fmt.Errorf("发送消息到房间 %s 失败：%w", roomID, err)
	}
	return nil
}

// SendMessage 向指定房间发送主动消息。
//
// 该方法用于发送 AI 生成的主动聊天消息，支持文本和通知两种消息类型。
// 发送成功后会自动更新状态跟踪器中的消息计数和时间戳。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - roomID: 目标房间 ID
//   - message: 要发送的消息内容
//   - isNotice: 是否为通知消息（true 为通知，false 为普通文本）
//
// 返回值:
//   - error: 发送过程中发生的错误
func (m *ProactiveManager) SendMessage(ctx context.Context, roomID id.RoomID, message string, isNotice bool) error {
	logger := slog.With("room_id", roomID, "message_type", map[bool]string{true: "notice", false: "text"}[isNotice])

	if message == "" {
		return fmt.Errorf("消息内容不能为空")
	}

	logger.Debug("准备发送主动消息")

	// 根据消息类型选择发送方法
	var eventID id.EventID
	var err error
	if isNotice {
		eventID, err = m.roomService.SendNotice(ctx, roomID.String(), message)
	} else {
		eventID, err = m.roomService.SendMessage(ctx, roomID.String(), message)
	}

	if err != nil {
		return fmt.Errorf("发送主动消息到房间 %s 失败：%w", roomID, err)
	}

	// 发送成功后更新状态跟踪器
	m.stateTracker.RecordProactiveMessage(roomID)

	logger.Info("主动消息发送成功", "event_id", eventID)
	return nil
}

// RecordUserMessage 记录用户发送的消息。
//
// 此方法用于更新房间的最后消息时间，以供静默检测使用。
// 当 Matrix 消息处理器收到用户消息时，应调用此方法。
//
// 参数:
//   - roomID: 用户发送消息的房间 ID
func (m *ProactiveManager) RecordUserMessage(roomID id.RoomID) {
	if m.stateTracker == nil {
		return
	}
	m.stateTracker.RecordUserMessage(roomID)
}

// TriggerCoordinator 协调所有主动聊天触发器。
//
// 它负责：
// - 统一管理静默触发器、定时触发器和速率限制器
// - 定期检查所有触发器状态
// - 应用速率限制 before 触发任何动作
// - 记录触发事件日志
type TriggerCoordinator struct {
	config          *config.ProactiveConfig
	silenceTrigger  *SilenceTrigger
	scheduleTrigger *ScheduleTrigger
	rateLimiter     *RateLimiter
	stateTracker    *StateTracker
	roomLister      RoomLister // 新增：用于获取房间列表
}

// NewTriggerCoordinator 创建并返回一个新的触发协调器实例。
//
// 参数:
//   - cfg: 主动聊天配置（必须非 nil）
//   - silenceTrigger: 静默触发器（必须非 nil）
//   - scheduleTrigger: 定时触发器（必须非 nil）
//   - rateLimiter: 速率限制器（必须非 nil）
//   - stateTracker: 状态跟踪器（必须非 nil）
//   - roomLister: 房间列表获取接口（必须非 nil）
//
// 返回值:
//   - *TriggerCoordinator: 创建的触发协调器
//   - error: 初始化过程中的错误
func NewTriggerCoordinator(
	cfg *config.ProactiveConfig,
	silenceTrigger *SilenceTrigger,
	scheduleTrigger *ScheduleTrigger,
	rateLimiter *RateLimiter,
	stateTracker *StateTracker,
	roomLister RoomLister, // 新增参数
) (*TriggerCoordinator, error) {
	if cfg == nil {
		return nil, fmt.Errorf("主动聊天配置不能为空")
	}

	if silenceTrigger == nil {
		return nil, fmt.Errorf("静默触发器不能为空")
	}

	if scheduleTrigger == nil {
		return nil, fmt.Errorf("定时触发器不能为空")
	}

	if rateLimiter == nil {
		return nil, fmt.Errorf("速率限制器不能为空")
	}

	if stateTracker == nil {
		return nil, fmt.Errorf("状态跟踪器不能为空")
	}

	if roomLister == nil {
		return nil, fmt.Errorf("房间列表获取接口不能为空")
	}

	coordinator := &TriggerCoordinator{
		config:          cfg,
		silenceTrigger:  silenceTrigger,
		scheduleTrigger: scheduleTrigger,
		rateLimiter:     rateLimiter,
		stateTracker:    stateTracker,
		roomLister:      roomLister, // 新增
	}

	slog.Debug("触发协调器初始化完成",
		"silence_enabled", cfg.Silence.Enabled,
		"schedule_enabled", cfg.Schedule.Enabled,
		"new_member_enabled", cfg.NewMember.Enabled)

	return coordinator, nil
}

// TriggerResult 表示触发器检查的结果。
type TriggerResult struct {
	// TriggerType 表示触发类型（"silence", "schedule", "new_member"）
	TriggerType string
	// RoomID 是触发消息的房间 ID
	RoomID id.RoomID
	// ShouldTrigger 表示是否应该触发
	ShouldTrigger bool
	// Reason 描述触发原因或跳过原因
	Reason string
}

// CheckAndTrigger 检查所有触发器并返回需要触发的房间列表。
//
// 它执行以下步骤：
// 1. 检查静默触发器（如果启用）
// 2. 检查定时触发器（如果启用）
// 3. 对每个需要触发的房间应用速率限制
// 4. 返回通过速率限制的房间列表
//
// 参数:
//   - ctx: 上下文，用于取消操作
//
// 返回值:
//   - []TriggerResult: 触发结果列表
func (tc *TriggerCoordinator) CheckAndTrigger(ctx context.Context) []TriggerResult {
	results := []TriggerResult{}

	// 检查静默触发
	if tc.config.Silence.Enabled {
		silenceResults := tc.checkSilenceTrigger(ctx)
		results = append(results, silenceResults...)
	}

	// 检查定时触发
	if tc.config.Schedule.Enabled {
		scheduleResults := tc.checkScheduleTrigger(ctx)
		results = append(results, scheduleResults...)
	}

	return results
}

// checkSilenceTrigger 检查静默触发器并应用速率限制。
func (tc *TriggerCoordinator) checkSilenceTrigger(ctx context.Context) []TriggerResult {
	results := []TriggerResult{}

	silentRooms, err := tc.silenceTrigger.Check(ctx)
	if err != nil {
		slog.Error("静默触发器检查失败", "error", err)
		return results
	}

	for _, room := range silentRooms {
		result := TriggerResult{
			TriggerType: "silence",
			RoomID:      room.RoomID,
		}

		// 应用速率限制
		if !tc.rateLimiter.CanSpeak(room.RoomID) {
			result.ShouldTrigger = false
			result.Reason = "被速率限制阻止"
			results = append(results, result)
			continue
		}

		result.ShouldTrigger = true
		result.Reason = fmt.Sprintf("静默时长 %v 超过阈值 %v",
			room.SilentDuration.Round(time.Minute),
			tc.silenceTrigger.GetThreshold())
		results = append(results, result)
	}

	return results
}

// checkScheduleTrigger 检查定时触发器并应用速率限制。
func (tc *TriggerCoordinator) checkScheduleTrigger(ctx context.Context) []TriggerResult {
	results := []TriggerResult{}

	// 检查定时触发器是否应该触发
	if !tc.scheduleTrigger.Check(ctx) {
		return results
	}

	// 获取所有已加入的房间
	rooms, err := tc.roomLister.GetJoinedRooms(ctx)
	if err != nil {
		slog.Error("获取已加入房间失败", "error", err)
		return results
	}

	slog.Debug("定时触发器触发，检查房间列表",
		"room_count", len(rooms),
		"scheduled_times", tc.scheduleTrigger.GetScheduledTimes())

	for _, room := range rooms {
		result := TriggerResult{
			TriggerType: "schedule",
			RoomID:      room.ID,
		}

		// 应用速率限制
		if !tc.rateLimiter.CanSpeak(room.ID) {
			result.ShouldTrigger = false
			result.Reason = "被速率限制阻止"
			results = append(results, result)
			continue
		}

		result.ShouldTrigger = true
		result.Reason = "定时触发"
		results = append(results, result)
	}

	return results
}

// GetSilenceTrigger 返回静默触发器。
func (tc *TriggerCoordinator) GetSilenceTrigger() *SilenceTrigger {
	return tc.silenceTrigger
}

// GetScheduleTrigger 返回定时触发器。
func (tc *TriggerCoordinator) GetScheduleTrigger() *ScheduleTrigger {
	return tc.scheduleTrigger
}

// GetRateLimiter 返回速率限制器。
func (tc *TriggerCoordinator) GetRateLimiter() *RateLimiter {
	return tc.rateLimiter
}
