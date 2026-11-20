package main

import (
	"bytes"
	"context"
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
	o, err := parseArgs(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if o.listen != defaultListen || o.models != "./models" || o.binary != "llama-server" || o.data != "./data" || o.parallel != 3 {
		t.Errorf("unexpected defaults: %+v", o)
	}
}

func TestParseArgsEnvironmentDefault(t *testing.T) {
	o, err := parseArgs(nil, func(k string) string {
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
	o, err := parseArgs([]string{"-listen", "127.0.0.1:7070"}, func(string) string { return "127.0.0.1:9999" })
	if err != nil {
		t.Fatal(err)
	}
	if o.listen != "127.0.0.1:7070" {
		t.Errorf("explicit -listen must win: %q", o.listen)
	}
}

func TestParseArgsRejectsUnknownFlag(t *testing.T) {
	if _, err := parseArgs([]string{"-bogus"}, func(string) string { return "" }); err == nil {
		t.Fatal("unknown flags must fail")
	}
}

func TestParseArgsHelp(t *testing.T) {
	// -h on a ContinueOnError flag set reports ErrHelp without exiting.
	if _, err := parseArgs([]string{"-h"}, func(string) string { return "" }); err == nil {
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
	go func() { serveErr <- serve(ctx, o, &stdout) }()

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
	err := serve(context.Background(), options{listen: freeListenAddress(t), parallel: 0}, io.Discard)
	if err == nil {
		t.Fatal("invalid download parallelism must fail startup")
	}
}
