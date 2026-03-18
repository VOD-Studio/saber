// Package ai 提供与 AI 相关的功能，包括主动聊天触发逻辑。
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

// RateLimiter 管理主动聊天的速率限制。
//
// 它负责：
// - 检查每日消息数量是否超过限制
// - 检查距离上次主动消息的时间间隔
// - 提供 CanSpeak 方法判断是否允许发送消息
type RateLimiter struct {
	config       *config.ProactiveConfig
	stateTracker *StateTracker
}

// NewRateLimiter 创建并返回一个新的速率限制器实例。
//
// 参数:
//   - cfg: 主动聊天配置（必须非 nil）
//   - stateTracker: 状态跟踪器实例（必须非 nil）
//
// 返回值:
//   - *RateLimiter: 创建的速率限制器
//   - error: 初始化过程中的错误
func NewRateLimiter(cfg *config.ProactiveConfig, stateTracker *StateTracker) (*RateLimiter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("主动聊天配置不能为空")
	}

	if stateTracker == nil {
		return nil, fmt.Errorf("状态跟踪器不能为空")
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("主动聊天配置验证失败：%w", err)
	}

	limiter := &RateLimiter{
		config:       cfg,
		stateTracker: stateTracker,
	}

	slog.Debug("速率限制器初始化完成",
		"max_messages_per_day", cfg.MaxMessagesPerDay,
		"min_interval_minutes", cfg.MinIntervalMinutes)

	return limiter, nil
}

// CanSpeak 检查是否允许向指定房间发送主动消息。
//
// 它检查以下条件：
// 1. 今日已发送消息数 < MaxMessagesPerDay
// 2. 距离上次主动消息的时间 >= MinIntervalMinutes
//
// 参数:
//   - roomID: 房间 ID
//
// 返回值:
//   - bool: true 表示允许发送，false 表示被速率限制
func (rl *RateLimiter) CanSpeak(roomID id.RoomID) bool {
	state := rl.stateTracker.GetState(roomID)

	// 检查每日消息数量限制
	if state.MessagesToday >= rl.config.MaxMessagesPerDay {
		slog.Debug("速率限制：达到每日消息上限",
			"room_id", roomID,
			"messages_today", state.MessagesToday,
			"max_messages_per_day", rl.config.MaxMessagesPerDay)
		return false
	}

	// 检查最小时间间隔
	if !state.LastProactiveTime.IsZero() {
		elapsed := time.Since(state.LastProactiveTime)
		minInterval := time.Duration(rl.config.MinIntervalMinutes) * time.Minute

		if elapsed < minInterval {
			slog.Debug("速率限制：未达到最小时间间隔",
				"room_id", roomID,
				"elapsed_minutes", int(elapsed.Minutes()),
				"min_interval_minutes", rl.config.MinIntervalMinutes)
			return false
		}
	}

	// 通过所有检查，允许发送
	slog.Debug("速率限制检查通过",
		"room_id", roomID,
		"messages_today", state.MessagesToday,
		"last_proactive_time", state.LastProactiveTime)

	return true
}

// GetDailyLimit 返回每日消息限制。
//
// 参数:
//   - none
//
// 返回值:
//   - int: 每日最大消息数
func (rl *RateLimiter) GetDailyLimit() int {
	return rl.config.MaxMessagesPerDay
}

// GetMinInterval 返回最小时间间隔。
//
// 参数:
//   - none
//
// 返回值:
//   - time.Duration: 最小时间间隔
func (rl *RateLimiter) GetMinInterval() time.Duration {
	return time.Duration(rl.config.MinIntervalMinutes) * time.Minute
}

// GetRemainingMessages 返回今日剩余可发送消息数。
//
// 参数:
//   - roomID: 房间 ID
//
// 返回值:
//   - int: 剩余可发送消息数
func (rl *RateLimiter) GetRemainingMessages(roomID id.RoomID) int {
	state := rl.stateTracker.GetState(roomID)

	remaining := rl.config.MaxMessagesPerDay - state.MessagesToday
	if remaining < 0 {
		return 0
	}
	return remaining
}

// TimeUntilNextAllowed 返回距离下次允许发送消息的时间。
//
// 如果已经达到每日限制，返回 0 表示今日无法再发送。
// 如果还未达到限制但时间间隔未满足，返回剩余等待时间。
// 如果已经满足所有条件，返回 0 表示可以立即发送。
//
// 参数:
//   - roomID: 房间 ID
//
// 返回值:
//   - time.Duration: 距离下次允许发送的时间
func (rl *RateLimiter) TimeUntilNextAllowed(roomID id.RoomID) time.Duration {
	state := rl.stateTracker.GetState(roomID)

	// 检查每日限制
	if state.MessagesToday >= rl.config.MaxMessagesPerDay {
		return 0 // 今日已达限制
	}

	// 检查时间间隔
	if !state.LastProactiveTime.IsZero() {
		elapsed := time.Since(state.LastProactiveTime)
		minInterval := time.Duration(rl.config.MinIntervalMinutes) * time.Minute

		if elapsed < minInterval {
			return minInterval - elapsed
		}
	}

	return 0 // 可以立即发送
}

