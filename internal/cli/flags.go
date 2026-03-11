package cli

import (
	"flag"
)

// Version 可在构建时使用以下命令设置：
// go build -ldflags="-X 'saber/internal/cli.Version=1.0.0'"
var Version = "dev"

// Flags 包含已解析的命令行标志。
type Flags struct {
	ConfigPath     string
	Verbose        bool
	ShowVersion    bool
	GenerateConfig bool
}

// Parse 解析命令行标志并返回 *Flags。
func Parse() *Flags {
	f := &Flags{}

	flag.StringVar(&f.ConfigPath, "config", "./config.yaml", "config file path")
	flag.StringVar(&f.ConfigPath, "c", "./config.yaml", "config file path (shorthand)")
	flag.BoolVar(&f.Verbose, "verbose", false, "enable debug logging")
	flag.BoolVar(&f.Verbose, "v", false, "enable debug logging (shorthand)")
	flag.BoolVar(&f.ShowVersion, "version", false, "show version")
	flag.BoolVar(&f.GenerateConfig, "generate-config", false, "generate example config")

	flag.Parse()

	return f
}
