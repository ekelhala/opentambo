package lifecycle

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// UninstallOptions controls removal scope. Defaults preserve every byte of
// operator data: models, SQLite state and administrator configuration
// survive a routine uninstall.
type UninstallOptions struct {
	// PurgeState removes the managed state tree (SQLite database, cache
	// namespaces). Never anything outside it.
	PurgeState bool
	// PurgeModels additionally removes the managed model directory, and
	// only when the installer itself created it (record-tracked).
	PurgeModels bool
	// PurgeAll removes state and models together and additionally drops
	// the etc directory; every destructive rule below still applies.
	PurgeAll bool
}

// Uninstaller removes the installation without needing the original
// checkout, install script, model mount, native runtime or a healthy
// database: lifecycle dispatch runs before any application initialization.
type Uninstaller struct {
	Cfg Config
	Cmd Command
	// Out receives the step-by-step report for the operator.
	Out io.Writer
}

func (un *Uninstaller) logf(format string, args ...any) {
	if un.Out != nil {
		_, _ = fmt.Fprintf(un.Out, format+"\n", args...)
	}
}

// preflight enforces a supported host and root privileges, but never the
// native runtime, model mount or a healthy database: uninstall has to work
// when those are already gone. Failures happen before any mutation.
func (un *Uninstaller) preflight() error {
	if err := un.Cfg.validate(); err != nil {
		return err
	}
	if err := checkHostFn(); err != nil {
		return err
	}
	if !isRootFn() {
		return fmt.Errorf("this command requires root privileges; re-run with sudo (nothing was changed)")
	}
	return nil
}

// Uninstall stops the service, removes installer-owned integration
// resources, and reports what was deliberately retained. It is idempotent:
// a partially removed installation completes cleanly.
func (un *Uninstaller) Uninstall(ctx context.Context, opts UninstallOptions) error {
	if err := un.preflight(); err != nil {
		return err
	}
	rec, found, err := loadRecord(un.Cfg.RecordPath())
	if err != nil {
		return err
	}
	if !found {
		un.logf("no installation record at %s; removing known integration resources best-effort", un.Cfg.RecordPath())
		rec = recordFromConfig(un.Cfg)
	}
	if err = un.stopService(ctx); err != nil {
		return err
	}
	if err = un.removePaths(rec, opts); err != nil {
		return err
	}
	return un.removeIdentity(ctx, rec, opts)
}

// stopService stops and disables the unit, waiting for managed children
// (cgroup-bound) to exit, and tolerates an already-absent unit.
func (un *Uninstaller) stopService(ctx context.Context) error {
	for _, args := range [][]string{
		{"stop", "opentambo.service"},
		{"disable", "opentambo.service"},
	} {
		if _, err := un.Cmd.Run(ctx, "systemctl", args...); err != nil {
			un.logf("systemctl %s: continued (already done or unit absent)", args[0])
		}
	}
	unit := un.Cfg.path(un.Cfg.UnitPath)
	if err := removeFileIfPresent(unit); err != nil {
		return err
	}
	if _, err := un.Cmd.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		un.logf("systemctl daemon-reload: continued (%v)", err)
	}
	un.logf("service stopped, disabled and unregistered")
	return nil
}

// removePaths deletes only installer-owned files whose recorded paths
// match the configured layout, re-validating everything before deletion.
// Operator data is kept unless explicitly purged.
func (un *Uninstaller) removePaths(rec Record, opts UninstallOptions) error {
	c := un.Cfg
	if rec.BinPath != c.BinPath {
		un.logf("retaining binary %s: the record points at a different install path than the current layout", rec.BinPath)
	} else {
		bin, err := safeRemovalPath(rec.BinPath, c.BinPath, c.Prefix)
		if err != nil {
			return err
		}
		if err = removeFileIfPresent(bin); err != nil {
			return err
		}
		un.logf("removed %s", bin)
	}
	purge := opts.PurgeState || opts.PurgeAll
	if purge {
		if err := un.purgeStateTree(rec, opts); err != nil {
			return err
		}
	} else {
		un.logf("retained durable state and SQLite database at %s", c.StateDir)
		un.logf("retained model directory at %s", c.ModelsDir)
	}
	return un.removeEtc(rec, opts)
}

// purgeStateTree removes the managed state tree while enforcing the model
// opt-in rule: a model directory nested in the tree is removed only when
// the operator explicitly asked and the installer itself created it; an
// external model directory is never recursively removed.
func (un *Uninstaller) purgeStateTree(rec Record, opts UninstallOptions) error {
	c := un.Cfg
	state, err := resolveSafe(c.StateDir, c.Prefix)
	if err != nil {
		return err
	}
	wanted := opts.PurgeModels || opts.PurgeAll
	models, merr := resolveSafe(c.ModelsDir, c.Prefix)
	if merr == nil && models != state && isUnder(models+"/", state+"/") {
		if !wanted || !rec.CreatedModelsDir {
			if err = un.purgeStateKeepModels(state, models); err != nil {
				return err
			}
			un.logf("purged managed state tree %s (SQLite database, cache metadata and blobs)", state)
			reason := "removal needs --purge-models"
			if !rec.CreatedModelsDir {
				reason = "the installer did not create this directory"
			}
			un.logf("retained model directory at %s: %s", c.ModelsDir, reason)
			return nil
		}
	}
	if err = os.RemoveAll(state); err != nil {
		return fmt.Errorf("remove state tree %s: %w", state, err)
	}
	un.logf("purged managed state tree %s (SQLite database, cache metadata and blobs)", state)
	if wanted {
		return un.purgeModelsDir(rec)
	}
	return nil
}

