// Package acp 通过 stdin/stdout 以 Agent Client Protocol 把编辑器接入本机 Saber 服务。
//
// ACP 只充当本机代理：它把编辑器的会话提示转成 OpenAI 兼容请求，交给已经运行的
// `saber serve` 处理，不重复装配模型、任务或工具服务。协议使用 NDJSON 形式的
// JSON-RPC 2.0，每行一个完整的请求、响应或通知。
package acp

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"rua.plus/saber/internal/config"
)

const usageText = `用法: saber acp [选项]

通过 stdin/stdout 以 ACP (JSON-RPC 2.0) 与编辑器通信。使用前需要先运行 saber serve，
令牌由配置中的 server.token_file 指向的文件提供。

选项:
  -c, --config <路径>   配置文件路径（默认 ./config.yaml）
      --server <URL>    Saber 服务地址（默认 http://<server.listen>）
  -h, --help            显示本帮助
`

// options 是 saber acp 子命令的命令行选项。
type options struct {
	// configPath 是 Saber 配置文件路径，默认 ./config.yaml。
	configPath string
	// serverURL 非空时覆盖配置中的服务地址。
	serverURL string
}

// Run 以 os.Stdin 为输入启动 ACP 服务，直到输入结束或上下文取消。
//
// 用户可见的错误已写入 stderr，返回值只用于让入口设置非零退出码。
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return run(ctx, args, os.Stdin, stdout, stderr)
}

// run 是 Run 的可注入输入版本，便于测试用管道驱动协议。
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	opts, err := parseArgs(args, stdout)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return report(stderr, err)
	}

	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return report(stderr, fmt.Errorf("加载配置 %s 失败: %w", opts.configPath, err))
	}
	token, err := readToken(cfg.Server.TokenPath(opts.configPath))
	if err != nil {
		return report(stderr, err)
	}

	endpoint := opts.serverURL
	if endpoint == "" {
		if strings.TrimSpace(cfg.Server.Listen) == "" {
			return report(stderr, errors.New("配置缺少 server.listen，且未通过 --server 指定服务地址"))
		}
		endpoint = "http://" + cfg.Server.Listen
	}
	client, err := newChatClient(endpoint, token)
	if err != nil {
		return report(stderr, err)
	}
	if err := client.probe(ctx); err != nil {
		return report(stderr, err)
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	c := newConn(ctx, client, stdin, stdout, logger)
	if err := c.serve(); err != nil {
		return report(stderr, err)
	}
	return nil
}

// parseArgs 解析子命令选项；--help 时把用法写入 stdout。
func parseArgs(args []string, stdout io.Writer) (options, error) {
	opts := options{configPath: "./config.yaml"}
	fs := flag.NewFlagSet("saber acp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.configPath, "config", opts.configPath, "配置文件路径")
	fs.StringVar(&opts.configPath, "c", opts.configPath, "配置文件路径（简写）")
	fs.StringVar(&opts.serverURL, "server", "", "Saber 服务地址")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, werr := io.WriteString(stdout, usageText); werr != nil {
				return options{}, fmt.Errorf("输出帮助失败: %w", werr)
			}
			return options{}, flag.ErrHelp
		}
		return options{}, fmt.Errorf("参数错误: %w（使用 --help 查看用法）", err)
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("多余参数: %s", fs.Arg(0))
	}
	return opts, nil
}

// readToken 读取服务端令牌；令牌值本身不写入日志或错误信息。
func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取服务端令牌失败（%s）: %w；请先运行 `saber serve` 生成令牌", path, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("服务端令牌文件 %s 为空，请先运行 `saber serve` 生成令牌", path)
	}
	return token, nil
}

// report 把错误写入 stderr 并返回，入口据此设置非零退出码。
func report(stderr io.Writer, err error) error {
	if _, werr := fmt.Fprintf(stderr, "saber acp: %v\n", err); werr != nil {
		return fmt.Errorf("%v（写入 stderr 失败: %w）", err, werr)
	}
	return err
}
