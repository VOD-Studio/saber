package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"rua.plus/saber/internal/chat"
)

// Capabilities 仅声明平台协议确实支持的展示能力。
func (p *connector) Capabilities() chat.Capabilities {
	var caps chat.Capabilities
	switch p.name {
	case "mattermost", "whatsapp", "buzz":
		caps = chat.Capabilities{Edit: true, Reply: true, Typing: p.name == "mattermost"}
	case "wecom":
		caps = chat.Capabilities{Edit: true, Reply: true, ReplyState: true}
	case "signal":
		caps = chat.Capabilities{Typing: true, Reply: true}
	case "teams", "google_chat", "feishu", "bluebubbles", "qqbot":
		caps = chat.Capabilities{Reply: true}
	case "relay":
		p.mu.Lock()
		caps = p.relayCaps
		p.mu.Unlock()
		return caps
	}
	if contains([]string{"mattermost", "whatsapp_cloud", "feishu", "qqbot", "weixin", "bluebubbles", "email", "line", "teams", "google_chat", "ntfy", "sms", "wecom_callback"}, p.name) || (p.cfg.SharedMediaDir != "" && contains([]string{"signal", "simplex", "photon", "whatsapp", "buzz"}, p.name)) {
		caps.Audio = true
		caps.Video = true
		caps.Files = p.name != "line"
	}
	return caps
}

