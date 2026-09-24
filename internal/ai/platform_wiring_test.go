package ai

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/matrix"
)

type matrixTaskTestCommand struct {
	service *Service
	adapter *matrix.ChatAdapter
}

func (c matrixTaskTestCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	message := c.adapter.Message(ctx, userID, roomID, "!task "+strings.Join(args, " "))
	if len(args) == 0 {
		return c.service.TaskCommand(ctx, message, c.adapter, "", "")
	}
	return c.service.TaskCommand(ctx, message, c.adapter, args[0], strings.Join(args[1:], " "))
}

func wireMatrixPlatform(t *testing.T, service *Service, commands *matrix.CommandService, media *matrix.MediaService) {
	t.Helper()
	require.NotNil(t, commands)
	adapter := matrix.NewChatAdapter(commands, media, service.config.Matrix.Media, false, service.HandleChat)
	commands.RegisterCommand("ai", adapter)
	commands.RegisterCommand("task", matrixTaskTestCommand{service: service, adapter: adapter})
	if service.tasks != nil {
		require.NoError(t, service.RegisterTaskDelivery("matrix", matrix.NewChatAdapter(commands, media, service.config.Matrix.Media, false, nil)))
	}
}

func runMatrixChat(service *Service, ctx context.Context, userID id.UserID, roomID id.RoomID, modelName, text string) error {
	adapter := matrix.NewChatAdapter(service.matrixService, service.mediaService, service.config.Matrix.Media, service.config.Matrix.StreamEdit.Enabled, nil)
	_, err := service.HandleChatModel(ctx, adapter.Message(ctx, userID, roomID, text), adapter, modelName)
	return err
}
