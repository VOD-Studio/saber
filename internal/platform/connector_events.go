package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"rua.plus/saber/internal/chat"
)

func (p *connector) decodeEvents(ctx context.Context, payload map[string]any) ([]chat.Message, error) {
	var result []chat.Message
	switch p.name {
	case "whatsapp_cloud":
		for _, entry := range arr(payload["entry"]) {
			for _, change := range arr(obj(entry)["changes"]) {
				value := obj(obj(change)["value"])
				if str(obj(value["metadata"])["phone_number_id"]) != p.cfg.BotID {
					return nil, errors.New("WhatsApp Cloud phone_number_id 不匹配")
				}
				for _, raw := range arr(value["messages"]) {
					m := obj(raw)
					msg := chat.Message{ID: stableID(m), SenderID: str(m["from"]), Direct: true, Session: chat.Session{Conversation: str(m["from"])}, ReplyTo: str(obj(m["context"])["id"]), Text: str(obj(m["text"])["body"])}
					kind := str(m["type"])
					if contains([]string{"image", "audio", "video", "document"}, kind) {
						media := obj(m[kind])
						meta, err := p.request(ctx, "GET", url.PathEscape(str(media["id"])), nil)
						if err != nil {
							return nil, err
						}
						attachment, err := p.fetchAuthorizedExternalMedia(ctx, msg, str(meta["url"]), str(meta["mime_type"]), str(media["filename"]), true)
						if err != nil {
							return nil, err
						}
						msg.Attachments = append(msg.Attachments, attachment)
						msg.Text = str(media["caption"])
					}
					if msg.Text != "" || len(msg.Attachments) > 0 {
						result = append(result, msg)
					}
				}
			}
		}
	case "line":
		for _, raw := range arr(payload["events"]) {
			event := obj(raw)
			if str(event["type"]) != "message" {
				continue
			}
			source, m := obj(event["source"]), obj(event["message"])
			sender := str(source["userId"])
			direct := str(source["type"]) == "user"
			conversation := first(source, "groupId", "roomId", "userId")
			msg := chat.Message{ID: stableID(m), SenderID: sender, Direct: direct, Text: str(m["text"]), Session: chat.Session{Conversation: conversation}}
			if str(m["type"]) != "text" {
				attachment, err := p.fetchAuthorizedMedia(ctx, msg, "v2/bot/message/"+url.PathEscape(msg.ID)+"/content", "", str(m["fileName"]), str(m["type"]))
				if err != nil {
					return nil, err
				}
				msg.Attachments = []chat.Attachment{attachment}
			}
			result = append(result, msg)
		}
	case "feishu":
		event := obj(payload["event"])
		m, sender := obj(event["message"]), obj(obj(event["sender"])["sender_id"])
		if len(m) == 0 {
			return nil, nil
		}
		msg := chat.Message{ID: str(m["message_id"]), SenderID: first(sender, "open_id", "user_id", "union_id"), Direct: str(m["chat_type"]) == "p2p", Session: chat.Session{Conversation: str(m["chat_id"]), Thread: str(m["thread_id"])}, ReplyTo: str(m["parent_id"])}
		var content map[string]any
		if err := json.Unmarshal([]byte(str(m["content"])), &content); err != nil {
			return nil, err
		}
		msg.Text = str(content["text"])
		kind := str(m["message_type"])
		if kind != "text" {
			key := first(content, "image_key", "file_key")
			if key != "" {
				resourceType := "file"
				if kind == "image" {
					resourceType = "image"
				}
				attachment, err := p.fetchAuthorizedMedia(ctx, msg, "im/v1/messages/"+url.PathEscape(msg.ID)+"/resources/"+url.PathEscape(key)+"?type="+resourceType, "", str(content["file_name"]), kind)
				if err != nil {
					return nil, err
				}
				msg.Attachments = []chat.Attachment{attachment}
			} else if kind == "post" {
				msg.Text = jsonString(content)
			}
		}
		result = append(result, msg)
	case "teams":
		if str(payload["type"]) != "message" {
			return nil, nil
		}
		if tenant := str(obj(obj(payload["channelData"])["tenant"])["id"]); tenant != p.cfg.TenantID {
			return nil, errors.New("teams tenant 不匹配")
		}
		conversation, from := obj(payload["conversation"]), obj(payload["from"])
		direct := str(conversation["conversationType"]) == "personal"
		msg := chat.Message{ID: stableID(payload), SenderID: str(from["id"]), Direct: direct, Text: str(payload["text"]), ReplyTo: str(payload["replyToId"]), Session: chat.Session{Conversation: str(conversation["id"])}}
		if err := p.incomingAllowed(msg); err != nil {
			return nil, err
		}
		serviceURL := str(payload["serviceUrl"])
		if err := p.checkTeamsURL(serviceURL); err != nil {
			return nil, err
		}
		if err := p.setState(ctx, "teams-service:"+msg.Session.Conversation, serviceURL); err != nil {
			return nil, err
		}
		for _, raw := range arr(payload["attachments"]) {
			attachment := obj(raw)
			mediaURL := str(attachment["contentUrl"])
			if mediaURL == "" {
				continue
			}
			a, err := p.fetchAuthorizedExternalMedia(ctx, msg, mediaURL, str(attachment["contentType"]), str(attachment["name"]), true)
			if err != nil {
				return nil, err
			}
			msg.Attachments = append(msg.Attachments, a)
		}
		result = append(result, msg)
	case "google_chat":
		if str(payload["type"]) != "MESSAGE" {
			return nil, nil
		}
		m := obj(payload["message"])
		space, user := obj(payload["space"]), obj(payload["user"])
		msg := chat.Message{ID: str(m["name"]), SenderID: str(user["name"]), Direct: str(space["type"]) == "DM", Text: first(m, "argumentText", "text"), Session: chat.Session{Conversation: str(space["name"]), Thread: str(obj(m["thread"])["name"])}}
		for _, raw := range arr(m["attachment"]) {
			a := obj(raw)
			ref := obj(a["attachmentDataRef"])
			resource := str(ref["resourceName"])
			if resource != "" {
				attachment, err := p.fetchAuthorizedMedia(ctx, msg, resource+":download?alt=media", str(a["contentType"]), str(a["contentName"]), "")
				if err != nil {
					return nil, err
				}
				msg.Attachments = append(msg.Attachments, attachment)
			}
		}
		result = append(result, msg)
	case "bluebubbles":
		if str(payload["type"]) != "new-message" {
			return nil, nil
		}
		m := obj(payload["data"])
		if fromSelf, ok := m["isFromMe"].(bool); ok && fromSelf {
			return nil, nil
		}
		chats := arr(m["chats"])
		if len(chats) == 0 {
			return nil, errors.New("BlueBubbles 缺少聊天 GUID")
		}
		c := obj(chats[0])
		participants := arr(c["participants"])
		msg := chat.Message{ID: str(m["guid"]), SenderID: str(obj(m["handle"])["address"]), Direct: len(participants) == 1, Text: str(m["text"]), Session: chat.Session{Conversation: str(c["guid"])}, ReplyTo: str(m["threadOriginatorGuid"])}
		for _, raw := range arr(m["attachments"]) {
			a := obj(raw)
			attachment, err := p.fetchAuthorizedMedia(ctx, msg, "api/v1/attachment/"+url.PathEscape(str(a["guid"]))+"/download", str(a["mimeType"]), str(a["transferName"]), "")
			if err != nil {
				return nil, err
			}
			msg.Attachments = append(msg.Attachments, attachment)
		}
		result = append(result, msg)
	case "msgraph_webhook":
		for _, raw := range arr(payload["value"]) {
			notification := obj(raw)
			resource := str(notification["resource"])
			if strings.Contains(resource, "..") || (!strings.HasPrefix(resource, "chats/") && !strings.HasPrefix(resource, "teams/")) {
				return nil, errors.New("msgraph resource 不在聊天范围")
			}
			m, err := p.request(ctx, "GET", resource, nil)
			if err != nil {
				return nil, err
			}
			target, _, ok := strings.Cut(resource, "/messages/")
			if !ok {
				return nil, errors.New("msgraph resource 缺少 messages")
			}
			msg := chat.Message{ID: str(m["id"]), SenderID: str(obj(obj(m["from"])["user"])["id"]), Direct: strings.HasPrefix(target, "chats/"), Text: str(obj(m["body"])["content"]), Session: chat.Session{Conversation: target}}
			result = append(result, msg)
		}
	case "qqbot":
		m := obj(payload["d"])
		event := str(payload["t"])
		sender := first(obj(m["author"]), "member_openid", "user_openid", "id")
		target := ""
		direct := false
		switch event {
		case "GROUP_AT_MESSAGE_CREATE":
			target = "group:" + str(m["group_openid"])
		case "C2C_MESSAGE_CREATE":
			target = "c2c:" + sender
			direct = true
		case "AT_MESSAGE_CREATE", "MESSAGE_CREATE":
			target = "channel:" + str(m["channel_id"])
		case "DIRECT_MESSAGE_CREATE":
			target = "c2c:" + sender
			direct = true
		default:
			return nil, nil
		}
		msg := chat.Message{ID: stableID(m), SenderID: sender, Direct: direct, Text: strings.TrimSpace(str(m["content"])), Session: chat.Session{Conversation: target}}
		for _, raw := range arr(m["attachments"]) {
			a := obj(raw)
			attachment, err := p.fetchAuthorizedExternalMedia(ctx, msg, str(a["url"]), str(a["content_type"]), str(a["filename"]), false)
			if err != nil {
				return nil, err
			}
			msg.Attachments = append(msg.Attachments, attachment)
		}
		result = append(result, msg)
	case "raft":
		for _, key := range []string{"body", "content", "message", "messages", "preview", "snippet", "text"} {
			if payload[key] != nil {
				return nil, errors.New("raft wake 不能携带内容")
			}
		}
		id := first(payload, "eventId", "attemptId", "messageId", "delivery_id", "wake_id", "id")
		if id == "" {
			return nil, errors.New("raft wake 需要稳定 delivery ID")
		}
		result = append(result, chat.Message{ID: id, SenderID: "raft-bridge", Direct: true, Text: "Raft 工作区有新活动。使用已授权执行工具运行配置的 raft --profile " + p.cfg.Profile + " manual get raft-cli-overview，读取真实活动并通过 Raft CLI 交付。", Session: chat.Session{Conversation: p.cfg.Profile}})
	case "webhook":
		// Webhook 平台自身定义此事件协议；原生平台不使用通用回调兼容路径。
		source := obj(payload["source"])
		msg := chat.Message{ID: stableID(payload), SenderID: first(source, "user_id", "sender_id"), Direct: str(source["chat_type"]) == "dm", Text: str(payload["text"]), Session: chat.Session{Conversation: first(source, "chat_id", "conversation"), Thread: str(source["thread_id"])}, ReplyTo: str(payload["reply_to"])}
		result = append(result, msg)
	default:
		return nil, fmt.Errorf("%s 不使用 HTTP 回调事件", p.name)
	}
	return result, nil
}

