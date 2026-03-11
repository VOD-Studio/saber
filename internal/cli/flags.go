package cli

import (
	"flag"
)

// Version can be set at build time with:
// go build -ldflags="-X 'saber/internal/cli.Version=1.0.0'"
var Version = "dev"

// Flags holds parsed command-line flags.
type Flags struct {
	ConfigPath     string
	Verbose        bool
	ShowVersion    bool
	GenerateConfig bool
}

// Parse parses command-line flags and returns *Flags.
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
