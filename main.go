// Package main 是 Saber Matrix Bot 的入口点。
//
// Saber 是一个基于 Matrix 协议的机器人，支持 AI 对话、端到端加密和自动重连等功能。
// 本文件负责启动机器人并传递构建时的版本信息。
package main

import (
	"rua.plus/saber/internal/bot"
	"rua.plus/saber/internal/matrix"
)

var (
	version   = "dev"
	gitCommit = "unknown"
	gitBranch = "unknown"
	buildTime = "unknown"
	goVersion = "unknown"
	platform  = "unknown"
)

func main() {
	bot.Run(matrix.BuildInfo{
		Version:   version,
		GitCommit: gitCommit,
		GitBranch: gitBranch,
		BuildTime: buildTime,
		GoVersion: goVersion,
		Platform:  platform,
	})
}
