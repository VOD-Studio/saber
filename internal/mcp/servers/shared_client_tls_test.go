// Package servers 提供内置 MCP 服务器实现。
package servers

import (
	"crypto/tls"
	"net/http"
	"testing"
)

// TestSharedHTTPClientTLSMinVersion 测试共享 HTTP 客户端的 TLS 最低版本。
func TestSharedHTTPClientTLSMinVersion(t *testing.T) {
	t.Parallel()

	client := GetSharedHTTPClient()
	if client == nil {
		t.Fatal("GetSharedHTTPClient() returned nil")
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("client.Transport is not *http.Transport")
	}

	if transport.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig is nil")
	}

	expectedMinVersion := uint16(tls.VersionTLS12)
	if transport.TLSClientConfig.MinVersion < expectedMinVersion {
		t.Errorf("TLS MinVersion = %v, want >= %v (TLS 1.2)",
			transport.TLSClientConfig.MinVersion, expectedMinVersion)
	}
}
