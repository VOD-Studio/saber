// Package matrix 提供 Matrix 事件处理功能。
package matrix

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"golang.org/x/sync/semaphore"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// EventHandler 封装 CommandService 并实现 mautrix 事件处理。
type EventHandler struct {
	service          *CommandService
	logger           *slog.Logger
	startTime        time.Time           // 机器人启动时间，用于过滤历史消息
	sem              *semaphore.Weighted // 并发限制信号量
	proactiveManager interface {
		OnNewMember(ctx context.Context, roomID id.RoomID, userID id.UserID) error
		RecordUserMessage(roomID id.RoomID)
	}
}

// NewEventHandler 创建一个新的事件处理器。
// 记录启动时间，用于过滤启动前的历史消息。
//
// 参数:
//   - service: 命令服务
//   - maxConcurrent: 最大并发事件处理数
func NewEventHandler(service *CommandService, maxConcurrent int) *EventHandler {
	if maxConcurrent <= 0 {
		maxConcurrent = 10 // 默认值
	}

	return &EventHandler{
		service:   service,
		logger:    slog.With("component", "event_handler"),
		startTime: time.Now(),
		sem:       semaphore.NewWeighted(int64(maxConcurrent)),
	}
}

// SetProactiveManager 设置主动聊天管理器。
func (h *EventHandler) SetProactiveManager(manager interface {
	OnNewMember(ctx context.Context, roomID id.RoomID, userID id.UserID) error
	RecordUserMessage(roomID id.RoomID)
}) {
	h.proactiveManager = manager
	slog.Debug("设置主动聊天管理器")
}

// OnMessage 处理传入的消息事件。
// 这设计用于作为 Syncer.OnEvent 回调使用。
func (h *EventHandler) OnMessage(ctx context.Context, evt *event.Event) {
	logger := h.logger.With(
		"event_id", evt.ID.String(),
		"type", evt.Type.String(),
		"sender", evt.Sender.String())

	logger.Debug("Processing event")

	// 过滤启动前的历史消息
	// 检查消息时间戳是否早于机器人启动时间
	if evt.Timestamp != 0 && evt.Timestamp < h.startTime.UnixMilli() {
		logger.Debug("跳过启动前的历史消息",
			"event_time", time.UnixMilli(evt.Timestamp).Format(time.RFC3339),
			"start_time", h.startTime.Format(time.RFC3339))
		return
	}

	// 记录用户消息时间（排除机器人自己的消息）
	if h.proactiveManager != nil && evt.Sender != h.service.botID {
		h.proactiveManager.RecordUserMessage(evt.RoomID)
	}

	// 为每个消息创建独立的 goroutine 进行并发处理
	go func() {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				logger.Error("Panic recovered in message handler",
					"panic", r,
					"stack_trace", string(stack))
			}
		}()

		// 获取信号量，限制并发
		// 使用 context.Background() 避免因父上下文取消导致请求被拒绝
		semCtx, semCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer semCancel()

		if err := h.sem.Acquire(semCtx, 1); err != nil {
			logger.Warn("无法获取并发槽位，跳过事件处理",
				"error", err,
				"event_id", evt.ID.String())
			return
		}
		defer h.sem.Release(1)

		// 创建独立上下文，带有 5 分钟超时
		msgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// 复制 SyncTokenContextKey 到新上下文（如果存在）
		if token := ctx.Value(mautrix.SyncTokenContextKey); token != nil {
			msgCtx = context.WithValue(msgCtx, mautrix.SyncTokenContextKey, token)
		}

		if err := h.service.HandleEvent(msgCtx, evt); err != nil {
			logger.Error("Event handling failed", "error", err)
		}
	}()
}

// OnMember 处理成员事件（包括邀请和新成员加入）。
func (h *EventHandler) OnMember(ctx context.Context, evt *event.Event) {
	logger := h.logger.With(
		"event_id", evt.ID.String(),
		"room", evt.RoomID.String(),
		"sender", evt.Sender.String())

	// 解析成员事件内容
	content, ok := evt.Content.Parsed.(*event.MemberEventContent)
	if !ok {
		logger.Debug("无法解析成员事件内容")
		return
	}

	// 检查 StateKey 是否存在
	if evt.StateKey == nil {
		logger.Debug("成员事件没有 state key")
		return
	}

	targetUserID := id.UserID(*evt.StateKey)

	// 处理不同类型的成员事件
	switch content.Membership {
	case event.MembershipInvite:
		// 处理邀请事件：检查是否邀请机器人自己
		if targetUserID != h.service.botID {
			logger.Debug("邀请目标不是本机器人", "target", targetUserID)
			return
		}

		// 接受邀请
		logger.Info("接受房间邀请", "inviter", evt.Sender.String())
		_, err := h.service.client.JoinRoom(ctx, evt.RoomID.String(), nil)
		if err != nil {
			logger.Error("接受邀请失败", "error", err)
			return
		}

		logger.Info("成功接受邀请")

	case event.MembershipJoin:
		// 处理成员加入事件：触发新成员欢迎
		// 忽略机器人自己的加入事件
		if targetUserID == h.service.botID {
			logger.Debug("忽略机器人自己的加入事件")
			return
		}

		// 检查是否需要触发新成员欢迎
		if h.proactiveManager != nil {
			go func() {
				defer func() {
					if r := recover(); r != nil {
						stack := debug.Stack()
						logger.Error("新成员欢迎处理发生 panic",
							"panic", r,
							"stack_trace", string(stack))
					}
				}()

				// 创建独立上下文，带有超时
				welcomeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				logger.Info("触发新成员欢迎",
					"room", evt.RoomID.String(),
					"new_member", targetUserID.String())

				if err := h.proactiveManager.OnNewMember(welcomeCtx, evt.RoomID, targetUserID); err != nil {
					logger.Error("新成员欢迎处理失败", "error", err)
				}
			}()
		}

	default:
		logger.Debug("忽略其他成员事件", "membership", content.Membership)
	}
}

// OnEvent 是通用事件处理器，分发到适当的处理器。
//
// TODO: 添加 E2EE 加密事件处理
// 当 evt.Type == event.EventEncrypted 时，需要先解密：
//
//	decryptedEvt, err := h.service.client.GetCryptoService().Decrypt(ctx, evt)
//	if err != nil { log error and return }
//	evt = decryptedEvt
//
// 然后再分发到 OnMessage 处理
func (h *EventHandler) OnEvent(ctx context.Context, evt *event.Event) {
	switch evt.Type {
	case event.EventMessage:
		h.OnMessage(ctx, evt)
	case event.StateMember:
		h.OnMember(ctx, evt)
	default:
		h.logger.Debug("Ignoring non-message event", "type", evt.Type.String())
	}
}

// Service 返回底层的 CommandService。
func (h *EventHandler) Service() *CommandService {
	return h.service
}
