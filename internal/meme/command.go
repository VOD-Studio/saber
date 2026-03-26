package meme

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/matrix"
)

// CommandService 定义命令服务接口。
// 这是一个简化的接口，仅包含 meme 命令需要的方法。
type CommandService interface {
	SendText(ctx context.Context, roomID id.RoomID, body string) error
	SendReply(ctx context.Context, roomID id.RoomID, body string, replyTo id.EventID) (id.EventID, error)
}

// UploadMediaFunc 定义上传媒体的函数签名。
type UploadMediaFunc func(ctx context.Context, roomID id.RoomID, data []byte, mimeType, filename string) (id.ContentURIString, error)

// MemeCommand 处理 !meme 命令。
type MemeCommand struct {
	service     *Service
	cmdService  CommandService
	client      *mautrix.Client
	uploadMedia UploadMediaFunc
}

// NewMemeCommand 创建一个新的 meme 命令处理器。
func NewMemeCommand(cmdService CommandService, client *mautrix.Client, memeService *Service) *MemeCommand {
	return &MemeCommand{
		service:     memeService,
		cmdService:  cmdService,
		client:      client,
		uploadMedia: defaultUploadMedia(client),
	}
}

// defaultUploadMedia 返回默认的上传媒体函数。
func defaultUploadMedia(client *mautrix.Client) UploadMediaFunc {
	return func(ctx context.Context, roomID id.RoomID, data []byte, mimeType, filename string) (id.ContentURIString, error) {
		// 上传到 Matrix 服务器
		resp, err := client.UploadBytes(ctx, data, mimeType)
		if err != nil {
			return "", fmt.Errorf("上传失败：%w", err)
		}
		return id.ContentURIString(resp.ContentURI.String()), nil
	}
}

// Handle 实现 CommandHandler。
//
// 用法:
//
//	!meme <关键词>           - 搜索 GIF（默认）
//	!meme gif <关键词>       - 搜索 GIF
//	!meme sticker <关键词>   - 搜索 Sticker
//	!meme meme <关键词>      - 搜索 Meme
//
// 示例:
//
//	!meme happy
//	!meme sticker hello
func (c *MemeCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	// 检查服务是否可用
	if c.service == nil || !c.service.IsEnabled() {
		return c.cmdService.SendText(ctx, roomID, "Meme 服务未启用，请在配置中设置 meme.enabled=true 和 meme.api_key")
	}

	// 解析参数
	contentType, query := parseArgs(args)
	if query == "" {
		helpText := `Meme 命令用法:
!meme <关键词>          - 搜索 GIF（默认）
!meme gif <关键词>      - 搜索 GIF 动图
!meme sticker <关键词>  - 搜索 Sticker 贴纸
!meme meme <关键词>     - 搜索 Meme 梗图

示例:
  !meme happy
  !meme sticker hello
  !meme meme funny`
		return c.cmdService.SendText(ctx, roomID, helpText)
	}

	slog.Info("处理 meme 命令",
		"user", userID.String(),
		"room", roomID.String(),
		"query", query,
		"type", contentType)

	// 搜索 GIF
	gif, err := c.service.GetRandom(ctx, query, contentType)
	if err != nil {
		slog.Error("搜索失败", "error", err, "query", query)
		return c.cmdService.SendText(ctx, roomID, fmt.Sprintf("搜索「%s」失败：%v", query, err))
	}

	// 下载图片
	imageData, err := c.service.DownloadImage(ctx, gif)
	if err != nil {
		slog.Error("下载图片失败", "error", err, "url", gif.URL)
		return c.cmdService.SendText(ctx, roomID, fmt.Sprintf("下载图片失败：%v", err))
	}

	// 上传到 Matrix
	mxcURI, err := c.uploadMedia(ctx, roomID, imageData, gif.MimeType, "meme.gif")
	if err != nil {
		slog.Error("上传图片失败", "error", err)
		return c.cmdService.SendText(ctx, roomID, fmt.Sprintf("上传图片失败：%v", err))
	}

	// 发送图片消息
	content := &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    gif.Title,
		URL:     mxcURI,
		Info: &event.FileInfo{
			MimeType: gif.MimeType,
			Width:    gif.Width,
			Height:   gif.Height,
		},
	}

	// 如果上下文中有 EventID，则作为回复发送
	if eventID := matrix.GetEventID(ctx); eventID != "" {
		content.RelatesTo = &event.RelatesTo{
			InReplyTo: &event.InReplyTo{
				EventID: eventID,
			},
		}
	}

	_, err = c.client.SendMessageEvent(ctx, roomID, event.EventMessage, content)
	if err != nil {
		slog.Error("发送图片消息失败", "error", err)
		return fmt.Errorf("发送图片消息失败：%w", err)
	}

	slog.Info("Meme 发送成功",
		"user", userID.String(),
		"room", roomID.String(),
		"gif_id", gif.ID,
		"title", gif.Title)

	return nil
}

// parseArgs 解析命令参数，返回内容类型和搜索关键词。
//
// 支持的格式:
//   - !meme <keyword>         → 默认搜索 GIF
//   - !meme gif <keyword>     → 搜索 GIF
//   - !meme sticker <keyword> → 搜索 Sticker
//   - !meme meme <keyword>    → 搜索 Meme
//
// 参数:
//   - args: 命令参数列表（不含命令名本身）
//
// 返回值:
//   - ContentType: 内容类型（GIF/Sticker/Meme）
//   - string: 搜索关键词
func parseArgs(args []string) (ContentType, string) {
	if len(args) == 0 {
		// 无参数，返回默认类型和空关键词
		return ContentTypeGIF, ""
	}

	// 检查第一个参数是否为子命令
	switch args[0] {
	case "gif":
		// !meme gif <keyword>
		if len(args) > 1 {
			return ContentTypeGIF, strings.Join(args[1:], " ")
		}
		return ContentTypeGIF, ""
	case "sticker":
		// !meme sticker <keyword>
		if len(args) > 1 {
			return ContentTypeSticker, strings.Join(args[1:], " ")
		}
		return ContentTypeSticker, ""
	case "meme":
		// !meme meme <keyword>
		if len(args) > 1 {
			return ContentTypeMeme, strings.Join(args[1:], " ")
		}
		return ContentTypeMeme, ""
	default:
		// 无子命令，默认使用 GIF 类型
		// !meme <keyword>
		return ContentTypeGIF, strings.Join(args, " ")
	}
}
