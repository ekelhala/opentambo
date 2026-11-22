package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Installer performs the privileged lifecycle steps. All host mutation goes
// through Cmd (recorded in tests) and Config.Prefix (a staging root), so the
// whole flow is exercised hermetically while the production seams stay thin.
type Installer struct {
	Cfg Config
	Cmd Command
	// Out receives the step-by-step report for the operator.
	Out io.Writer
	// SelfPath is the executable to install; defaults to the running
	// binary (the bootstrap passes the freshly downloaded release).
	SelfPath func() (string, error)
}

func (in Installer) logf(format string, args ...any) {
	if in.Out != nil {
		_, _ = fmt.Fprintf(in.Out, format+"\n", args...)
	}
}

// Install provisions the standard Linux/systemd layout. Every step is
// idempotent; an interrupted install is completed by re-running it. The
// ownership record is written only after everything it describes exists,
// and preflight failures happen before any mutation.
func (in *Installer) Install(ctx context.Context) error {
	if err := Preflight(in.Cfg); err != nil {
		return err
	}
	rec, existed, err := loadRecord(in.Cfg.RecordPath())
	if err != nil {
		return err
	}
	if !existed {
		rec = recordFromConfig(in.Cfg)
	}
	if err = in.ensureIdentity(ctx, &rec); err != nil {
		return err
	}
	if err = in.prepareDirs(&rec); err != nil {
		return err
	}
	if err = in.validateRuntimeAccess(ctx, &rec); err != nil {
		return err
	}
	if err = in.installBinary(); err != nil {
		return err
	}
	if err = in.installUnit(); err != nil {
		return err
	}
	if err = in.migrateState(&rec); err != nil {
		return err
	}
	if existed {
		in.logf("upgrade complete: configuration, service identity, models and database state preserved")
	}
	if err = in.enableService(ctx, &rec); err != nil {
		return err
	}
	return nil
}

// ensureIdentity creates the dedicated system group/user when absent and
// never repurposes a conflicting pre-existing identity. The record keeps
// exactly which identities the installer created, so uninstall may only
// remove those.
func (in *Installer) ensureIdentity(ctx context.Context, rec *Record) error {
	c := in.Cfg
	groupExists := in.accountExists(ctx, "group", c.Group)

	if !groupExists {
		if _, err := in.Cmd.Run(ctx, "groupadd", "--system", c.Group); err != nil {
			return fmt.Errorf("create group %s: %w", c.Group, err)
		}
		rec.CreatedGroup = true
		in.logf("created system group %s", c.Group)
	}
	userExists := in.accountExists(ctx, "passwd", c.User)
	if !userExists {
		if _, err := in.Cmd.Run(ctx, "useradd", "--system", "--gid", c.Group,
			"--home-dir", c.path(c.StateDir), "--shell", "/usr/sbin/nologin",
			"--no-create-home", c.User); err != nil {
			return fmt.Errorf("create user %s: %w", c.User, err)
		}
		rec.CreatedUser = true
		in.logf("created system user %s (no login shell)", c.User)
	} else if err := in.checkExistingUser(ctx); err != nil {
		return err
	}
	if len(c.ExtraGroups) > 0 {
		for _, g := range c.ExtraGroups {
			if err := checkAccountName("group", g); err != nil {
				return err
			}
			if _, err := in.Cmd.Run(ctx, "usermod", "-aG", g, c.User); err != nil {
				return fmt.Errorf("grant supplementary group %s to %s: %w", g, c.User, err)
			}
			in.logf("granted supplementary group %s to %s", g, c.User)
		}
	}
	return nil
}

