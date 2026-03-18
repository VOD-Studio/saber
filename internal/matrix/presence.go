// Package matrix 提供 Matrix 客户端功能，包括在线状态跟踪、
// 输入指示器、已读回执和自动重连支持。
package matrix

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// PresenceState 表示用户的在线状态。
type PresenceState string

const (
	// PresenceOnline 表示用户在线。
	PresenceOnline PresenceState = "online"
	// PresenceOffline 表示用户离线。
	PresenceOffline PresenceState = "offline"
	// PresenceUnavailable 表示用户不可用（离开）。
	PresenceUnavailable PresenceState = "unavailable"
)

// PresenceInfo 包含用户的在线状态信息。
type PresenceInfo struct {
	UserID          id.UserID
	Presence        PresenceState
	StatusMsg       string
	LastActiveAgo   time.Duration
	CurrentlyActive bool
}

// ReconnectConfig 保存自动重连的配置。
type ReconnectConfig struct {
	// MaxRetries 是最大重连尝试次数。
	// 设置为 0 表示无限重试（不推荐）。
	MaxRetries int

	// InitialDelay 是首次重试前的初始退避延迟。
	InitialDelay time.Duration

	// MaxDelay 是重试之间的最大退避延迟。
	MaxDelay time.Duration

	// Multiplier 是指数退避乘数。
	// delay = min(initialDelay * multiplier^attempt, maxDelay)
	Multiplier float64
}

// DefaultReconnectConfig 返回带有合理默认值的 ReconnectConfig。
func DefaultReconnectConfig() *ReconnectConfig {
	return &ReconnectConfig{
		MaxRetries:   10,
		InitialDelay: time.Second,
		MaxDelay:     5 * time.Minute,
		Multiplier:   2.0,
	}
}

// PresenceEventHandler 是处理 Matrix 事件的回调函数类型。
type PresenceEventHandler func(ctx context.Context, evt *event.Event)

// SessionSaver 是断开连接时保存会话状态的回调函数类型。
type SessionSaver func(path string) error

// PresenceService 提供在线状态跟踪、输入指示器和自动重连功能。
type PresenceService struct {
	client        *mautrix.Client
	reconnectCfg  *ReconnectConfig
	sessionSaver  SessionSaver
	sessionPath   string
	lastPresence  PresenceState
	lastStatusMsg string
}

// NewPresenceService 使用给定的 Matrix 客户端创建一个新的 PresenceService。
func NewPresenceService(client *mautrix.Client) *PresenceService {
	return &PresenceService{
		client:       client,
		reconnectCfg: DefaultReconnectConfig(),
	}
}

// SetReconnectConfig 设置自定义的重连配置。
func (p *PresenceService) SetReconnectConfig(cfg *ReconnectConfig) {
	p.reconnectCfg = cfg
}

// SetSessionSaver 设置会话保存回调和路径以进行会话持久化。
func (p *PresenceService) SetSessionSaver(saver SessionSaver, path string) {
	p.sessionSaver = saver
	p.sessionPath = path
}

// SetPresence 设置用户的在线状态和可选的状态消息。
func (p *PresenceService) SetPresence(state PresenceState, statusMsg string) error {
	ctx := context.Background()
	return p.SetPresenceWithContext(ctx, state, statusMsg)
}

// SetPresenceWithContext 设置用户的在线状态，支持上下文。
func (p *PresenceService) SetPresenceWithContext(ctx context.Context, state PresenceState, statusMsg string) error {
	slog.Info("Setting presence state", "presence", string(state), "status_msg", statusMsg)

	req := mautrix.ReqPresence{
		Presence:  event.Presence(state),
		StatusMsg: statusMsg,
	}

	err := p.client.SetPresence(ctx, req)
	if err != nil {
		slog.Error("Failed to set presence", "presence", string(state), "error", err)
		return fmt.Errorf("failed to set presence: %w", err)
	}

	p.lastPresence = state
	p.lastStatusMsg = statusMsg

	slog.Info("Presence state updated successfully", "presence", string(state))

	return nil
}

// GetPresence 检索给定用户的在线信息。
func (p *PresenceService) GetPresence(userID string) (*PresenceInfo, error) {
	ctx := context.Background()
	return p.GetPresenceWithContext(ctx, userID)
}

