package matrixplatform

import (
	"context"
	"errors"
	"log/slog"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"

	"rua.plus/saber/internal/matrix"
)

// Session 是一次 Matrix 会话的运行面：注册事件回调、上报在线状态、自动进房、跑同步循环。
//
// 平台接入端只依赖这四个动作，因此测试可以注入替身，验证「Start 只启动一次同步、
// Stop 取消并等待退出」而不必连接真实 homeserver。
type Session interface {
	// Register 把事件回调挂到同步器上；必须在 Sync 之前调用一次。
	Register(events *matrix.EventHandler) error
	// Online 上报在线状态。
	Online() error
	// AutoJoin 加入配置里的房间；单个房间失败只记日志。
	AutoJoin()
	// Sync 阻塞运行同步循环（含自动重连），直到 ctx 取消。
	Sync(ctx context.Context) error
}

// mautrixSession 是 Session 的真实实现，包装 MatrixClient 与在线状态服务。
type mautrixSession struct {
	client   *matrix.MatrixClient
	presence *matrix.PresenceService
	rooms    []string
}

// NewSession 绑定已登录的 Matrix 客户端与自动进房列表。
//
// 构造只做准备，不发起请求；登录与 E2EE 初始化由装配层在调用前完成。
func NewSession(client *matrix.MatrixClient, autoJoinRooms []string) Session {
	session := &mautrixSession{client: client, rooms: append([]string(nil), autoJoinRooms...)}
	if client != nil {
		session.presence = matrix.NewPresenceService(client.GetClient())
	}
	return session
}

// Register 注册同步回调与消息、成员事件处理器。
//
// 只有 DefaultSyncer 支持 OnEventType；自定义同步器无法挂载回调，这里直接报错，
// 而不是启动一个收不到消息的同步循环。
func (s *mautrixSession) Register(events *matrix.EventHandler) error {
	if s.client == nil {
		return errors.New("matrix 会话缺少客户端")
	}
	if events == nil {
		return errors.New("matrix 会话缺少事件处理器")
	}
	mautrixClient := s.client.GetClient()
	syncer, ok := mautrixClient.Syncer.(*mautrix.DefaultSyncer)
	if !ok {
		return errors.New("matrix 同步器不是 DefaultSyncer，无法注册事件处理器")
	}
	syncer.OnSync(mautrixClient.DontProcessOldEvents)
	syncer.OnEventType(event.EventMessage, events.OnMessage)
	syncer.OnEventType(event.StateMember, events.OnMember)
	return nil
}

// Online 把机器人标记为在线；失败只记日志，不阻塞启动。
func (s *mautrixSession) Online() error {
	if s.presence == nil {
		return errors.New("matrix 会话缺少在线状态服务")
	}
	return s.presence.SetPresence("online", "Saber Bot is running")
}

// AutoJoin 加入配置的房间。缺少客户端或房间列表为空时什么也不做。
func (s *mautrixSession) AutoJoin() {
	if s.client == nil || len(s.rooms) == 0 {
		return
	}
	rooms := matrix.NewRoomService(s.client)
	for _, roomID := range s.rooms {
		slog.Info("Joining room", "room", roomID)
		if _, err := rooms.JoinRoom(context.Background(), roomID); err != nil {
			slog.Warn("Failed to join room", "room", roomID, "error", err)
		}
	}
}

// Sync 运行带自动重连的同步循环；ctx 取消是正常退出，不当作错误。
func (s *mautrixSession) Sync(ctx context.Context) error {
	if s.presence == nil {
		return errors.New("matrix 会话缺少在线状态服务")
	}
	reconnectCfg := matrix.DefaultReconnectConfig()
	slog.Info("Starting Matrix sync with auto-reconnect",
		"max_retries", reconnectCfg.MaxRetries,
		"initial_delay", reconnectCfg.InitialDelay,
		"max_delay", reconnectCfg.MaxDelay)
	return s.presence.StartSyncWithReconnect(ctx, reconnectCfg)
}
