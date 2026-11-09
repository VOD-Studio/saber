package platform

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"rua.plus/saber/internal/chat"
)

type hostedMedia struct {
	Data    []byte `json:"data"`
	MIME    string `json:"mime"`
	Expires int64  `json:"expires"`
}

func (p *connector) hostMedia(ctx context.Context, upload uploadedMedia) (string, error) {
	if p.cfg.PublicURL == "" || p.cfg.Listen == "" {
		return "", errors.New("平台媒体需要 public_url 和 listen，外部服务必须能访问该地址")
	}
	if len(upload.Data) == 0 || int64(len(upload.Data)) > p.cfg.MaxMediaBytes {
		return "", errors.New("媒体为空或大小超限")
	}
	id := uuid.NewString() + uuid.NewString()
	record := hostedMedia{Data: upload.Data, MIME: upload.MIME, Expires: time.Now().Add(time.Hour).Unix()}
	if err := p.setState(ctx, "hosted:"+id, jsonString(record)); err != nil {
		return "", err
	}
	return strings.TrimRight(p.cfg.PublicURL, "/") + "/media/" + id, nil
}
func (p *connector) serveMedia(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/media/") {
		return false
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(405)
		return true
	}
	id := strings.TrimPrefix(r.URL.Path, "/media/")
	if strings.ContainsAny(id, "/\\\x00") {
		http.NotFound(w, r)
		return true
	}
	var record hostedMedia
	if err := json.Unmarshal([]byte(p.state("hosted:"+id)), &record); err != nil || record.Expires < time.Now().Unix() {
		_, _ = p.db.ExecContext(r.Context(), "DELETE FROM state WHERE key=?", "hosted:"+id)
		http.NotFound(w, r)
		return true
	}
	w.Header().Set("Content-Type", record.MIME)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	if r.Method == "GET" {
		_, _ = w.Write(record.Data)
	}
	return true
}
func (p *connector) sendURLMedia(ctx context.Context, reply chat.Reply, upload uploadedMedia) (string, error) {
	if p.name == "google_chat" {
		return p.sendGoogleMedia(ctx, reply, upload)
	}
	if p.name == "ntfy" {
		return p.sendNtfyMedia(ctx, reply, upload)
	}
	link, err := p.hostMedia(ctx, upload)
	if err != nil {
		return "", err
	}
	target := reply.Session.Conversation
	var out map[string]any
	switch p.name {
	case "line":
		kind := upload.Kind
		if kind == "file" {
			return "", errors.New("LINE Bot API 不支持任意文件推送")
		}
		message := map[string]any{"type": kind, "originalContentUrl": link}
		if kind == "audio" {
			duration := upload.DurationMS
			if duration <= 0 {
				return "", errors.New("LINE audio 必须提供真实 duration_ms")
			}
			message["duration"] = duration
		} else {
			message["previewImageUrl"] = link
		}
		out, err = p.request(ctx, "POST", "v2/bot/message/push", map[string]any{"to": target, "messages": []any{message}})
	case "sms":
		out, err = p.request(ctx, "POST", "Accounts/"+url.PathEscape(p.cfg.AppID)+"/Messages.json", url.Values{"From": {p.cfg.Phone}, "To": {target}, "Body": {reply.Text}, "MediaUrl": {link}})
	case "teams":
		out, err = p.request(ctx, "POST", "v3/conversations/"+url.PathEscape(target)+"/activities", map[string]any{"type": "message", "text": reply.Text, "attachments": []any{map[string]string{"contentType": upload.MIME, "contentUrl": link, "name": upload.Name}}})
	default:
		return "", fmt.Errorf("%s 没有 URL 媒体协议", p.name)
	}
	return stableID(out), err
}
func (p *connector) sendGoogleMedia(ctx context.Context, reply chat.Reply, upload uploadedMedia) (string, error) {
	target := reply.Session.Conversation
	if !strings.HasPrefix(target, "spaces/") || strings.Contains(target, "..") {
		return "", errors.New("google_chat space 无效")
	}
	u, err := url.Parse(p.cfg.Endpoint)
	if err != nil {
		return "", err
	}
	u.Path = "/upload/v1/" + target + "/attachments:upload"
	q := u.Query()
	q.Set("filename", upload.Name)
	q.Set("uploadType", "media")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(upload.Data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", upload.MIME)
	if err = p.authorizeRequest(ctx, req); err != nil {
		return "", err
	}
	response, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("google_chat 附件上传 HTTP %d", response.StatusCode)
	}
	var resource map[string]any
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&resource); err != nil {
		return "", err
	}
	reference := obj(resource["attachmentDataRef"])
	if str(reference["resourceName"]) == "" {
		return "", errors.New("google_chat 未返回 attachmentDataRef")
	}
	out, err := p.request(ctx, "POST", target+"/messages", map[string]any{"text": reply.Text, "attachment": []any{map[string]any{"attachmentDataRef": reference}}})
	return str(out["name"]), err
}
func decodeA2AFile(encoded, mime, name string, limit int64) (chat.Attachment, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return chat.Attachment{}, err
	}
	if int64(len(data)) > limit {
		return chat.Attachment{}, errors.New("A2A 文件超限")
	}
	return mediaAttachment(data, mime, name, ""), nil
}
