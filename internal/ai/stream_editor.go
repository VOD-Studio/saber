package ai

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
)

// MessageSender 定义发送 Matrix 消息的接口。
//
// 它抽象了基本的文本发送和带关系的消息发送功能。
type MessageSender interface {
	// SendText 向房间发送文本消息。
	SendText(ctx context.Context, roomID id.RoomID, body string) error

	// SendTextWithRelatesTo 向房间发送文本消息，并指定关系。
	// 返回发送的消息事件 ID 和错误。
	SendTextWithRelatesTo(ctx context.Context, roomID id.RoomID, body string, relatesTo *event.RelatesTo) (id.EventID, error)
}

// StreamEditor 实现流式编辑功能，用于在流式响应期间更新 Matrix 消息。
//
// 它维护一个初始消息，然后根据配置的阈值和间隔编辑该消息。
type StreamEditor struct {
	matrixService MessageSender
	roomID        id.RoomID
	initialMsg    string
	messageID     id.EventID
	mu            sync.Mutex
	lastEditTime  time.Time
	editCount     int
	config        config.StreamEditConfig
	stopped       bool
	finalSent     bool
}

// NewStreamEditor 创建一个新的流编辑器实例。
//
// 参数:
//   - matrixService: 用于发送 Matrix 消息的服务
//   - roomID: 消息所在的房间 ID
//   - initialMsg: 初始消息内容
//   - config: 流编辑配置
//
// 返回值:
//   - *StreamEditor: 创建的流编辑器实例
func NewStreamEditor(matrixService MessageSender, roomID id.RoomID, initialMsg string, config config.StreamEditConfig) *StreamEditor {
	return &StreamEditor{
		matrixService: matrixService,
		roomID:        roomID,
		initialMsg:    initialMsg,
		config:        config,
		lastEditTime:  time.Time{},
		editCount:     0,
		stopped:       false,
	}
}

// Start 发送初始消息并启动流编辑。
//
// 它发送第一条消息，保存事件 ID 以供后续编辑使用。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//
// 返回值:
//   - error: 操作过程中发生的错误
func (se *StreamEditor) Start(ctx context.Context) error {
	se.mu.Lock()
	defer se.mu.Unlock()

	if !se.config.Enabled {
		slog.Debug("流编辑未启用，跳过发送")
		se.lastEditTime = time.Now()
		return nil
	}

	initialContent := se.initialMsg
	if initialContent == "" {
		initialContent = "..."
	}

	slog.Debug("发送流式初始消息", "room", se.roomID, "content", initialContent)
	eventID, err := se.matrixService.SendTextWithRelatesTo(ctx, se.roomID, initialContent, nil)
	if err != nil {
		slog.Error("发送初始消息失败", "room", se.roomID, "error", err)
		return fmt.Errorf("failed to send initial message: %w", err)
	}
	se.messageID = eventID
	slog.Debug("初始消息已发送", "event_id", eventID)

	se.lastEditTime = time.Now()
	return nil
}

// Update 更新流编辑的消息内容。
//
// 它检查是否应该根据配置的阈值和间隔进行编辑。
// 如果达到编辑限制或流已停止，则返回错误。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - content: 新的消息内容
//
// 返回值:
//   - error: 操作过程中发生的错误
func (se *StreamEditor) Update(ctx context.Context, content string) error {
	se.mu.Lock()
	defer se.mu.Unlock()

	if se.stopped || se.finalSent {
		return nil
	}

	if !se.config.Enabled {
		slog.Debug("流编辑未启用，直接发送消息", "content", content)
		return se.matrixService.SendText(ctx, se.roomID, content)
	}

	if se.messageID == "" {
		slog.Debug("messageID为空，直接发送新消息", "content", content)
		eventID, err := se.matrixService.SendTextWithRelatesTo(ctx, se.roomID, content, nil)
		if err != nil {
			slog.Error("发送消息失败", "room", se.roomID, "error", err)
			return fmt.Errorf("failed to send message: %w", err)
		}
		se.messageID = eventID
		se.editCount++
		se.lastEditTime = time.Now()
		slog.Debug("消息已发送", "event_id", eventID)
		return nil
	}

	if se.editCount >= se.config.MaxEdits {
		slog.Debug("达到最大编辑次数，跳过更新，等待最终消息", "edit_count", se.editCount, "max_edits", se.config.MaxEdits)
		return nil
	}

	now := time.Now()
	if se.editCount > 0 && now.Sub(se.lastEditTime).Milliseconds() < int64(se.config.EditIntervalMs) {
		slog.Debug("编辑间隔太短，跳过", "elapsed_ms", now.Sub(se.lastEditTime).Milliseconds())
		return nil
	}

	relatesTo := &event.RelatesTo{
		Type:    event.RelReplace,
		EventID: se.messageID,
	}

	slog.Debug("编辑消息", "event_id", se.messageID, "content", content)
	_, err := se.matrixService.SendTextWithRelatesTo(ctx, se.roomID, content, relatesTo)
	if err != nil {
		slog.Error("编辑消息失败", "room", se.roomID, "event_id", se.messageID, "error", err)
		return fmt.Errorf("failed to update message: %w", err)
	}

	se.editCount++
	se.lastEditTime = now
	slog.Debug("消息编辑成功", "edit_count", se.editCount)

	return nil
}

// SendFinal 发送最终消息内容。
//
// 如果已经有消息ID，则编辑该消息；否则发送新消息。
// 此方法只会执行一次，后续调用将被忽略。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - content: 最终的消息内容
//
// 返回值:
//   - error: 操作过程中发生的错误
func (se *StreamEditor) SendFinal(ctx context.Context, content string) error {
	se.mu.Lock()
	defer se.mu.Unlock()

	if se.finalSent {
		slog.Debug("最终消息已发送，跳过")
		return nil
	}

	se.finalSent = true

	if !se.config.Enabled {
		slog.Debug("流编辑未启用，直接发送最终消息", "content", content)
		return se.matrixService.SendText(ctx, se.roomID, content)
	}

	if se.messageID == "" {
		slog.Debug("messageID为空，发送最终消息", "content", content)
		_, err := se.matrixService.SendTextWithRelatesTo(ctx, se.roomID, content, nil)
		return err
	}

	relatesTo := &event.RelatesTo{
		Type:    event.RelReplace,
		EventID: se.messageID,
	}

	slog.Debug("发送最终消息（编辑）", "event_id", se.messageID, "content", content)
	_, err := se.matrixService.SendTextWithRelatesTo(ctx, se.roomID, content, relatesTo)
	if err != nil {
		slog.Error("发送最终消息失败", "room", se.roomID, "error", err)
		return fmt.Errorf("failed to send final message: %w", err)
	}

	slog.Debug("最终消息发送成功")
	return nil
}

// Stop 停止流编辑。
func (se *StreamEditor) Stop() {
	se.mu.Lock()
	defer se.mu.Unlock()

	se.stopped = true
}

// IsStopped 检查流编辑是否已停止。
//
// 返回值:
//   - bool: 如果流编辑已停止则返回 true
func (se *StreamEditor) IsStopped() bool {
	se.mu.Lock()
	defer se.mu.Unlock()

	return se.stopped
}
