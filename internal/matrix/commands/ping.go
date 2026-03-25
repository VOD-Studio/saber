// Package commands 提供 Matrix 机器人的命令处理实现。
package commands

import (
	"context"

	"maunium.net/go/mautrix/id"
)

// Sender 定义发送消息的服务接口。
type Sender interface {
	// SendFormattedText 向房间发送格式化消息（支持 HTML）。
	SendFormattedText(ctx context.Context, roomID id.RoomID, html, plain string) error
}

// PingCommand 响应 "Pong!"。
type PingCommand struct {
	sender Sender
}

// NewPingCommand 创建一个新的 ping 命令处理器。
func NewPingCommand(sender Sender) *PingCommand {
	return &PingCommand{sender: sender}
}

// Handle 实现 CommandHandler。
func (c *PingCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	html := "<strong>🏓 Pong!</strong>"
	plain := "🏓 Pong!"
	return c.sender.SendFormattedText(ctx, roomID, html, plain)
}
