// Package cli 提供命令行标志解析功能。
package cli

import (
	"flag"
)

// Flags 包含已解析的命令行标志。
type Flags struct {
	ConfigPath     string
	Verbose        bool
	ShowVersion    bool
	GenerateConfig bool
	OutputPath     string // generate-config 的输出路径
}

// Parse 解析命令行标志并返回 *Flags。
func Parse() *Flags {
	f := &Flags{}

	flag.StringVar(&f.ConfigPath, "config", "./config.yaml", "config file path")
	flag.StringVar(&f.ConfigPath, "c", "./config.yaml", "config file path (shorthand)")
	flag.BoolVar(&f.Verbose, "verbose", false, "enable debug logging")
	flag.BoolVar(&f.Verbose, "v", false, "enable debug logging (shorthand)")
	flag.BoolVar(&f.ShowVersion, "version", false, "show version")
	flag.BoolVar(&f.GenerateConfig, "generate-config", false, "generate example config (output to stdout, use -o to write to file)")
	flag.StringVar(&f.OutputPath, "o", "", "output file path (used with -generate-config)")

	flag.Parse()

	return f
}
