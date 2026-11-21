// Package lifecycle implements the privileged installation commands
// (`opentambo install`, `opentambo uninstall`). Runtime serving needs no
// privileges; only these lifecycle commands do, and every step is
// idempotent so an interrupted install or uninstall can be re-run safely.
//
// All host effects go through two narrow seams so the whole flow is
// testable without a real root or systemd: Command runs the external
// tools (account, systemctl), and every filesystem path is resolved under
// Config.Prefix (the packaging convention called DESTDIR). Paths read
// from the installation record are re-validated before any removal, and
// generic purge never touches anything outside the managed state tree.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Command executes host tools. Production uses the real shell tools;
// tests record invocations and return canned results.
type Command interface {
	// Run executes name with args and returns combined output.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	// Chown sets ownership; the production implementation shells out to
	// chown so the privilege boundary stays in one place.
	Chown(path, owner, group string) error
	// ChownRecursive sets ownership for a whole tree (imported state).
	ChownRecursive(path, owner, group string) error
}

// ShellCommand is the production Command: it runs the real host tools.
type ShellCommand struct{}

func (ShellCommand) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (ShellCommand) Chown(path, owner, group string) error {
	_, err := exec.Command("chown", owner+":"+group, path).CombinedOutput()
	return err
}

func (ShellCommand) ChownRecursive(path, owner, group string) error {
	_, err := exec.Command("chown", "-R", owner+":"+group, path).CombinedOutput()
	return err
}

// Config describes the standard system layout. Every path is absolute and
// gets Prefix prepended, so tests (and packaging) relocate the whole
// installation by setting Prefix.
type Config struct {
	Prefix      string // staging root; normally "/" (empty string)
	BinPath     string // installed executable
	EtcDir      string // administrator configuration + install record
	StateDir    string // durable state (SQLite, cache) — the service's data dir
	ModelsDir   string // managed model directory (created when absent)
	UnitPath    string // systemd unit file
	LlamaServer string // native runtime the service will exec
	Listen      string // manager listen address
	User        string // service account
	Group       string // service group
	// ExtraGroups are appended to the service account (e.g. GPU access
	// groups). The installer never guesses them.
	ExtraGroups []string
	// MountGuard is an optional path (model mount) that must be a mounted
	// filesystem before the service starts.
	MountGuard string
	// SourceBinary overrides the copied executable (bootstrap installs
	// pass the freshly downloaded release instead of /proc/self/exe).
	SourceBinary string
	// MigrateFrom optionally imports durable state from a per-user
	// installation's --data directory. Only ever set from an explicit
	// --import-state request; the old installation is never modified.
	MigrateFrom string
}

// DefaultConfig returns the standard Linux layout from issue #16.
func DefaultConfig() Config {
	return Config{
		BinPath:     "/usr/local/bin/opentambo",
		EtcDir:      "/etc/opentambo",
		StateDir:    "/var/lib/opentambo",
		ModelsDir:   "/var/lib/opentambo/models",
		UnitPath:    "/etc/systemd/system/opentambo.service",
		LlamaServer: "/usr/local/bin/llama-server",
		Listen:      "127.0.0.1:8090",
		User:        "opentambo",
		Group:       "opentambo",
	}
}

func (c Config) path(p string) string { return filepath.Join(c.Prefix, p) }

// RecordPath is the root-controlled installation ownership record.
func (c Config) RecordPath() string { return c.path(filepath.Join(c.EtcDir, "install.json")) }

// validate rejects relative paths, whitespace, and prefix escapes before
// anything on the host is touched.
func (c Config) validate() error {
	for name, p := range map[string]string{
		"binary": c.BinPath, "etc": c.EtcDir, "state": c.StateDir,
		"models": c.ModelsDir, "unit": c.UnitPath, "llama-server": c.LlamaServer,
	} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%s path must be absolute, got %q", name, p)
		}
		if strings.ContainsAny(p, " \t\n") {
			return fmt.Errorf("%s path must not contain whitespace, got %q", name, p)
		}
	}
	if c.User == "" || c.Group == "" {
		return errors.New("service user and group are required")
	}
	if c.MigrateFrom != "" && !filepath.IsAbs(c.MigrateFrom) {
		return fmt.Errorf("--import-state path must be absolute, got %q", c.MigrateFrom)
	}
	if err := checkAccountName("user", c.User); err != nil {
		return err
	}
	return checkAccountName("group", c.Group)
}

func checkAccountName(kind, name string) error {
	if name != filepath.Base(name) || strings.ContainsAny(name, "/\\:") {
		return fmt.Errorf("service %s %q is not a plain account name", kind, name)
	}
	return nil
}

// Preflight refuses unsupported hosts before any mutation: Linux, a
// running systemd, and root privileges.
func Preflight(c Config) error {
	if err := c.validate(); err != nil {
		return err
	}
	if err := checkHostFn(); err != nil {
		return err
	}
	if !isRootFn() {
		return fmt.Errorf("this command requires root privileges; re-run with sudo (nothing was changed)")
	}
	// Fail for a missing native runtime before provisioning anything.
	if _, err := os.Stat(c.path(c.LlamaServer)); err != nil {
		return fmt.Errorf("native runtime %s is not present: %w; pass --llama-server with the absolute path of your llama.cpp build (nothing was changed)", c.LlamaServer, err)
	}
	return nil
}

// Test seams for the privileged checks: unit tests run unprivileged and
// (on CI containers) without systemd, so they substitute these variables.
// Production code never reassigns them.
var (
	isRootFn    = func() bool { return os.Geteuid() == 0 }
	checkHostFn = CheckHost
)

// CheckHost rejects non-Linux and hosts without a running systemd, with
// actionable errors and before any change is made.
func CheckHost() error {
	if err := checkLinux(); err != nil {
		return err
	}
	return checkSystemd()
}

func checkSystemd() error {
	info, err := os.Stat("/run/systemd/system")
	if err != nil || !info.IsDir() {
		return errors.New("systemd is not running on this host; automatic installation supports Linux with systemd only (nothing was changed)")
	}
	return nil
}
