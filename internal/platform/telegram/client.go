package telegramplatform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rua.plus/saber/internal/config"
)

// maxAPIResponseBytes 限制单个响应体大小：Bot API 响应很小，读爆内存只可能是异常。
const maxAPIResponseBytes = 1 << 20

// client 是 Telegram Bot API 的 HTTP 客户端。
//
// 普通请求与长轮询各用一个 http.Client：getUpdates 会在服务端挂起 poll timeout，
// 套上 http_timeout_seconds 的总超时等于自己掐断长轮询。
type client struct {
	base        string
	token       string
	api         *http.Client
	poll        *http.Client
	pollTimeout time.Duration
}

// newClient 按配置构造客户端。api_base 已由 config 校验过协议前缀，这里只去尾斜杠。
func newClient(cfg config.TelegramConfig) *client {
	timeout := time.Duration(cfg.HTTPTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	pollTimeout := time.Duration(cfg.PollTimeoutSeconds) * time.Second
	if pollTimeout <= 0 {
		pollTimeout = 30 * time.Second
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.APIBase), "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	transport := func() *http.Transport {
		if shared, ok := http.DefaultTransport.(*http.Transport); ok {
			return shared.Clone()
		}
		return &http.Transport{}
	}
	pollTransport := transport()
	// 长轮询的读超时要在挂起时长之外再留一次网络往返的余量。
	pollTransport.ResponseHeaderTimeout = pollTimeout + timeout
	return &client{
		base:        base,
		token:       strings.TrimSpace(cfg.BotToken),
		api:         &http.Client{Timeout: timeout, Transport: transport()},
		poll:        &http.Client{Transport: pollTransport},
		pollTimeout: pollTimeout,
	}
}

// endpoint 拼接带 token 的调用地址；返回值只用于发请求，绝不写日志。
func (c *client) endpoint(method string) string {
	return c.base + "/bot" + c.token + "/" + method
}

// call 发起一次 Bot API 调用并解出 result。out 为 nil 时只关心成功与否。
func (c *client) call(ctx context.Context, httpClient *http.Client, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram 序列化 %s 请求失败: %w", method, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram 构造 %s 请求失败: %w", method, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("telegram 调用 %s 失败: %w", method, err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			slog.Debug("关闭 telegram 响应体失败", "method", method, "error", closeErr)
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes))
	if err != nil {
		return fmt.Errorf("telegram 读取 %s 响应失败: %w", method, err)
	}
	var envelope apiEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("telegram 解析 %s 响应失败（HTTP %d）: %w", method, response.StatusCode, err)
	}
	if !envelope.OK {
		apiErr := &apiError{
			Method:  method,
			Status:  response.StatusCode,
			Code:    envelope.ErrorCode,
			Message: envelope.Description,
		}
		if envelope.Parameters != nil && envelope.Parameters.RetryAfter > 0 {
			apiErr.retryDelay = time.Duration(envelope.Parameters.RetryAfter) * time.Second
		}
		if response.StatusCode == http.StatusTooManyRequests && apiErr.retryDelay == 0 {
			apiErr.retryDelay = time.Second
		}
		return apiErr
	}
	if out == nil || len(envelope.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("telegram 解析 %s result 失败: %w", method, err)
	}
	return nil
}

// getMe 取机器人身份：自回声过滤与被 @ 判定都依赖它。
func (c *client) getMe(ctx context.Context) (botUser, error) {
	var out botUser
	if err := c.call(ctx, c.api, "getMe", map[string]any{}, &out); err != nil {
		return botUser{}, err
	}
	return out, nil
}

// getUpdates 长轮询新消息。offset 是下一条待确认的 update_id。
func (c *client) getUpdates(ctx context.Context, offset int64) ([]update, error) {
	payload := map[string]any{
		"offset":          offset,
		"timeout":         int(c.pollTimeout / time.Second),
		"allowed_updates": []string{"message"},
	}
	var out []update
	if err := c.call(ctx, c.poll, "getUpdates", payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// sendMessage 发送文本消息，返回可用于编辑的消息 ID。
func (c *client) sendMessage(ctx context.Context, chatID int64, threadID int, text string, replyTo int64) (message, error) {
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if threadID > 0 {
		payload["message_thread_id"] = threadID
	}
	if replyTo > 0 {
		payload["reply_parameters"] = map[string]any{"message_id": replyTo, "allow_sending_without_reply": true}
	}
	var out message
	if err := c.call(ctx, c.api, "sendMessage", payload, &out); err != nil {
		return message{}, err
	}
	return out, nil
}

// editMessageText 原地替换机器人自己发出的消息正文。
func (c *client) editMessageText(ctx context.Context, chatID, messageID int64, text string) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}
	return c.call(ctx, c.api, "editMessageText", payload, nil)
}

// sendChatAction 上报输入状态；active 为 false 时 Telegram 没有对应语义，由调用方跳过。
func (c *client) sendChatAction(ctx context.Context, chatID int64, threadID int, action string) error {
	payload := map[string]any{"chat_id": chatID, "action": action}
	if threadID > 0 {
		payload["message_thread_id"] = threadID
	}
	return c.call(ctx, c.api, "sendChatAction", payload, nil)
}

// parseChatID 解析会话 ID；Telegram 的 chat_id 是整数。
func parseChatID(conversation string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(conversation), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("telegram 会话 ID %q 不是整数: %w", conversation, err)
	}
	return id, nil
}

// parseMessageID 解析消息 ID；空值返回 0。
func parseMessageID(id string) (int64, error) {
	value := strings.TrimSpace(id)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("telegram 消息 ID %q 不是整数: %w", id, err)
	}
	return parsed, nil
}

// parseThreadID 解析话题 ID；空值或非法值都退回 0（主会话）。
func parseThreadID(thread string) int {
	if thread == "" {
		return 0
	}
	parsed, err := strconv.Atoi(thread)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}