// ScheduleTrigger 实现定时触发器。
//
// 它在配置的时间点触发主动聊天，每天每个时间点只触发一次。
// 时间格式为 "HH:MM"（24 小时制）。
type ScheduleTrigger struct {
	config *config.ScheduleConfig

	// mu 保护 triggeredToday 和 lastTriggerDate
	mu sync.RWMutex

	// triggeredToday 记录今天已触发的时间点
	// key 为 "HH:MM" 格式的时间字符串
	triggeredToday map[string]bool

	// lastTriggerDate 记录上次触发的日期
	// 用于检测日期变化并重置 triggeredToday
	lastTriggerDate time.Time

	// parsedTimes 存储解析后的时间（小时和分钟）
	parsedTimes []scheduleTime
}

// scheduleTime 存储解析后的时间。
type scheduleTime struct {
	hour   int
	minute int
	// original 存储原始时间字符串，用于去重
	original string
}

// NewScheduleTrigger 创建并返回一个新的定时触发器实例。
//
// 参数:
//   - cfg: 定时聊天配置（必须非 nil）
//
// 返回值:
//   - *ScheduleTrigger: 创建的定时触发器
//   - error: 初始化过程中的错误
func NewScheduleTrigger(cfg *config.ScheduleConfig) (*ScheduleTrigger, error) {
	if cfg == nil {
		return nil, fmt.Errorf("定时聊天配置不能为空")
	}

	// 解析配置的时间字符串
	parsedTimes := make([]scheduleTime, 0, len(cfg.Times))
	for _, t := range cfg.Times {
		hour, minute, err := parseTime(t)
		if err != nil {
			return nil, fmt.Errorf("解析时间 %q 失败: %w", t, err)
		}
		parsedTimes = append(parsedTimes, scheduleTime{
			hour:     hour,
			minute:   minute,
			original: t,
		})
	}

	trigger := &ScheduleTrigger{
		config:          cfg,
		triggeredToday:  make(map[string]bool),
		lastTriggerDate: time.Now(),
		parsedTimes:     parsedTimes,
	}

	slog.Debug("定时触发器初始化完成",
		"enabled", cfg.Enabled,
		"times", cfg.Times)

	return trigger, nil
}

// parseTime 解析 "HH:MM" 格式的时间字符串。
//
// 返回值:
//   - int: 小时（0-23）
//   - int: 分钟（0-59）
//   - error: 解析错误
func parseTime(timeStr string) (int, int, error) {
	if len(timeStr) != 5 {
		return 0, 0, fmt.Errorf("时间格式必须为 HH:MM")
	}

	t, err := time.Parse("15:04", timeStr)
	if err != nil {
		return 0, 0, fmt.Errorf("无效的时间格式: %w", err)
	}

	return t.Hour(), t.Minute(), nil
}

// Check 检查当前时间是否匹配配置的定时时间点。
//
// 如果当前时间匹配某个配置的时间点，且今天该时间点尚未触发，
// 则返回 true 并标记为已触发。
//
// 参数:
//   - ctx: 上下文（用于未来扩展，目前未使用）
//
// 返回值:
//   - bool: true 表示应该触发，false 表示不触发
func (t *ScheduleTrigger) Check(ctx context.Context) bool {
	if !t.config.Enabled {
		return false
	}

	now := time.Now()
	currentHour := now.Hour()
	currentMinute := now.Minute()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	t.mu.Lock()
	defer t.mu.Unlock()

	// 检查日期是否变化，如果是则重置触发记录
	if t.lastTriggerDate.Before(today) {
		t.triggeredToday = make(map[string]bool)
		t.lastTriggerDate = today
	}

	// 检查当前时间是否匹配配置的时间点
	for _, pt := range t.parsedTimes {
		if pt.hour == currentHour && pt.minute == currentMinute {
			// 检查今天是否已触发该时间点
			if !t.triggeredToday[pt.original] {
				t.triggeredToday[pt.original] = true
				return true
			}
		}
	}

	return false
}

// Reset 重置触发器的每日状态。
//
// 此方法主要用于测试，正常情况下 Check 方法会自动处理日期变化。
func (t *ScheduleTrigger) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.triggeredToday = make(map[string]bool)
	t.lastTriggerDate = time.Now()
}

// GetScheduledTimes 返回配置的定时时间点列表。
//
// 返回值是原始时间字符串的副本，调用者可以安全修改。
func (t *ScheduleTrigger) GetScheduledTimes() []string {
	times := make([]string, len(t.parsedTimes))
	for i, pt := range t.parsedTimes {
		times[i] = pt.original
	}
	return times
}

// IsTriggeredToday 检查指定时间点今天是否已触发。
//
// 参数:
//   - timeStr: 时间字符串（格式："HH:MM"）
//
// 返回值:
//   - bool: true 表示今天已触发
func (t *ScheduleTrigger) IsTriggeredToday(timeStr string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.triggeredToday[timeStr]
}

