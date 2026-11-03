// Package main 是 Saber 的入口点。
//
// Saber 默认提供常驻聊天服务，也支持独立终端界面和可选的 Matrix 入口。
// 本文件负责启动机器人并传递构建时的版本信息。
package main

import (
	"context"
	"log/slog"
	"os"

	"rua.plus/saber/internal/acp"

	// 注册 SQLite 驱动 (纯 Go 实现)
	_ "rua.plus/saber/internal/db"

	"rua.plus/saber/internal/bot"
	"rua.plus/saber/internal/matrix"
)

var (
	version       = "dev"
	gitCommit     = "unknown"
	gitBranch     = "unknown"
	buildTime     = "unknown"
	goVersion     = "unknown"
	buildPlatform = "unknown"
)

func main() {
	// acp 是独立的 stdio 协议入口，不启动 Matrix 机器人。
	// 用户可见的错误由 acp 写入 stderr，这里只负责设置退出码。
	if len(os.Args) > 1 && os.Args[1] == "acp" {
		if err := acp.Run(context.Background(), os.Args[2:], os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
		return
	}

	err := bot.Run(matrix.BuildInfo{
		Version:       version,
		GitCommit:     gitCommit,
		GitBranch:     gitBranch,
		BuildTime:     buildTime,
		GoVersion:     goVersion,
		BuildPlatform: buildPlatform,
	})

	if err != nil {
		// 检查是否为退出码错误（如 --version、--generate-config）
		if code, ok := bot.IsExitCode(err); ok {
			os.Exit(code)
		}
		// 其他错误
		slog.Error("Saber 启动失败", "error", err)
		os.Exit(1)
	}
}
