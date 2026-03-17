// Package servers 提供内置 MCP 服务器实现。
package servers

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
