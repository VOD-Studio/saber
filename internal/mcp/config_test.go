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
			name: "disabled server",
			cfg: &config.ServerConfig{
				Enabled: false,
				Type:    "any",
			},
			wantErr: false,
		},
		{
			name: "builtin server enabled",
			cfg: &config.ServerConfig{
				Enabled: true,
				Type:    ServerTypeBuiltin,
			},
			wantErr: false,
		},
		{
			name: "stdio server with command",
			cfg: &config.ServerConfig{
				Enabled: true,
				Type:    ServerTypeStdio,
				Command: "/usr/bin/mcp-server",
			},
			wantErr: false,
		},
		{
			name: "stdio server without command",
			cfg: &config.ServerConfig{
				Enabled: true,
				Type:    ServerTypeStdio,
				Command: "",
			},
			wantErr: true,
		},
		{
			name: "http server with url",
			cfg: &config.ServerConfig{
				Enabled: true,
				Type:    ServerTypeHTTP,
				URL:     "https://example.com/mcp",
			},
			wantErr: false,
		},
		{
			name: "http server without url",
			cfg: &config.ServerConfig{
				Enabled: true,
				Type:    ServerTypeHTTP,
				URL:     "",
			},
			wantErr: true,
		},
		{
			name: "unknown server type",
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
