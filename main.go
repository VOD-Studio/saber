package main

import (
	"rua.plus/saber/internal/bot"
)

var version = "dev"

func main() {
	bot.Run(version)
}
