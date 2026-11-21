package main

import (
	"context"
	"flag"
	"io"
	"strings"

	"github.com/ekelhala/opentambo/internal/lifecycle"
)

// Lifecycle commands (install/uninstall) are dispatched before any
// application initialization: removing the service must work even when the
// model mount is absent, the native runtime is gone or the database is
// damaged. Runtime serving itself never needs privileges.

func runInstall(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("opentambo install", flag.ContinueOnError)
	cfg := lifecycle.DefaultConfig()
	var extra string
	fs.StringVar(&cfg.LlamaServer, "llama-server", cfg.LlamaServer, "absolute path of the native llama-server executable")
	fs.StringVar(&cfg.ModelsDir, "models", cfg.ModelsDir, "managed model directory")
	fs.StringVar(&cfg.StateDir, "data", cfg.StateDir, "durable state directory (SQLite database and caches)")
	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "manager listen address")
	fs.StringVar(&cfg.User, "user", cfg.User, "dedicated service account")
	fs.StringVar(&cfg.Group, "group", cfg.Group, "dedicated service group")
	fs.StringVar(&extra, "extra-groups", "", "comma-separated supplementary groups for GPU access (e.g. render,video)")
	fs.StringVar(&cfg.MountGuard, "mount", "", "path that must be a mounted filesystem before the service starts")
	fs.StringVar(&cfg.MigrateFrom, "import-state", "", "import durable state from a per-user installation's data directory (source is left untouched)")
	fs.StringVar(&cfg.Prefix, "prefix", "", "staging root for the whole layout (packaging/verification)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if extra != "" {
		cfg.ExtraGroups = strings.Split(extra, ",")
	}
	in := &lifecycle.Installer{Cfg: cfg, Cmd: lifecycle.ShellCommand{}, Out: stdout}
	return in.Install(context.Background())
}

func runUninstall(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("opentambo uninstall", flag.ContinueOnError)
	cfg := lifecycle.DefaultConfig()
	var opts lifecycle.UninstallOptions
	fs.StringVar(&cfg.User, "user", cfg.User, "service account of the installation")
	fs.StringVar(&cfg.Group, "group", cfg.Group, "service group of the installation")
	fs.StringVar(&cfg.Prefix, "prefix", "", "staging root of the layout to remove")
	fs.BoolVar(&opts.PurgeState, "purge", false, "also remove the managed state tree (SQLite database, caches)")
	fs.BoolVar(&opts.PurgeModels, "purge-models", false, "with --purge: also remove the installer-created model directory")
	fs.BoolVar(&opts.PurgeAll, "purge-all", false, "remove state, models and configuration (never external model trees)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	un := &lifecycle.Uninstaller{Cfg: cfg, Cmd: lifecycle.ShellCommand{}, Out: stdout}
	return un.Uninstall(context.Background(), opts)
}
