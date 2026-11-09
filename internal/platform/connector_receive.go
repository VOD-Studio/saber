package platform

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"rua.plus/saber/internal/chat"
)

func (p *connector) checkDependency(ctx context.Context) error {
	switch p.name {
	case "buzz", "raft":
		if _, err := exec.LookPath(p.cfg.Command); err != nil {
			return fmt.Errorf("%s 官方 CLI 未安装: %w", p.name, err)
		}
		if p.name == "buzz" {
			if p.cfg.Endpoint == "" || p.cfg.Secret == "" {
				return errors.New("buzz 需要 endpoint（relay）和 secret（Nostr 私钥）")
			}
			if len(p.cfg.Channels) == 0 {
				return errors.New("buzz 需要 channels（明确监听的频道 ID）")
			}
		}
	case "ntfy":
		if len(p.cfg.Channels) == 0 {
			return errors.New("ntfy 需要 channels（topic）")
		}
	case "line", "feishu", "whatsapp_cloud", "teams", "google_chat", "wecom_callback", "sms", "bluebubbles", "msgraph_webhook":
		if p.cfg.Listen == "" {
			return fmt.Errorf("%s 需要原生事件回调 listen", p.name)
		}
	}
	if p.name == "raft" {
		return p.startRaftBridge(ctx)
	}
	if p.name == "mattermost" {
		out, err := p.request(ctx, "GET", "api/v4/users/me", nil)
		if err != nil {
			return err
		}
		p.cfg.BotID = str(out["id"])
		if p.cfg.BotID == "" {
			return errors.New("mattermost users/me 未返回机器人 ID")
		}
	}
	return ctx.Err()
}

func (p *connector) receiveStream(ctx context.Context) error {
	path := ""
	switch p.name {
	case "signal":
		path = "api/v1/events?account=" + url.QueryEscape(p.cfg.Phone)
	case "photon":
		path = "inbound"
	case "ntfy":
		path = strings.Join(p.cfg.Channels, ",") + "/json?since=" + url.QueryEscape(p.state("stream_cursor"))
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(p.cfg.Endpoint, "/")+"/"+path, nil)
	if err != nil {
		return err
	}
	if err = p.authorizeRequest(ctx, req); err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	client := *p.client
	client.Timeout = 0
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return fmt.Errorf("入站 stream HTTP %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if p.name == "signal" {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		var event map[string]any
		if err = json.Unmarshal([]byte(line), &event); err != nil {
			return err
		}
		messages, err := p.decodeRealtime(ctx, event)
		if err != nil {
			return err
		}
		for _, msg := range messages {
			if err = p.accept(ctx, msg); err != nil {
				if isDenied(err) {
					continue
				}
				return err
			}
		}
		if id := stableID(event); id != "" {
			if err = p.setState(ctx, "stream_cursor", id); err != nil {
				return err
			}
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}
func isDenied(err error) bool {
	return strings.Contains(err.Error(), "授权") || strings.Contains(err.Error(), "提及")
}

func (p *connector) receivePoll(ctx context.Context) error {
	ticker := time.NewTicker(time.Duration(p.cfg.PollSeconds) * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		switch p.name {
		case "whatsapp":
			request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(p.cfg.Endpoint, "/")+"/messages", nil)
			if err != nil {
				return err
			}
			if err = p.authorizeRequest(ctx, request); err != nil {
				return err
			}
			response, err := p.client.Do(request)
			if err != nil {
				return err
			}
			if response.StatusCode != 200 {
				_ = response.Body.Close()
				return fmt.Errorf("WhatsApp 桥接 /messages HTTP %d", response.StatusCode)
			}
			var events []map[string]any
			err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&events)
			_ = response.Body.Close()
			if err != nil {
				return err
			}
			for _, event := range events {
				if err = p.dispatchRealtime(ctx, event); err != nil && !isDenied(err) {
					return err
				}
			}
		case "weixin":
			out, err := p.request(ctx, "POST", "ilink/bot/getupdates", map[string]any{"get_updates_buf": p.state("weixin_cursor"), "base_info": map[string]string{"channel_version": "2.2.0"}})
			if err != nil {
				return err
			}
			for _, raw := range arr(out["msgs"]) {
				if err = p.dispatchRealtime(ctx, obj(raw)); err != nil && !isDenied(err) {
					return err
				}
			}
			if next := str(out["get_updates_buf"]); next != "" {
				if err = p.setState(ctx, "weixin_cursor", next); err != nil {
					return err
				}
			}
		case "buzz":
			for _, channel := range p.cfg.Channels {
				args := []string{"messages", "get", "--channel", channel, "--limit", "100"}
				if cursor := p.state("buzz:" + channel); cursor != "" {
					args = append(args, "--since", cursor)
				}
				data, err := p.cli(ctx, args, "")
				if err != nil {
					return err
				}
				var events []map[string]any
				if err = json.Unmarshal(data, &events); err != nil {
					return err
				}
				for _, event := range events {
					msg := chat.Message{ID: str(event["id"]), SenderID: str(event["pubkey"]), Text: str(event["content"]), Direct: false, Session: chat.Session{Conversation: channel}}
					if err = p.accept(ctx, msg); err != nil && !isDenied(err) {
						return err
					}
					if cursor := str(event["created_at"]); cursor != "" {
						if err = p.setState(ctx, "buzz:"+channel, cursor); err != nil {
							return err
						}
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return ctx.Err()
}
func (p *connector) dispatchRealtime(ctx context.Context, event map[string]any) error {
	messages, err := p.decodeRealtime(ctx, event)
	if err != nil {
		return err
	}
	for _, msg := range messages {
		if err = p.accept(ctx, msg); err != nil {
			return err
		}
	}
	return nil
}

func (p *connector) cli(ctx context.Context, args []string, input string) ([]byte, error) {
	command := exec.CommandContext(ctx, p.cfg.Command, args...)
	command.Stdin = strings.NewReader(input)
	command.Env = os.Environ()
	if p.name == "buzz" {
		command.Env = append(command.Env, "BUZZ_RELAY_URL="+p.cfg.Endpoint, "BUZZ_PRIVATE_KEY="+p.cfg.Secret)
	}
	data, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("%s CLI: %s", p.name, p.redact(string(exit.Stderr)))
		}
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, errors.New("平台 CLI 返回超限")
	}
	return data, nil
}
func (p *connector) sendBuzz(ctx context.Context, reply chat.Reply) (string, error) {
	args := []string{"messages", "send", "--channel", reply.Session.Conversation, "--content", "-"}
	if reply.ReplyTo != "" {
		args = append(args, "--reply-to", reply.ReplyTo)
	}
	data, err := p.cli(ctx, args, reply.Text)
	if err != nil {
		return "", err
	}
	var response map[string]any
	if err = json.Unmarshal(data, &response); err != nil {
		return "", err
	}
	id := stableID(response)
	if id == "" {
		return "", errors.New("buzz CLI 未返回可确认事件 ID")
	}
	return id, nil
}
