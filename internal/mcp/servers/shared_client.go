// Package servers 提供内置 MCP 服务器实现。
package servers

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// sharedHTTPClient 是共享的 HTTP 客户端，用于所有 MCP HTTP 请求。
// 使用共享 Transport 实现连接复用，减少 TCP/TLS 握手开销。
var sharedHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		// 强制 TLS 1.2 或更高版本
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	},
}

// GetSharedHTTPClient 返回共享的 HTTP 客户端实例。
func GetSharedHTTPClient() *http.Client {
	return sharedHTTPClient
}
