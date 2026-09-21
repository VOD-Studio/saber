// Package cli 提供命令行标志解析功能。
package cli

import (
	"flag"
	"fmt"
	"os"
)

// Flags 包含已解析的命令行标志。
type Flags struct {
	Command        string
	ParseError     error
	ConfigPath     string
	Verbose        bool
	ShowVersion    bool
	GenerateConfig bool
	OutputPath     string // generate-config 的输出路径
}

// Parse 解析命令行标志并返回 *Flags。
func Parse() *Flags {
	f, err := ParseArgs(os.Args[1:])
	f.ParseError = err
	return f
}

// ParseArgs 使用独立 FlagSet 解析子命令，保留旧的全局参数写法。
func ParseArgs(args []string) (*Flags, error) {
	f := &Flags{Command: "serve"}
	fs := flag.NewFlagSet("saber [serve]", flag.ContinueOnError)
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}

	fs.StringVar(&f.ConfigPath, "config", "./config.yaml", "config file path")
	fs.StringVar(&f.ConfigPath, "c", "./config.yaml", "config file path (shorthand)")
	fs.BoolVar(&f.Verbose, "verbose", false, "enable debug logging")
	fs.BoolVar(&f.Verbose, "v", false, "enable debug logging (shorthand)")
	fs.BoolVar(&f.ShowVersion, "version", false, "show version")
	fs.BoolVar(&f.GenerateConfig, "generate-config", false, "generate example config (output to stdout, use -o to write to file)")
	fs.StringVar(&f.OutputPath, "o", "", "output file path (used with -generate-config)")

	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() > 0 {
		if fs.NArg() != 1 || fs.Arg(0) != "serve" {
			return f, fmt.Errorf("未知子命令: %s", fs.Arg(0))
		}
	}
	return f, nil
}
