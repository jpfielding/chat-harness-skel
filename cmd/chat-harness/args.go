package main

import (
	"flag"
	"fmt"
	"os"
)

// args holds parsed command-line options.
type args struct {
	ConfigPath           string
	ValidateConfig       bool
	AllowUnauthenticated bool
	Addr                 string
	ShowVersion          bool
}

func parseArgs(argv []string) (*args, error) {
	fs := flag.NewFlagSet("chat-harness", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	a := &args{}
	fs.StringVar(&a.ConfigPath, "config", "config.toml", "path to TOML config")
	fs.BoolVar(&a.ValidateConfig, "validate-config", false, "validate config and exit")
	fs.BoolVar(&a.AllowUnauthenticated, "allow-unauthenticated", false, "run HTTP server without bearer auth (dev only)")
	fs.StringVar(&a.Addr, "addr", "", "override HTTP listen address (e.g. :8080)")
	fs.BoolVar(&a.ShowVersion, "version", false, "print version info and exit")

	if err := fs.Parse(argv); err != nil {
		return nil, fmt.Errorf("parse args: %w", err)
	}
	return a, nil
}