// accountExists reports whether a getent database (group/passwd) already
// knows the name. Any lookup failure counts as absent: the create step
// then reports the real error if the account exists but is unreadable.
func (in *Installer) accountExists(ctx context.Context, database, name string) bool {
	out, err := in.Cmd.Run(ctx, "getent", database, name)
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// checkExistingUser refuses a pre-existing account that cannot serve as the
// service identity (an interactive login shell), rather than silently
// repurposing it.
func (in *Installer) checkExistingUser(ctx context.Context) error {
	out, err := in.Cmd.Run(ctx, "getent", "passwd", in.Cfg.User)
	if err != nil {
		return fmt.Errorf("inspect existing user %s: %w", in.Cfg.User, err)
	}
	fields := strings.Split(strings.TrimSpace(string(out)), ":")
	if len(fields) < 7 {
		return fmt.Errorf("existing user %s has an unexpected passwd entry", in.Cfg.User)
	}
	shell := fields[6]
	if shell != "/usr/sbin/nologin" && shell != "/bin/false" && shell != "/sbin/nologin" {
		return fmt.Errorf("user %s already exists as an interactive account (shell %s); refusing to repurpose it — use --user to pick another service account (nothing was changed)", in.Cfg.User, shell)
	}
	return nil
}

// prepareDirs lays out etc/state/models with the service account owning
// only what it needs, and the privileged paths outside its write control.
func (in *Installer) prepareDirs(rec *Record) error {
	c := in.Cfg
	if err := os.MkdirAll(c.path(c.EtcDir), 0755); err != nil {
		return fmt.Errorf("create %s: %w", c.EtcDir, err)
	}
	if err := in.mkdirManaged(c.StateDir, 0750); err != nil {
		return err
	}
	info, err := os.Stat(c.path(c.ModelsDir))
	switch {
	case os.IsNotExist(err):
		if err = in.mkdirManaged(c.ModelsDir, 0750); err != nil {
			return err
		}
		rec.CreatedModelsDir = true
		in.logf("created managed model directory %s", c.ModelsDir)
	case err != nil:
		return fmt.Errorf("inspect %s: %w", c.ModelsDir, err)
	case !info.IsDir():
		return fmt.Errorf("%s exists but is not a directory", c.ModelsDir)
	default:
		// An operator-managed tree is adopted as-is: never recursively
		// chowned by the installer (issue #16).
		in.logf("adopting existing model directory %s (no ownership changes)", c.ModelsDir)
	}
	// Privileged resources stay root-owned even when the state tree is
	// service-owned.
	return in.Cmd.Chown(c.path(c.EtcDir), "root", "root")
}

func (in *Installer) mkdirManaged(dir string, mode os.FileMode) error {
	full := in.Cfg.path(dir)
	created := false
	info, err := os.Stat(full)
	switch {
	case os.IsNotExist(err):
		if err = os.MkdirAll(full, mode); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		created = true
	case err != nil:
		return fmt.Errorf("inspect %s: %w", dir, err)
	case !info.IsDir():
		return fmt.Errorf("%s exists but is not a directory", dir)
	}
	if err := os.Chmod(full, mode); err != nil {
		return fmt.Errorf("permissions on %s: %w", dir, err)
	}
	if created {
		if err := in.Cmd.Chown(full, in.Cfg.User, in.Cfg.Group); err != nil {
			return fmt.Errorf("ownership of %s: %w", dir, err)
		}
		in.logf("created %s (owner %s:%s, mode %o)", dir, in.Cfg.User, in.Cfg.Group, mode)
	}
	return nil
}

// validateRuntimeAccess proves the *service account* can execute the
// native runtime and read the model directory — an interactive user's
// access is not sufficient proof (issue #16).
func (in *Installer) validateRuntimeAccess(ctx context.Context, rec *Record) error {
	c := in.Cfg
	if _, err := os.Stat(c.path(c.LlamaServer)); err != nil {
		return fmt.Errorf("native runtime %s is not present under this host's layout: %w; pass --llama-server with the absolute path of your llama.cpp build", c.LlamaServer, err)
	}
	if out, err := in.Cmd.Run(ctx, "runuser", "-u", c.User, "--", "test", "-x", c.LlamaServer); err != nil {
		return fmt.Errorf("service user %s cannot execute %s: %s", c.User, c.LlamaServer, strings.TrimSpace(string(out)))
	}
	if out, err := in.Cmd.Run(ctx, "runuser", "-u", c.User, "--", "test", "-r", c.ModelsDir); err != nil {
		return fmt.Errorf("service user %s cannot read the model directory %s: %s; grant group/ACL read+traverse access, e.g.:\n  setfacl -R -m u:%s:rx %s",
			c.User, c.ModelsDir, strings.TrimSpace(string(out)), c.User, c.ModelsDir)
	}
	rec.LlamaServer = c.LlamaServer
	return nil
}

// installBinary replaces the executable atomically: a crash between copy
// and rename leaves the previous binary fully intact.
func (in *Installer) installBinary() error {
	c := in.Cfg
	src := c.SourceBinary
	if src == "" {
		self, err := in.selfPath()
		if err != nil {
			return err
		}
		src = self
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("source binary %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(c.path(c.BinPath)), 0755); err != nil {
		return err
	}
	tmp := c.path(c.BinPath) + ".new"
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read source binary: %w", err)
	}
	if len(data) == 0 || !isExecutableImage(data) {
		return fmt.Errorf("source binary %s is not a usable executable; refusing to replace %s", src, c.BinPath)
	}
	if err = os.WriteFile(tmp, data, 0755); err != nil {
		return err
	}
	if err = in.Cmd.Chown(filepath.Dir(c.path(c.BinPath)), "root", "root"); err != nil {
		return err
	}
	if err = os.Rename(tmp, c.path(c.BinPath)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", c.BinPath, err)
	}
	in.logf("installed %s (%d bytes, root-owned)", c.BinPath, len(data))
	return nil
}

func (in *Installer) selfPath() (string, error) {
	if in.SelfPath != nil {
		return in.SelfPath()
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate the running executable: %w", err)
	}
	return exe, nil
}

func isExecutableImage(data []byte) bool {
	// ELF magic: the only image type a Linux release ships, and cheap
	// proof against a truncated or wrong-format download.
	return len(data) > 4 && data[0] == 0x7f && data[1] == 'E' && data[2] == 'L' && data[3] == 'F'
}

func (in *Installer) installUnit() error {
	c := in.Cfg
	unit := renderUnit(c)
	if err := os.MkdirAll(filepath.Dir(c.path(c.UnitPath)), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(c.path(c.UnitPath), []byte(unit), 0644); err != nil {
		return fmt.Errorf("write unit %s: %w", c.UnitPath, err)
	}
	in.logf("wrote systemd unit %s", c.UnitPath)
	return nil
}

// enableService commits the installation: the ownership record is written
// first, so a crash during enable still leaves a trackable installation.
func (in *Installer) enableService(ctx context.Context, rec *Record) error {
	if err := saveRecord(in.Cfg.RecordPath(), *rec); err != nil {
		return fmt.Errorf("write installation record: %w", err)
	}
	if _, err := in.Cmd.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if _, err := in.Cmd.Run(ctx, "systemctl", "enable", "--now", "opentambo.service"); err != nil {
		return errors.New("systemctl enable --now failed; the unit is installed but not started — inspect `systemctl status opentambo` and re-run the installer once fixed")
	}
	out, err := in.Cmd.Run(ctx, "systemctl", "is-active", "opentambo.service")
	state := strings.TrimSpace(string(out))
	if err != nil || state != "active" {
		return fmt.Errorf("service reports %q; inspect `systemctl status opentambo` (installation is recorded and re-running the installer is safe)", state)
	}
	in.logf("service state: active, enabled at boot, logs in journald (journalctl -u opentambo)")
	return nil
}
