package main

import (
	"os"
	"strings"
	"testing"
)

// The lifecycle subcommands must be reachable before any serving setup,
// but they demand root: unprivileged test processes have to be refused
// before anything on the host is touched. Root test runners (containers)
// would get past that gate and stop at the missing-runtime preflight, so
// only the failure paths are exercised here.

func TestInstallCommandFlagParsing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root test runner: refuse to probe lifecycle flags against the real layout")
	}
	err := runInstall(nil, discardWriter{})
	if err == nil || !strings.Contains(err.Error(), "root privileges") {
		t.Fatalf("unprivileged install = %v", err)
	}
	err = runInstall([]string{"--listen", "127.0.0.1:9999", "--extra-groups", "render,video"}, discardWriter{})
	if err == nil || !strings.Contains(err.Error(), "root privileges") {
		t.Fatalf("unprivileged install --listen = %v", err)
	}
	if err := runInstall([]string{"--nope"}, discardWriter{}); err == nil {
		t.Fatal("unknown flag accepted")
	}
}

func TestUninstallCommandFlagParsing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root test runner: refuse to probe lifecycle flags against the real layout")
	}
	err := runUninstall(nil, discardWriter{})
	if err == nil || !strings.Contains(err.Error(), "root privileges") {
		t.Fatalf("unprivileged uninstall = %v", err)
	}
	err = runUninstall([]string{"--purge", "--purge-models", "--prefix", "/tmp/does-not-exist-opentambo"}, discardWriter{})
	if err == nil || !strings.Contains(err.Error(), "root privileges") {
		t.Fatalf("unprivileged uninstall --purge = %v", err)
	}
	if err := runUninstall([]string{"--nope"}, discardWriter{}); err == nil {
		t.Fatal("unknown flag accepted")
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestLifecycleDispatch(t *testing.T) {
	// Non-lifecycle arguments fall through to server flag parsing.
	for _, args := range [][]string{nil, {}, {"--listen", "127.0.0.1:8090"}, {"--version"}} {
		if handled, err := lifecycleCommand(args, discardWriter{}); handled || err != nil {
			t.Fatalf("lifecycleCommand(%v) = handled %v err %v, want fall-through", args, handled, err)
		}
	}
	// Lifecycle subcommands are always handled; unprivileged they must
	// fail with the privileges error (covered by the flag-parsing tests).
	if os.Geteuid() == 0 {
		t.Skip("root test runner: refuse to probe lifecycle dispatch against the real layout")
	}
	handled, err := lifecycleCommand([]string{"install"}, discardWriter{})
	if !handled || err == nil || !strings.Contains(err.Error(), "root privileges") {
		t.Fatalf("install dispatch = handled %v err %v", handled, err)
	}
	handled, err = lifecycleCommand([]string{"uninstall", "--purge"}, discardWriter{})
	if !handled || err == nil || !strings.Contains(err.Error(), "root privileges") {
		t.Fatalf("uninstall dispatch = handled %v err %v", handled, err)
	}
}
