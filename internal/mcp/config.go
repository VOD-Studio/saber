// Package mcp 提供 MCP (Model Context Protocol) 集成功能。
package mcp

import (
	"fmt"

	"rua.plus/saber/internal/config"
)

// Server types
const (
	ServerTypeBuiltin = "builtin"
	ServerTypeStdio   = "stdio"
	ServerTypeHTTP    = "http"
)

// ValidateServerConfig 验证服务器配置。
//
// 它检查配置的类型和必需字段是否正确设置。
func ValidateServerConfig(c *config.ServerConfig) error {
	if !c.Enabled {
		return nil
	}

	switch c.Type {
	case ServerTypeBuiltin:
		// No additional validation required
	case ServerTypeStdio:
		if c.Command == "" {
			return fmt.Errorf("stdio server requires command field")
		}
	case ServerTypeHTTP:
		if c.URL == "" {
			return fmt.Errorf("http server requires url field")
		}
	default:
		return fmt.Errorf("unknown server type: %s", c.Type)
	}

	return nil
}