// purgeStateKeepModels clears the state tree around a nested model
// directory: move it aside, remove the tree, move it back.
func (un *Uninstaller) purgeStateKeepModels(state, models string) error {
	tmp := state + ".models-keep"
	if err := os.Rename(models, tmp); err != nil {
		return fmt.Errorf("move models aside: %w", err)
	}
	if err := os.RemoveAll(state); err != nil {
		_ = os.Rename(tmp, models)
		return fmt.Errorf("remove state tree %s: %w", state, err)
	}
	if err := os.MkdirAll(filepath.Dir(models), 0750); err != nil {
		_ = os.Rename(tmp, models)
		return err
	}
	if err := os.Rename(tmp, models); err != nil {
		return fmt.Errorf("restore models: %w (preserved at %s)", err, tmp)
	}
	return nil
}

// purgeModelsDir may only delete an installer-created model directory, and
// only when it lies inside the managed state tree. External model trees are
// never recursively removed by any purge.
func (un *Uninstaller) purgeModelsDir(rec Record) error {
	c := un.Cfg
	if !rec.CreatedModelsDir {
		un.logf("retaining %s: the installer did not create this directory", c.ModelsDir)
		return nil
	}
	models, err := resolveWithin(c.ModelsDir, c.Prefix, c.StateDir)
	if err != nil {
		// Deliberate retention, not a swallowed failure: an external model
		// tree is never recursively removed by a purge.
		un.logf("retaining %s: outside the managed state tree (%v)", c.ModelsDir, err)
		return nil
	}
	if err = os.RemoveAll(models); err != nil {
		return fmt.Errorf("remove models directory: %w", err)
	}
	un.logf("purged installer-created model directory %s", models)
	return nil
}

// removeEtc deletes the ownership record and, for --purge-all, the etc
// directory — but only once it is empty of anything else the operator put
// there.
func (un *Uninstaller) removeEtc(rec Record, opts UninstallOptions) error {
	c := un.Cfg
	etc := c.path(c.EtcDir)
	recordPath := c.path(rec.EtcDir + "/install.json")
	if rec.EtcDir == c.EtcDir {
		if err := removeFileIfPresent(recordPath); err != nil {
			return err
		}
	}
	if !opts.PurgeAll {
		un.logf("retained administrator configuration at %s", c.EtcDir)
		return nil
	}
	entries, err := os.ReadDir(etc)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(entries) == 0 {
		if err = os.Remove(etc); err != nil && !os.IsNotExist(err) {
			return err
		}
		un.logf("removed empty configuration directory %s", c.EtcDir)
		return nil
	}
	un.logf("retained %s: it holds files the installer did not create", c.EtcDir)
	return nil
}

// removeIdentity drops the service account only when the installer created
// it and the purge also removed every file that account could own (state
// and models alike). Pre-existing identities are never touched, and the
// account is kept whenever retained files still belong to it.
func (un *Uninstaller) removeIdentity(ctx context.Context, rec Record, opts UninstallOptions) error {
	c := un.Cfg
	fullyPurged := opts.PurgeAll || (opts.PurgeState && opts.PurgeModels)
	if !fullyPurged {
		un.logf("retained service account %s: installer-owned files remain on disk", c.User)
		return nil
	}
	if !rec.CreatedUser {
		un.logf("retained user %s: it existed before the installation", c.User)
	} else if _, err := un.Cmd.Run(ctx, "userdel", c.User); err != nil {
		return fmt.Errorf("remove installer-created user %s: %w", c.User, err)
	} else {
		un.logf("removed installer-created user %s", c.User)
	}
	if !rec.CreatedGroup {
		un.logf("retained group %s: it existed before the installation", c.Group)
		return nil
	}
	// userdel may already have removed a same-named private group
	// (USERGROUPS_ENAB hosts); only delete what still exists.
	if !un.accountExists(ctx, "group", c.Group) {
		un.logf("removed installer-created group %s (with the user)", c.Group)
		return nil
	}
	if _, err := un.Cmd.Run(ctx, "groupdel", c.Group); err != nil {
		return fmt.Errorf("remove installer-created group %s: %w", c.Group, err)
	}
	un.logf("removed installer-created group %s", c.Group)
	return nil
}

// accountExists mirrors the installer lookup: a getent database entry with
// output means the identity is present.
func (un *Uninstaller) accountExists(ctx context.Context, database, name string) bool {
	out, err := un.Cmd.Run(ctx, "getent", database, name)
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

func removeFileIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
