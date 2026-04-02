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

// TestBuiltinServersInfo 测试 BuiltinServersInfo 函数。
func TestBuiltinServersInfo(t *testing.T) {
	info := BuiltinServersInfo()

	if len(info) == 0 {
		t.Error("BuiltinServersInfo should return at least one server")
	}

	// 验证 web_fetch 服务器
	var foundWebFetch bool
	for _, srv := range info {
		if srv.Name == "web_fetch" {
			foundWebFetch = true
			if srv.Description == "" {
				t.Error("web_fetch server should have description")
			}
			if len(srv.Tools) == 0 {
				t.Error("web_fetch server should have tools")
			}
		}
	}
	if !foundWebFetch {
		t.Error("BuiltinServersInfo should include web_fetch server")
	}

	// 验证 web_search 服务器
	var foundWebSearch bool
	for _, srv := range info {
		if srv.Name == "web_search" {
			foundWebSearch = true
			if srv.Description == "" {
				t.Error("web_search server should have description")
			}
		}
	}
	if !foundWebSearch {
		t.Error("BuiltinServersInfo should include web_search server")
	}

	// 验证 js_sandbox 服务器
	var foundJSSandbox bool
	for _, srv := range info {
		if srv.Name == "js_sandbox" {
			foundJSSandbox = true
			if srv.Description == "" {
				t.Error("js_sandbox server should have description")
			}
		}
	}
	if !foundJSSandbox {
		t.Error("BuiltinServersInfo should include js_sandbox server")
	}
}

// TestBuiltinServers_List 测试 BuiltinServers 列表。
func TestBuiltinServers_List(t *testing.T) {
	if len(BuiltinServers) == 0 {
		t.Error("BuiltinServers should not be empty")
	}

	// 验证 BuiltinServers 包含预期的服务器
	expectedServers := map[string]bool{
		"web_fetch":  false,
		"web_search": false,
		"js_sandbox": false,
	}

	for _, name := range BuiltinServers {
		if _, ok := expectedServers[name]; ok {
			expectedServers[name] = true
		}
	}

	for name, found := range expectedServers {
		if !found {
			t.Errorf("BuiltinServers should include %s", name)
		}
	}
}

// TestToolInfo 测试 ToolInfo 结构体。
func TestToolInfo(t *testing.T) {
	tool := ToolInfo{
		Name:        "test_tool",
		Description: "A test tool",
	}

	if tool.Name != "test_tool" {
		t.Errorf("expected name 'test_tool', got %q", tool.Name)
	}
	if tool.Description != "A test tool" {
		t.Errorf("expected description 'A test tool', got %q", tool.Description)
	}
}

// TestBuiltinServerInfo 测试 BuiltinServerInfo 结构体。
func TestBuiltinServerInfo(t *testing.T) {
	info := BuiltinServerInfo{
		Name:        "test_server",
		Description: "A test server",
		Tools: []ToolInfo{
			{Name: "tool1", Description: "Tool 1"},
			{Name: "tool2", Description: "Tool 2"},
		},
	}

	if info.Name != "test_server" {
		t.Errorf("expected name 'test_server', got %q", info.Name)
	}
	if len(info.Tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(info.Tools))
	}
}
