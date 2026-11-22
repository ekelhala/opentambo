package lifecycle

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// stateDBFileName mirrors the embedded database file name from
// internal/desk/store; the lifecycle layer stays dependency-free and only
// needs to recognise a directory as an OpenTambo data directory.
const stateDBFileName = "opentambo.db"

// migrateState imports durable state from a per-user installation into the
// managed state tree. It runs only when the operator asked for it with
// --import-state: nothing is moved silently, the source installation is
// left untouched (so the operator can roll back), and the import refuses
// to overwrite a live destination database — two managers can therefore
// never end up sharing one data directory through this path.
func (in *Installer) migrateState(rec *Record) error {
	c := in.Cfg
	src := c.MigrateFrom
	if src == "" {
		return nil
	}
	srcFull := c.path(src)
	stateFull := c.path(c.StateDir)
	if srcFull == stateFull || isUnder(srcFull+"/", stateFull+"/") {
		return fmt.Errorf("--import-state %s is inside the managed state tree %s (nothing was changed)", src, c.StateDir)
	}
	if _, err := os.Stat(filepath.Join(srcFull, stateDBFileName)); err != nil {
		return fmt.Errorf("no OpenTambo state database at %s: %w; point --import-state at the old installation's --data directory (nothing was changed)", src, err)
	}
	if rec.StateMigratedFrom == src {
		if _, err := os.Stat(filepath.Join(stateFull, stateDBFileName)); err == nil {
			in.logf("durable state already imported from %s", src)
			return nil
		}
	}
	if _, err := os.Stat(filepath.Join(stateFull, stateDBFileName)); err == nil {
		return fmt.Errorf("managed state tree %s already has a live database: refusing to overwrite it with %s", c.StateDir, src)
	}
	if err := copyStateTree(srcFull, stateFull); err != nil {
		return err
	}
	if err := in.Cmd.ChownRecursive(stateFull, c.User, c.Group); err != nil {
		return fmt.Errorf("hand imported state to service account %s: %w", c.User, err)
	}
	rec.StateMigratedFrom = src
	in.logf("imported durable state from %s", src)
	in.logf("the per-user installation was left untouched; stop its manager before starting this service so two managers never run against the same history")
	return nil
}

// copyStateTree copies a plain-file state tree (SQLite database, cache
// namespaces, import markers), preserving permissions but skipping a
// top-level "models" directory: model weights are large operator assets
// and move only under an explicit decision, never as a side effect of a
// state import.
func copyStateTree(src, dst string) error {
	skipModels := filepath.Join(src, "models")
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == skipModels && d.IsDir() {
			return fs.SkipDir
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(p, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}
