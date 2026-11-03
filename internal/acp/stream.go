package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxErrorBody 限制读取上游错误体的字节数，避免异常响应占满内存。
const maxErrorBody = 64 << 10

// probeTimeout 限制启动时探测服务可用性的等待时间。
const probeTimeout = 5 * time.Second

// upstreamError 表示 Saber 服务返回的非 200 响应，不包含请求头或令牌。
type upstreamError struct {
	// Status 是 HTTP 状态码。
	Status int
	// Message 是已截断的上游错误描述。
	Message string
}

// Error 实现 error 接口。
func (e *upstreamError) Error() string {
	return fmt.Sprintf("Saber 服务返回 %d: %s", e.Status, e.Message)
}

// chatMessage 是 OpenAI 兼容接口的纯文本消息。
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest 是流式聊天请求；model 为空时由服务端选择默认模型。
type chatRequest struct {
	Model    string        `json:"model,omitempty"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// streamChunk 是 SSE 数据块；上游可能用 error 字段表示运行失败。
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

// chatClient 调用本机 Saber 的 OpenAI 兼容接口。
type chatClient struct {
	endpoint string
	token    string
	http     *http.Client
}

// newChatClient 校验服务地址并创建客户端；令牌只允许发往回环地址。
func newChatClient(endpoint, token string) (*chatClient, error) {
	normalized, err := normalizeEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	return &chatClient{endpoint: normalized, token: token, http: &http.Client{}}, nil
}

// normalizeEndpoint 校验地址是本机 HTTP 入口且不包含路径。
func normalizeEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("服务地址 %q 无效，应为 http://127.0.0.1:8320 这样的本机地址", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("服务地址 %q 仅支持 http 或 https", raw)
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("服务地址 %q 不应包含路径", raw)
	}
	host := u.Hostname()
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("服务地址 %q 不是本机地址；服务端令牌只允许发往回环地址", raw)
		}
	}
	return u.Scheme + "://" + u.Host, nil
}

// probe 通过 /v1/models 检查服务可达性和令牌有效性。
// 非鉴权类错误（例如服务未启用 AI 返回 503）不阻止启动，具体错误在提示时返回。
func (c *chatClient) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/v1/models", nil)
	if err != nil {
		return fmt.Errorf("无法构造探测请求: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接 Saber 服务 %s: %w；请先运行 `saber serve`", c.endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("Saber 服务拒绝了令牌（HTTP %d），请确认配置中的 token_file 与服务端一致", resp.StatusCode)
	}
	return nil
}

// stream 运行一次流式聊天；每收到一段文本就调用 emit。
func (c *chatClient) stream(ctx context.Context, model string, messages []chatMessage, emit func(string) error) error {
	payload, err := json.Marshal(chatRequest{Model: model, Messages: messages, Stream: true})
	if err != nil {
		return fmt.Errorf("编码聊天请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("构造聊天请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return readUpstreamError(resp)
	}
	return readSSE(ctx, resp.Body, emit)
}

// readUpstreamError 提取 OpenAI 风格错误消息并截断到可读长度。
func readUpstreamError(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	message := strings.TrimSpace(string(body))
	if err == nil {
		var envelope struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			switch {
			case envelope.Error != nil && envelope.Error.Message != "":
				message = envelope.Error.Message
			case envelope.Message != "":
				message = envelope.Message
			}
		}
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return &upstreamError{Status: resp.StatusCode, Message: truncate(message, 500)}
}

// readSSE 解析按行分隔的 SSE 流；忽略注释与心跳，[DONE] 结束。
func readSSE(ctx context.Context, body io.Reader, emit func(string) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			return nil
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("解析流式响应失败: %w", err)
		}
		if chunk.Error != nil {
			message := chunk.Error.Message
			if message == "" {
				message = "上游模型运行失败"
			}
			return &upstreamError{Status: http.StatusBadGateway, Message: truncate(message, 500)}
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content == "" {
				continue
			}
			if err := emit(choice.Delta.Content); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("读取流式响应失败: %w", err)
	}
	return nil
}

// truncate 按字符截断消息，避免多字节字符被切断。
func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
