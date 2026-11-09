package discordplatform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/platform"
)

type mediaAttachment struct {
	ID           string  `json:"id"`
	Filename     string  `json:"filename"`
	ContentType  string  `json:"content_type"`
	URL          string  `json:"url"`
	Size         int64   `json:"size"`
	DurationSecs float64 `json:"duration_secs"`
}
type uploadedFile struct {
	Session      chat.Session `json:"session"`
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	UploadedName string       `json:"uploaded_filename"`
}

func (p *Platform) receiveMedia(ctx context.Context, item gatewayMessage) ([]chat.Attachment, error) {
	attachments := make([]chat.Attachment, 0, len(item.Attachments))
	for _, file := range item.Attachments {
		if file.Size > 20<<20 {
			return nil, errors.New("discord 入站附件超出 20 MiB")
		}
		u, err := url.Parse(file.URL)
		if err != nil {
			return nil, err
		}
		base, _ := url.Parse(p.api.base)
		if u.Hostname() != base.Hostname() && !strings.HasSuffix(u.Hostname(), ".discordapp.com") && !strings.HasSuffix(u.Hostname(), ".discordapp.net") {
			return nil, errors.New("discord 附件必须来自固定 CDN")
		}
		req, err := http.NewRequestWithContext(ctx, "GET", file.URL, nil)
		if err != nil {
			return nil, err
		}
		attachment, err := platform.DownloadMedia(req, p.api.api, p.api.base, 20<<20, file.ContentType, file.Filename, "")
		if err != nil {
			return nil, err
		}
		attachment.DurationMS = int64(file.DurationSecs * 1000)
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

// UploadFile 使用 Discord attachment slot 与签名 PUT 真正上传文件，不创建消息。
func (a *Adapter) UploadFile(ctx context.Context, file chat.FileUpload) ([]byte, error) {
	if err := a.validate(ctx, file.Session); err != nil {
		return nil, err
	}
	if len(file.Data) == 0 || len(file.Data) > 25<<20 {
		return nil, errors.New("discord 文件为空或超过 25 MiB")
	}
	var slots struct {
		Attachments []struct {
			ID             string `json:"id"`
			UploadURL      string `json:"upload_url"`
			UploadFilename string `json:"upload_filename"`
		} `json:"attachments"`
	}
	if err := a.api.do(ctx, "POST", "/channels/"+url.PathEscape(file.Session.Conversation)+"/attachments", map[string]any{"files": []any{map[string]any{"id": "0", "filename": file.Name, "file_size": len(file.Data)}}}, &slots); err != nil {
		return nil, err
	}
	if len(slots.Attachments) != 1 || slots.Attachments[0].UploadURL == "" {
		return nil, errors.New("discord 未返回上传 slot")
	}
	slot := slots.Attachments[0]
	req, err := http.NewRequestWithContext(ctx, "PUT", slot.UploadURL, bytes.NewReader(file.Data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if err = platform.UploadMedia(req, a.api.api, a.api.base); err != nil {
		return nil, err
	}
	return json.Marshal(uploadedFile{Session: file.Session, ID: slot.ID, Name: file.Name, UploadedName: slot.UploadFilename})
}

// SendUploadedFile 发送已上传的 Discord 文件，并复用真实 nonce 幂等键。
func (a *Adapter) SendUploadedFile(ctx context.Context, reply chat.Reply, payload []byte) (string, error) {
	if err := a.validate(ctx, reply.Session); err != nil {
		return "", err
	}
	var file uploadedFile
	if err := json.Unmarshal(payload, &file); err != nil {
		return "", err
	}
	if file.Session != reply.Session {
		return "", errors.New("discord 文件不能跨账号或会话使用")
	}
	var response struct {
		ID string `json:"id"`
	}
	body := map[string]any{"content": platform.SplitMessage(reply.Text, maxContentRunes)[0], "attachments": []any{map[string]string{"id": file.ID, "filename": file.Name, "uploaded_filename": file.UploadedName}}, "allowed_mentions": map[string]any{"parse": []string{}}, "nonce": nonce(reply.TransactionID), "enforce_nonce": reply.TransactionID != ""}
	if reply.ReplyTo != "" {
		body["message_reference"] = map[string]any{"message_id": reply.ReplyTo, "fail_if_not_exists": false}
	}
	if err := a.api.do(ctx, "POST", "/channels/"+url.PathEscape(reply.Session.Conversation)+"/messages", body, &response); err != nil {
		return "", err
	}
	if response.ID == "" {
		return "", errors.New("discord 文件发送未返回消息 ID")
	}
	return response.ID, nil
}

// SendImage 通过 Discord 附件上传发送图片。
func (a *Adapter) SendImage(ctx context.Context, reply chat.Reply, data []byte, mime, name string, width, height int) (string, error) {
	return a.sendMedia(ctx, reply, chat.Attachment{Kind: "image", Data: data, Name: name, MIMEType: mime})
}

// SendAudio 通过 Discord 原生附件发送可播放音频。
func (a *Adapter) SendAudio(ctx context.Context, reply chat.Reply, attachment chat.Attachment) (string, error) {
	return a.sendMedia(ctx, reply, attachment)
}

// SendVideo 通过 Discord 原生附件发送可播放视频。
func (a *Adapter) SendVideo(ctx context.Context, reply chat.Reply, attachment chat.Attachment) (string, error) {
	return a.sendMedia(ctx, reply, attachment)
}
func (a *Adapter) sendMedia(ctx context.Context, reply chat.Reply, attachment chat.Attachment) (string, error) {
	if len(attachment.Data) == 0 {
		return "", errors.New("discord 媒体需要实际字节")
	}
	payload, err := a.UploadFile(ctx, chat.FileUpload{Session: reply.Session, ReplyTo: reply.ReplyTo, Name: attachment.Name, Data: attachment.Data})
	if err != nil {
		return "", err
	}
	return a.SendUploadedFile(ctx, reply, payload)
}
