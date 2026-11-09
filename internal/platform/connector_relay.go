package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/websocket"
	"rua.plus/saber/internal/chat"
)

func (p *connector) relayToken() string {
	signed := fmt.Sprintf("%s:%d", p.Account(), time.Now().Add(5*time.Minute).Unix())
	mac := hmac.New(sha256.New, []byte(p.cfg.Secret))
	_, _ = mac.Write([]byte(signed))
	return base64.RawURLEncoding.EncodeToString([]byte(signed + ":" + hex.EncodeToString(mac.Sum(nil))))
}
func (p *connector) receiveRelay(ctx context.Context) error {
	options, err := websocket.NewConfig(p.cfg.WebSocketURL, p.cfg.Endpoint)
	if err != nil {
		return err
	}
	options.Header.Set("Authorization", "Bearer "+p.relayToken())
	options.Dialer = &net.Dialer{Timeout: 15 * time.Second}
	ws, err := websocket.DialConfig(options)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()
	ws.MaxPayloadBytes = 4 << 20
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-run.Done(); _ = ws.Close() }()
	var writer, pendingMu sync.Mutex
	pending := make(map[string]chan map[string]any)
	write := func(frame map[string]any) error {
		writer.Lock()
		defer writer.Unlock()
		return websocket.JSON.Send(ws, frame)
	}
	if err = write(map[string]any{"type": "hello", "platform": p.cfg.AppID, "botId": p.cfg.BotID}); err != nil {
		return err
	}
	_ = ws.SetReadDeadline(time.Now().Add(20 * time.Second))
	var frame map[string]any
	if err = websocket.JSON.Receive(ws, &frame); err != nil {
		return err
	}
	_ = ws.SetReadDeadline(time.Time{})
	if str(frame["type"]) != "descriptor" {
		return errors.New("relay 缺少 capability descriptor")
	}
	descriptor := obj(frame["descriptor"])
	if len(descriptor) == 0 {
		descriptor = obj(frame["capabilities"])
	}
	if len(descriptor) == 0 {
		descriptor = frame
	}
	if n, _ := descriptor["contract_version"].(float64); n != 1 {
		return errors.New("relay contract_version 不是固定快照支持的 1")
	}
	if str(descriptor["platform"]) != p.cfg.AppID {
		return errors.New("relay descriptor 平台与可信配置不匹配")
	}
	caps := chat.Capabilities{Edit: descriptor["supports_edit"] == true, Reply: descriptor["supports_threads"] == true}
	ops := arr(descriptor["supported_ops"])
	for _, op := range ops {
		if str(op) == "send_media" {
			caps.Audio = true
			caps.Video = true
			caps.Files = true
		}
	}
	send := func(ctx context.Context, action map[string]any) (map[string]any, error) {
		id := uuid.NewString()
		wait := make(chan map[string]any, 1)
		pendingMu.Lock()
		pending[id] = wait
		pendingMu.Unlock()
		defer func() { pendingMu.Lock(); delete(pending, id); pendingMu.Unlock() }()
		if err := write(map[string]any{"type": "outbound", "requestId": id, "platform": p.cfg.AppID, "botId": p.cfg.BotID, "action": action}); err != nil {
			return nil, err
		}
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case result := <-wait:
			if apiErr(result) {
				return nil, fmt.Errorf("relay 操作被拒绝: %s", p.redact(jsonString(result)))
			}
			return result, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-run.Done():
			return nil, errors.New("relay 已断开，操作结果未知；不自动重放")
		case <-timer.C:
			return nil, errors.New("relay 操作回执超时，结果未知；不自动重放")
		}
	}
	p.mu.Lock()
	p.conn = ws
	p.sendSocket = send
	p.relayCaps = caps
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.conn = nil; p.sendSocket = nil; p.mu.Unlock() }()
	for {
		var frame map[string]any
		if err = websocket.JSON.Receive(ws, &frame); err != nil {
			return err
		}
		switch str(frame["type"]) {
		case "outbound_result":
			pendingMu.Lock()
			wait := pending[str(frame["requestId"])]
			pendingMu.Unlock()
			if wait != nil {
				select {
				case wait <- obj(frame["result"]):
				default:
				}
			}
		case "inbound":
			event := obj(frame["event"])
			source := obj(event["source"])
			if platform := str(source["platform"]); platform != "" && platform != p.cfg.AppID {
				return errors.New("relay 入站平台与配置不匹配")
			}
			msg := chat.Message{ID: first(event, "message_id", "id"), SenderID: str(source["user_id"]), Direct: str(source["chat_type"]) == "dm", Text: str(event["text"]), Session: chat.Session{Conversation: str(source["chat_id"]), Thread: str(source["thread_id"])}, ReplyTo: str(event["reply_to_message_id"])}
			if err := p.incomingAllowed(msg); err != nil {
				continue
			}
			for i, raw := range arr(event["media_urls"]) {
				link := str(raw)
				mime := ""
				types := arr(event["media_types"])
				if i < len(types) {
					mime = str(types[i])
				}
				attachment, err := p.fetchRelayMedia(ctx, link, mime)
				if err != nil {
					return err
				}
				msg.Attachments = append(msg.Attachments, attachment)
			}
			if err = p.accept(ctx, msg); err != nil && !isDenied(err) {
				return err
			}
			if bufferID := str(frame["bufferId"]); bufferID != "" {
				if err = write(map[string]any{"type": "inbound_ack", "bufferId": bufferID}); err != nil {
					return err
				}
			}
		case "interrupt_inbound":
			conversation := str(frame["chat_id"])
			rows, err := p.db.QueryContext(ctx, "SELECT value FROM state WHERE key LIKE 'message-session:%'")
			if err != nil {
				return err
			}
			var ids []string
			for rows.Next() {
				var value string
				if err = rows.Scan(&value); err != nil {
					_ = rows.Close()
					return err
				}
				var id, target string
				id, target, _ = strings.Cut(value, "\x00")
				if target == conversation {
					ids = append(ids, id)
				}
			}
			_ = rows.Close()
			for _, id := range ids {
				p.cancelMessage(id)
			}
		case "passthrough_forward":
			messages, err := p.relayPassthrough(ctx, obj(frame["forward"]))
			if err != nil {
				return err
			}
			for _, msg := range messages {
				if err = p.accept(ctx, msg); err != nil && !isDenied(err) {
					return err
				}
			}
			if id := str(frame["bufferId"]); id != "" {
				if err = write(map[string]any{"type": "inbound_ack", "bufferId": id}); err != nil {
					return err
				}
			}
		}
	}
}
func (p *connector) sendRelay(ctx context.Context, action map[string]any) (string, error) {
	p.mu.Lock()
	send := p.sendSocket
	p.mu.Unlock()
	if send == nil {
		return "", errors.New("relay 未连接")
	}
	out, err := send(ctx, action)
	if err != nil {
		return "", err
	}
	if success, ok := out["success"].(bool); !ok || !success {
		return "", errors.New("relay 未确认发送成功")
	}
	return first(out, "message_id", "messageId"), nil
}
func (p *connector) fetchRelayMedia(ctx context.Context, link, mime string) (chat.Attachment, error) {
	u, err := url.Parse(link)
	if err != nil {
		return chat.Attachment{}, err
	}
	base, err := url.Parse(p.cfg.Endpoint)
	if err != nil {
		return chat.Attachment{}, err
	}
	if u.Host == base.Host && u.Scheme == base.Scheme && strings.HasPrefix(u.Path, "/relay/media/") {
		return p.fetchExternalMedia(ctx, link, mime, "", true)
	}
	return p.fetchExternalMedia(ctx, link, mime, "", false)
}
