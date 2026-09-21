// Package main 是 Saber 的入口点。
//
// Saber 默认提供终端 AI 对话，也支持可选的 Matrix 入口、端到端加密和自动重连。
// 本文件负责启动机器人并传递构建时的版本信息。
package main

import (
	"log/slog"
	"os"

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
		slog.Error("机器人启动失败", "error", err)
		os.Exit(1)
	}
}