func (p *connector) decodeRealtime(ctx context.Context, payload map[string]any) ([]chat.Message, error) {
	switch p.name {
	case "whatsapp":
		if own, ok := payload["isFromMe"].(bool); ok && own {
			return nil, nil
		}
		if str(payload["event"]) == "delivery_status" {
			return nil, nil
		}
		group, _ := payload["isGroup"].(bool)
		msg := chat.Message{ID: stableID(payload), SenderID: first(payload, "sender", "senderId", "senderJid"), Direct: !group, Text: first(payload, "text", "body", "message"), Session: chat.Session{Conversation: first(payload, "chatId", "chatJid")}, ReplyTo: str(payload["quotedMessageId"])}
		if err := p.incomingAllowed(msg); err != nil {
			return nil, err
		}
		for _, raw := range arr(payload["mediaUrls"]) {
			link := str(raw)
			var a chat.Attachment
			var err error
			if strings.HasPrefix(link, "https://") || strings.HasPrefix(link, "http://") {
				a, err = p.fetchAuthorizedExternalMedia(ctx, msg, link, str(payload["mime"]), str(payload["fileName"]), false)
			} else {
				a, err = p.sharedMedia(link, str(payload["mime"]), str(payload["fileName"]), str(payload["mediaType"]))
			}
			if err != nil {
				return nil, err
			}
			msg.Attachments = append(msg.Attachments, a)
		}
		return []chat.Message{msg}, nil
	case "signal":
		env := obj(payload["envelope"])
		if len(env) == 0 {
			env = obj(obj(payload["params"])["envelope"])
		}
		data := obj(env["dataMessage"])
		if len(data) == 0 || str(env["sourceNumber"]) == p.cfg.Phone {
			return nil, nil
		}
		sender := first(env, "sourceUuid", "sourceNumber", "source")
		group := str(obj(data["groupInfo"])["groupId"])
		target := sender
		if group != "" {
			target = "group:" + group
		}
		msg := chat.Message{ID: str(data["timestamp"]), SenderID: sender, Direct: group == "", Text: str(data["message"]), Session: chat.Session{Conversation: target}, ReplyTo: str(obj(data["quote"])["id"])}
		if err := p.incomingAllowed(msg); err != nil {
			return nil, err
		}
		for _, raw := range arr(data["attachments"]) {
			a := obj(raw)
			response, err := p.request(ctx, "POST", "api/v1/rpc", map[string]any{"jsonrpc": "2.0", "id": "attachment", "method": "getAttachment", "params": map[string]string{"account": p.cfg.Phone, "id": str(a["id"])}})
			if err != nil {
				return nil, err
			}
			encoded := str(obj(response["result"])["data"])
			if encoded == "" {
				encoded = str(response["result"])
			}
			data, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, err
			}
			if int64(len(data)) > p.cfg.MaxMediaBytes {
				return nil, errors.New("signal 媒体超限")
			}
			msg.Attachments = append(msg.Attachments, mediaAttachment(data, str(a["contentType"]), str(a["filename"]), ""))
		}
		return []chat.Message{msg}, nil
	case "weixin":
		sender := str(payload["from_user_id"])
		msg := chat.Message{ID: str(payload["message_id"]), SenderID: sender, Direct: true, Session: chat.Session{Conversation: sender}}
		if err := p.incomingAllowed(msg); err != nil {
			return nil, err
		}
		if token := str(payload["context_token"]); token != "" {
			if err := p.setState(ctx, "context:"+sender, token); err != nil {
				return nil, err
			}
		}
		for _, raw := range arr(payload["item_list"]) {
			item := obj(raw)
			if n, _ := item["type"].(float64); n == 1 {
				msg.Text += str(obj(item["text_item"])["text"])
			} else {
				a, err := p.weixinMedia(ctx, item)
				if err != nil {
					return nil, err
				}
				msg.Attachments = append(msg.Attachments, a)
			}
		}
		return []chat.Message{msg}, nil
	case "photon":
		space, sender, content := obj(payload["space"]), obj(payload["sender"]), obj(payload["content"])
		if own, ok := payload["isFromMe"].(bool); ok && own {
			return nil, nil
		}
		replyTo := ""
		if str(content["type"]) == "reply" {
			replyTo = str(content["targetMessageId"])
			content = obj(content["content"])
		}
		msg := chat.Message{ID: str(payload["messageId"]), SenderID: str(sender["id"]), Direct: str(space["type"]) == "direct" || str(space["type"]) == "dm", Text: str(content["text"]), ReplyTo: replyTo, Session: chat.Session{Conversation: str(space["id"])}}
		if data := str(content["data"]); data != "" {
			bytes, err := base64.StdEncoding.DecodeString(data)
			if err != nil {
				return nil, err
			}
			if int64(len(bytes)) > p.cfg.MaxMediaBytes {
				return nil, errors.New("photon 媒体超限")
			}
			msg.Attachments = []chat.Attachment{mediaAttachment(bytes, str(content["mimeType"]), str(content["name"]), str(content["type"]))}
		}
		return []chat.Message{msg}, nil
	case "ntfy":
		if str(payload["event"]) != "message" {
			return nil, nil
		}
		topic := str(payload["topic"])
		sender := str(payload["sender"])
		if sender == "" {
			sender = topic
		}
		msg := chat.Message{ID: stableID(payload), SenderID: sender, Direct: true, Text: str(payload["message"]), Session: chat.Session{Conversation: topic}}
		if attachment := obj(payload["attachment"]); str(attachment["url"]) != "" {
			a, err := p.fetchAuthorizedExternalMedia(ctx, msg, str(attachment["url"]), str(attachment["type"]), str(attachment["name"]), false)
			if err != nil {
				return nil, err
			}
			msg.Attachments = []chat.Attachment{a}
		}
		return []chat.Message{msg}, nil
	}
	return p.decodeSocketEvents(ctx, payload)
}
