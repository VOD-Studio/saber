package violetplatform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"rua.plus/saber/internal/chat"
)

// GetAvatar 从 Violet 读取当前 Bot Token 所属账号的最新头像。
func (p *Platform) GetAvatar(ctx context.Context) (chat.Avatar, error) {
	if p == nil || p.api == nil {
		return chat.Avatar{}, fmt.Errorf("violet 平台未配置")
	}
	profile, err := p.api.profile(ctx)
	if err != nil {
		return chat.Avatar{}, err
	}
	avatar, err := p.avatarFromProfile(profile)
	if err != nil {
		return chat.Avatar{}, err
	}
	p.setIdentity(profile)
	return avatar, nil
}

// SetAvatar 上传图片后设置自身头像，失败时不改写本地身份快照。
func (p *Platform) SetAvatar(ctx context.Context, data []byte, mimeType, filename string) (chat.Avatar, error) {
	if p == nil || p.api == nil {
		return chat.Avatar{}, fmt.Errorf("violet 平台未配置")
	}
	if err := chat.ValidateAvatarImage(data, mimeType); err != nil {
		return chat.Avatar{}, err
	}
	profile, err := p.api.uploadAvatar(ctx, data, mimeType, filename)
	if err != nil {
		return chat.Avatar{}, err
	}
	if profile.AvatarURL == "" {
		return chat.Avatar{}, fmt.Errorf("violet 头像更新未返回 avatar_url")
	}
	avatar, err := p.avatarFromProfile(profile)
	if err != nil {
		return chat.Avatar{}, err
	}
	p.setIdentity(profile)
	return avatar, nil
}

func (p *Platform) avatarFromProfile(profile botProfileDTO) (chat.Avatar, error) {
	if profile.ID == "" || profile.UserID == "" {
		return chat.Avatar{}, fmt.Errorf("violet 头像资料缺少 bot 或用户标识")
	}
	if profile.AvatarURL == "" {
		return chat.Avatar{}, nil
	}
	avatarURL, err := resolveMediaURL(p.violet.Endpoint, profile.AvatarURL)
	if err != nil {
		return chat.Avatar{}, err
	}
	return chat.Avatar{URL: avatarURL}, nil
}

func (c *client) uploadAvatar(ctx context.Context, data []byte, mimeType, filename string) (botProfileDTO, error) {
	body, contentType, err := multipartImageBody(data, mimeType, filename)
	if err != nil {
		return botProfileDTO{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/avatar", body)
	if err != nil {
		return botProfileDTO{}, fmt.Errorf("构造 violet 头像请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", contentType)
	resp, err := c.api.Do(req)
	if err != nil {
		return botProfileDTO{}, fmt.Errorf("请求 violet 头像更新失败: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("关闭 violet 头像响应失败", "error", err)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return botProfileDTO{}, statusError(resp)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return botProfileDTO{}, fmt.Errorf("读取 violet 头像响应失败: %w", err)
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return botProfileDTO{}, fmt.Errorf("violet 头像响应为空")
	}
	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return botProfileDTO{}, fmt.Errorf("解析 violet 头像响应失败: %w", err)
	}
	var profile botProfileDTO
	if err := json.Unmarshal(env.Data, &profile); err != nil {
		return botProfileDTO{}, fmt.Errorf("解析 violet 头像资料失败: %w", err)
	}
	return profile, nil
}

var _ chat.AvatarAdapter = (*Platform)(nil)