// RoomLister 定义获取已加入房间的接口。
//
// 该接口用于解耦 SilenceTrigger 与具体的 RoomService 实现，便于测试。
type RoomLister interface {
	GetJoinedRooms(ctx context.Context) ([]matrix.RoomInfo, error)
}

// SilentRoom 表示一个处于静默状态的房间。
type SilentRoom struct {
	// RoomID 是房间的唯一标识符。
	RoomID id.RoomID
	// SilentDuration 是房间已静默的时长。
	SilentDuration time.Duration
	// LastMessageTime 是最后一次用户消息的时间。
	LastMessageTime time.Time
}

// SilenceTrigger 检测处于静默状态的房间。
//
// 它负责：
// - 获取机器人已加入的所有房间
// - 检查每个房间的最后消息时间
// - 计算静默时长并判断是否超过阈值
// - 返回需要触发主动消息的房间列表
type SilenceTrigger struct {
	config       *config.SilenceConfig
	stateTracker *StateTracker
	roomLister   RoomLister
}

// NewSilenceTrigger 创建并返回一个新的静默触发器实例。
//
// 参数:
//   - cfg: 静默检测配置（必须非 nil）
//   - stateTracker: 状态跟踪器实例（必须非 nil）
//   - roomLister: 房间列表获取接口（必须非 nil）
//
// 返回值:
//   - *SilenceTrigger: 创建的静默触发器
//   - error: 初始化过程中的错误
func NewSilenceTrigger(
	cfg *config.SilenceConfig,
	stateTracker *StateTracker,
	roomLister RoomLister,
) (*SilenceTrigger, error) {
	if cfg == nil {
		return nil, fmt.Errorf("静默检测配置不能为空")
	}

	if stateTracker == nil {
		return nil, fmt.Errorf("状态跟踪器不能为空")
	}

	if roomLister == nil {
		return nil, fmt.Errorf("房间列表获取接口不能为空")
	}

	trigger := &SilenceTrigger{
		config:       cfg,
		stateTracker: stateTracker,
		roomLister:   roomLister,
	}

	slog.Debug("静默触发器初始化完成",
		"enabled", cfg.Enabled,
		"threshold_minutes", cfg.ThresholdMinutes,
		"check_interval_minutes", cfg.CheckIntervalMinutes)

	return trigger, nil
}

// Check 检查所有已加入的房间，返回处于静默状态的房间列表。
//
// 它执行以下步骤：
// 1. 获取机器人已加入的所有房间
// 2. 对每个房间，检查最后用户消息时间
// 3. 计算静默时长（time.Since(lastMessageTime)）
// 4. 如果静默时长超过配置的阈值，将该房间加入返回列表
//
// 参数:
//   - ctx: 上下文，用于取消操作
//
// 返回值:
//   - []SilentRoom: 处于静默状态的房间列表
//   - error: 检查过程中的错误
func (t *SilenceTrigger) Check(ctx context.Context) ([]SilentRoom, error) {
	rooms, err := t.roomLister.GetJoinedRooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取已加入房间失败：%w", err)
	}

	slog.Debug("开始静默检测", "room_count", len(rooms))

	threshold := time.Duration(t.config.ThresholdMinutes) * time.Minute
	now := time.Now()
	var silentRooms []SilentRoom

	for _, room := range rooms {
		// 检查上下文是否已取消
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("静默检测被取消：%w", ctx.Err())
		default:
		}

		// 获取房间状态
		state := t.stateTracker.GetState(room.ID)

		// 计算静默时长
		var silentDuration time.Duration
		if state.LastMessageTime.IsZero() {
			// 如果从未有用户消息，视为静默时间为最大值（使用当前时间）
			// 这样可以在房间刚加入时触发欢迎消息
			silentDuration = threshold + 1
		} else {
			silentDuration = now.Sub(state.LastMessageTime)
		}

		// 检查是否超过阈值
		if silentDuration >= threshold {
			silentRooms = append(silentRooms, SilentRoom{
				RoomID:          room.ID,
				SilentDuration:  silentDuration,
				LastMessageTime: state.LastMessageTime,
			})

			slog.Debug("检测到静默房间",
				"room_id", room.ID,
				"silent_duration", silentDuration.Round(time.Minute),
				"threshold", threshold)
		}
	}

	slog.Debug("静默检测完成",
		"total_rooms", len(rooms),
		"silent_rooms", len(silentRooms))

	return silentRooms, nil
}

// IsEnabled 返回静默检测是否已启用。
func (t *SilenceTrigger) IsEnabled() bool {
	return t.config.Enabled
}

// GetThreshold 返回静默阈值时长。
func (t *SilenceTrigger) GetThreshold() time.Duration {
	return time.Duration(t.config.ThresholdMinutes) * time.Minute
}

// GetCheckInterval 返回检查间隔时长。
func (t *SilenceTrigger) GetCheckInterval() time.Duration {
	return time.Duration(t.config.CheckIntervalMinutes) * time.Minute
}
