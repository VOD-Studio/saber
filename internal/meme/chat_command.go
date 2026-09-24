package meme

import (
	"context"
	"fmt"
	"strings"

	"rua.plus/saber/internal/chat"
)

// ChatCommand 从共享语义中搜索梗图，发送能力由来源平台提供。
func (s *Service) ChatCommand(ctx context.Context, message chat.Message, adapter chat.Adapter, raw string) error {
	reply := func(text string) error {
		_, err := adapter.Send(ctx, chat.Reply{Session: message.Session, ReplyTo: message.ID, Text: text})
		return err
	}
	image, ok := adapter.(chat.ImageAdapter)
	if !ok {
		return reply("该平台暂不支持图片发送")
	}
	if !s.IsEnabled() {
		return reply("Meme 服务未启用")
	}
	fields := strings.Fields(raw)
	if len(fields) > 0 && strings.HasPrefix(fields[0], "--") {
		fields[0] = strings.TrimPrefix(fields[0], "--")
	}
	contentType, query := parseArgs(fields)
	if query == "" {
		return reply("用法：/meme [gif|sticker|meme] <关键词>")
	}
	gif, err := s.GetRandom(ctx, query, contentType)
	if err != nil {
		return reply(fmt.Sprintf("搜索「%s」失败：%v", query, err))
	}
	data, err := s.DownloadImage(ctx, gif)
	if err != nil {
		return reply("下载图片失败：" + err.Error())
	}
	_, err = image.SendImage(ctx, chat.Reply{Session: message.Session, ReplyTo: message.ID, Text: gif.Title}, data, gif.MimeType, gif.Title, gif.Width, gif.Height)
	return err
}
