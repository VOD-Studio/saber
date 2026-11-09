package platform

import (
	"context"
	"encoding/json"
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

func (p *connector) receiveSocket(ctx context.Context) error {
	endpoint := p.cfg.WebSocketURL
	if endpoint == "" {
		switch p.name {
		case "mattermost":
			u, err := url.Parse(p.cfg.Endpoint)
			if err != nil {
				return err
			}
			if u.Scheme == "https" {
				u.Scheme = "wss"
			} else {
				u.Scheme = "ws"
			}
			u.Path = strings.TrimRight(u.Path, "/") + "/api/v4/websocket"
			endpoint = u.String()
		case "wecom":
			endpoint = "wss://openws.work.weixin.qq.com"
		case "qqbot":
			out, err := p.request(ctx, "GET", "gateway/bot", nil)
			if err != nil {
				return err
			}
			endpoint = str(out["url"])
		case "dingtalk":
			out, err := p.request(ctx, "POST", "v1.0/gateway/connections/open", map[string]any{"clientId": p.cfg.AppID, "clientSecret": p.cfg.Secret, "subscriptions": []any{map[string]string{"type": "CALLBACK", "topic": "/v1.0/im/bot/messages/get"}}, "ua": "saber-go"})
			if err != nil {
				return err
			}
			endpoint = str(out["endpoint"]) + "?ticket=" + url.QueryEscape(str(out["ticket"]))
		}
	}
	if endpoint == "" {
		return errors.New("平台缺少 websocket_url")
	}
	options, err := websocket.NewConfig(endpoint, p.cfg.Endpoint)
	if err != nil {
		return err
	}
	options.Dialer = &net.Dialer{Timeout: 15 * time.Second}
	if p.cfg.Token != "" {
		options.Header.Set("Authorization", "Bearer "+p.cfg.Token)
	}
	ws, err := websocket.DialConfig(options)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()
	ws.MaxPayloadBytes = 4 << 20
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-run.Done():
			_ = ws.Close()
		case <-closed:
		}
	}()
	var writeMu, pendingMu sync.Mutex
	pending := make(map[string]chan map[string]any)
	write := func(value map[string]any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return websocket.JSON.Send(ws, value)
	}
	send := func(ctx context.Context, payload map[string]any) (map[string]any, error) {
		id := uuid.NewString()
		switch p.name {
		case "simplex":
			payload["corrId"] = id
		case "wecom":
			headers := obj(payload["headers"])
			if str(headers["req_id"]) != "" {
				id = str(headers["req_id"])
			}
			headers["req_id"] = id
			payload["headers"] = headers
		case "mattermost":
			payload["seq"] = time.Now().UnixMilli()
		}
		if p.name == "mattermost" {
			return map[string]any{}, write(payload)
		}
		channel := make(chan map[string]any, 1)
		pendingMu.Lock()
		pending[id] = channel
		pendingMu.Unlock()
		defer func() { pendingMu.Lock(); delete(pending, id); pendingMu.Unlock() }()
		if err := write(payload); err != nil {
			return nil, err
		}
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case out := <-channel:
			if apiErr(out) {
				return nil, fmt.Errorf("平台 WebSocket 请求拒绝: %s", p.redact(jsonString(out)))
			}
			return out, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-run.Done():
			return nil, errors.New("平台 WebSocket 已断开")
		case <-timer.C:
			return nil, errors.New("平台 WebSocket 请求超时")
		}
	}
	p.mu.Lock()
	p.conn = ws
	p.sendSocket = send
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.conn = nil; p.sendSocket = nil; p.mu.Unlock() }()
	heart := 30 * time.Second
	switch p.name {
	case "mattermost":
		if err = write(map[string]any{"seq": 1, "action": "authentication_challenge", "data": map[string]string{"token": p.cfg.Token}}); err != nil {
			return err
		}
	case "wecom":
		if err = write(map[string]any{"cmd": "aibot_subscribe", "headers": map[string]any{"req_id": "subscribe"}, "body": map[string]string{"bot_id": p.cfg.BotID, "secret": p.cfg.Secret, "device_id": p.Account()}}); err != nil {
			return err
		}
	case "qqbot":
		var hello map[string]any
		if err = websocket.JSON.Receive(ws, &hello); err != nil {
			return err
		}
		if n, _ := hello["op"].(float64); n != 10 {
			return errors.New("QQ Gateway 缺少 hello")
		}
		if ms, _ := obj(hello["d"])["heartbeat_interval"].(float64); ms > 0 {
			heart = time.Duration(ms) * time.Millisecond
		}
		token, err := p.accessToken(ctx)
		if err != nil {
			return err
		}
		if session, seq := p.state("qq_session"), p.state("qq_seq"); session != "" && seq != "" {
			var n any
			_ = json.Unmarshal([]byte(seq), &n)
			err = write(map[string]any{"op": 6, "d": map[string]any{"token": "QQBot " + token, "session_id": session, "seq": n}})
		} else {
			err = write(map[string]any{"op": 2, "d": map[string]any{"token": "QQBot " + token, "intents": (1 << 25) | (1 << 12) | (1 << 30), "shard": []int{0, 1}, "properties": map[string]string{"$os": "linux", "$browser": "saber", "$device": "saber"}}})
		}
		if err != nil {
			return err
		}
	}
	go func() {
		ticker := time.NewTicker(heart)
		defer ticker.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-ticker.C:
				switch p.name {
				case "wecom":
					_ = write(map[string]any{"cmd": "ping", "headers": map[string]any{"req_id": uuid.NewString()}})
				case "qqbot":
					var seq any
					_ = json.Unmarshal([]byte(p.state("qq_seq")), &seq)
					_ = write(map[string]any{"op": 1, "d": seq})
				}
			}
		}
	}()
	for run.Err() == nil {
		var event map[string]any
		if err = websocket.JSON.Receive(ws, &event); err != nil {
			return err
		}
		responseID := first(event, "corrId")
		if responseID == "" {
			responseID = str(obj(event["headers"])["req_id"])
		}
		pendingMu.Lock()
		response := pending[responseID]
		pendingMu.Unlock()
		if response != nil && str(event["cmd"]) != "aibot_msg_callback" {
			select {
			case response <- event:
			default:
			}
			continue
		}
		switch p.name {
		case "wecom":
			if str(obj(event["headers"])["req_id"]) == "subscribe" {
				if apiErr(event) {
					return errors.New("WeCom 订阅认证被拒绝")
				}
				continue
			}
			if str(obj(event["body"])["event_type"]) == "disconnected_event" {
				return errors.New("WeCom 账号被另一实例接管")
			}
		case "qqbot":
			if seq := event["s"]; seq != nil {
				if err = p.setState(ctx, "qq_seq", jsonString(seq)); err != nil {
					return err
				}
			}
			op, _ := event["op"].(float64)
			if op == 7 {
				return errors.New("QQ Gateway 要求重连")
			}
			if op == 9 {
				_ = p.setState(ctx, "qq_session", "")
				_ = p.setState(ctx, "qq_seq", "")
				return errors.New("QQ Gateway session 无效")
			}
			if str(event["t"]) == "READY" {
				if err = p.setState(ctx, "qq_session", str(obj(event["d"])["session_id"])); err != nil {
					return err
				}
				continue
			}
			if op != 0 {
				continue
			}
		case "dingtalk":
			kind := str(event["type"])
			headers := obj(event["headers"])
			if kind == "SYSTEM" {
				_ = write(map[string]any{"code": 200, "headers": map[string]any{"messageId": headers["messageId"], "contentType": "application/json"}, "message": "OK", "data": event["data"]})
				if str(headers["topic"]) == "disconnect" {
					return errors.New("DingTalk 要求重连")
				}
				continue
			}
			if kind == "CALLBACK" {
				var data map[string]any
				if err = json.Unmarshal([]byte(str(event["data"])), &data); err != nil {
					return err
				}
				messages, decodeErr := p.decodeDingtalk(ctx, data)
				if decodeErr != nil {
					return decodeErr
				}
				for _, msg := range messages {
					if err = p.accept(ctx, msg); err != nil && !isDenied(err) {
						return err
					}
				}
				_ = write(map[string]any{"code": 200, "headers": map[string]any{"messageId": headers["messageId"], "contentType": "application/json"}, "message": "OK", "data": "{\"response\":null}"})
				continue
			}
		}
		messages, err := p.decodeSocketEvents(ctx, event)
		if err != nil {
			return err
		}
		for _, msg := range messages {
			if err = p.accept(ctx, msg); err != nil && !isDenied(err) {
				return err
			}
		}
	}
	return run.Err()
}