// Send 通过固定平台原生协议发送，不允许模型或入站载荷指定任意凭据地址。
func (p *connector) Send(ctx context.Context, reply chat.Reply) (string, error) {
	if err := p.validateSession(ctx, reply.Session); err != nil {
		return "", err
	}
	var err error
	ctx, err = p.deliveryContext(ctx, reply.Session)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(reply.Text) == "" && (p.name != "wecom" || reply.Status == "") {
		return "", errors.New("出站正文为空")
	}
	if p.name == "irc" {
		return p.sendIRC(ctx, reply)
	}
	if p.name == "email" {
		return p.sendEmail(ctx, reply)
	}
	if p.name == "buzz" {
		return p.sendBuzz(ctx, reply)
	}
	if p.name == "wecom" || p.name == "simplex" {
		return p.sendWS(ctx, reply, false)
	}
	if p.name == "relay" {
		return p.sendRelay(ctx, map[string]any{"op": "send", "chat_id": reply.Session.Conversation, "content": reply.Text, "reply_to": reply.ReplyTo, "metadata": map[string]string{"thread_id": reply.Session.Thread, "transaction_id": reply.TransactionID}})
	}
	if p.name == "a2a" || p.name == "webhook" {
		return p.replyLocal(ctx, reply)
	}
	if p.name == "raft" {
		return "", errors.New("raft wake-only 通道不接受聊天发送；使用已授权 exec 调用 raft CLI 投递，不能伪造 adapter 成功")
	}
	limit := 4000
	switch p.name {
	case "sms":
		limit = 1500
	case "weixin":
		limit = 1800
	case "line":
		limit = 4500
	case "google_chat":
		limit = 30000
	case "mattermost":
		limit = 15000
	}
	pieces := splitRunes(reply.Text, limit)
	last := ""
	for i, text := range pieces {
		item := reply
		item.Text = text
		if i > 0 {
			item.ReplyTo = ""
		}
		id, err := p.sendOne(ctx, item)
		if err != nil {
			return last, err
		}
		last = id
	}
	return last, nil
}
func splitRunes(text string, n int) []string {
	r := []rune(text)
	out := make([]string, 0, (len(r)+n-1)/n)
	for len(r) > 0 {
		take := min(len(r), n)
		out = append(out, string(r[:take]))
		r = r[take:]
	}
	return out
}
func (p *connector) sendOne(ctx context.Context, reply chat.Reply) (string, error) {
	target := reply.Session.Conversation
	path := ""
	var payload any
	switch p.name {
	case "mattermost":
		path = "api/v4/posts"
		payload = map[string]any{"channel_id": target, "message": reply.Text, "root_id": reply.Session.Thread, "pending_post_id": reply.TransactionID}
	case "whatsapp":
		path = "send"
		payload = map[string]any{"chatId": target, "message": reply.Text, "replyTo": reply.ReplyTo}
	case "whatsapp_cloud":
		path = url.PathEscape(p.cfg.BotID) + "/messages"
		body := map[string]any{"messaging_product": "whatsapp", "to": target, "type": "text", "text": map[string]any{"body": reply.Text, "preview_url": false}}
		if reply.ReplyTo != "" {
			body["context"] = map[string]string{"message_id": reply.ReplyTo}
		}
		payload = body
	case "signal":
		path = "api/v1/rpc"
		params := map[string]any{"account": p.cfg.Phone, "message": reply.Text}
		if strings.HasPrefix(target, "group:") {
			params["groupId"] = strings.TrimPrefix(target, "group:")
		} else {
			params["recipients"] = []string{target}
		}
		payload = map[string]any{"jsonrpc": "2.0", "id": reply.TransactionID, "method": "send", "params": params}
	case "sms":
		path = "Accounts/" + url.PathEscape(p.cfg.AppID) + "/Messages.json"
		payload = url.Values{"From": {p.cfg.Phone}, "To": {target}, "Body": {reply.Text}}
	case "line":
		path = "v2/bot/message/push"
		payload = map[string]any{"to": target, "messages": []any{map[string]string{"type": "text", "text": reply.Text}}}
	case "feishu":
		content, _ := json.Marshal(map[string]string{"text": reply.Text})
		if reply.ReplyTo != "" {
			path = "im/v1/messages/" + url.PathEscape(reply.ReplyTo) + "/reply"
			payload = map[string]any{"msg_type": "text", "content": string(content), "reply_in_thread": reply.Session.Thread != ""}
		} else {
			path = "im/v1/messages?receive_id_type=chat_id"
			payload = map[string]any{"receive_id": target, "msg_type": "text", "content": string(content), "uuid": reply.TransactionID}
		}
	case "dingtalk":
		path = "v1.0/robot/groupMessages/send"
		body := map[string]any{"robotCode": p.cfg.AppID, "openConversationId": target, "msgKey": "sampleMarkdown", "msgParam": jsonString(map[string]any{"title": "Saber", "text": reply.Text})}
		if strings.HasPrefix(target, "user:") {
			path = "v1.0/robot/oToMessages/batchSend"
			delete(body, "openConversationId")
			body["userIds"] = []string{strings.TrimPrefix(target, "user:")}
		}
		payload = body
	case "wecom_callback":
		path = "message/send"
		payload = map[string]any{"touser": target, "msgtype": "markdown", "agentid": p.cfg.AppID, "markdown": map[string]string{"content": reply.Text}, "enable_duplicate_check": 1}
	case "weixin":
		contextToken := p.state("context:" + target)
		if contextToken == "" {
			return "", errors.New("weixin 会话尚无 context_token，需该用户先发消息")
		}
		path = "ilink/bot/sendmessage"
		payload = map[string]any{"base_info": map[string]string{"channel_version": "2.2.0"}, "msg": map[string]any{"from_user_id": "", "to_user_id": target, "client_id": reply.TransactionID, "message_type": 2, "message_state": 2, "context_token": contextToken, "item_list": []any{map[string]any{"type": 1, "text_item": map[string]string{"text": reply.Text}}}}}
	case "bluebubbles":
		path = "api/v1/message/text"
		payload = map[string]any{"chatGuid": target, "message": reply.Text, "tempGuid": reply.TransactionID, "method": "apple-script"}
	case "photon":
		path = "send"
		payload = map[string]any{"spaceId": target, "text": reply.Text, "format": "markdown"}
	case "ntfy":
		path = ""
		payload = map[string]any{"topic": target, "message": reply.Text, "title": "Saber"}
	case "qqbot":
		scope, id, ok := strings.Cut(target, ":")
		if !ok {
			return "", errors.New("QQ 会话必须带 group/c2c/channel 作用域")
		}
		switch scope {
		case "group":
			path = "v2/groups/" + url.PathEscape(id) + "/messages"
		case "c2c":
			path = "v2/users/" + url.PathEscape(id) + "/messages"
		case "channel":
			path = "channels/" + url.PathEscape(id) + "/messages"
		default:
			return "", errors.New("QQ 会话作用域无效")
		}
		payload = map[string]any{"content": reply.Text, "msg_type": 0, "msg_id": reply.ReplyTo, "msg_seq": 1}
	case "google_chat":
		if !strings.HasPrefix(target, "spaces/") || strings.Contains(target, "..") {
			return "", errors.New("google_chat space 无效")
		}
		path = target + "/messages"
		body := map[string]any{"text": reply.Text}
		if reply.Session.Thread != "" {
			body["thread"] = map[string]string{"name": reply.Session.Thread}
			path += "?messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"
		}
		payload = body
	case "teams":
		path = "v3/conversations/" + url.PathEscape(target) + "/activities"
		payload = map[string]any{"type": "message", "text": reply.Text, "textFormat": "markdown", "replyToId": reply.ReplyTo}
	case "msgraph_webhook":
		if strings.HasPrefix(target, "chats/") {
			path = target + "/messages"
		} else if strings.HasPrefix(target, "teams/") && strings.Contains(target, "/channels/") {
			path = target + "/messages"
		} else {
			return "", errors.New("msgraph 会话必须为 chats/id 或 teams/id/channels/id")
		}
		payload = map[string]any{"body": map[string]string{"contentType": "text", "content": reply.Text}}
	default:
		return "", fmt.Errorf("平台 %s 没有文本发送协议", p.name)
	}
	out, err := p.request(ctx, "POST", path, payload)
	if err != nil {
		return "", err
	}
	id := stableID(out)
	if id == "" {
		id = first(obj(out["data"]), "message_id", "id", "guid")
		if id == "" {
			id = first(obj(out["result"]), "timestamp", "id")
			if id == "" {
				items := arr(out["messages"])
				if len(items) > 0 {
					id = stableID(obj(items[0]))
				}
			}
		}
	}
	if id == "" {
		id = reply.TransactionID
	}
	return id, nil
}
func jsonString(v any) string { data, _ := json.Marshal(v); return string(data) }

