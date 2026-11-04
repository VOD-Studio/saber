package chat

import (
	"context"
	"fmt"
	"net/http"
)

// Avatar 是机器人当前公开头像的定位信息；URL 为空表示未设置头像。
type Avatar struct {
	URL string `json:"url"`
}

// AvatarAdapter 只读写接入端自身账号的头像，不接受任意用户或房间目标。
// 头像公开可见，平台负责上传原始图片并更新自身资料。
type AvatarAdapter interface {
	// GetAvatar 读取自身账号的当前头像。
	GetAvatar(ctx context.Context) (Avatar, error)
	// SetAvatar 上传图片并设为自身账号的头像；失败时不得报告成功。
	SetAvatar(ctx context.Context, data []byte, mimeType, filename string) (Avatar, error)
}

// ValidateAvatarImage 防止把错误页或超大内容上传为公开头像；以内容类型而非扩展名判定。
func ValidateAvatarImage(data []byte, mimeType string) error {
	const maxBytes = 10 << 20
	if len(data) == 0 || len(data) > maxBytes {
		return fmt.Errorf("头像图片大小必须为 1..%d 字节", maxBytes)
	}
	detected := http.DetectContentType(data)
	switch detected {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return fmt.Errorf("头像类型 %q 不受支持，只接受 PNG/JPEG/GIF/WebP", detected)
	}
	if detected != mimeType {
		return fmt.Errorf("头像声明类型 %q 与实际类型 %q 不符", mimeType, detected)
	}
	return nil
}
