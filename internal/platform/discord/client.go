package discordplatform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rua.plus/saber/internal/config"
)

// maxAPIResponseBytes 限制单个 REST 响应体大小；Discord 响应都很小，读爆内存只可能是异常。
const maxAPIResponseBytes = 1 << 20

// userAgent 会被 Discord 边缘节点校验；带上项目标识才算合规客户端。
const userAgent = "SaberBot (https://github.com/rua.plus/saber, 0.0.5)"

// client 是 Discord REST API 客户端。
type client struct {
	base  string
	token string
	api   *http.Client
}

// newClient 按配置构造 REST 客户端。
func newClient(cfg config.DiscordConfig) *client {
	timeout := time.Duration(cfg.HTTPTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.APIBase), "/")
	if base == "" {
		base = "https://discord.com/api/v10"
	}
	transport := func() *http.Transport {
		if shared, ok := http.DefaultTransport.(*http.Transport); ok {
			return shared.Clone()
		}
		return &http.Transport{}
	}
	return &client{
		base:  base,
		token: strings.TrimSpace(cfg.BotToken),
		api:   &http.Client{Timeout: timeout, Transport: transport()},
	}
}

// do 发起一次 REST 调用并解出响应；out 为 nil 时忽略响应体。
func (c *client) do(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("discord 序列化 %s 请求失败: %w", method, err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return fmt.Errorf("discord 构造 %s 请求失败: %w", method, err)
	}
	request.Header.Set("Authorization", "Bot "+c.token)
	request.Header.Set("User-Agent", userAgent)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.api.Do(request)
	if err != nil {
		return fmt.Errorf("discord 调用 %s 失败: %w", method, err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			slog.Debug("关闭 discord 响应体失败", "method", method, "error", closeErr)
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes))
	if err != nil {
		return fmt.Errorf("discord 读取 %s 响应失败: %w", method, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return newAPIError(method, response.StatusCode, raw)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("discord 解析 %s 响应失败: %w", method, err)
	}
	return nil
}

// newAPIError 把错误响应翻译成带重试提示的 apiError。
func newAPIError(method string, status int, raw []byte) error {
	var payload struct {
		Message    string  `json:"message"`
		RetryAfter float64 `json:"retry_after"`
	}
	apiErr := &apiError{Method: method, Status: status}
	if err := json.Unmarshal(raw, &payload); err == nil {
		apiErr.Message = payload.Message
		if payload.RetryAfter > 0 {
			apiErr.retryDelay = time.Duration(payload.RetryAfter * float64(time.Second))
		}
	}
	if status == http.StatusTooManyRequests && apiErr.retryDelay == 0 {
		apiErr.retryDelay = time.Second
	}
	return apiErr
}

// gatewayURL 取本次连接应使用的 Gateway 地址。
func (c *client) gatewayURL(ctx context.Context) (string, error) {
	var out gatewayBot
	if err := c.do(ctx, http.MethodGet, "/gateway/bot", nil, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("discord gateway/bot 未返回连接地址")
	}
	return out.URL, nil
}

// sendMessage 发送一条文本消息并返回消息 ID。
//
// allowed_mentions 显式清空：模型输出里的 @everyone 之类的字样不应该变成真实 ping。
func (c *client) sendMessage(ctx context.Context, channelID, content, replyTo, nonce string) (string, error) {
	payload := map[string]any{
		"content":          content,
		"allowed_mentions": map[string]any{"parse": []string{}},
	}
	if replyTo != "" {
		payload["message_reference"] = map[string]any{"message_id": replyTo, "fail_if_not_exists": false}
	}
	if nonce != "" {
		payload["nonce"] = nonce
		payload["enforce_nonce"] = true
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/channels/"+url.PathEscape(channelID)+"/messages", payload, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("discord 发送消息未返回消息 ID")
	}
	return out.ID, nil
}

// editMessage 原地更新机器人自己发出的消息。
func (c *client) editMessage(ctx context.Context, channelID, messageID, content string) error {
	payload := map[string]any{
		"content":          content,
		"allowed_mentions": map[string]any{"parse": []string{}},
	}
	return c.do(ctx, http.MethodPatch, "/channels/"+url.PathEscape(channelID)+"/messages/"+url.PathEscape(messageID), payload, nil)
}

// sendTyping 触发频道输入状态；Discord 的输入提示约 10 秒后自动消失。
func (c *client) sendTyping(ctx context.Context, channelID string) error {
	return c.do(ctx, http.MethodPost, "/channels/"+url.PathEscape(channelID)+"/typing", nil, nil)
}
