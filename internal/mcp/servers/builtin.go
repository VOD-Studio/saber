// Package servers 提供内置 MCP 服务器实现。
package servers

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// BuiltinServers 定义所有可用的内置 MCP 服务器名称。
// 这些服务器不需要配置即可自动启用。
var BuiltinServers = []string{
	"web_fetch",
}

// BuiltinServerInfo 包含内置服务器的元信息。
type BuiltinServerInfo struct {
	Name        string
	Description string
	Tools       []ToolInfo
}

// ToolInfo 包含工具的元信息。
type ToolInfo struct {
	Name        string
	Description string
}

// BuiltinServersInfo 返回所有内置服务器的详细信息。
func BuiltinServersInfo() []BuiltinServerInfo {
	return []BuiltinServerInfo{
		{
			Name:        "web_fetch",
			Description: "网页获取与内容提取",
			Tools: []ToolInfo{
				{Name: "fetch_url", Description: "获取网页内容并转换为文本"},
			},
		},
	}
}

// CreateBuiltinServer 创建内置 MCP 服务器并使用内存传输。
func CreateBuiltinServer(ctx context.Context, name string) (*mcp.Client, *mcp.ClientSession, error) {
	var server *mcp.Server

	switch name {
	case "web_fetch":
		server = NewWebFetchServer()
	default:
		return nil, nil, fmt.Errorf("未知的内置服务器: %s", name)
	}

	// 创建内存传输
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	// 在 goroutine 中启动服务器
	go func() {
		if err := server.Run(ctx, serverTransport); err != nil {
			slog.Error("内置服务器失败", "server", name, "error", err)
		}
	}()

	// 创建并连接客户端
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "saber-bot",
		Version: "1.0.0",
	}, nil)

	session, err := client.Connect(ctx, clientTransport)
	if err != nil {
		return nil, nil, fmt.Errorf("连接内置服务器失败: %w", err)
	}

	return client, session, nil
}
