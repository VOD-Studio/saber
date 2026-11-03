package slackplatform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"rua.plus/saber/internal/config"
)

// maxAPIResponseBytes 限制单个响应体大小；Slack 响应都很小。
const maxAPIResponseBytes = 1 << 20

// client 是 Slack Web API 与 Socket Mode 的共享客户端。
//
// 两个 token 权限互相独立：bot_token（xoxb-）调 Web API，app_token（xapp-）
// 只用于 apps.connections.open。混用会得到 not_authed/invalid_auth。
type client struct {
	base     string
	botToken string
	appToken string
	api      *http.Client
}

// newClient 按配置构造客户端。
func newClient(cfg config.SlackConfig) *client {
	timeout := time.Duration(cfg.HTTPTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.APIBase), "/")
	if base == "" {
		base = "https://slack.com/api"
	}
	return &client{
		base:     base,
		botToken: strings.TrimSpace(cfg.BotToken),
		appToken: strings.TrimSpace(cfg.AppToken),
		api:      &http.Client{Timeout: timeout},
	}
}

// post 以表单编码调用一个 API，并把响应解到 out。
//
// 成功与否看响应体里的 ok 字段而不是 HTTP 状态：Slack 大量业务错误也返回 200。
func (c *client) post(ctx context.Context, method, token string, values url.Values, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+method, strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("slack 构造 %s 请求失败: %w", method, err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.api.Do(request)
	if err != nil {
		return fmt.Errorf("slack 调用 %s 失败: %w", method, err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			slog.Debug("关闭 slack 响应体失败", "method", method, "error", closeErr)
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes))
	if err != nil {
		return fmt.Errorf("slack 读取 %s 响应失败: %w", method, err)
	}
	var envelope slackResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("slack 解析 %s 响应失败（HTTP %d）: %w", method, response.StatusCode, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.OK {
		apiErr := &apiError{Method: method, Status: response.StatusCode, Code: envelope.Error}
		if apiErr.Code == "" {
			apiErr.Code = http.StatusText(response.StatusCode)
		}
		if retryAfter, parseErr := strconv.ParseFloat(response.Header.Get("Retry-After"), 64); parseErr == nil && retryAfter > 0 {
			apiErr.retryDelay = time.Duration(retryAfter * float64(time.Second))
		}
		if response.StatusCode == http.StatusTooManyRequests && apiErr.retryDelay == 0 {
			apiErr.retryDelay = time.Second
		}
		return apiErr
	}
	if out == nil || len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("slack 解析 %s result 失败: %w", method, err)
	}
	return nil
}

// authTest 取 bot 自身的 user_id：群聊 @ 检测与自回声过滤都依赖它。
func (c *client) authTest(ctx context.Context) (authIdentity, error) {
	var out authIdentity
	if err := c.post(ctx, "auth.test", c.botToken, url.Values{}, &out); err != nil {
		return authIdentity{}, err
	}
	return out, nil
}

// connectionsOpen 用 app_token 换取本次 Socket Mode 的 WebSocket 地址。
func (c *client) connectionsOpen(ctx context.Context) (string, error) {
	var out slackResponse
	if err := c.post(ctx, "apps.connections.open", c.appToken, url.Values{}, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("slack apps.connections.open 未返回连接地址")
	}
	return out.URL, nil
}

// postMessage 发送一条消息并返回消息 ts（Slack 的消息 ID）。
//
// threadTS 非空时发到对应线程：Slack 没有独立的引用字段，线程就是回复的载体。
func (c *client) postMessage(ctx context.Context, channel, text, threadTS string) (string, error) {
	values := url.Values{"channel": {channel}, "text": {text}}
	if threadTS != "" {
		values.Set("thread_ts", threadTS)
	}
	var out struct {
		TS string `json:"ts"`
	}
	if err := c.post(ctx, "chat.postMessage", c.botToken, values, &out); err != nil {
		return "", err
	}
	if out.TS == "" {
		return "", errors.New("slack chat.postMessage 未返回消息 ts")
	}
	return out.TS, nil
}

// updateMessage 就地更新一条消息；ts 必须是本 bot 发出的消息。
func (c *client) updateMessage(ctx context.Context, channel, ts, text string) error {
	values := url.Values{"channel": {channel}, "ts": {ts}, "text": {text}}
	return c.post(ctx, "chat.update", c.botToken, values, nil)
}
