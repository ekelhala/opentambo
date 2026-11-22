package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ekelhala/opentambo/internal/desk"
)

// options is the effective startup configuration resolved from flags with
// the documented environment fallbacks.
type options struct {
	listen   string
	models   string
	binary   string
	data     string
	parallel int
}

const defaultListen = "127.0.0.1:8090"

// parseArgs resolves startup options. The OPENTAMBO_LISTEN environment
// variable supplies the default listen address; an explicit -listen flag
// still wins.
func parseArgs(args []string, getenv func(string) string) (options, error) {
	envListen := getenv("OPENTAMBO_LISTEN")
	if envListen == "" {
		envListen = defaultListen
	}
	fs := flag.NewFlagSet("opentambo", flag.ContinueOnError)
	o := options{}
	fs.StringVar(&o.listen, "listen", envListen, "HTTP listen address (env OPENTAMBO_LISTEN)")
	fs.StringVar(&o.models, "models", "./models", "model directory")
	fs.StringVar(&o.binary, "llama-server", "llama-server", "native llama-server executable")
	fs.StringVar(&o.data, "data", "./data", "job history and launch settings directory")
	fs.IntVar(&o.parallel, "downloads", 3, "maximum parallel downloads")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	return o, nil
}

// serve runs the manager until ctx is cancelled, then shuts the HTTP server
// and all managed children down gracefully.
func serve(ctx context.Context, o options, stdout io.Writer) error {
	app, err := desk.New(o.models, o.binary, o.data, o.parallel)
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
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		app.Close()
		return err
	}
	// Wait for child processes and download workers to finish on shutdown.
	<-shutdownRequested
	app.Close()
	return nil
}

// lifecycleCommand dispatches install/uninstall before any serving setup
// or flag parsing for the server itself; uninstall in particular must work
// with a damaged database, an absent model mount or a missing native
// runtime. It reports whether the subcommand was handled.
func lifecycleCommand(args []string, stdout io.Writer) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "install":
		return true, runInstall(args[1:], stdout)
	case "uninstall":
		return true, runUninstall(args[1:], stdout)
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
	o, err := parseArgs(os.Args[1:], os.Getenv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, o, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