func (p *connector) sendWS(ctx context.Context, reply chat.Reply, edit bool) (string, error) {
	p.mu.Lock()
	send := p.sendSocket
	p.mu.Unlock()
	if send == nil {
		return "", errors.New("平台 WebSocket 未连接")
	}
	var payload map[string]any
	switch p.name {
	case "simplex":
		target := "@" + reply.Session.Conversation
		if strings.HasPrefix(reply.Session.Conversation, "group:") {
			target = "#" + strings.TrimPrefix(reply.Session.Conversation, "group:")
		}
		items := []any{map[string]any{"msgContent": map[string]string{"type": "text", "text": reply.Text}}}
		payload = map[string]any{"cmd": "/_send " + target + " json " + jsonString(items)}
	case "wecom":
		reqID := p.state("wecom-req:" + reply.ReplyTo)
		if reqID != "" {
			streamID := p.state("wecom-stream:" + reply.ReplyTo)
			if streamID == "" {
				streamID = uuid.NewString()
				if err := p.setState(ctx, "wecom-stream:"+reply.ReplyTo, streamID); err != nil {
					return "", err
				}
			}
			finish := reply.Status == "" || reply.Status == chat.ReplyCompleted || reply.Status == chat.ReplyFailed
			payload = map[string]any{"cmd": "aibot_respond_msg", "headers": map[string]any{"req_id": reqID}, "body": map[string]any{"msgtype": "stream", "stream": map[string]any{"id": streamID, "finish": finish, "content": reply.Text}}}
		} else {
			payload = map[string]any{"cmd": "aibot_send_msg", "body": map[string]any{"chatid": reply.Session.Conversation, "msgtype": "markdown", "markdown": map[string]string{"content": reply.Text}}}
		}
	default:
		return "", errors.New("平台没有 WebSocket 发送协议")
	}
	out, err := send(ctx, payload)
	if err != nil {
		return "", err
	}
	if p.name == "wecom" && p.state("wecom-stream:"+reply.ReplyTo) != "" {
		return p.state("wecom-stream:" + reply.ReplyTo), nil
	}
	return first(out, "id", "message_id", "msgid"), nil
}

