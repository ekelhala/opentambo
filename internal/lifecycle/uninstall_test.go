package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var errNoSystemd = errors.New("systemd is not running on this host")

// installedForUninstall stages a full install and then plants operator
// data (database, model file) that a default uninstall must preserve.
func installedForUninstall(t *testing.T) (Config, string) {
	t.Helper()
	cfg, prefix := stageRoots(t)
	if err := (&Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	db := filepath.Join(prefix, cfg.StateDir, "opentambo.db")
	if err := os.WriteFile(db, []byte("state"), 0600); err != nil {
		t.Fatal(err)
	}
	gguf := filepath.Join(prefix, cfg.ModelsDir, "m.gguf")
	if err := os.WriteFile(gguf, []byte("GGUF"), 0644); err != nil {
		t.Fatal(err)
	}
	return cfg, prefix
}

func runUninstall(t *testing.T, cfg Config, opts UninstallOptions) (*fakeCmd, string) {
	t.Helper()
	fake := &fakeCmd{out: map[string]string{}}
	log := &strings.Builder{}
	un := &Uninstaller{Cfg: cfg, Cmd: fake, Out: log}
	if err := un.Uninstall(context.Background(), opts); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, log)
	}
	return fake, log.String()
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func TestUninstallDefaultPreservesOperatorData(t *testing.T) {
	cfg, prefix := installedForUninstall(t)
	fake, log := runUninstall(t, cfg, UninstallOptions{})

	for _, want := range []string{
		"systemctl stop opentambo.service",
		"systemctl disable opentambo.service",
		"systemctl daemon-reload",
	} {
		if !fake.has(want) {
			t.Errorf("uninstall did not run %q: %v", want, fake.calls)
		}
	}
	if fake.has("userdel") || fake.has("groupdel") {
		t.Errorf("default uninstall removed identities: %v", fake.calls)
	}
	if exists(t, filepath.Join(prefix, cfg.BinPath)) {
		t.Error("binary survived uninstall")
	}
	if exists(t, filepath.Join(prefix, cfg.UnitPath)) {
		t.Error("unit file survived uninstall")
	}
	if exists(t, filepath.Join(prefix, cfg.EtcDir, "install.json")) {
		t.Error("ownership record survived uninstall")
	}
	if !exists(t, filepath.Join(prefix, cfg.StateDir, "opentambo.db")) {
		t.Error("default uninstall deleted the SQLite database")
	}
	if !exists(t, filepath.Join(prefix, cfg.ModelsDir, "m.gguf")) {
		t.Error("default uninstall deleted models")
	}
	if !exists(t, filepath.Join(prefix, cfg.EtcDir)) {
		t.Error("default uninstall deleted the etc directory")
	}
	for _, want := range []string{"retained durable state", "retained model directory", "retained administrator configuration"} {
		if !strings.Contains(log, want) {
			t.Errorf("report missing %q:\n%s", want, log)
		}
	}
}

func TestUninstallPurgeStateKeepsNestedModels(t *testing.T) {
	cfg, prefix := installedForUninstall(t)
	_, log := runUninstall(t, cfg, UninstallOptions{PurgeState: true})

	if exists(t, filepath.Join(prefix, cfg.StateDir, "opentambo.db")) {
		t.Error("--purge kept the SQLite database")
	}
	if !exists(t, filepath.Join(prefix, cfg.ModelsDir, "m.gguf")) {
		t.Error("--purge without --purge-models deleted the nested model directory")
	}
	if !strings.Contains(log, "needs --purge-models") {
		t.Errorf("report missing the model opt-in explanation:\n%s", log)
	}
}

func TestUninstallPurgeStateAndModelsRemovesBoth(t *testing.T) {
	cfg, prefix := installedForUninstall(t)
	fake, log := runUninstall(t, cfg, UninstallOptions{PurgeState: true, PurgeModels: true})

	if exists(t, filepath.Join(prefix, cfg.StateDir)) {
		t.Error("state tree survived full purge")
	}
	if !fake.has("userdel opentambo") {
		t.Errorf("installer-created user survived a full purge: %v", fake.calls)
	}
	// The fake reports the group as still present, so the uninstaller must
	// delete it explicitly rather than assume userdel did.
	fake2 := &fakeCmd{out: map[string]string{"getent group opentambo": "opentambo:x:995:"}}
	un := &Uninstaller{Cfg: cfg, Cmd: fake2, Out: &strings.Builder{}}
	if err := un.removeIdentity(context.Background(), Record{CreatedUser: true, CreatedGroup: true}, UninstallOptions{PurgeAll: true}); err != nil {
		t.Fatal(err)
	}
	if !fake2.has("groupdel opentambo") {
		t.Errorf("present installer-created group not deleted: %v", fake2.calls)
	}
	if !strings.Contains(log, "purged installer-created model directory") {
		t.Errorf("report missing model purge:\n%s", log)
	}
}

