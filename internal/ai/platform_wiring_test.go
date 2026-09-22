package ai

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/matrix"
)

// matrixEntrypoint 复刻平台接入端的行为：把 Matrix 命令规范化为 chat.Message 后交给核心。
// 生产装配见 internal/platform/matrix，这里仅用于仍需要完整 !ai/!task 命令链路的测试。
type matrixEntrypoint struct{ service *Service }

func (e matrixEntrypoint) HandleCommand(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string, modelName string) error {
	adapter := matrix.NewChatAdapter(e.service.matrixService, e.service.mediaService, e.service.config.Matrix.Media, e.service.config.Matrix.StreamEdit.Enabled, func(ctx context.Context, message chat.Message, reply chat.Adapter) (agent.Result, error) {
		if modelName == "" {
			return e.service.HandleChat(ctx, message, reply)
		}
		return e.service.HandleChatModel(ctx, message, reply, modelName)
	})
	return adapter.Handle(ctx, userID, roomID, args)
}

// NormalizeCommand 与生产实现一致：不解析媒体、不做流式编辑。
func (e matrixEntrypoint) NormalizeCommand(ctx context.Context, userID id.UserID, roomID id.RoomID, text string) (chat.Message, chat.Adapter, error) {
	adapter := matrix.NewChatAdapter(e.service.matrixService, nil, e.service.config.Matrix.Media, false, nil)
	return adapter.Message(ctx, userID, roomID, text), adapter, nil
}

// Session 复用 Matrix adapter 的房间与线程解析，与生产实现一致。
func (e matrixEntrypoint) Session(ctx context.Context, roomID id.RoomID) chat.Session {
	return matrix.NewChatAdapter(e.service.matrixService, nil, e.service.config.Matrix.Media, false, nil).Session(ctx, roomID)
}

// OutboundAdapter 与生产实现一致，交付时保留流式编辑与媒体能力。
func (e matrixEntrypoint) OutboundAdapter() chat.Adapter {
	return matrix.NewChatAdapter(e.service.matrixService, e.service.mediaService, e.service.config.Matrix.Media, e.service.config.Matrix.StreamEdit.Enabled, nil)
}

// EventID 读取 Matrix 入站事件标识。
func (e matrixEntrypoint) EventID(ctx context.Context) string {
	return string(matrix.GetEventID(ctx))
}

// wireMatrixPlatform 模拟 Matrix 平台接入端的装配：注入聊天入口并注册任务结果投递。
// 任务未启用时只装配聊天入口。
func wireMatrixPlatform(t *testing.T, service *Service, commands *matrix.CommandService, media *matrix.MediaService) {
	t.Helper()
	require.NotNil(t, commands, "Matrix 命令服务不能为空")
	service.SetChatEntrypoint(matrixEntrypoint{service: service})
	if service.tasks != nil {
		require.NoError(t, service.RegisterTaskDelivery("matrix", matrix.NewChatAdapter(commands, media, service.config.Matrix.Media, false, nil)))
	}
}
