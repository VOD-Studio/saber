package ai

import (
	"context"
	"fmt"
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

	if se.config.Enabled {
		eventID, err := se.matrixService.SendTextWithRelatesTo(ctx, se.roomID, se.initialMsg, nil)
		if err != nil {
			return fmt.Errorf("failed to send initial message: %w", err)
		}
		se.messageID = eventID
	}

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

	// 检查流是否已停止
	if se.stopped {
		return fmt.Errorf("stream editor is stopped")
	}

	// 检查是否启用流编辑
	if !se.config.Enabled {
		return nil
	}

	// 检查是否达到最大编辑次数
	if se.editCount >= se.config.MaxEdits {
		return fmt.Errorf("maximum edit limit reached: %d", se.config.MaxEdits)
	}

	// 检查编辑间隔
	now := time.Now()
	if now.Sub(se.lastEditTime).Milliseconds() < int64(se.config.EditIntervalMs) {
		// 如果距离上次编辑时间太短，跳过本次编辑
		return nil
	}

	// 创建关系对象
	relatesTo := &event.RelatesTo{
		Type:    event.RelReplace,
		EventID: se.messageID,
	}

	// 发送编辑后的消息
	_, err := se.matrixService.SendTextWithRelatesTo(ctx, se.roomID, content, relatesTo)
	if err != nil {
		return fmt.Errorf("failed to update message: %w", err)
	}

	// 更新统计信息
	se.editCount++
	se.lastEditTime = now

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