func TestUninstallPurgeModelsRefusesAdoptedDirectory(t *testing.T) {
	cfg, prefix := stageRoots(t)
	models := filepath.Join(prefix, cfg.ModelsDir)
	if err := os.MkdirAll(models, 0755); err != nil {
		t.Fatal(err)
	}
	gguf := filepath.Join(models, "m.gguf")
	if err := os.WriteFile(gguf, []byte("GGUF"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := (&Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, log := runUninstall(t, cfg, UninstallOptions{PurgeState: true, PurgeModels: true})

	if !exists(t, gguf) {
		t.Error("purge deleted a model directory the installer did not create")
	}
	if !strings.Contains(log, "did not create this directory") {
		t.Errorf("report missing the refusal reason:\n%s", log)
	}
}

func TestUninstallPurgeNeverTouchesExternalModels(t *testing.T) {
	cfg, prefix := stageRoots(t)
	external := filepath.Join(prefix, "/mnt/models")
	if err := os.MkdirAll(external, 0755); err != nil {
		t.Fatal(err)
	}
	gguf := filepath.Join(external, "m.gguf")
	if err := os.WriteFile(gguf, []byte("GGUF"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg.ModelsDir = "/mnt/models"
	if err := (&Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	runUninstall(t, cfg, UninstallOptions{PurgeState: true, PurgeModels: true, PurgeAll: true})

	if !exists(t, gguf) {
		t.Error("a purge deleted an external model directory")
	}
}

func TestUninstallPurgeAllRemovesEmptyEtcOnly(t *testing.T) {
	cfg, prefix := installedForUninstall(t)
	runUninstall(t, cfg, UninstallOptions{PurgeAll: true})
	if exists(t, filepath.Join(prefix, cfg.EtcDir)) {
		t.Error("--purge-all kept an empty etc directory")
	}

	cfg, prefix = installedForUninstall(t)
	toml := filepath.Join(prefix, cfg.EtcDir, "override.conf")
	if err := os.WriteFile(toml, []byte("[operator]"), 0600); err != nil {
		t.Fatal(err)
	}
	_, log := runUninstall(t, cfg, UninstallOptions{PurgeAll: true})
	if !exists(t, toml) {
		t.Error("--purge-all deleted an operator configuration file")
	}
	if !strings.Contains(log, "did not create") {
		t.Errorf("report missing the etc retention reason:\n%s", log)
	}
}

func TestUninstallKeepsPreExistingIdentities(t *testing.T) {
	cfg, prefix := stageRoots(t)
	fake := activeFake()
	fake.out["getent group opentambo"] = "opentambo:x:995:"
	fake.out["getent passwd opentambo"] = "opentambo:x:995:995::/var/lib/opentambo:/usr/sbin/nologin"
	if err := (&Installer{Cfg: cfg, Cmd: fake, Out: &strings.Builder{}}).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake2, log := runUninstall(t, cfg, UninstallOptions{PurgeAll: true})
	if fake2.has("userdel") || fake2.has("groupdel") {
		t.Errorf("uninstall deleted identities it did not create: %v", fake2.calls)
	}
	if !strings.Contains(log, "existed before the installation") {
		t.Errorf("report missing the identity retention reason:\n%s", log)
	}
	_ = prefix
}

func TestUninstallWithoutRecordIsBestEffort(t *testing.T) {
	cfg, prefix := stageRoots(t)
	// Partial installation: files exist, record does not.
	for _, p := range []string{cfg.BinPath, cfg.UnitPath} {
		full := filepath.Join(prefix, p)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	_, log := runUninstall(t, cfg, UninstallOptions{})
	if exists(t, filepath.Join(prefix, cfg.BinPath)) || exists(t, filepath.Join(prefix, cfg.UnitPath)) {
		t.Error("best-effort uninstall left integration files behind")
	}
	if !strings.Contains(log, "no installation record") {
		t.Errorf("report missing the no-record notice:\n%s", log)
	}
}

func TestUninstallRetainsBinaryOnLayoutMismatch(t *testing.T) {
	cfg, prefix := installedForUninstall(t)
	// Simulate a record that points at a different install tree than the
	// one the current flags describe: removal must refuse, not guess.
	mismatch := cfg
	mismatch.BinPath = "/opt/other/opentambo"
	_, log := runUninstall(t, mismatch, UninstallOptions{})
	if !exists(t, filepath.Join(prefix, cfg.BinPath)) {
		t.Error("uninstall removed a binary the record did not account for")
	}
	if !strings.Contains(log, "different install path") {
		t.Errorf("report missing the mismatch notice:\n%s", log)
	}
}

func TestUninstallPreflightRefuses(t *testing.T) {
	oldRoot, oldHost := isRootFn, checkHostFn
	defer func() { isRootFn, checkHostFn = oldRoot, oldHost }()

	cfg := DefaultConfig()
	isRootFn = func() bool { return false }
	checkHostFn = func() error { return nil }
	err := (&Uninstaller{Cfg: cfg}).Uninstall(context.Background(), UninstallOptions{})
	if err == nil || !strings.Contains(err.Error(), "root privileges") {
		t.Fatalf("non-root uninstall = %v", err)
	}
	isRootFn = func() bool { return true }
	checkHostFn = func() error { return errNoSystemd }
	err = (&Uninstaller{Cfg: cfg}).Uninstall(context.Background(), UninstallOptions{})
	if err == nil || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("no-systemd uninstall = %v", err)
	}
	// Config validation runs before anything else.
	checkHostFn = func() error { return nil }
	cfg.BinPath = "relative"
	err = (&Uninstaller{Cfg: cfg}).Uninstall(context.Background(), UninstallOptions{})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative-path uninstall = %v", err)
	}
}
