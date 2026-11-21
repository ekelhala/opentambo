package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCmd records every host-tool invocation and answers from a canned
// table keyed by the full command line. Missing keys succeed with empty
// output, which is what accountExists reads as "account absent".
type fakeCmd struct {
	calls []string
	out   map[string]string
	fail  map[string]bool
	// failCmd fails any invocation whose command line starts with the
	// key, for lines containing dynamic staging paths.
	failCmd map[string]bool
}

func (f *fakeCmd) fails(line string) bool {
	if f.fail[line] {
		return true
	}
	for prefix := range f.failCmd {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeCmd) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, line)
	if f.fails(line) {
		return []byte("boom"), errors.New("exit status 1")
	}
	if o, ok := f.out[line]; ok {
		return []byte(o), nil
	}
	return nil, nil
}

func (f *fakeCmd) Chown(path, owner, group string) error {
	line := "chown " + owner + ":" + group + " " + path
	f.calls = append(f.calls, line)
	if f.fail["chown"] {
		return errors.New("chown: not permitted")
	}
	return nil
}

func (f *fakeCmd) ChownRecursive(path, owner, group string) error {
	line := "chown -R " + owner + ":" + group + " " + path
	f.calls = append(f.calls, line)
	if f.fail["chown"] {
		return errors.New("chown: not permitted")
	}
	return nil
}

func (f *fakeCmd) has(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// stageRoots points the whole standard layout into a temp staging prefix
// with a fake ELF source binary and a present native runtime, and makes
// the unprivileged test process pass the preflight checks.
func stageRoots(t *testing.T) (Config, string) {
	t.Helper()
	oldRoot, oldHost := isRootFn, checkHostFn
	isRootFn = func() bool { return true }
	checkHostFn = func() error { return nil }
	t.Cleanup(func() { isRootFn, checkHostFn = oldRoot, oldHost })

	prefix := t.TempDir()
	cfg := DefaultConfig()
	cfg.Prefix = prefix
	src := filepath.Join(prefix, "source", "opentambo")
	if err := os.MkdirAll(filepath.Dir(src), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("\x7fELF fake release"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg.SourceBinary = src
	llama := filepath.Join(prefix, cfg.LlamaServer)
	if err := os.MkdirAll(filepath.Dir(llama), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(llama, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return cfg, prefix
}

func activeFake() *fakeCmd {
	return &fakeCmd{out: map[string]string{"systemctl is-active opentambo.service": "active"}}
}

func TestPreflightRejectsBadLayout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BinPath = "relative/opentambo"
	if err := Preflight(cfg); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative path = %v", err)
	}
	cfg = DefaultConfig()
	cfg.StateDir = "/var/lib/my state"
	if err := Preflight(cfg); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("whitespace path = %v", err)
	}
	cfg = DefaultConfig()
	cfg.User = "root:x"
	if err := Preflight(cfg); err == nil || !strings.Contains(err.Error(), "account name") {
		t.Fatalf("bad user = %v", err)
	}
	cfg = DefaultConfig()
	cfg.Group = "a/b"
	if err := Preflight(cfg); err == nil || !strings.Contains(err.Error(), "account name") {
		t.Fatalf("bad group = %v", err)
	}
	cfg = DefaultConfig()
	cfg.User = ""
	if err := Preflight(cfg); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("empty user = %v", err)
	}
}

func TestPreflightRequiresRootAndSystemd(t *testing.T) {
	oldRoot, oldHost := isRootFn, checkHostFn
	defer func() { isRootFn, checkHostFn = oldRoot, oldHost }()
	isRootFn = func() bool { return false }
	checkHostFn = func() error { return nil }
	if err := Preflight(DefaultConfig()); err == nil || !strings.Contains(err.Error(), "root privileges") {
		t.Fatalf("non-root = %v", err)
	}
	isRootFn = func() bool { return true }
	checkHostFn = func() error { return errors.New("systemd is not running") }
	if err := Preflight(DefaultConfig()); err == nil || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("no systemd = %v", err)
	}
}
