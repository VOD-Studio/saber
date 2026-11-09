package slackplatform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/platform"
)

type mediaFile struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	MIMEType           string `json:"mimetype"`
	Size               int64  `json:"size"`
	URLPrivate         string `json:"url_private"`
	URLPrivateDownload string `json:"url_private_download"`
}
type uploadedFile struct {
	Session chat.Session `json:"session"`
	ID      string       `json:"id"`
	Name    string       `json:"name"`
}

func (p *Platform) receiveMedia(ctx context.Context, item slackEvent) ([]chat.Attachment, error) {
	attachments := make([]chat.Attachment, 0, len(item.Files))
	for _, file := range item.Files {
		if file.Size > 20<<20 {
			return nil, errors.New("slack 入站文件超出 20 MiB")
		}
		link := file.URLPrivateDownload
		if link == "" {
			link = file.URLPrivate
		}
		u, err := url.Parse(link)
		if err != nil {
			return nil, err
		}
		base, _ := url.Parse(p.api.base)
		if u.Hostname() != base.Hostname() && !strings.HasSuffix(u.Hostname(), ".slack.com") && !strings.HasSuffix(u.Hostname(), ".slack-files.com") {
			return nil, errors.New("slack 私有文件必须来自平台域名")
		}
		req, err := http.NewRequestWithContext(ctx, "GET", link, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+p.api.botToken)
		attachment, err := platform.DownloadMedia(req, p.api.api, p.api.base, 20<<20, file.MIMEType, file.Name, "")
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

// UploadFile 使用 Slack external upload API 真正上传字节，不创建消息。
func (a *Adapter) UploadFile(ctx context.Context, file chat.FileUpload) ([]byte, error) {
	if err := a.validate(ctx, file.Session); err != nil {
		return nil, err
	}
	if len(file.Data) == 0 || len(file.Data) > 20<<20 {
		return nil, errors.New("slack 文件为空或超过 20 MiB")
	}
	var slot struct {
		UploadURL string `json:"upload_url"`
		FileID    string `json:"file_id"`
	}
	if err := a.api.post(ctx, "files.getUploadURLExternal", a.api.botToken, url.Values{"filename": {file.Name}, "length": {strconv.Itoa(len(file.Data))}}, &slot); err != nil {
		return nil, err
	}
	if slot.UploadURL == "" || slot.FileID == "" {
		return nil, errors.New("slack 未返回文件上传 slot")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", slot.UploadURL, bytes.NewReader(file.Data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if err = platform.UploadMedia(req, a.api.api, a.api.base); err != nil {
		return nil, err
	}
	return json.Marshal(uploadedFile{Session: file.Session, ID: slot.FileID, Name: file.Name})
}

// SendUploadedFile 用 completeUploadExternal 完成文件分享到原线程。
func (a *Adapter) SendUploadedFile(ctx context.Context, reply chat.Reply, payload []byte) (string, error) {
	if err := a.validate(ctx, reply.Session); err != nil {
		return "", err
	}
	var file uploadedFile
	if err := json.Unmarshal(payload, &file); err != nil {
		return "", err
	}
	if file.Session != reply.Session {
		return "", errors.New("slack 文件不能跨账号或会话使用")
	}
	files, _ := json.Marshal([]map[string]string{{"id": file.ID, "title": file.Name}})
	values := url.Values{"files": {string(files)}, "channel_id": {reply.Session.Conversation}, "initial_comment": {escapeText(reply.Text)}}
	thread := reply.Session.Thread
	if thread == "" {
		thread = reply.ReplyTo
	}
	if thread != "" {
		values.Set("thread_ts", thread)
	}
	var response struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := a.api.post(ctx, "files.completeUploadExternal", a.api.botToken, values, &response); err != nil {
		return "", err
	}
	if len(response.Files) == 0 {
		return "", errors.New("slack 文件完成上传未返回确认")
	}
	return response.Files[0].ID, nil
}

// SendImage 使用 Slack 上传 API 发送可见图片。
func (a *Adapter) SendImage(ctx context.Context, reply chat.Reply, data []byte, mime, name string, width, height int) (string, error) {
	return a.sendMedia(ctx, reply, chat.Attachment{Kind: "image", Data: data, Name: name, MIMEType: mime})
}

// SendAudio 使用 Slack 文件 API 发送可播放音频。
func (a *Adapter) SendAudio(ctx context.Context, reply chat.Reply, attachment chat.Attachment) (string, error) {
	return a.sendMedia(ctx, reply, attachment)
}

// SendVideo 使用 Slack 文件 API 发送视频。
func (a *Adapter) SendVideo(ctx context.Context, reply chat.Reply, attachment chat.Attachment) (string, error) {
	return a.sendMedia(ctx, reply, attachment)
}
func (a *Adapter) sendMedia(ctx context.Context, reply chat.Reply, attachment chat.Attachment) (string, error) {
	if len(attachment.Data) == 0 {
		return "", errors.New("slack 媒体需要实际字节")
	}
	payload, err := a.UploadFile(ctx, chat.FileUpload{Session: reply.Session, ReplyTo: reply.ReplyTo, Name: attachment.Name, Data: attachment.Data})
	if err != nil {
		return "", err
	}
	return a.SendUploadedFile(ctx, reply, payload)
}
