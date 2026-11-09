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

	"rua.plus/saber/internal/chat"
)

func (p *connector) uploadRelayMedia(ctx context.Context, a chat.Attachment) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(p.cfg.Endpoint, "/")+"/relay/media", bytes.NewReader(a.Data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", a.MIMEType)
	req.Header.Set("X-Media-Filename", a.Name)
	req.Header.Set("Authorization", "Bearer "+p.relayToken())
	response, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("relay 媒体上传 HTTP %d", response.StatusCode)
	}
	var out map[string]any
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&out); err != nil {
		return "", err
	}
	id := str(out["id"])
	if id == "" {
		return "", errors.New("relay 上传未返回媒体 ID")
	}
	return id, nil
}
func (p *connector) relayPassthrough(ctx context.Context, forward map[string]any) ([]chat.Message, error) {
	if str(forward["platform"]) != p.cfg.AppID || str(forward["botId"]) != p.cfg.BotID || str(forward["method"]) != "POST" {
		return nil, errors.New("relay passthrough 身份或方法不匹配")
	}
	data, err := base64.StdEncoding.DecodeString(str(forward["bodyB64"]))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, errors.New("relay 交互超限")
	}
	switch p.cfg.AppID {
	case "discord":
		var event map[string]any
		if err = json.Unmarshal(data, &event); err != nil {
			return nil, err
		}
		member := obj(event["member"])
		user := obj(member["user"])
		direct := len(member) == 0
		if direct {
			user = obj(event["user"])
		}
		command := obj(event["data"])
		kind, _ := event["type"].(float64)
		text := ""
		switch int(kind) {
		case 2:
			if !contains([]string{"saber", "hermes", "ask"}, str(command["name"])) {
				return nil, errors.New("relay 未登记的 slash command")
			}
			for _, raw := range arr(command["options"]) {
				option := obj(raw)
				if contains([]string{"prompt", "message", "query"}, str(option["name"])) {
					text = str(option["value"])
					break
				}
			}
		case 3:
			text = str(command["custom_id"])
			if !trustedControlText(text) {
				return nil, errors.New("relay button 不是已登记的任务控制")
			}
		case 5:
			for _, row := range arr(command["components"]) {
				for _, component := range arr(obj(row)["components"]) {
					text += str(obj(component)["value"]) + "\n"
				}
			}
			if text == "" {
				return nil, errors.New("relay modal 为空")
			}
		default:
			return nil, errors.New("relay Discord interaction type 不受支持")
		}
		msg := chat.Message{ID: str(event["id"]), SenderID: str(user["id"]), Direct: direct, Text: text, Session: chat.Session{Conversation: str(event["channel_id"])}, ReplyTo: str(obj(event["message"])["id"])}
		return []chat.Message{msg}, nil
	case "slack":
		values, err := url.ParseQuery(string(data))
		if err != nil {
			return nil, err
		}
		if raw := values.Get("payload"); raw != "" {
			var event map[string]any
			if err = json.Unmarshal([]byte(raw), &event); err != nil {
				return nil, err
			}
			user, channel, container := obj(event["user"]), obj(event["channel"]), obj(event["container"])
			actions := arr(event["actions"])
			if len(actions) == 0 {
				return nil, errors.New("relay Slack interaction 缺少 action")
			}
			action := obj(actions[0])
			text := str(action["value"])
			if !trustedControlText(text) {
				return nil, errors.New("relay Slack action 不是已登记的任务控制")
			}
			return []chat.Message{{ID: first(event, "trigger_id") + ":" + first(action, "action_ts"), SenderID: str(user["id"]), Direct: strings.HasPrefix(str(channel["id"]), "D"), Text: text, Session: chat.Session{Conversation: str(channel["id"]), Thread: str(container["thread_ts"])}, ReplyTo: str(container["message_ts"])}}, nil
		}
		if !contains([]string{"/saber", "/hermes"}, values.Get("command")) {
			return nil, errors.New("relay 未登记的 Slack slash command")
		}
		return []chat.Message{{ID: values.Get("trigger_id"), SenderID: values.Get("user_id"), Direct: strings.HasPrefix(values.Get("channel_id"), "D"), Text: values.Get("text"), Session: chat.Session{Conversation: values.Get("channel_id")}}}, nil
	default:
		return nil, fmt.Errorf("relay 平台 %s 不提供交互透传协议", p.cfg.AppID)
	}
}
func trustedControlText(text string) bool {
	for _, prefix := range []string{"/task ", "/answer ", "/cancel ", "/approve ", "/deny "} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}