func (p *connector) decodeSocketEvents(ctx context.Context, event map[string]any) ([]chat.Message, error) {
	switch p.name {
	case "qqbot":
		return p.decodeEvents(ctx, event)
	case "mattermost":
		if str(event["event"]) != "posted" {
			return nil, nil
		}
		data := obj(event["data"])
		var post map[string]any
		if err := json.Unmarshal([]byte(str(data["post"])), &post); err != nil {
			return nil, err
		}
		if str(post["user_id"]) == p.cfg.BotID {
			return nil, nil
		}
		msg := chat.Message{ID: str(post["id"]), SenderID: str(post["user_id"]), Text: str(post["message"]), Direct: str(data["channel_type"]) == "D", Session: chat.Session{Conversation: str(post["channel_id"]), Thread: str(post["root_id"])}, ReplyTo: str(post["root_id"])}
		for _, raw := range arr(post["file_ids"]) {
			attachment, err := p.fetchAuthorizedMedia(ctx, msg, "api/v4/files/"+url.PathEscape(str(raw)), "", "", "")
			if err != nil {
				return nil, err
			}
			msg.Attachments = append(msg.Attachments, attachment)
		}
		return []chat.Message{msg}, nil
	case "wecom":
		cmd := str(event["cmd"])
		if cmd != "aibot_msg_callback" && cmd != "aibot_callback" {
			return nil, nil
		}
		body := obj(event["body"])
		sender := str(obj(body["from"])["userid"])
		target := first(body, "chatid")
		if target == "" {
			target = sender
		}
		msg := chat.Message{ID: str(body["msgid"]), SenderID: sender, Direct: str(body["chattype"]) == "single", Text: str(obj(body["text"])["content"]), Session: chat.Session{Conversation: target}}
		if err := p.incomingAllowed(msg); err != nil {
			return nil, err
		}
		if req := str(obj(event["headers"])["req_id"]); req != "" {
			if err := p.setState(ctx, "wecom-req:"+msg.ID, req); err != nil {
				return nil, err
			}
		}
		for _, kind := range []string{"image", "voice", "file", "video"} {
			media := obj(body[kind])
			if link := str(media["url"]); link != "" {
				a, err := p.fetchAuthorizedExternalMedia(ctx, msg, link, "", str(media["filename"]), false)
				if err != nil {
					return nil, err
				}
				if key := str(media["aeskey"]); key != "" {
					a, err = decryptWecomAttachment(a, key)
					if err != nil {
						return nil, err
					}
				}
				msg.Attachments = append(msg.Attachments, a)
			}
		}
		return []chat.Message{msg}, nil
	case "simplex":
		return p.decodeSimplex(ctx, event)
	}
	return nil, nil
}
func (p *connector) decodeDingtalk(ctx context.Context, data map[string]any) ([]chat.Message, error) {
	sender := first(data, "senderStaffId", "senderId")
	direct := str(data["conversationType"]) == "1"
	target := str(data["conversationId"])
	if direct {
		target = "user:" + sender
	}
	msg := chat.Message{ID: first(data, "msgId", "messageId"), SenderID: sender, Direct: direct, Text: str(obj(data["text"])["content"]), Session: chat.Session{Conversation: target}}
	for _, key := range []string{"pictureDownloadCode", "downloadCode"} {
		if code := str(obj(data["content"])[key]); code != "" {
			out, err := p.request(ctx, "POST", "v1.0/robot/messageFiles/download", map[string]string{"downloadCode": code, "robotCode": p.cfg.AppID})
			if err != nil {
				return nil, err
			}
			attachment, err := p.fetchAuthorizedExternalMedia(ctx, msg, str(out["downloadUrl"]), "", str(obj(data["content"])["fileName"]), false)
			if err != nil {
				return nil, err
			}
			msg.Attachments = append(msg.Attachments, attachment)
		}
	}
	return []chat.Message{msg}, nil
}
