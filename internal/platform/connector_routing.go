package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"rua.plus/saber/internal/chat"
)

type platformEndpointKey struct{}

func (p *connector) deliveryContext(ctx context.Context, s chat.Session) (context.Context, error) {
	if p.name != "teams" {
		return ctx, nil
	}
	endpoint := p.state("teams-service:" + s.Conversation)
	if endpoint == "" {
		return nil, errors.New("teams 会话没有已认证 serviceUrl；目标用户需先联系 bot")
	}
	if err := p.checkTeamsURL(endpoint); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, platformEndpointKey{}, endpoint), nil
}
func (p *connector) checkTeamsURL(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return errors.New("teams serviceUrl 无效")
	}
	own, _ := url.Parse(p.cfg.Endpoint)
	if own != nil && u.Host == own.Host {
		return nil
	}
	if strings.HasSuffix(u.Hostname(), ".botframework.com") || strings.HasSuffix(u.Hostname(), ".trafficmanager.net") {
		return nil
	}
	return errors.New("teams serviceUrl 不在固定 Microsoft 域名范围")
}
func (p *connector) incomingAllowed(msg chat.Message) error {
	if msg.SenderID == "" || msg.SenderID == p.cfg.BotID {
		return errors.New("平台发送者无效或为机器人自身")
	}
	if !p.cfg.AllowAllUsers && !contains(p.cfg.AllowedUsers, msg.SenderID) {
		return errors.New("平台用户未获授权")
	}
	if !msg.Direct && !contains(p.cfg.AllowedGroups, msg.Session.Conversation) && !contains(p.cfg.AllowedGroups, "*") {
		return errors.New("群聊未获授权")
	}
	return nil
}
func (p *connector) receiptID(msg chat.Message) string {
	if p.name == "a2a" {
		return msg.ID
	}
	msg.Session.Platform = p.name
	msg.Session.Account = p.Account()
	digest := sha256.Sum256([]byte(string(msg.Session.Key()) + "\x00" + msg.SenderID + "\x00" + msg.ID))
	return hex.EncodeToString(digest[:])
}
func (p *connector) sharedMedia(path, mime, name, kind string) (chat.Attachment, error) {
	root := p.cfg.SharedMediaDir
	if root == "" {
		return chat.Attachment{}, errors.New("平台文件需要 shared_media_dir，不读取 daemon 任意路径")
	}
	if !filepath.IsAbs(root) {
		return chat.Attachment{}, errors.New("shared_media_dir 必须为绝对路径")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(relative) {
		return chat.Attachment{}, errors.New("平台文件越过 shared_media_dir")
	}
	scope, err := os.OpenRoot(root)
	if err != nil {
		return chat.Attachment{}, err
	}
	defer func() { _ = scope.Close() }()
	file, err := scope.Open(relative)
	if err != nil {
		return chat.Attachment{}, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, p.cfg.MaxMediaBytes+1))
	if err != nil {
		return chat.Attachment{}, err
	}
	if int64(len(data)) > p.cfg.MaxMediaBytes {
		return chat.Attachment{}, errors.New("平台媒体大小超限")
	}
	return mediaAttachment(data, mime, name, kind), nil
}

func (p *connector) fetchAuthorizedMedia(ctx context.Context, msg chat.Message, path, mime, name, kind string) (chat.Attachment, error) {
	if err := p.incomingAllowed(msg); err != nil {
		return chat.Attachment{}, err
	}
	return p.fetchMedia(ctx, path, mime, name, kind)
}
func (p *connector) fetchAuthorizedExternalMedia(ctx context.Context, msg chat.Message, link, mime, name string, auth bool) (chat.Attachment, error) {
	if err := p.incomingAllowed(msg); err != nil {
		return chat.Attachment{}, err
	}
	return p.fetchExternalMedia(ctx, link, mime, name, auth)
}