// Edit 更新真实可编辑消息；其他平台明确拒绝，不伪造成功。
func (p *connector) Edit(ctx context.Context, id string, reply chat.Reply) error {
	if err := p.validateSession(ctx, reply.Session); err != nil {
		return err
	}
	if id == "" {
		return errors.New("编辑缺少消息 ID")
	}
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	wait := 200*time.Millisecond - time.Since(p.lastEdit)
	if wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	p.lastEdit = time.Now()
	switch p.name {
	case "mattermost":
		_, err := p.request(ctx, "PUT", "api/v4/posts/"+url.PathEscape(id), map[string]string{"id": id, "message": reply.Text})
		return err
	case "whatsapp":
		_, err := p.request(ctx, "POST", "edit", map[string]string{"chatId": reply.Session.Conversation, "messageId": id, "message": reply.Text})
		return err
	case "buzz":
		_, err := p.cli(ctx, []string{"messages", "edit", "--event", id, "--content=" + reply.Text}, "")
		return err
	case "wecom":
		_, err := p.sendWS(ctx, reply, true)
		return err
	case "relay":
		_, err := p.sendRelay(ctx, map[string]any{"op": "edit", "chat_id": reply.Session.Conversation, "message_id": id, "content": reply.Text, "metadata": map[string]string{"thread_id": reply.Session.Thread}})
		return err
	default:
		return errors.New("平台不支持消息编辑")
	}
}

// SetTyping 仅在协议有输入状态时发送；未声明该能力的平台返回明确错误。
func (p *connector) SetTyping(ctx context.Context, s chat.Session, on bool) error {
	if err := p.validateSession(ctx, s); err != nil {
		return err
	}
	switch p.name {
	case "mattermost":
		if !on {
			return nil
		}
		p.mu.Lock()
		send := p.sendSocket
		p.mu.Unlock()
		if send == nil {
			return errors.New("mattermost WebSocket 未连接")
		}
		_, err := send(ctx, map[string]any{"action": "user_typing", "data": map[string]string{"channel_id": s.Conversation, "parent_id": s.Thread}})
		return err
	case "signal":
		params := map[string]any{"account": p.cfg.Phone, "stop": !on}
		if strings.HasPrefix(s.Conversation, "group:") {
			params["groupId"] = strings.TrimPrefix(s.Conversation, "group:")
		} else {
			params["recipients"] = []string{s.Conversation}
		}
		_, err := p.request(ctx, "POST", "api/v1/rpc", map[string]any{"jsonrpc": "2.0", "id": "typing", "method": "sendTyping", "params": params})
		return err
	case "relay":
		_, err := p.sendRelay(ctx, map[string]any{"op": "typing", "chat_id": s.Conversation, "metadata": map[string]any{"thread_id": s.Thread, "active": on}})
		return err
	default:
		return errors.New("平台不支持输入状态")
	}
}
