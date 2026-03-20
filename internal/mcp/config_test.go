//go:build goolm

package mcp

import (
	"testing"

	"rua.plus/saber/internal/config"
)

func TestValidateServerConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.ServerConfig
		wantErr bool
	}{
		{
			name: "禁用服务器",
			cfg: &config.ServerConfig{
				Enabled: false,
				Type:    "any",
			},
			wantErr: false,
		},
		{
			name: "内置服务器已启用",
			cfg: &config.ServerConfig{
				Enabled: true,
				Type:    ServerTypeBuiltin,
			},
			wantErr: false,
		},
		{
			name: "Stdio 服务器有命令",
			cfg: &config.ServerConfig{
				Enabled: true,
				Type:    ServerTypeStdio,
				Command: "/usr/bin/mcp-server",
			},
			wantErr: false,
		},
		{
			name: "Stdio 服务器无命令",
			cfg: &config.ServerConfig{
				Enabled: true,
				Type:    ServerTypeStdio,
				Command: "",
			},
			wantErr: true,
		},
		{
			name: "HTTP 服务器有 URL",
			cfg: &config.ServerConfig{
				Enabled: true,
				Type:    ServerTypeHTTP,
				URL:     "https://example.com/mcp",
			},
			wantErr: false,
		},
		{
			name: "HTTP 服务器无 URL",
			cfg: &config.ServerConfig{
				Enabled: true,
				Type:    ServerTypeHTTP,
				URL:     "",
			},
			wantErr: true,
		},
		{
			name: "未知服务器类型",
			cfg: &config.ServerConfig{
				Enabled: true,
				Type:    "unknown",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateServerConfig(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateServerConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestServerTypeConstants(t *testing.T) {
	if ServerTypeBuiltin != "builtin" {
		t.Errorf("ServerTypeBuiltin = %q, want %q", ServerTypeBuiltin, "builtin")
	}
	if ServerTypeStdio != "stdio" {
		t.Errorf("ServerTypeStdio = %q, want %q", ServerTypeStdio, "stdio")
	}
	if ServerTypeHTTP != "http" {
		t.Errorf("ServerTypeHTTP = %q, want %q", ServerTypeHTTP, "http")
	}
}
