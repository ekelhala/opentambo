package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ekelhala/opentambo/internal/config"
	"github.com/ekelhala/opentambo/internal/desk"
)

// options is the effective startup configuration, resolved with the
// documented precedence: explicit CLI flag > environment override >
// YAML configuration > built-in default.
type options struct {
	listen     string
	models     string
	binary     string
	data       string
	parallel   int
	configPath string
}

const defaultListen = "127.0.0.1:8090"

// builtinOptions are the manual-mode defaults used when neither YAML,
// environment nor flags supply a value.
func builtinOptions() options {
	return options{
		listen:   defaultListen,
		models:   "./models",
		binary:   config.DefaultExecutable,
		data:     "./data",
		parallel: config.DefaultParallel,
	}
}

// parseArgs resolves startup options. A YAML file (selected by --config,
// OPENTAMBO_CONFIG or the standard system path) supplies values below the
// flags; OPENTAMBO_LISTEN still overrides the file's listen address; a
// flag the operator actually supplied always wins — parser defaults never
// clobber the file.
func parseArgs(args []string, getenv func(string) string) (options, *config.Effective, error) {
	fs := flag.NewFlagSet("opentambo", flag.ContinueOnError)
	f := options{}
	fs.StringVar(&f.configPath, "config", "", "YAML configuration file (env OPENTAMBO_CONFIG, default "+config.SystemPath+" when present)")
	fs.StringVar(&f.listen, "listen", "", "HTTP listen address (env OPENTAMBO_LISTEN)")
	fs.StringVar(&f.models, "models", "", "model directory")
	fs.StringVar(&f.binary, "llama-server", "", "native llama-server executable")
	fs.StringVar(&f.data, "data", "", "job history and launch settings directory")
	fs.IntVar(&f.parallel, "downloads", 0, "maximum parallel downloads")
	if err := fs.Parse(args); err != nil {
		return options{}, nil, err
	}
	set := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })

	o := builtinOptions()
	eff, err := config.Discovery{ConfigFlag: f.configPath, Env: getenv}.Load()
	if err != nil {
		return options{}, nil, err
	}
	if eff != nil {
		o.listen, o.models, o.binary = eff.Listen, eff.ModelsDir, eff.Executable
		o.data, o.parallel = eff.DataDir, eff.Parallel
	}
	if env := getenv("OPENTAMBO_LISTEN"); env != "" {
		o.listen = env
	}
	if set["listen"] {
		o.listen = f.listen
	}
	if set["models"] {
		o.models = f.models
	}
	if set["llama-server"] {
		o.binary = f.binary
	}
	if set["data"] {
		o.data = f.data
	}
	if set["downloads"] {
		o.parallel = f.parallel
	}
	return o, eff, nil
}

// configSystemPath is overridable in tests; it mirrors config.SystemPath.
var configSystemPath = config.SystemPath

// runConfigCheck validates the selected configuration file without any
// side effects: no database, downloads, launches or host changes, and no
// environment values are echoed.
func runConfigCheck(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("config check", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "configuration file to validate (env OPENTAMBO_CONFIG, default "+config.SystemPath+")")
	if err := fs.Parse(args); err != nil {
		return err
	}
	d := config.Discovery{ConfigFlag: *cfgPath, SystemPath: configSystemPath}
	path, _ := d.Selected()
	if path == "" {
		return fmt.Errorf("no configuration file to check: pass --config, set OPENTAMBO_CONFIG, or create %s", config.SystemPath)
	}
	eff, err := config.LoadFile(path)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "configuration OK: %s (schema_version %d)\n", path, config.SchemaVersion)
	_, _ = fmt.Fprintf(stdout, "  listen %s\n  data %s\n  models %s\n  llama-server %s\n  downloads %d\n  prompt_cache total %d bytes, profiles %d\n",
		eff.Listen, eff.DataDir, eff.ModelsDir, eff.Executable, eff.Parallel, eff.MaxTotalBytes, len(eff.Profiles))
	for name, p := range eff.Profiles {
		_, _ = fmt.Fprintf(stdout, "  profile %s: model %s port %d autostart %t\n", name, p.Model, p.Port, p.Autostart)
	}
	return nil
}

// serve runs the manager until ctx is cancelled, then shuts the HTTP server
// and all managed children down gracefully.
func serve(ctx context.Context, o options, eff *config.Effective, stdout io.Writer) error {
	app, err := desk.NewWithConfig(o.models, o.binary, o.data, o.parallel, eff)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: o.listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second}
	shutdownRequested := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(shutdownRequested)
		shutdownCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = server.Shutdown(shutdownCtx)
	}()
	if _, err := io.WriteString(stdout, "opentambo listening at http://"+o.listen+" (no authentication)\n"); err != nil {
		return err
	}
	// Configured autostart profiles launch through the normal lifecycle; a
	// failing profile is reported per profile and never takes the manager
	// down. Children run in their own process groups, so a slow or broken
	// launch cannot delay the control plane either.
	go app.AutostartConfigured()
	if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		app.Close()
		return err
	}
	// Wait for child processes and download workers to finish on shutdown.
	<-shutdownRequested
	app.Close()
	return nil
}

// lifecycleCommand dispatches install/uninstall/config before any serving
// setup or flag parsing for the server itself; uninstall in particular
// must work with a damaged database, an absent model mount or a missing
// native runtime. It reports whether the subcommand was handled.
func lifecycleCommand(args []string, stdout io.Writer) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "install":
		return true, runInstall(args[1:], stdout)
	case "uninstall":
		return true, runUninstall(args[1:], stdout)
	case "config":
		if len(args) > 1 && args[1] == "check" {
			return true, runConfigCheck(args[2:], stdout)
		}
		return true, errors.New("unknown config subcommand; supported: config check --config PATH")
	}
	return false, nil
}

func main() {
	log.SetFlags(0)
	if handled, err := lifecycleCommand(os.Args[1:], os.Stdout); handled {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	o, eff, err := parseArgs(os.Args[1:], os.Getenv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, o, eff, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
