package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ekelhala/opentambo/internal/config"
)

func TestInstallFailsWhenConfigurationIsUninspectable(t *testing.T) {
	// A config directory that cannot even be stat'ed must abort the install
	// before the unit or binary changes, so the host is never half-managed.
	cfg, prefix := stageRoots(t)
	etc := filepath.Join(prefix, cfg.EtcDir)
	if err := os.MkdirAll(etc, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(etc, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(etc, 0755) })
	err := (&Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "config.yaml") {
		t.Fatalf("unreadable config directory must abort install, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(prefix, cfg.BinPath)); statErr == nil {
		t.Error("install wrote the binary despite the configuration failure")
	}
}

func TestInstallSeedsOperatorConfiguration(t *testing.T) {
	cfg, prefix, _ := installedTree(t)
	path := filepath.Join(prefix, cfg.ConfigPath())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The seeded file must be a valid schema-v1 configuration resolving to
	// the installed layout, so the service starts from identical values.
	eff, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("seeded configuration does not parse: %v\n%s", err, data)
	}
	if eff.Listen != cfg.Listen || eff.DataDir != cfg.StateDir || eff.ModelsDir != cfg.ModelsDir || eff.Executable != cfg.LlamaServer {
		t.Errorf("seeded configuration does not match the layout: %+v", eff)
	}
	rec, found, err := loadRecord(cfg.RecordPath())
	if err != nil || !found {
		t.Fatalf("record: found=%v err=%v", found, err)
	}
	if !rec.CreatedConfig {
		t.Error("install did not record that it created the configuration file")
	}
}

func TestInstallKeepsExistingOperatorConfiguration(t *testing.T) {
	cfg, prefix := stageRoots(t)
	dir := filepath.Join(prefix, cfg.EtcDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(prefix, cfg.ConfigPath())
	edited := "schema_version: 1\nhttp:\n  listen: \"127.0.0.1:9999\"\n"
	if err := os.WriteFile(path, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	if err := (&Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != edited {
		t.Errorf("install rewrote an operator-owned configuration file:\n%s", data)
	}
	rec, found, err := loadRecord(cfg.RecordPath())
	if err != nil || !found {
		t.Fatalf("record: found=%v err=%v", found, err)
	}
	if rec.CreatedConfig {
		t.Error("record claims ownership of a configuration file the installer did not create")
	}
}

func TestUninstallPurgeRemovesOnlyInstallerSeededConfig(t *testing.T) {
	// Installer-seeded: --purge-all removes it and the now-empty etc tree.
	cfg, prefix := installedForUninstall(t)
	configPath := filepath.Join(prefix, cfg.ConfigPath())
	runUninstall(t, cfg, UninstallOptions{PurgeAll: true})
	if exists(t, configPath) {
		t.Error("--purge-all kept a configuration file the installer seeded")
	}

	// Operator-owned: --purge-all must leave it in place.
	cfg, prefix = installedForUninstall(t)
	configPath = filepath.Join(prefix, cfg.ConfigPath())
	edited := "schema_version: 1\n"
	if err := os.WriteFile(configPath, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveRecord(cfg.RecordPath(), mustRecordWithoutConfig(t, cfg)); err != nil {
		t.Fatal(err)
	}
	runUninstall(t, cfg, UninstallOptions{PurgeAll: true})
	if !exists(t, configPath) {
		t.Error("--purge-all deleted an operator-owned configuration file")
	}
}

func TestSeedConfigStatFailure(t *testing.T) {
	// An etc directory the installer cannot even traverse must abort with
	// the inspection error, never a blind write.
	cfg, prefix := stageRoots(t)
	etc := filepath.Join(prefix, cfg.EtcDir)
	if err := os.MkdirAll(etc, 0755); err != nil {
		t.Fatal(err)
	}
	chmodFor(t, etc, 0600)
	rec := recordFromConfig(cfg)
	err := (&Installer{Cfg: cfg, Out: &strings.Builder{}}).seedConfig(&rec)
	if err == nil || !strings.Contains(err.Error(), "inspect") {
		t.Fatalf("untraversable etc = %v", err)
	}
}

func TestSeededConfigPathOwnershipDiscipline(t *testing.T) {
	cfg, prefix := stageRoots(t)
	rec := recordFromConfig(cfg)
	rec.CreatedConfig = true
	foreign := rec
	foreign.EtcDir = "/etc/other-install"
	if _, err := seededConfigPath(foreign, cfg); err == nil {
		t.Fatal("recorded etc outside the configured layout was accepted")
	}
	path, err := seededConfigPath(rec, cfg)
	if err != nil || path != filepath.Join(prefix, cfg.ConfigPath()) {
		t.Fatalf("seeded path = %q, err %v", path, err)
	}
	rec.CreatedConfig = false
	if path, err := seededConfigPath(rec, cfg); err != nil || path != "" {
		t.Fatalf("unowned record = %q, err %v", path, err)
	}
}

func TestRemoveEtcRefusesUnremovableSeededConfig(t *testing.T) {
	// A seeded config that cannot be removed (here a non-empty directory
	// impersonating the file) must surface the failure, not claim a purge.
	cfg, prefix := stageRoots(t)
	if err := os.MkdirAll(filepath.Join(prefix, cfg.ConfigPath(), "inner"), 0755); err != nil {
		t.Fatal(err)
	}
	rec := recordFromConfig(cfg)
	rec.CreatedConfig = true
	err := (&Uninstaller{Cfg: cfg, Out: &strings.Builder{}}).removeEtc(rec, UninstallOptions{PurgeAll: true})
	if err == nil || !strings.Contains(err.Error(), "remove") {
		t.Fatalf("unremovable seeded config = %v", err)
	}
}

func mustRecordWithoutConfig(t *testing.T, cfg Config) Record {
	t.Helper()
	rec, found, err := loadRecord(cfg.RecordPath())
	if err != nil || !found {
		t.Fatalf("record: found=%v err=%v", found, err)
	}
	rec.CreatedConfig = false
	return rec
}
