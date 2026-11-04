package matrixplatform

import (
	"context"
	"errors"
	"fmt"

	"maunium.net/go/mautrix"

	"rua.plus/saber/internal/chat"
)

// WithAvatarClient 注入已登录的 SDK 客户端，只允许操作该客户端自身的公开头像。
func WithAvatarClient(client *mautrix.Client) Option {
	return func(p *Platform) { p.avatarClient = client }
}

// GetAvatar 通过 homeserver 读取机器人自身账号的头像，不读取配置缓存。
func (p *Platform) GetAvatar(ctx context.Context) (chat.Avatar, error) {
	if p.avatarClient == nil || p.avatarClient.UserID == "" {
		return chat.Avatar{}, errors.New("matrix 平台缺少已登录的头像客户端")
	}
	uri, err := p.avatarClient.GetAvatarURL(ctx, p.avatarClient.UserID)
	if err != nil {
		return chat.Avatar{}, fmt.Errorf("读取 Matrix 自身头像: %w", err)
	}
	return chat.Avatar{URL: uri.String()}, nil
}

// SetAvatar 公开上传原始图片，上传成功后才更新机器人自身账号的头像。
func (p *Platform) SetAvatar(ctx context.Context, data []byte, mimeType, filename string) (chat.Avatar, error) {
	if p.avatarClient == nil || p.avatarClient.UserID == "" {
		return chat.Avatar{}, errors.New("matrix 平台缺少已登录的头像客户端")
	}
	if err := chat.ValidateAvatarImage(data, mimeType); err != nil {
		return chat.Avatar{}, err
	}
	upload, err := p.avatarClient.UploadBytesWithName(ctx, data, mimeType, filename)
	if err != nil {
		return chat.Avatar{}, fmt.Errorf("上传 Matrix 公开头像: %w", err)
	}
	if upload == nil || upload.ContentURI.IsEmpty() {
		return chat.Avatar{}, errors.New("matrix 头像上传未返回 MXC 地址")
	}
	if err := p.avatarClient.SetAvatarURL(ctx, upload.ContentURI); err != nil {
		return chat.Avatar{}, fmt.Errorf("更新 Matrix 自身头像: %w", err)
	}
	return chat.Avatar{URL: upload.ContentURI.String()}, nil
}

var _ chat.AvatarAdapter = (*Platform)(nil)
