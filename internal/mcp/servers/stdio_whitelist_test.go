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

// TestCommandWhitelist_NonStdioType 测试非 stdio 类型不需要验证。
func TestCommandWhitelist_NonStdioType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *config.ServerConfig
		wantErr bool
	}{
		{
			name: "http 类型不需要验证",
			cfg: &config.ServerConfig{
				Type:    "http",
				Command: "/bin/echo",
			},
			wantErr: false,
		},
		{
			name: "sse 类型不需要验证",
			cfg: &config.ServerConfig{
				Type:    "sse",
				Command: "/bin/echo",
			},
			wantErr: false,
		},
		{
			name: "builtin 类型不需要验证",
			cfg: &config.ServerConfig{
				Type:    "builtin",
				Command: "",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCommand(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateCommand() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestCommandWhitelist_EmptyCommand 测试空命令的处理。
func TestCommandWhitelist_EmptyCommand(t *testing.T) {
	t.Parallel()

	cfg := &config.ServerConfig{
		Type:            "stdio",
		Command:         "",
		AllowedCommands: []string{"/bin/echo"},
	}

	// 空命令时 filepath.Abs 会返回当前目录路径
	// 这应该不在白名单中
	err := validateCommand(cfg)
	if err == nil {
		t.Error("validateCommand() should deny empty command")
	}
}
