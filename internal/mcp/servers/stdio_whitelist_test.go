// Package servers 提供内置 MCP 服务器实现。
package servers

import (
	"testing"

	"rua.plus/saber/internal/config"
)

// TestCommandWhitelist_EmptyDeniesAll 测试空白名单拒绝所有命令。
func TestCommandWhitelist_EmptyDeniesAll(t *testing.T) {
	t.Parallel()

	cfg := &config.ServerConfig{
		Type:    "stdio",
		Command: "/bin/echo",
		Args:    []string{"test"},
	}

	err := validateCommand(cfg)
	if err == nil {
		t.Error("validateCommand() should deny command when whitelist is empty")
	}
}

// TestCommandWhitelist_AllowConfigured 测试白名单允许配置的命令。
func TestCommandWhitelist_AllowConfigured(t *testing.T) {
	t.Parallel()

	cfg := &config.ServerConfig{
		Type:            "stdio",
		Command:         "/bin/echo",
		Args:            []string{"test"},
		AllowedCommands: []string{"/bin/echo", "/usr/bin/cat"},
	}

	err := validateCommand(cfg)
	if err != nil {
		t.Errorf("validateCommand() should allow whitelisted command, got error: %v", err)
	}
}

// TestCommandWhitelist_DenyNotInWhitelist 测试拒绝不在白名单的命令。
func TestCommandWhitelist_DenyNotInWhitelist(t *testing.T) {
	t.Parallel()

	cfg := &config.ServerConfig{
		Type:            "stdio",
		Command:         "/bin/bash",
		Args:            []string{"-c", "echo test"},
		AllowedCommands: []string{"/bin/echo", "/usr/bin/cat"},
	}

	err := validateCommand(cfg)
	if err == nil {
		t.Error("validateCommand() should deny command not in whitelist")
	}
}
