package platform

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"rua.plus/saber/internal/chat"
)

func mediaAttachment(data []byte, mime, name, kind string) chat.Attachment {
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	if kind == "voice" {
		kind = "audio"
	}
	if kind == "document" {
		kind = "file"
	}
	if !contains([]string{"image", "audio", "video", "file"}, kind) {
		kind = "file"
		for _, candidate := range []string{"image", "audio", "video"} {
			if strings.HasPrefix(mime, candidate+"/") {
				kind = candidate
			}
		}
	}
	return chat.Attachment{Kind: kind, Name: filepath.Base(name), MIMEType: mime, Data: data, Size: int64(len(data))}
}
func (p *connector) fetchMedia(ctx context.Context, path, mime, name, kind string) (chat.Attachment, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(p.cfg.Endpoint, "/")+"/"+strings.TrimLeft(path, "/"), nil)
	if err != nil {
		return chat.Attachment{}, err
	}
	if err = p.authorizeRequest(ctx, req); err != nil {
		return chat.Attachment{}, err
	}
	return p.readMedia(req, p.client, mime, name, kind)
}
func (p *connector) fetchExternalMedia(ctx context.Context, link, mime, name string, auth bool) (chat.Attachment, error) {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return chat.Attachment{}, errors.New("无效媒体 URL")
	}
	own, _ := url.Parse(p.cfg.Endpoint)
	ownHost := own != nil && u.Hostname() == own.Hostname()
	if auth && !ownHost && !p.trustedMediaHost(u.Hostname()) {
		return chat.Attachment{}, errors.New("拒绝向非平台媒体主机发送凭据")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if !ownHost {
		transport.DialContext = publicDial
	}
	client := *p.client
	client.Transport = transport
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, "GET", link, nil)
	if err != nil {
		return chat.Attachment{}, err
	}
	if auth {
		if err = p.authorizeRequest(ctx, req); err != nil {
			return chat.Attachment{}, err
		}
	}
	return p.readMedia(req, &client, mime, name, "")
}
func (p *connector) trustedMediaHost(host string) bool {
	switch p.name {
	case "whatsapp_cloud":
		return host == "lookaside.fbsbx.com" || strings.HasSuffix(host, ".fbcdn.net")
	case "sms":
		return host == "api.twilio.com" || strings.HasSuffix(host, ".twilio.com")
	case "teams":
		return strings.HasSuffix(host, ".botframework.com") || strings.HasSuffix(host, ".trafficmanager.net")
	case "google_chat":
		return host == "chat.googleapis.com" || host == "www.googleapis.com"
	}
	return false
}
func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, item := range ips {
		ip := item.IP
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
			return nil, errors.New("媒体 URL 指向非公开网络")
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("媒体主机无地址")
	}
	dial := net.Dialer{Timeout: 15 * time.Second}
	return dial.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}
func (p *connector) readMedia(req *http.Request, client *http.Client, mime, name, kind string) (chat.Attachment, error) {
	response, err := client.Do(req)
	if err != nil {
		return chat.Attachment{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return chat.Attachment{}, fmt.Errorf("媒体下载 HTTP %d", response.StatusCode)
	}
	if response.ContentLength > p.cfg.MaxMediaBytes {
		return chat.Attachment{}, errors.New("媒体大小超限")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, p.cfg.MaxMediaBytes+1))
	if err != nil {
		return chat.Attachment{}, err
	}
	if int64(len(data)) > p.cfg.MaxMediaBytes {
		return chat.Attachment{}, errors.New("媒体大小超限")
	}
	if mime == "" {
		mime = response.Header.Get("Content-Type")
	}
	return mediaAttachment(data, mime, name, kind), nil
}

func (p *connector) multipart(ctx context.Context, path string, fields map[string]string, field, name string, data []byte) (map[string]any, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, err
		}
	}
	part, err := writer.CreateFormFile(field, filepath.Base(name))
	if err != nil {
		return nil, err
	}
	if _, err = part.Write(data); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(p.cfg.Endpoint, "/")+"/"+path, &buffer)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if err = p.authorizeRequest(ctx, req); err != nil {
		return nil, err
	}
	response, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("媒体上传 HTTP %d: %s", response.StatusCode, p.redact(string(body)))
	}
	var out map[string]any
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if apiErr(out) {
		return nil, errors.New(p.redact(string(body)))
	}
	return out, nil
}

