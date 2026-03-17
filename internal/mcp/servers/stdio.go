// Package servers 提供 MCP 服务器连接实现。
package servers

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"rua.plus/saber/internal/config"
)

// CreateStdioServer 创建使用 stdio 传输的 MCP 服务器连接。
//
// 它启动指定的命令作为子进程，并通过 stdin/stdout 进行 JSON-RPC 通信。
// 环境变量从配置中传递给子进程。
func CreateStdioServer(ctx context.Context, name string, cfg *config.ServerConfig) (*mcp.Client, *mcp.ClientSession, error) {
	// 验证命令字段
	if cfg.Command == "" {
		return nil, nil, fmt.Errorf("stdio 服务器 %s 缺少 command 字段", name)
	}

	// 创建命令上下文（带可选超时）
	cmdCtx := ctx
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		cmdCtx, cancel = context.WithTimeout(ctx, time.Duration(cfg.Timeout)*time.Second)
		defer cancel()
	}

	// 创建执行命令
	cmd := exec.CommandContext(cmdCtx, cfg.Command, cfg.Args...)

	// 设置环境变量
	if len(cfg.Env) > 0 {
		// 获取当前环境并追加自定义变量
		env := make([]string, 0, len(cfg.Env))
		for k, v := range cfg.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = append(cmd.Environ(), env...)
	}

	// 创建 stdio 传输
	transport := mcp.NewCommandTransport(cmd)

	// 创建客户端
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "saber-bot",
		Version: "1.0.0",
	}, nil)

	// 连接到服务器
	session, err := client.Connect(cmdCtx, transport)
	if err != nil {
		return nil, nil, fmt.Errorf("连接 stdio 服务器 %s 失败: %w", name, err)
	}

	return client, session, nil
}
