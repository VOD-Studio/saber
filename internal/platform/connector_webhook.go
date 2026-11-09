package platform

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"rua.plus/saber/internal/chat"
)

func (p *connector) webhook(w http.ResponseWriter, r *http.Request) {
	if p.name == "a2a" {
		p.a2aRPC(w, r)
		return
	}
	if p.serveMedia(w, r) || p.raftAuxiliary(w, r) || p.webhookResult(w, r) {
		return
	}
	if r.URL.Path != p.cfg.WebhookPath {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		switch p.name {
		case "whatsapp_cloud":
			if equalSecret(r.URL.Query().Get("hub.verify_token"), p.cfg.VerifyToken) && r.URL.Query().Get("hub.mode") == "subscribe" {
				_, _ = io.WriteString(w, r.URL.Query().Get("hub.challenge"))
				return
			}
		case "msgraph_webhook":
			if token := r.URL.Query().Get("validationToken"); token != "" {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = io.WriteString(w, token)
				return
			}
		case "wecom_callback":
			plain, err := p.decryptWecom(r, r.URL.Query().Get("echostr"))
			if err == nil {
				_, _ = w.Write(plain)
				return
			}
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if p.name == "sms" {
		p.smsWebhook(w, r, body)
		return
	}
	if p.name == "wecom_callback" {
		p.wecomWebhook(w, r, body)
		return
	}
	var payload map[string]any
	if err = json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if err = p.authenticateCallback(r, body, payload); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch p.name {
	case "feishu":
		if encrypted := str(payload["encrypt"]); encrypted != "" {
			plain, err := decryptFeishu(p.cfg.EncodingAESKey, encrypted)
			if err != nil {
				http.Error(w, "invalid encryption", 400)
				return
			}
			if err = json.Unmarshal(plain, &payload); err != nil {
				http.Error(w, "invalid event", 400)
				return
			}
		}
		if challenge := str(payload["challenge"]); challenge != "" {
			writeJSON(w, 200, map[string]string{"challenge": challenge})
			return
		}
	case "qqbot":
		if n, ok := payload["op"].(float64); ok && n == 13 {
			plain := obj(payload["d"])
			timestamp := str(plain["event_ts"])
			token := str(plain["plain_token"])
			key := qqPrivateKey(p.cfg.Secret)
			signature := ed25519.Sign(key, []byte(timestamp+token))
			writeJSON(w, 200, map[string]string{"plain_token": token, "signature": hex.EncodeToString(signature)})
			return
		}
	}
	messages, err := p.decodeEvents(r.Context(), payload)
	if err != nil {
		status := 400
		if isDenied(err) {
			status = 403
		}
		http.Error(w, p.redact(err.Error()), status)
		return
	}
	receipts := make([]string, 0, len(messages))
	for _, msg := range messages {
		if err = p.accept(r.Context(), msg); err != nil {
			status := 403
			if strings.Contains(err.Error(), "队列") {
				status = 503
			}
			http.Error(w, p.redact(err.Error()), status)
			return
		}
		receipts = append(receipts, p.receiptID(msg))
	}
	writeJSON(w, 200, map[string]any{"ok": true, "receipts": receipts})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (p *connector) authenticateCallback(r *http.Request, body []byte, payload map[string]any) error {
	switch p.name {
	case "whatsapp_cloud":
		if verifyMAC(p.cfg.Secret, body, r.Header.Get("X-Hub-Signature-256"), "hex") {
			return nil
		}
	case "line":
		if verifyMAC(p.cfg.Secret, body, r.Header.Get("X-Line-Signature"), "base64") {
			return nil
		}
	case "feishu":
		timestamp, nonce := r.Header.Get("X-Lark-Request-Timestamp"), r.Header.Get("X-Lark-Request-Nonce")
		if p.cfg.EncodingAESKey != "" {
			sum := sha256.Sum256(append([]byte(timestamp+nonce+p.cfg.EncodingAESKey), body...))
			if equalSecret(hex.EncodeToString(sum[:]), r.Header.Get("X-Lark-Signature")) {
				return nil
			}
		} else if equalSecret(first(obj(payload["header"]), "token"), p.cfg.VerifyToken) || equalSecret(str(payload["token"]), p.cfg.VerifyToken) {
			return nil
		}
	case "teams", "google_chat":
		return p.verifyJWT(r.Context(), r.Header.Get("Authorization"))
	case "qqbot":
		signature, err := hex.DecodeString(r.Header.Get("X-Signature-Ed25519"))
		if err == nil {
			data := append([]byte(r.Header.Get("X-Signature-Timestamp")), body...)
			if ed25519.Verify(qqPrivateKey(p.cfg.Secret).Public().(ed25519.PublicKey), data, signature) {
				return nil
			}
		}
	case "bluebubbles":
		if equalSecret(r.URL.Query().Get("password"), p.cfg.Password) {
			return nil
		}
	case "msgraph_webhook":
		items := arr(payload["value"])
		if len(items) == 0 {
			return errors.New("msgraph 通知为空")
		}
		for _, item := range items {
			if !equalSecret(str(obj(item)["clientState"]), p.cfg.VerifyToken) {
				return errors.New("msgraph clientState 不匹配")
			}
		}
		return nil
	case "raft":
		if equalSecret(r.Header.Get("x-raft-bridge-token"), p.cfg.Token) {
			return nil
		}
	default:
		if equalSecret(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), p.cfg.Token) {
			return nil
		}
	}
	return errors.New("平台回调认证失败")
}
func qqPrivateKey(secret string) ed25519.PrivateKey {
	key := []byte(secret)
	for len(key) < 32 {
		key = append(key, []byte(secret)...)
	}
	return ed25519.NewKeyFromSeed(key[:32])
}
func decryptFeishu(secret, encoded string) ([]byte, error) {
	key := sha256.Sum256([]byte(secret))
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) < 32 || len(data)%16 != 0 {
		return nil, errors.New("feishu 密文无效")
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(data)-16)
	cipher.NewCBCDecrypter(block, data[:16]).CryptBlocks(plain, data[16:])
	return unpad(plain, 16)
}
func unpad(data []byte, block int) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("padding 为空")
	}
	n := int(data[len(data)-1])
	if n < 1 || n > block || n > len(data) {
		return nil, errors.New("padding 无效")
	}
	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return nil, errors.New("padding 无效")
		}
	}
	return data[:len(data)-n], nil
}
func (p *connector) decryptWecom(r *http.Request, encrypted string) ([]byte, error) {
	q := r.URL.Query()
	parts := []string{p.cfg.VerifyToken, q.Get("timestamp"), q.Get("nonce"), encrypted}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	if !equalSecret(hex.EncodeToString(sum[:]), q.Get("msg_signature")) {
		return nil, errors.New("WeCom 签名无效")
	}
	key, err := base64.StdEncoding.DecodeString(p.cfg.EncodingAESKey + "=")
	if err != nil || len(key) != 32 {
		return nil, errors.New("WeCom AES key 无效")
	}
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil || len(data) == 0 || len(data)%16 != 0 {
		return nil, errors.New("WeCom 密文无效")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, key[:16]).CryptBlocks(plain, data)
	plain, err = unpad(plain, 32)
	if err != nil || len(plain) < 20 {
		return nil, errors.New("WeCom 明文无效")
	}
	n := int(binary.BigEndian.Uint32(plain[16:20]))
	if n > len(plain)-20 {
		return nil, errors.New("WeCom 长度无效")
	}
	if !equalSecret(string(plain[20+n:]), p.cfg.TenantID) {
		return nil, errors.New("WeCom 租户不匹配")
	}
	return plain[20 : 20+n], nil
}
func (p *connector) wecomWebhook(w http.ResponseWriter, r *http.Request, body []byte) {
	var envelope struct {
		Encrypt string `xml:"Encrypt"`
	}
	if err := xml.Unmarshal(body, &envelope); err != nil {
		http.Error(w, "invalid XML", 400)
		return
	}
	plain, err := p.decryptWecom(r, envelope.Encrypt)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var msg struct {
		ID      string `xml:"MsgId"`
		From    string `xml:"FromUserName"`
		To      string `xml:"ToUserName"`
		Content string `xml:"Content"`
		Type    string `xml:"MsgType"`
		MediaID string `xml:"MediaId"`
	}
	if err = xml.Unmarshal(plain, &msg); err != nil {
		http.Error(w, "invalid event", 400)
		return
	}
	message := chat.Message{ID: msg.ID, SenderID: msg.From, Text: msg.Content, Direct: true, Session: chat.Session{Conversation: msg.From}}
	if msg.MediaID != "" {
		attachment, err := p.fetchAuthorizedMedia(r.Context(), message, "media/get?media_id="+url.QueryEscape(msg.MediaID), "", "", "")
		if err != nil {
			http.Error(w, "media download failed", http.StatusBadGateway)
			return
		}
		message.Attachments = []chat.Attachment{attachment}
	}
	if err = p.accept(r.Context(), message); err != nil {
		http.Error(w, p.redact(err.Error()), http.StatusForbidden)
		return
	}
	_, _ = io.WriteString(w, "success")
}
func (p *connector) smsWebhook(w http.ResponseWriter, r *http.Request, body []byte) {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	signed := strings.TrimRight(p.cfg.PublicURL, "/") + p.cfg.WebhookPath
	for _, k := range keys {
		signed += k + values.Get(k)
	}
	mac := hmac.New(sha1.New, []byte(p.cfg.Secret))
	_, _ = mac.Write([]byte(signed))
	if !equalSecret(base64.StdEncoding.EncodeToString(mac.Sum(nil)), r.Header.Get("X-Twilio-Signature")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	msg := chat.Message{ID: values.Get("MessageSid"), SenderID: values.Get("From"), Direct: true, Text: values.Get("Body"), Session: chat.Session{Conversation: values.Get("From")}}
	for i := range 10 {
		media := values.Get(fmt.Sprintf("MediaUrl%d", i))
		if media == "" {
			break
		}
		attachment, err := p.fetchExternalMedia(r.Context(), media, values.Get(fmt.Sprintf("MediaContentType%d", i)), "", false)
		if err != nil {
			http.Error(w, "media download failed", http.StatusBadGateway)
			return
		}
		msg.Attachments = append(msg.Attachments, attachment)
	}
	if err = p.accept(r.Context(), msg); err != nil {
		http.Error(w, p.redact(err.Error()), http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, `<?xml version="1.0"?><Response/>`)
}

func (p *connector) replyLocal(ctx context.Context, reply chat.Reply) (string, error) {
	if err := p.setState(ctx, "reply:"+reply.Session.Conversation+":"+reply.ReplyTo, reply.Text); err != nil {
		return "", err
	}
	return reply.TransactionID, nil
}
func (p *connector) waitReply(ctx context.Context, id string) (string, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state, detail string
		err := p.db.QueryRowContext(ctx, "SELECT state,error FROM inbox WHERE id=?", id).Scan(&state, &detail)
		if err != nil {
			return "", err
		}
		switch state {
		case "completed":
			return state, nil
		case "failed", "interrupted", "canceled":
			return "", errors.New(detail)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
