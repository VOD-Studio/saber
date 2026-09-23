package violetplatform

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
)

// apiPrefix 是 Violet Bot API 的挂载路径。
//
// 它有意挂在 /api/v1 组内：路径前缀由 Violet 侧固定，改它等于换端点。
const apiPrefix = "/api/v1/chat/bot"

// maxContentRunes 是 Violet 出站正文的字符上限（按 Unicode 字符计，中文一个字算一个）。
const maxContentRunes = 10000

// contentTruncatedSuffix 在超长正文被截断时追加，让读者知道后面还有内容没送达。
const contentTruncatedSuffix = "\n\n…（内容过长，已截断）"

// apiError 携带状态码、Violet 错误码与消息：401/403 与 5xx 的处置方式不同，
// 只留一句文本会让上层没法判断「凭据失效」和「站点暂时不可用」。
type apiError struct {
	Code    string
	Message string
	Status  int
}

// Error 实现 error。
func (e *apiError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("violet API %d %s: %s", e.Status, e.Code, e.Message)
	case e.Message != "":
		return fmt.Sprintf("violet API %d: %s", e.Status, e.Message)
	default:
		return fmt.Sprintf("violet API %d", e.Status)
	}
}

// clientStatus 按状态码给上层分流判据：401/403 说明凭据问题，重试无意义；
// 5xx 与网络错误属于暂时不可用，应当退避重连。
func clientStatus(err error) int {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

// client 是 Violet Bot API 的 HTTP 客户端。
type client struct {
	base   string
	token  string
	api    *http.Client // 普通请求，带总超时
	stream *http.Client // SSE 长连接，不能设总超时，靠 idle 看门狗断开
}

// newClient 按配置构造客户端。endpoint 已由 config 校验过协议前缀，这里只做拼接。
func newClient(endpoint, token string, timeoutSeconds int) *client {
	transport := func() *http.Transport {
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return &http.Transport{}
		}
		return base.Clone()
	}
	timeout := time.Duration(timeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	streamTransport := transport()
	streamTransport.ResponseHeaderTimeout = timeout
	return &client{
		base:   strings.TrimSuffix(endpoint, "/") + apiPrefix,
		token:  token,
		api:    &http.Client{Timeout: timeout, Transport: transport()},
		stream: &http.Client{Transport: streamTransport},
	}
}

// profile 取本 bot 身份。入站的自回声判定与被 @ 判定都依赖它，取到一次就够。
func (c *client) profile(ctx context.Context) (botProfileDTO, error) {
	var out botProfileDTO
	if err := c.request(ctx, http.MethodGet, "/profile", nil, nil, "", &out); err != nil {
		return botProfileDTO{}, err
	}
	return out, nil
}

// conversation 读取单个会话，主要用于拿 kind 判定私聊还是群聊。
func (c *client) conversation(ctx context.Context, conversationID string) (conversationDTO, error) {
	var out conversationDTO
	path := "/conversations/" + url.PathEscape(conversationID)
	if err := c.request(ctx, http.MethodGet, path, nil, nil, "", &out); err != nil {
		return conversationDTO{}, err
	}
	return out, nil
}

// conversations 列出本 bot 参与的会话，返回第一页即可满足断线重连后的补拉准备。
func (c *client) conversations(ctx context.Context) ([]conversationDTO, error) {
	var out []conversationDTO
	query := url.Values{"limit": []string{"50"}}
	if err := c.request(ctx, http.MethodGet, "/conversations", query, nil, "", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// messages 拉取一页历史。Violet 按 created_at 倒序返回，补拉时要自己反转顺序。
func (c *client) messages(ctx context.Context, conversationID, cursor string, limit int) ([]messageDTO, string, bool, error) {
	var out []messageDTO
	query := url.Values{"limit": []string{fmt.Sprint(limit)}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	path := "/conversations/" + url.PathEscape(conversationID) + "/messages"
	meta, err := c.requestMeta(ctx, http.MethodGet, path, query, nil, "", &out)
	if err != nil {
		return nil, "", false, err
	}
	if meta == nil || meta.Pagination == nil {
		return out, "", false, nil
	}
	return out, meta.Pagination.NextCursor, meta.Pagination.HasMore, nil
}

// send 发送文本消息。idempotencyKey 是 Violet 必填的幂等头，同键重试返回同一条消息，
// 因此任务投递这类「重放应是同一条」的场景必须把上层给的 TransactionID 原样带上。
func (c *client) send(ctx context.Context, conversationID string, body outgoingMessage, idempotencyKey string) (messageDTO, error) {
	var out messageDTO
	path := "/conversations/" + url.PathEscape(conversationID) + "/messages"
	if err := c.request(ctx, http.MethodPost, path, nil, body, idempotencyKey, &out); err != nil {
		return messageDTO{}, err
	}
	return out, nil
}

// edit 整体替换自己发过的消息正文，流式回复靠它逐步定稿。
func (c *client) edit(ctx context.Context, conversationID, messageID string, body outgoingMessage) error {
	path := "/conversations/" + url.PathEscape(conversationID) + "/messages/" + url.PathEscape(messageID)
	return c.request(ctx, http.MethodPatch, path, nil, body, "", nil)
}

// setTyping 上报输入状态，204 无响应体。
func (c *client) setTyping(ctx context.Context, conversationID string, active bool) error {
	path := "/conversations/" + url.PathEscape(conversationID) + "/typing"
	return c.request(ctx, http.MethodPost, path, nil, map[string]any{"is_typing": active}, "", nil)
}

// openEvents 打开 SSE 事件流。返回的 Body 必须由调用方关闭。
func (c *client) openEvents(ctx context.Context) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/events", nil)
	if err != nil {
		return nil, fmt.Errorf("构造 violet 事件流请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 violet 事件流失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		err = statusError(resp)
		_ = resp.Body.Close()
		return nil, err
	}
	if resp.Header.Get("Content-Type") != "" && !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("violet 事件流返回了非 SSE 内容类型 %q", resp.Header.Get("Content-Type"))
	}
	return resp.Body, nil
}

// request 发一次请求并把 data 解到 out（out 为 nil 时忽略响应体）。
func (c *client) request(ctx context.Context, method, path string, query url.Values, body any, idempotencyKey string, out any) error {
	_, err := c.requestMeta(ctx, method, path, query, body, idempotencyKey, out)
	return err
}

// requestMeta 与 request 相同，但额外返回分页 meta，供游标翻页的端点使用。
func (c *client) requestMeta(ctx context.Context, method, path string, query url.Values, body any, idempotencyKey string, out any) (*envelopeMeta, error) {
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("编码 violet 请求体失败: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("构造 violet 请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.api.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 violet 失败: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			// 关闭失败不影响已取到的响应，但要留痕：连接泄漏只会在这里显形。
			slog.Warn("关闭 violet 响应失败", "error", closeErr)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 violet 响应失败: %w", err)
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, nil
	}
	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("解析 violet 响应失败: %w", err)
	}
	if out == nil || len(env.Data) == 0 {
		return env.Meta, nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return nil, fmt.Errorf("解析 violet 数据失败: %w", err)
	}
	return env.Meta, nil
}

// statusError 把非 2xx 响应转成 apiError，尽量带上 Violet 的错误码与消息。
func statusError(resp *http.Response) error {
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &apiError{Status: resp.StatusCode, Message: "响应体读取失败"}
	}
	apiErr := &apiError{Status: resp.StatusCode}
	var body errorBody
	if json.Unmarshal(payload, &body) == nil {
		apiErr.Code = body.Error
		apiErr.Message = strings.TrimSpace(body.Message)
	}
	if apiErr.Message == "" {
		apiErr.Message = strings.TrimSpace(string(payload))
	}
	return apiErr
}
