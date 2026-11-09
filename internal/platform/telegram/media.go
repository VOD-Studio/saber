package telegramplatform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/platform"
)

type mediaFile struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MIMEType string `json:"mime_type"`
	Duration int64  `json:"duration"`
	FileSize int64  `json:"file_size"`
}
type stagedFile struct {
	Session chat.Session `json:"session"`
	Name    string       `json:"name"`
	Data    []byte       `json:"data"`
	Kind    string       `json:"kind"`
	MIME    string       `json:"mime"`
}

func (item message) hasMedia() bool {
	return len(item.Photo) > 0 || item.Voice != nil || item.Audio != nil || item.Video != nil || item.Document != nil || item.Animation != nil
}
func (p *Platform) receiveMedia(ctx context.Context, item message) ([]chat.Attachment, error) {
	var files []struct {
		file mediaFile
		kind string
	}
	if len(item.Photo) > 0 {
		files = append(files, struct {
			file mediaFile
			kind string
		}{item.Photo[len(item.Photo)-1], "image"})
	}
	for _, entry := range []struct {
		file *mediaFile
		kind string
	}{{item.Voice, "audio"}, {item.Audio, "audio"}, {item.Video, "video"}, {item.Document, "file"}, {item.Animation, "video"}} {
		if entry.file != nil {
			files = append(files, struct {
				file mediaFile
				kind string
			}{*entry.file, entry.kind})
		}
	}
	attachments := make([]chat.Attachment, 0, len(files))
	for _, entry := range files {
		if entry.file.FileSize > 20<<20 {
			return nil, errors.New("telegram 媒体超出 20 MiB")
		}
		var result struct {
			FilePath string `json:"file_path"`
		}
		if err := p.api.call(ctx, p.api.api, "getFile", map[string]string{"file_id": entry.file.FileID}, &result); err != nil {
			return nil, err
		}
		if result.FilePath == "" || strings.Contains(result.FilePath, "..") || strings.ContainsAny(result.FilePath, "?\\#") {
			return nil, errors.New("telegram file_path 无效")
		}
		req, err := http.NewRequestWithContext(ctx, "GET", p.api.base+"/file/bot"+p.api.token+"/"+result.FilePath, nil)
		if err != nil {
			return nil, err
		}
		attachment, err := platform.DownloadMedia(req, p.api.api, p.api.base, 20<<20, entry.file.MIMEType, entry.file.FileName, entry.kind)
		if err != nil {
			return nil, err
		}
		attachment.DurationMS = entry.file.Duration * 1000
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

// UploadFile 对无法单独上传的 Bot API 持久暂存真实字节；实际发送在 SendUploadedFile 中完成。
func (a *Adapter) UploadFile(ctx context.Context, file chat.FileUpload) ([]byte, error) {
	if err := a.validate(ctx, file.Session); err != nil {
		return nil, err
	}
	if len(file.Data) == 0 || len(file.Data) > 50<<20 {
		return nil, errors.New("telegram 文件为空或超过 50 MiB")
	}
	return json.Marshal(stagedFile{Session: file.Session, Name: file.Name, Data: file.Data, Kind: "document", MIME: http.DetectContentType(file.Data)})
}

// SendUploadedFile 用 Bot API multipart 真实发送已暂存的文件，拒绝跨会话复用。
func (a *Adapter) SendUploadedFile(ctx context.Context, reply chat.Reply, payload []byte) (string, error) {
	if err := a.validate(ctx, reply.Session); err != nil {
		return "", err
	}
	var file stagedFile
	if err := json.Unmarshal(payload, &file); err != nil {
		return "", err
	}
	if file.Session != reply.Session {
		return "", errors.New("telegram 文件不能跨账号或会话使用")
	}
	return a.sendMedia(ctx, reply, file)
}

// SendImage 发送 Telegram 图片消息。
func (a *Adapter) SendImage(ctx context.Context, reply chat.Reply, data []byte, mime, name string, width, height int) (string, error) {
	return a.sendMedia(ctx, reply, stagedFile{Session: reply.Session, Name: name, Data: data, Kind: "photo", MIME: mime})
}

// SendAudio 发送 Telegram 原生语音消息。
func (a *Adapter) SendAudio(ctx context.Context, reply chat.Reply, attachment chat.Attachment) (string, error) {
	return a.sendAttachment(ctx, reply, attachment, "voice")
}

// SendVideo 发送 Telegram 原生视频消息。
func (a *Adapter) SendVideo(ctx context.Context, reply chat.Reply, attachment chat.Attachment) (string, error) {
	return a.sendAttachment(ctx, reply, attachment, "video")
}
func (a *Adapter) sendAttachment(ctx context.Context, reply chat.Reply, attachment chat.Attachment, kind string) (string, error) {
	if len(attachment.Data) == 0 {
		return "", errors.New("telegram 媒体需要实际 Data 字节")
	}
	return a.sendMedia(ctx, reply, stagedFile{Session: reply.Session, Name: attachment.Name, Data: attachment.Data, Kind: kind, MIME: attachment.MIMEType})
}
func (a *Adapter) sendMedia(ctx context.Context, reply chat.Reply, file stagedFile) (string, error) {
	if err := a.validate(ctx, reply.Session); err != nil {
		return "", err
	}
	if len(file.Data) == 0 || len(file.Data) > 50<<20 {
		return "", errors.New("telegram 文件为空或超限")
	}
	chatID, err := parseChatID(reply.Session.Conversation)
	if err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	fields := map[string]string{"chat_id": strconv.FormatInt(chatID, 10), "caption": string([]rune(reply.Text)[:min(1000, len([]rune(reply.Text)))])}
	if reply.Session.Thread != "" {
		fields["message_thread_id"] = reply.Session.Thread
	}
	if reply.ReplyTo != "" {
		fields["reply_parameters"] = fmt.Sprintf(`{"message_id":%s,"allow_sending_without_reply":true}`, reply.ReplyTo)
	}
	for key, value := range fields {
		if err = writer.WriteField(key, value); err != nil {
			return "", err
		}
	}
	part, err := writer.CreateFormFile(file.Kind, file.Name)
	if err != nil {
		return "", err
	}
	if _, err = part.Write(file.Data); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	method := "send" + strings.ToUpper(file.Kind[:1]) + file.Kind[1:]
	req, err := http.NewRequestWithContext(ctx, "POST", a.api.endpoint(method), &buffer)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := a.api.api.Do(req)
	if err != nil {
		return "", errors.New("telegram 媒体发送网络失败")
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var envelope apiEnvelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return "", err
	}
	if !envelope.OK {
		return "", &apiError{Method: method, Status: response.StatusCode, Code: envelope.ErrorCode, Message: envelope.Description}
	}
	var sent message
	if err = json.Unmarshal(envelope.Result, &sent); err != nil {
		return "", err
	}
	if sent.MessageID == 0 {
		return "", errors.New("telegram 媒体发送未返回消息 ID")
	}
	return strconv.FormatInt(sent.MessageID, 10), nil
}
