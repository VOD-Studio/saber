// Package servers 提供 MCP 服务器连接实现。
package servers

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"rua.plus/saber/internal/config"
)

// validateCommand 验证命令是否在白名单中。
// 如果白名单为空，默认拒绝所有命令。
func validateCommand(cfg *config.ServerConfig) error {
	// 非 stdio 类型不需要验证
	if cfg.Type != "stdio" {
		return nil
	}

	// 空白名单拒绝所有
	if len(cfg.AllowedCommands) == 0 {
		return fmt.Errorf("命令 %q 被拒绝：未配置命令白名单", cfg.Command)
	}

	// 规范化路径进行比较
	absCmd, err := filepath.Abs(cfg.Command)
	if err != nil {
		return fmt.Errorf("无法解析命令路径：%w", err)
	}

	for _, allowed := range cfg.AllowedCommands {
		absAllowed, err := filepath.Abs(allowed)
		if err != nil {
			continue
		}
		if absCmd == absAllowed {
			return nil
		}
	}

	return fmt.Errorf("命令 %q 不在白名单中", cfg.Command)
}

// CreateStdioServer 创建使用 stdio 传输的 MCP 服务器连接。
//
// 它启动指定的命令作为子进程，并通过 stdin/stdout 进行 JSON-RPC 通信。
// 环境变量从配置中传递给子进程。
func CreateStdioServer(ctx context.Context, name string, cfg *config.ServerConfig) (*mcp.Client, *mcp.ClientSession, error) {
	// 验证命令字段
	if cfg.Command == "" {
		return nil, nil, fmt.Errorf("stdio 服务器 %s 缺少 command 字段", name)
	}

	// 验证命令白名单
	if err := validateCommand(cfg); err != nil {
		return nil, nil, fmt.Errorf("命令验证失败：%w", err)
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
	transport := &mcp.CommandTransport{
		Command: cmd,
	}

	// 创建客户端
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "saber-bot",
		Version: "1.0.0",
	}, nil)

	// 连接到服务器
	session, err := client.Connect(cmdCtx, transport, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("连接 stdio 服务器 %s 失败: %w", name, err)
	}

	return client, session, nil
}
