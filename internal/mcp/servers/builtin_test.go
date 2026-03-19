// Package servers 测试内置 MCP 服务器功能。
package servers

import (
	"context"
	"testing"

	"rua.plus/saber/internal/config"
)

func TestCreateBuiltinServer(t *testing.T) {
	ctx := context.Background()

	// 测试未知服务器
	client, session, err := CreateBuiltinServer(ctx, "unknown", nil)
	if err == nil {
		t.Error("Expected error for unknown server")
	}
	if client != nil || session != nil {
		t.Error("Expected nil client and session for unknown server")
	}

	// 测试 web_fetch 服务器（已实现）
	client, session, err = CreateBuiltinServer(ctx, "web_fetch", &config.BuiltinConfig{})
	if err != nil {
		t.Errorf("Expected success for web_fetch server, got error: %v", err)
	}
	if client == nil || session == nil {
		t.Error("Expected non-nil client and session for web_fetch server")
	}

	// 清理
	if session != nil {
		if err := session.Close(); err != nil {
			t.Logf("Warning: failed to close session: %v", err)
		}
	}
}