// GetPresenceWithContext 检索给定用户的在线信息，支持上下文。
func (p *PresenceService) GetPresenceWithContext(ctx context.Context, userID string) (*PresenceInfo, error) {
	slog.Debug("Getting presence for user", "user_id", userID)

	resp, err := p.client.GetPresence(ctx, id.UserID(userID))
	if err != nil {
		slog.Error("Failed to get presence", "user_id", userID, "error", err)
		return nil, fmt.Errorf("failed to get presence for user %s: %w", userID, err)
	}

	info := &PresenceInfo{
		UserID:          id.UserID(userID),
		Presence:        PresenceState(resp.Presence),
		StatusMsg:       resp.StatusMsg,
		LastActiveAgo:   time.Duration(resp.LastActiveAgo) * time.Millisecond,
		CurrentlyActive: resp.CurrentlyActive,
	}

	slog.Debug("Retrieved presence info", "user_id", userID, "presence", string(info.Presence), "currently_active", info.CurrentlyActive)

	return info, nil
}

// StartTyping 向房间发送输入指示器，超时时间以毫秒为单位。
// timeout 参数以毫秒为单位（默认 30000ms = 30 秒）。
func (p *PresenceService) StartTyping(roomID string, timeout int) error {
	ctx := context.Background()
	return p.StartTypingWithContext(ctx, roomID, time.Duration(timeout)*time.Millisecond)
}

// StartTypingWithContext 发送输入指示器，支持上下文。
func (p *PresenceService) StartTypingWithContext(ctx context.Context, roomID string, timeout time.Duration) error {
	slog.Debug("Starting typing indicator", "room_id", roomID, "timeout", timeout)

	_, err := p.client.UserTyping(ctx, id.RoomID(roomID), true, timeout)
	if err != nil {
		slog.Error("Failed to start typing indicator", "room_id", roomID, "error", err)
		return fmt.Errorf("failed to start typing in room %s: %w", roomID, err)
	}

	slog.Debug("Typing indicator started", "room_id", roomID)

	return nil
}

// StopTyping 停止房间中的输入指示器。
func (p *PresenceService) StopTyping(roomID string) error {
	ctx := context.Background()
	return p.StopTypingWithContext(ctx, roomID)
}

// StopTypingWithContext 停止输入指示器，支持上下文。
func (p *PresenceService) StopTypingWithContext(ctx context.Context, roomID string) error {
	slog.Debug("Stopping typing indicator", "room_id", roomID)

	_, err := p.client.UserTyping(ctx, id.RoomID(roomID), false, 0)
	if err != nil {
		slog.Error("Failed to stop typing indicator", "room_id", roomID, "error", err)
		return fmt.Errorf("failed to stop typing in room %s: %w", roomID, err)
	}

	slog.Debug("Typing indicator stopped", "room_id", roomID)

	return nil
}

// MarkAsRead 为房间中的特定事件发送已读回执。
func (p *PresenceService) MarkAsRead(roomID string, eventID string) error {
	ctx := context.Background()
	return p.MarkAsReadWithContext(ctx, roomID, eventID)
}

// MarkAsReadWithContext 发送已读回执，支持上下文。
func (p *PresenceService) MarkAsReadWithContext(ctx context.Context, roomID string, eventID string) error {
	slog.Debug("Marking message as read", "room_id", roomID, "event_id", eventID)

	err := p.client.MarkRead(ctx, id.RoomID(roomID), id.EventID(eventID))
	if err != nil {
		slog.Error("Failed to mark message as read", "room_id", roomID, "event_id", eventID, "error", err)
		return fmt.Errorf("failed to mark message as read in room %s: %w", roomID, err)
	}

	slog.Debug("Message marked as read", "room_id", roomID, "event_id", eventID)

	return nil
}

// SendReceipt 为事件发送特定类型的已读回执。
// 常见的回执类型是 event.ReceiptTypeRead 和 event.ReceiptTypeReadPrivate。
func (p *PresenceService) SendReceipt(roomID string, eventID string, receiptType event.ReceiptType) error {
	ctx := context.Background()
	return p.SendReceiptWithContext(ctx, roomID, eventID, receiptType)
}

// SendReceiptWithContext 发送已读回执，支持上下文。
func (p *PresenceService) SendReceiptWithContext(ctx context.Context, roomID string, eventID string, receiptType event.ReceiptType) error {
	slog.Debug("Sending receipt", "room_id", roomID, "event_id", eventID, "receipt_type", string(receiptType))

	err := p.client.SendReceipt(ctx, id.RoomID(roomID), id.EventID(eventID), receiptType, nil)
	if err != nil {
		slog.Error("Failed to send receipt", "room_id", roomID, "event_id", eventID, "error", err)
		return fmt.Errorf("failed to send receipt in room %s: %w", roomID, err)
	}

	slog.Debug("Receipt sent successfully", "room_id", roomID, "event_id", eventID)

	return nil
}

