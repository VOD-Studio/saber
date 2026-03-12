package main

import (
	"rua.plus/saber/internal/bot"
)

var (
	version = "dev"
	gitMsg  = "unknown"
)

func main() {
	bot.Run(version, gitMsg)
}
