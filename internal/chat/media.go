package chat

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// Validate 检查附件类型和载荷；远程 URL 的 SSRF 与文件授权由实际消费者检查。
func (a Attachment) Validate() error {
	switch a.Kind {
	case "image", "audio", "video", "file":
	default:
		return errors.New("unsupported chat attachment kind")
	}
	if a.Size < 0 || a.DurationMS < 0 {
		return errors.New("negative chat attachment size or duration")
	}
	if len(a.Data) > 0 {
		if a.Size > 0 && a.Size != int64(len(a.Data)) {
			return errors.New("chat attachment size mismatch")
		}
		return nil
	}
	if a.URL == "" {
		return errors.New("empty chat attachment")
	}
	if strings.HasPrefix(a.URL, "data:") {
		if !strings.Contains(a.URL, ";base64,") {
			return errors.New("chat attachment requires base64 data URL")
		}
		return nil
	}
	u, err := url.Parse(a.URL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("chat attachment requires HTTP(S) or data URL")
	}
	return nil
}

// AudioAdapter 是支持语音消息的可选出站能力；reply 的身份和目标不得来自模型参数。
type AudioAdapter interface {
	SendAudio(context.Context, Reply, Attachment) (string, error)
}

// VideoAdapter 是支持视频消息的可选出站能力。
type VideoAdapter interface {
	SendVideo(context.Context, Reply, Attachment) (string, error)
}
