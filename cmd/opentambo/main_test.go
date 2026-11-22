package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseArgsDefaults(t *testing.T) {
	o, _, err := parseArgs(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if o.listen != defaultListen || o.models != "./models" || o.binary != "llama-server" || o.data != "./data" || o.parallel != 3 {
		t.Errorf("unexpected defaults: %+v", o)
	}
}

func TestParseArgsEnvironmentDefault(t *testing.T) {
	o, _, err := parseArgs(nil, func(k string) string {
		if k == "OPENTAMBO_LISTEN" {
			return "127.0.0.1:9999"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.listen != "127.0.0.1:9999" {
		t.Errorf("OPENTAMBO_LISTEN was not used as the default: %q", o.listen)
	}
}

func TestParseArgsExplicitFlagBeatsEnvironment(t *testing.T) {
	o, _, err := parseArgs([]string{"-listen", "127.0.0.1:7070"}, func(k string) string {
		if k == "OPENTAMBO_LISTEN" {
			return "127.0.0.1:9999"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.listen != "127.0.0.1:7070" {
		t.Errorf("explicit -listen must win: %q", o.listen)
	}
}

func TestParseArgsRejectsUnknownFlag(t *testing.T) {
	if _, _, err := parseArgs([]string{"-bogus"}, func(string) string { return "" }); err == nil {
		t.Fatal("unknown flags must fail")
	}
}

func TestParseArgsServeSubcommandKeepsFlags(t *testing.T) {
	// "serve" is the explicit run subcommand: flags after it must still be
	// parsed and the configuration file must still be discovered.
	path := stageConfig(t, yamlWith("127.0.0.1:7311", t.TempDir()))
	o, eff, err := parseArgs([]string{"serve", "--config", path}, func(string) string { return "" })
	if err != nil || eff == nil || o.listen != "127.0.0.1:7311" {
		t.Fatalf("serve must keep flags: %+v eff=%v err=%v", o, eff, err)
	}
	if _, _, err := parseArgs([]string{"serve", "-listen", "127.0.0.1:7312"}, func(string) string { return "" }); err != nil {
		t.Fatalf("flags after serve must parse: %v", err)
	}
}

func TestParseArgsRejectsStrayPositional(t *testing.T) {
	// Silently ignoring a misspelled subcommand would start the manager on
	// built-in defaults; it must fail loudly instead.
	if _, _, err := parseArgs([]string{"srve"}, func(string) string { return "" }); err == nil {
		t.Fatal("stray positional arguments must fail")
	}
}

func TestParseArgsHelp(t *testing.T) {
	// -h on a ContinueOnError flag set reports ErrHelp without exiting.
	if _, _, err := parseArgs([]string{"-h"}, func(string) string { return "" }); err == nil {
		t.Fatal("help must stop execution")
	}
}

func freeListenAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	return addr
}

func TestServeLifecycle(t *testing.T) {
	addr := freeListenAddress(t)
	o := options{
		listen:   addr,
		models:   filepath.Join(t.TempDir(), "models"),
		binary:   "llama-server",
		data:     filepath.Join(t.TempDir(), "data"),
		parallel: 2,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout bytes.Buffer
	serveErr := make(chan error, 1)
	go func() { serveErr <- serve(ctx, o, nil, &stdout) }()

	client := &http.Client{Timeout: 10 * time.Second}
	var health int
	deadline := time.Now().Add(15 * time.Second)
	for {
		res, err := client.Get("http://" + addr + "/healthz")
		if err == nil {
			health = res.StatusCode
			res.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never became healthy: %v (stdout: %s)", err, stdout.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if health != 200 {
		t.Fatalf("/healthz status = %d", health)
	}
	if !strings.Contains(stdout.String(), "opentambo listening") {
		t.Errorf("missing startup line: %q", stdout.String())
	}
	// The models directory is created on demand by desk.New.
	if _, err := os.ReadDir(o.models); err != nil {
		t.Fatalf("models dir not created: %v", err)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("clean shutdown must return nil, got %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not return after context cancellation")
	}
	// The listener is fully released.
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("port still accepting connections after shutdown")
	}
}

func TestServeReportsStartupFailure(t *testing.T) {
	// parallel outside the accepted range is rejected before the port binds.
	err := serve(context.Background(), options{listen: freeListenAddress(t), parallel: 0}, nil, io.Discard)
	if err == nil {
		t.Fatal("invalid download parallelism must fail startup")
	}
}

// stageConfig writes an operator YAML file and returns its path.
func stageConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func yamlWith(listen, dir string) string {
	return "schema_version: 1\nhttp:\n  listen: \"" + listen + "\"\npaths:\n  data_dir: \"" + dir + "/data\"\n  models_dir: \"" + dir + "/models\"\ndownloads:\n  parallel: 5\n"
}

func TestParseArgsYamlFileSuppliesValues(t *testing.T) {
	path := stageConfig(t, yamlWith("127.0.0.1:8123", t.TempDir()))
	o, eff, err := parseArgs([]string{"--config", path}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if eff == nil || o.listen != "127.0.0.1:8123" || o.parallel != 5 {
		t.Fatalf("YAML values must apply: %+v eff=%v", o, eff != nil)
	}
	if !strings.HasSuffix(o.data, "/data") || !strings.HasSuffix(o.models, "/models") {
		t.Errorf("paths must come from the file: %+v", o)
	}
}

func TestParseArgsEnvironmentOverridesYaml(t *testing.T) {
	path := stageConfig(t, yamlWith("127.0.0.1:8123", t.TempDir()))
	o, _, err := parseArgs([]string{"--config", path}, func(k string) string {
		if k == "OPENTAMBO_LISTEN" {
			return "127.0.0.1:9099"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.listen != "127.0.0.1:9099" {
		t.Errorf("OPENTAMBO_LISTEN must override the file: %q", o.listen)
	}
}

func TestParseArgsFlagOverridesEnvironmentAndYaml(t *testing.T) {
	path := stageConfig(t, yamlWith("127.0.0.1:8123", t.TempDir()))
	o, _, err := parseArgs([]string{"--config", path, "-listen", "127.0.0.1:7070"}, func(k string) string {
		if k == "OPENTAMBO_LISTEN" {
			return "127.0.0.1:9099"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.listen != "127.0.0.1:7070" {
		t.Errorf("an explicit flag must win: %q", o.listen)
	}
}

func TestParseArgsConfigEnvSelectsFile(t *testing.T) {
	path := stageConfig(t, yamlWith("127.0.0.1:8123", t.TempDir()))
	o, _, err := parseArgs(nil, func(k string) string {
		if k == "OPENTAMBO_CONFIG" {
			return path
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.listen != "127.0.0.1:8123" {
		t.Errorf("OPENTAMBO_CONFIG must select the file: %q", o.listen)
	}
}

func TestParseArgsMissingExplicitFileFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	if _, _, err := parseArgs([]string{"--config", missing}, func(string) string { return "" }); err == nil {
		t.Fatal("an explicitly selected missing file must fail startup")
	}
}

func TestParseArgsInvalidYamlFails(t *testing.T) {
	path := stageConfig(t, "schema_version: 1\nbogus: true\n")
	if _, _, err := parseArgs([]string{"--config", path}, func(string) string { return "" }); err == nil {
		t.Fatal("an unknown field must fail startup")
	}
}

func TestConfigCheckReportsValidFile(t *testing.T) {
	path := stageConfig(t, yamlWith("127.0.0.1:8123", t.TempDir())+
		"profiles:\n  tiny:\n    model: m.gguf\n    port: 8123\n    autostart: true\n")
	var stdout bytes.Buffer
	if err := runConfigCheck([]string{"--config", path}, &stdout); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "configuration OK") || !strings.Contains(out, "profile tiny") {
		t.Errorf("unexpected report: %s", out)
	}
}

func TestConfigCheckRejectsInvalidFile(t *testing.T) {
	path := stageConfig(t, "schema_version: 2\n")
	if err := runConfigCheck([]string{"--config", path}, io.Discard); err == nil {
		t.Fatal("an unsupported schema must be rejected")
	}
}

func TestConfigCheckRejectsUnknownFlag(t *testing.T) {
	if err := runConfigCheck([]string{"--bogus"}, io.Discard); err == nil {
		t.Fatal("an unknown flag must be rejected")
	}
}

func TestLifecycleCommandUnknownConfigSubcommand(t *testing.T) {
	path := stageConfig(t, yamlWith("127.0.0.1:8123", t.TempDir()))
	if handled, err := lifecycleCommand([]string{"config", "check", "--config", path}, io.Discard); !handled || err != nil {
		t.Fatalf("config check must be dispatched: handled=%v err=%v", handled, err)
	}
	handled, err := lifecycleCommand([]string{"config", "oops"}, io.Discard)
	if !handled || err == nil || !strings.Contains(err.Error(), "unknown config subcommand") {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	handled, err = lifecycleCommand([]string{"config"}, io.Discard)
	if !handled || err == nil {
		t.Fatalf("bare config must report usage: handled=%v err=%v", handled, err)
	}
	if handled, err := lifecycleCommand([]string{"serve"}, io.Discard); handled || err != nil {
		t.Fatalf("serve must fall through to the server: handled=%v err=%v", handled, err)
	}
}

func TestConfigCheckWithoutAnyFile(t *testing.T) {
	// With no --config, no env and a standard path staged to a location
	// that cannot exist, the command must say so instead of silently
	// passing.
	original := configSystemPath
	configSystemPath = filepath.Join(t.TempDir(), "absent", "config.yaml")
	defer func() { configSystemPath = original }()
	if err := runConfigCheck([]string{}, io.Discard); err == nil {
		t.Fatal("no file to check must be an error")
	}
}

// failWriter models a closed stdout (e.g. a dead pipe).
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("stdout closed") }

func TestServeReportsStdoutFailure(t *testing.T) {
	o := options{
		listen:   freeListenAddress(t),
		models:   filepath.Join(t.TempDir(), "models"),
		binary:   "llama-server",
		data:     filepath.Join(t.TempDir(), "data"),
		parallel: 1,
	}
	err := serve(context.Background(), o, nil, failWriter{})
	if err == nil || !strings.Contains(err.Error(), "stdout closed") {
		t.Fatalf("a failing stdout must abort startup with its error, got %v", err)
	}
}

// TestServeReportsBindFailure covers the ListenAndServe error path: a port
// already held by another listener must surface as a startup failure.
func TestServeReportsBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	o := options{
		listen:   listener.Addr().String(),
		models:   filepath.Join(t.TempDir(), "models"),
		binary:   "llama-server",
		data:     filepath.Join(t.TempDir(), "data"),
		parallel: 1,
	}
	if err := serve(context.Background(), o, nil, io.Discard); err == nil {
		t.Fatal("serving on an occupied port must fail")
	}
}