// calculateBackoff 计算给定重试尝试的退避延迟。
func (p *PresenceService) calculateBackoff(attempt int) time.Duration {
	delay := float64(p.reconnectCfg.InitialDelay)
	delay = delay * math.Pow(p.reconnectCfg.Multiplier, float64(attempt))

	result := min(time.Duration(delay), p.reconnectCfg.MaxDelay)

	return result
}

// saveSessionOnDisconnect 如果配置了保存器，则保存会话状态。
func (p *PresenceService) saveSessionOnDisconnect() {
	if p.sessionSaver == nil || p.sessionPath == "" {
		return
	}

	slog.Info("Saving session on disconnect", "path", p.sessionPath)

	if err := p.sessionSaver(p.sessionPath); err != nil {
		slog.Error("Failed to save session on disconnect", "path", p.sessionPath, "error", err)
	} else {
		slog.Info("Session saved successfully on disconnect")
	}
}

// restorePresence 在重连后恢复之前的在线状态。
func (p *PresenceService) restorePresence() error {
	if p.lastPresence == "" {
		// 没有设置过在线状态，默认为在线
		p.lastPresence = PresenceOnline
	}

	return p.SetPresence(p.lastPresence, p.lastStatusMsg)
}

// StartSyncWithReconnect 开始同步，断开时自动重连。
// 它使用指数退避进行重连尝试。
// 在调用此方法之前，应该为 syncer 配置事件处理器。
//
// 示例：
//
//	syncer := client.Syncer.(*mautrix.DefaultSyncer)
//	syncer.OnEventType(event.EventMessage, handler)
//	err := presence.StartSyncWithReconnect(ctx, nil)
func (p *PresenceService) StartSyncWithReconnect(ctx context.Context, cfg *ReconnectConfig) error {
	if cfg != nil {
		p.reconnectCfg = cfg
	}

	attempt := 0
	maxRetries := p.reconnectCfg.MaxRetries

	for {
		select {
		case <-ctx.Done():
			slog.Info("Context cancelled, stopping sync")
			return ctx.Err()
		default:
		}

		slog.Info("Starting sync", "attempt", attempt)

		// 开始同步 - 此调用会阻塞直到断开连接或出错
		err := p.client.SyncWithContext(ctx)

		if err != nil {
			// 检查上下文是否已取消
			if ctx.Err() != nil {
				slog.Info("Sync stopped due to context cancellation")
				return ctx.Err()
			}

			slog.Warn("Sync disconnected with error", "attempt", attempt, "error", err)

			// 断开连接时保存会话
			p.saveSessionOnDisconnect()

			// 检查重试次数限制
			if maxRetries > 0 && attempt >= maxRetries {
				slog.Error("Maximum reconnection attempts reached", "max_retries", maxRetries)
				return fmt.Errorf("maximum reconnection attempts (%d) reached: %w", maxRetries, err)
			}

			// 计算退避延迟
			backoff := p.calculateBackoff(attempt)

			slog.Info("Waiting before reconnection attempt", "backoff", backoff, "attempt", attempt)

			// 使用指数退避等待
			select {
			case <-ctx.Done():
				slog.Info("Context cancelled during backoff wait")
				return ctx.Err()
			case <-time.After(backoff):
			}

			attempt++

			slog.Info("Attempting to reconnect", "attempt", attempt)

			// 重连后恢复在线状态
			if restoreErr := p.restorePresence(); restoreErr != nil {
				slog.Warn("Failed to restore presence after reconnection", "error", restoreErr)
			} else {
				attempt = 0
			}
		} else {
			// 同步完成且无错误（通常不应该发生）
			slog.Info("Sync completed without error")
			return nil
		}
	}
}

// StartSyncWithReconnectSimple 使用默认配置开始同步并自动重连。
func (p *PresenceService) StartSyncWithReconnectSimple(ctx context.Context) error {
	return p.StartSyncWithReconnect(ctx, DefaultReconnectConfig())
}

// GetLastPresence 返回最后设置的在线状态。
func (p *PresenceService) GetLastPresence() (PresenceState, string) {
	return p.lastPresence, p.lastStatusMsg
}

// RestoreLastPresence 恢复最后已知的在线状态。
func (p *PresenceService) RestoreLastPresence() error {
	return p.restorePresence()
}
