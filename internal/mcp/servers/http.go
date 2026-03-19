// Package servers 提供 MCP 服务器连接实现。
package servers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"rua.plus/saber/internal/config"
)

// CreateHTTPServer 创建使用 HTTP 传输的 MCP 服务器连接。
//
// 它使用 Bearer 令牌认证来保护 HTTP 连接。
// 令牌必须通过配置的 token 字段提供，否则将返回错误。
func CreateHTTPServer(ctx context.Context, name string, cfg *config.ServerConfig) (*mcp.Client, *mcp.ClientSession, error) {
	if cfg.URL == "" {
		return nil, nil, fmt.Errorf("http 服务器 %s 缺少 url 字段", name)
	}

	if cfg.Token == "" {
		return nil, nil, fmt.Errorf("http 服务器 %s 缺少 token 字段（需要 Bearer 认证）", name)
	}

	timeout := 30 * time.Second
	if cfg.Timeout > 0 {
		timeout = time.Duration(cfg.Timeout) * time.Second
	}

	httpClient := &http.Client{
		Timeout: timeout,
		Transport: &bearerTransport{
			token:   cfg.Token,
			wrapped: http.DefaultTransport,
		},
	}

	transport := &mcp.StreamableClientTransport{
		Endpoint:   cfg.URL,
		HTTPClient: httpClient,
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "saber-bot",
		Version: "1.0.0",
	}, nil)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("连接 http 服务器 %s 失败: %w", name, err)
	}

	return client, session, nil
}

// bearerTransport 是一个 HTTP 传输包装器，自动添加 Bearer 认证头。
type bearerTransport struct {
	token   string
	wrapped http.RoundTripper
}

// RoundTrip 实现 http.RoundTripper 接口，添加 Authorization 头。
func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.wrapped.RoundTrip(req)
}