type uploadedMedia struct {
	Session    chat.Session   `json:"session"`
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	MIME       string         `json:"mime"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	ID         string         `json:"id,omitempty"`
	Path       string         `json:"path,omitempty"`
	Data       []byte         `json:"data,omitempty"`
	Item       map[string]any `json:"item,omitempty"`
}

// UploadFile 上传真实媒体，并把可信会话作用域保存在可恢复载荷内。
func (p *connector) UploadFile(ctx context.Context, file chat.FileUpload) ([]byte, error) {
	return p.uploadMedia(ctx, file, mediaAttachment(file.Data, "", file.Name, ""))
}

func (p *connector) uploadMedia(ctx context.Context, file chat.FileUpload, a chat.Attachment) ([]byte, error) {
	if err := p.validateSession(ctx, file.Session); err != nil {
		return nil, err
	}
	if len(file.Data) == 0 || int64(len(file.Data)) > p.cfg.MaxMediaBytes {
		return nil, errors.New("文件为空或大小超限")
	}
	upload := uploadedMedia{Session: file.Session, Name: filepath.Base(file.Name), Kind: a.Kind, MIME: a.MIMEType, DurationMS: a.DurationMS}
	switch p.name {
	case "mattermost":
		out, err := p.multipart(ctx, "api/v4/files", map[string]string{"channel_id": file.Session.Conversation}, "files", upload.Name, file.Data)
		if err != nil {
			return nil, err
		}
		items := arr(out["file_infos"])
		if len(items) == 0 {
			return nil, errors.New("mattermost 上传未返回 file ID")
		}
		upload.ID = str(obj(items[0])["id"])
	case "whatsapp_cloud":
		out, err := p.multipart(ctx, url.PathEscape(p.cfg.BotID)+"/media", map[string]string{"messaging_product": "whatsapp", "type": upload.MIME}, "file", upload.Name, file.Data)
		if err != nil {
			return nil, err
		}
		upload.ID = str(out["id"])
	case "feishu":
		path, field := "im/v1/files", "file"
		fields := map[string]string{"file_type": "stream", "file_name": upload.Name}
		if a.Kind == "image" {
			path, field = "im/v1/images", "image"
			fields = map[string]string{"image_type": "message"}
		}
		out, err := p.multipart(ctx, path, fields, field, upload.Name, file.Data)
		if err != nil {
			return nil, err
		}
		upload.ID = first(obj(out["data"]), "file_key", "image_key")
	case "qqbot":
		scope, target, ok := strings.Cut(file.Session.Conversation, ":")
		if !ok || (scope != "group" && scope != "c2c") {
			return nil, errors.New("QQ 媒体需 group/c2c 会话")
		}
		route := "groups"
		if scope == "c2c" {
			route = "users"
		}
		kind := 4
		switch a.Kind {
		case "image":
			kind = 1
		case "video":
			kind = 2
		case "audio":
			kind = 3
		}
		out, err := p.request(ctx, "POST", "v2/"+route+"/"+url.PathEscape(target)+"/files", map[string]any{"file_type": kind, "file_data": base64.StdEncoding.EncodeToString(file.Data), "srv_send_msg": false})
		if err != nil {
			return nil, err
		}
		upload.ID = str(out["file_info"])
	case "weixin":
		item, err := p.uploadWeixin(ctx, file.Session.Conversation, a)
		if err != nil {
			return nil, err
		}
		upload.Item = item
	case "relay":
		id, err := p.uploadRelayMedia(ctx, a)
		if err != nil {
			return nil, err
		}
		upload.ID = id
	case "signal", "simplex", "photon", "whatsapp", "buzz":
		root := p.state("media_root")
		if root == "" {
			return nil, errors.New("平台需要显式 shared_media_dir，不能把模型路径交给外部 daemon")
		}
		if err := os.MkdirAll(root, 0700); err != nil {
			return nil, err
		}
		path := filepath.Join(root, uuid.NewString()+filepath.Ext(upload.Name))
		if err := os.WriteFile(path, file.Data, 0600); err != nil {
			return nil, err
		}
		upload.Path = path
	case "bluebubbles", "email", "line", "teams", "google_chat", "ntfy", "sms", "wecom_callback":
		upload.Data = append([]byte(nil), file.Data...)
	default:
		return nil, fmt.Errorf("%s 协议不支持文件上传", p.name)
	}
	return json.Marshal(upload)
}

// SendUploadedFile 重用已上传的媒体，重新检查账号与目标，不再次上传已有 file ID。
func (p *connector) SendUploadedFile(ctx context.Context, reply chat.Reply, payload []byte) (string, error) {
	if err := p.validateSession(ctx, reply.Session); err != nil {
		return "", err
	}
	var upload uploadedMedia
	if err := json.Unmarshal(payload, &upload); err != nil {
		return "", err
	}
	if upload.Session != reply.Session {
		return "", errors.New("媒体载荷不能跨账号或会话使用")
	}
	target := reply.Session.Conversation
	switch p.name {
	case "relay":
		return p.sendRelay(ctx, map[string]any{"op": "send_media", "chat_id": target, "media_kind": upload.Kind, "source_url": strings.TrimRight(p.cfg.Endpoint, "/") + "/relay/media/" + url.PathEscape(upload.ID), "content": reply.Text, "reply_to": reply.ReplyTo, "metadata": map[string]string{"thread_id": reply.Session.Thread}})
	case "mattermost":
		out, err := p.request(ctx, "POST", "api/v4/posts", map[string]any{"channel_id": target, "message": reply.Text, "file_ids": []string{upload.ID}, "root_id": reply.Session.Thread, "pending_post_id": reply.TransactionID})
		return stableID(out), err
	case "whatsapp_cloud":
		kind := upload.Kind
		if kind == "file" {
			kind = "document"
		}
		media := map[string]string{"id": upload.ID}
		if kind != "audio" {
			media["caption"] = reply.Text
		}
		if kind == "document" {
			media["filename"] = upload.Name
		}
		out, err := p.request(ctx, "POST", url.PathEscape(p.cfg.BotID)+"/messages", map[string]any{"messaging_product": "whatsapp", "to": target, "type": kind, kind: media})
		items := arr(out["messages"])
		if len(items) > 0 {
			return stableID(obj(items[0])), err
		}
		return "", err
	case "feishu":
		kind, key := "file", "file_key"
		if upload.Kind == "image" {
			kind, key = "image", "image_key"
		}
		out, err := p.request(ctx, "POST", "im/v1/messages?receive_id_type=chat_id", map[string]any{"receive_id": target, "msg_type": kind, "content": jsonString(map[string]string{key: upload.ID}), "uuid": reply.TransactionID})
		return str(obj(out["data"])["message_id"]), err
	case "qqbot":
		scope, id, _ := strings.Cut(target, ":")
		route := "groups"
		if scope == "c2c" {
			route = "users"
		}
		out, err := p.request(ctx, "POST", "v2/"+route+"/"+url.PathEscape(id)+"/messages", map[string]any{"content": reply.Text, "msg_type": 7, "msg_id": reply.ReplyTo, "media": map[string]string{"file_info": upload.ID}})
		return stableID(out), err
	case "signal":
		params := map[string]any{"account": p.cfg.Phone, "message": reply.Text, "attachments": []string{upload.Path}}
		if strings.HasPrefix(target, "group:") {
			params["groupId"] = strings.TrimPrefix(target, "group:")
		} else {
			params["recipients"] = []string{target}
		}
		out, err := p.request(ctx, "POST", "api/v1/rpc", map[string]any{"jsonrpc": "2.0", "id": reply.TransactionID, "method": "send", "params": params})
		return str(obj(out["result"])["timestamp"]), err
	case "photon":
		out, err := p.request(ctx, "POST", "send-attachment", map[string]string{"spaceId": target, "path": upload.Path, "name": upload.Name, "mimeType": upload.MIME, "caption": reply.Text, "kind": "attachment"})
		return stableID(out), err
	case "whatsapp":
		kind := upload.Kind
		if kind == "file" {
			kind = "document"
		}
		out, err := p.request(ctx, "POST", "send-media", map[string]string{"chatId": target, "filePath": upload.Path, "mediaType": kind, "caption": reply.Text, "fileName": upload.Name})
		return stableID(out), err
	case "buzz":
		args := []string{"messages", "send", "--channel", target, "--file", upload.Path, "--content", "-"}
		data, err := p.cli(ctx, args, reply.Text)
		if err != nil {
			return "", err
		}
		var out map[string]any
		err = json.Unmarshal(data, &out)
		return stableID(out), err
	case "simplex":
		p.mu.Lock()
		send := p.sendSocket
		p.mu.Unlock()
		if send == nil {
			return "", errors.New("SimpleX daemon 未连接")
		}
		destination := "@" + target
		if strings.HasPrefix(target, "group:") {
			destination = "#" + strings.TrimPrefix(target, "group:")
		}
		out, err := send(ctx, map[string]any{"cmd": "/_send " + destination + " json " + jsonString([]any{map[string]any{"filePath": upload.Path, "msgContent": map[string]string{"type": "file", "text": reply.Text}}})})
		return stableID(obj(out["resp"])), err
	case "weixin":
		contextToken := p.state("context:" + target)
		if contextToken == "" {
			return "", errors.New("weixin context_token 缺失")
		}
		out, err := p.request(ctx, "POST", "ilink/bot/sendmessage", map[string]any{"base_info": map[string]string{"channel_version": "2.2.0"}, "msg": map[string]any{"to_user_id": target, "from_user_id": "", "client_id": reply.TransactionID, "message_type": 2, "message_state": 2, "context_token": contextToken, "item_list": []any{upload.Item}}})
		return first(out, "message_id", "client_id"), err
	case "bluebubbles":
		out, err := p.multipart(ctx, "api/v1/message/attachment", map[string]string{"chatGuid": target, "name": upload.Name, "tempGuid": reply.TransactionID, "method": "apple-script"}, "attachment", upload.Name, upload.Data)
		return str(obj(out["data"])["guid"]), err
	case "email":
		return p.sendEmailMedia(ctx, reply, upload)
	case "line", "sms", "teams", "google_chat", "ntfy":
		return p.sendURLMedia(ctx, reply, upload)
	case "wecom_callback":
		out, err := p.multipart(ctx, "media/upload?type=file", nil, "media", upload.Name, upload.Data)
		if err != nil {
			return "", err
		}
		out, err = p.request(ctx, "POST", "message/send", map[string]any{"touser": target, "msgtype": "file", "agentid": p.cfg.AppID, "file": map[string]string{"media_id": str(out["media_id"])}})
		return str(out["msgid"]), err
	default:
		return "", fmt.Errorf("%s 不支持媒体出站", p.name)
	}
}

// SendImage 用平台实际上传与发送协议交付图片。
func (p *connector) SendImage(ctx context.Context, reply chat.Reply, data []byte, mime, name string, width, height int) (string, error) {
	return p.sendAttachment(ctx, reply, mediaAttachment(data, mime, name, "image"))
}

// SendAudio 用平台实际媒体协议交付语音。
func (p *connector) SendAudio(ctx context.Context, reply chat.Reply, a chat.Attachment) (string, error) {
	a.Kind = "audio"
	return p.sendAttachment(ctx, reply, a)
}

// SendVideo 用平台实际媒体协议交付视频。
func (p *connector) SendVideo(ctx context.Context, reply chat.Reply, a chat.Attachment) (string, error) {
	a.Kind = "video"
	return p.sendAttachment(ctx, reply, a)
}
func (p *connector) sendAttachment(ctx context.Context, reply chat.Reply, a chat.Attachment) (string, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}
	if len(a.Data) == 0 {
		if strings.HasPrefix(a.URL, "data:") {
			_, encoded, ok := strings.Cut(a.URL, ";base64,")
			if !ok {
				return "", errors.New("无效 Data URL")
			}
			data, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return "", err
			}
			a.Data = data
		} else {
			downloaded, err := p.fetchExternalMedia(ctx, a.URL, a.MIMEType, a.Name, false)
			if err != nil {
				return "", err
			}
			a.Data = downloaded.Data
		}
	}
	if a.MIMEType == "" {
		a.MIMEType = http.DetectContentType(a.Data)
	}
	payload, err := p.uploadMedia(ctx, chat.FileUpload{Session: reply.Session, ReplyTo: reply.ReplyTo, Name: a.Name, Data: a.Data}, a)
	if err != nil {
		return "", err
	}
	return p.SendUploadedFile(ctx, reply, payload)
}

func decryptWecomAttachment(a chat.Attachment, key string) (chat.Attachment, error) {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return a, err
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return a, err
	}
	if len(a.Data) == 0 || len(a.Data)%aes.BlockSize != 0 {
		return a, errors.New("WeCom 媒体密文长度无效")
	}
	plain := make([]byte, len(a.Data))
	cipher.NewCBCDecrypter(block, raw[:16]).CryptBlocks(plain, a.Data)
	plain, err = unpad(plain, 32)
	if err != nil {
		return a, err
	}
	return mediaAttachment(plain, "", a.Name, a.Kind), nil
}
func (p *connector) weixinMedia(ctx context.Context, item map[string]any) (chat.Attachment, error) {
	n, _ := item["type"].(float64)
	key, kind, mime := "file_item", "file", ""
	switch int(n) {
	case 2:
		key, kind, mime = "image_item", "image", "image/jpeg"
	case 3:
		key, kind, mime = "voice_item", "audio", "audio/silk"
	case 5:
		key, kind, mime = "video_item", "video", "video/mp4"
	}
	payload := obj(item[key])
	media := obj(payload["media"])
	link := str(media["full_url"])
	if param := str(media["encrypt_query_param"]); param != "" {
		link = "https://novac2c.cdn.weixin.qq.com/c2c/download?encrypted_query_param=" + url.QueryEscape(param)
	}
	u, err := url.Parse(link)
	if err != nil || u.Hostname() != "novac2c.cdn.weixin.qq.com" {
		return chat.Attachment{}, errors.New("weixin 媒体必须来自固定腾讯 CDN")
	}
	attachment, err := p.fetchExternalMedia(ctx, link, mime, str(payload["file_name"]), false)
	if err != nil {
		return attachment, err
	}
	encoded := str(media["aes_key"])
	if encoded == "" {
		attachment.Kind = kind
		return attachment, nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return attachment, err
	}
	if len(raw) == 32 {
		raw, err = hex.DecodeString(string(raw))
		if err != nil {
			return attachment, err
		}
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return attachment, err
	}
	if len(attachment.Data)%16 != 0 {
		return attachment, errors.New("weixin 密文长度无效")
	}
	plain := make([]byte, len(attachment.Data))
	for offset := 0; offset < len(plain); offset += 16 {
		block.Decrypt(plain[offset:offset+16], attachment.Data[offset:offset+16])
	}
	plain, err = unpad(plain, 16)
	if err != nil {
		return attachment, err
	}
	return mediaAttachment(plain, mime, attachment.Name, kind), nil
}
func (p *connector) uploadWeixin(ctx context.Context, target string, a chat.Attachment) (map[string]any, error) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	n := 16 - len(a.Data)%16
	plain := append(append([]byte(nil), a.Data...), bytes.Repeat([]byte{byte(n)}, n)...)
	encrypted := make([]byte, len(plain))
	for offset := 0; offset < len(plain); offset += 16 {
		block.Encrypt(encrypted[offset:offset+16], plain[offset:offset+16])
	}
	hash := md5.Sum(a.Data)
	filekey := uuid.NewString()
	mediaType, itemType, itemKey := 3, 4, "file_item"
	switch a.Kind {
	case "image":
		mediaType, itemType, itemKey = 1, 2, "image_item"
	case "video":
		mediaType, itemType, itemKey = 2, 5, "video_item"
	case "audio":
		mediaType, itemType, itemKey = 4, 3, "voice_item"
	}
	response, err := p.request(ctx, "POST", "ilink/bot/getuploadurl", map[string]any{"base_info": map[string]string{"channel_version": "2.2.0"}, "filekey": filekey, "media_type": mediaType, "to_user_id": target, "rawsize": len(a.Data), "rawfilemd5": hex.EncodeToString(hash[:]), "filesize": len(encrypted), "no_need_thumb": true, "aeskey": hex.EncodeToString(key)})
	if err != nil {
		return nil, err
	}
	link := str(response["upload_full_url"])
	if link == "" {
		link = "https://novac2c.cdn.weixin.qq.com/c2c/upload?encrypted_query_param=" + url.QueryEscape(str(response["upload_param"])) + "&filekey=" + url.QueryEscape(filekey)
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.Hostname() != "novac2c.cdn.weixin.qq.com" {
		return nil, errors.New("weixin 上传 URL 不属于固定 CDN")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", link, bytes.NewReader(encrypted))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	out, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = out.Body.Close() }()
	param := out.Header.Get("x-encrypted-param")
	if out.StatusCode != 200 || param == "" {
		return nil, fmt.Errorf("weixin CDN 上传 HTTP %d 或缺少 encrypted param", out.StatusCode)
	}
	fields := map[string]any{"media": map[string]any{"encrypt_query_param": param, "aes_key": base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key))), "encrypt_type": 1}}
	switch a.Kind {
	case "image":
		fields["mid_size"] = len(encrypted)
	case "video":
		fields["video_size"] = len(encrypted)
		fields["play_length"] = a.DurationMS / 1000
		fields["video_md5"] = hex.EncodeToString(hash[:])
	case "audio":
		fields["playtime"] = a.DurationMS
	default:
		fields["file_name"] = a.Name
		fields["len"] = fmt.Sprint(len(a.Data))
	}
	return map[string]any{"type": itemType, itemKey: fields}, nil
}
