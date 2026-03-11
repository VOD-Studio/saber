// Package matrix provides Matrix client functionality including presence tracking,
// typing indicators, read receipts, and auto-reconnection support.
package matrix

import (
	"context"
	"fmt"
	"math"
	"time"

	"log/slog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// PresenceState represents a user's presence status.
type PresenceState string

const (
	// PresenceOnline indicates the user is online.
	PresenceOnline PresenceState = "online"
	// PresenceOffline indicates the user is offline.
	PresenceOffline PresenceState = "offline"
	// PresenceUnavailable indicates the user is unavailable (away).
	PresenceUnavailable PresenceState = "unavailable"
)

// PresenceInfo contains presence information for a user.
type PresenceInfo struct {
	UserID          id.UserID
	Presence        PresenceState
	StatusMsg       string
	LastActiveAgo   time.Duration
	CurrentlyActive bool
}

// ReconnectConfig holds configuration for auto-reconnection.
type ReconnectConfig struct {
	// MaxRetries is the maximum number of reconnection attempts.
	// Set to 0 for unlimited retries (not recommended).
	MaxRetries int

	// InitialDelay is the initial backoff delay before the first retry.
	InitialDelay time.Duration

	// MaxDelay is the maximum backoff delay between retries.
	MaxDelay time.Duration

	// Multiplier is the exponential backoff multiplier.
	// delay = min(initialDelay * multiplier^attempt, maxDelay)
	Multiplier float64
}

// DefaultReconnectConfig returns a ReconnectConfig with sensible defaults.
func DefaultReconnectConfig() *ReconnectConfig {
	return &ReconnectConfig{
		MaxRetries:   10,
		InitialDelay: time.Second,
		MaxDelay:     5 * time.Minute,
		Multiplier:   2.0,
	}
}

// PresenceEventHandler is a callback function type for handling Matrix events.
type PresenceEventHandler func(ctx context.Context, evt *event.Event)

// SessionSaver is a callback function type for saving session state on disconnect.
type SessionSaver func(path string) error

// PresenceService provides presence tracking, typing indicators, and auto-reconnection.
type PresenceService struct {
	client        *mautrix.Client
	reconnectCfg  *ReconnectConfig
	sessionSaver  SessionSaver
	sessionPath   string
	lastPresence  PresenceState
	lastStatusMsg string
}

// NewPresenceService creates a new PresenceService with the given Matrix client.
func NewPresenceService(client *mautrix.Client) *PresenceService {
	return &PresenceService{
		client:       client,
		reconnectCfg: DefaultReconnectConfig(),
	}
}

// SetReconnectConfig sets a custom reconnection configuration.
func (p *PresenceService) SetReconnectConfig(cfg *ReconnectConfig) {
	p.reconnectCfg = cfg
}

// SetSessionSaver sets the session saver callback and path for session persistence.
func (p *PresenceService) SetSessionSaver(saver SessionSaver, path string) {
	p.sessionSaver = saver
	p.sessionPath = path
}

// SetPresence sets the user's presence state with an optional status message.
func (p *PresenceService) SetPresence(state PresenceState, statusMsg string) error {
	ctx := context.Background()
	return p.SetPresenceWithContext(ctx, state, statusMsg)
}

// SetPresenceWithContext sets the user's presence state with context support.
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

// GetPresence retrieves the presence information for a given user.
func (p *PresenceService) GetPresence(userID string) (*PresenceInfo, error) {
	ctx := context.Background()
	return p.GetPresenceWithContext(ctx, userID)
}

// GetPresenceWithContext retrieves the presence information for a given user with context support.
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

// StartTyping sends a typing indicator to a room for a specified timeout in milliseconds.
// The timeout parameter is in milliseconds (default 30000ms = 30s).
func (p *PresenceService) StartTyping(roomID string, timeout int) error {
	ctx := context.Background()
	return p.StartTypingWithContext(ctx, roomID, time.Duration(timeout)*time.Millisecond)
}

// StartTypingWithContext sends a typing indicator with context support.
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

// StopTyping stops the typing indicator in a room.
func (p *PresenceService) StopTyping(roomID string) error {
	ctx := context.Background()
	return p.StopTypingWithContext(ctx, roomID)
}

// StopTypingWithContext stops the typing indicator with context support.
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

// MarkAsRead sends a read receipt for a specific event in a room.
func (p *PresenceService) MarkAsRead(roomID string, eventID string) error {
	ctx := context.Background()
	return p.MarkAsReadWithContext(ctx, roomID, eventID)
}

// MarkAsReadWithContext sends a read receipt with context support.
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

// SendReceipt sends a read receipt of a specific type for an event.
// Common receipt types are event.ReceiptTypeRead and event.ReceiptTypeReadPrivate.
func (p *PresenceService) SendReceipt(roomID string, eventID string, receiptType event.ReceiptType) error {
	ctx := context.Background()
	return p.SendReceiptWithContext(ctx, roomID, eventID, receiptType)
}

// SendReceiptWithContext sends a read receipt with context support.
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

// calculateBackoff calculates the backoff delay for a given retry attempt.
func (p *PresenceService) calculateBackoff(attempt int) time.Duration {
	delay := float64(p.reconnectCfg.InitialDelay)
	delay = delay * math.Pow(p.reconnectCfg.Multiplier, float64(attempt))

	result := min(time.Duration(delay), p.reconnectCfg.MaxDelay)

	return result
}

// saveSessionOnDisconnect saves the session state if a saver is configured.
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

// restorePresence restores the previous presence state after reconnection.
func (p *PresenceService) restorePresence() error {
	if p.lastPresence == "" {
		// No previous presence set, default to online
		p.lastPresence = PresenceOnline
	}

	return p.SetPresence(p.lastPresence, p.lastStatusMsg)
}

// StartSyncWithReconnect starts syncing with automatic reconnection on disconnect.
// It uses exponential backoff for reconnection attempts.
// The syncer should be configured with event handlers before calling this method.
//
// Example:
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

		// Start sync - this blocks until disconnect or error
		err := p.client.SyncWithContext(ctx)

		if err != nil {
			// Check if context was cancelled
			if ctx.Err() != nil {
				slog.Info("Sync stopped due to context cancellation")
				return ctx.Err()
			}

			slog.Warn("Sync disconnected with error", "attempt", attempt, "error", err)

			// Save session on disconnect
			p.saveSessionOnDisconnect()

			// Check retry limit
			if maxRetries > 0 && attempt >= maxRetries {
				slog.Error("Maximum reconnection attempts reached", "max_retries", maxRetries)
				return fmt.Errorf("maximum reconnection attempts (%d) reached: %w", maxRetries, err)
			}

			// Calculate backoff delay
			backoff := p.calculateBackoff(attempt)

			slog.Info("Waiting before reconnection attempt", "backoff", backoff, "attempt", attempt)

			// Wait with exponential backoff
			select {
			case <-ctx.Done():
				slog.Info("Context cancelled during backoff wait")
				return ctx.Err()
			case <-time.After(backoff):
			}

			attempt++

			slog.Info("Attempting to reconnect", "attempt", attempt)

			// Restore presence after reconnection
			if restoreErr := p.restorePresence(); restoreErr != nil {
				slog.Warn("Failed to restore presence after reconnection", "error", restoreErr)
			}
		} else {
			// Sync completed without error (shouldn't normally happen)
			slog.Info("Sync completed without error")
			return nil
		}
	}
}

// StartSyncWithReconnectSimple starts syncing with auto-reconnect using default configuration.
func (p *PresenceService) StartSyncWithReconnectSimple(ctx context.Context) error {
	return p.StartSyncWithReconnect(ctx, DefaultReconnectConfig())
}

// GetLastPresence returns the last set presence state.
func (p *PresenceService) GetLastPresence() (PresenceState, string) {
	return p.lastPresence, p.lastStatusMsg
}

// RestoreLastPresence restores the last known presence state.
func (p *PresenceService) RestoreLastPresence() error {
	return p.restorePresence()
}
