package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stageLegacy plants a per-user installation's data directory inside the
// staging prefix: a SQLite database, a cache namespace tree and a nested
// model directory that must never be swept into a state import.
func stageLegacy(t *testing.T, prefix, rel string) string {
	t.Helper()
	src := filepath.Join(prefix, rel)
	if err := os.MkdirAll(filepath.Join(src, "cache", "ns1"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, stateDBFileName), []byte("db"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "cache", "ns1", "e.bin"), []byte("kv"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "models"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "models", "big.gguf"), []byte("GGUF"), 0644); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestInstallImportStateFromPerUserInstall(t *testing.T) {
	cfg, prefix := stageRoots(t)
	src := stageLegacy(t, prefix, "/home/olduser/.local/share/opentambo")
	cfg.MigrateFrom = filepath.ToSlash(src[len(prefix):])
	fake := activeFake()
	log := &strings.Builder{}
	if err := (&Installer{Cfg: cfg, Cmd: fake, Out: log}).Install(context.Background()); err != nil {
		t.Fatalf("import install: %v\n%s", err, log)
	}

	state := filepath.Join(prefix, cfg.StateDir)
	for _, want := range []string{stateDBFileName, filepath.Join("cache", "ns1", "e.bin")} {
		if _, err := os.Stat(filepath.Join(state, want)); err != nil {
			t.Errorf("imported state missing %s: %v", want, err)
		}
	}
	if exists(t, filepath.Join(state, "models", "big.gguf")) {
		t.Error("state import swept a model file into the state tree")
	}
	// The legacy installation must be untouched for rollback.
	if _, err := os.Stat(filepath.Join(src, stateDBFileName)); err != nil {
		t.Errorf("import modified the source installation: %v", err)
	}
	if !fake.has("chown -R opentambo:opentambo " + state) {
		t.Errorf("imported state not handed to the service account: %v", fake.calls)
	}
	rec, found, err := loadRecord(cfg.RecordPath())
	if err != nil || !found || rec.StateMigratedFrom != cfg.MigrateFrom {
		t.Errorf("record lost the migration marker: %+v found=%v err=%v", rec, found, err)
	}
	if !strings.Contains(log.String(), "stop its manager") {
		t.Errorf("report missing the two-managers warning:\n%s", log)
	}
}

func TestInstallImportIsIdempotent(t *testing.T) {
	cfg, prefix := stageRoots(t)
	src := stageLegacy(t, prefix, "/home/olduser/.local/share/opentambo")
	cfg.MigrateFrom = filepath.ToSlash(src[len(prefix):])
	if err := (&Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Re-running the same import must not clobber edits made after it.
	db := filepath.Join(prefix, cfg.StateDir, stateDBFileName)
	if err := os.WriteFile(db, []byte("newer"), 0600); err != nil {
		t.Fatal(err)
	}
	log := &strings.Builder{}
	if err := (&Installer{Cfg: cfg, Cmd: activeFake(), Out: log}).Install(context.Background()); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	data, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "newer" {
		t.Errorf("re-import overwrote live state with %q", data)
	}
	if !strings.Contains(log.String(), "already imported") {
		t.Errorf("report missing the idempotency notice:\n%s", log)
	}
}

func TestInstallImportRefusesLiveDestination(t *testing.T) {
	cfg, prefix := stageRoots(t)
	src := stageLegacy(t, prefix, "/home/olduser/.local/share/opentambo")
	// A previous (non-import) install already made the state tree live
	// with a database the operator has used since.
	if err := (&Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(prefix, cfg.StateDir, stateDBFileName)
	if err := os.WriteFile(db, []byte("live"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.MigrateFrom = filepath.ToSlash(src[len(prefix):])
	log := &strings.Builder{}
	err := (&Installer{Cfg: cfg, Cmd: activeFake(), Out: log}).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already has a live database") {
		t.Fatalf("import over live state = %v\n%s", err, log)
	}
	data, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "live" {
		t.Errorf("refused import still touched the live database: %q", data)
	}
}

func TestInstallImportValidatesSource(t *testing.T) {
	cfg, _ := stageRoots(t)
	cfg.MigrateFrom = "relative/path"
	err := (&Installer{Cfg: cfg, Cmd: activeFake()}).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative import = %v", err)
	}

	cfg, _ = stageRoots(t)
	cfg.MigrateFrom = "/var/lib/opentambo/old"
	err = (&Installer{Cfg: cfg, Cmd: activeFake()}).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "inside the managed state tree") {
		t.Fatalf("nested import = %v", err)
	}

	cfg, _ = stageRoots(t)
	cfg.MigrateFrom = "/home/olduser/nowhere"
	err = (&Installer{Cfg: cfg, Cmd: activeFake()}).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "state database") {
		t.Fatalf("non-data source = %v", err)
	}
}
