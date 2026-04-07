// Package commands 提供 Matrix 机器人的命令处理实现。
package commands

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/id"
)

// MemeService 定义 Meme 服务接口。
type MemeService interface {
	// IsEnabled 检查服务是否启用。
	IsEnabled() bool
	// Search 搜索 meme。
	Search(ctx context.Context, query string, contentType int) (*MemeResult, error)
	// Download 下载 meme 图片。
	Download(ctx context.Context, meme *MemeResult) ([]byte, error)
}

// MemeResult 表示搜索到的 meme 结果。
type MemeResult struct {
	ID       string
	Title    string
	URL      string
	MimeType string
	Width    int
	Height   int
}

// MemeCommand 处理 !meme 命令。
type MemeCommand struct {
	sender Sender
	text   TextOnlySender
	svc    MemeService
}

// NewMemeCommand 创建一个新的 meme 命令处理器。
func NewMemeCommand(sender Sender, text TextOnlySender, svc MemeService) *MemeCommand {
	return &MemeCommand{
		sender: sender,
		text:   text,
		svc:    svc,
	}
}

// Handle 实现 CommandHandler。
func (c *MemeCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if c.svc == nil || !c.svc.IsEnabled() {
		return c.text.SendText(ctx, roomID, "Meme 服务未启用，请在配置中设置 meme.enabled=true 和 meme.api_key")
	}

	if len(args) == 0 {
		return c.text.SendText(ctx, roomID, "请提供搜索关键词，例如：!meme happy")
	}

	// 实际实现由外部包提供
	// 这里仅作为接口占位符
	return c.text.SendText(ctx, roomID, fmt.Sprintf("搜索关键词：%v", args))
}
