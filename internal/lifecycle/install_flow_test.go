package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installedTree(t *testing.T) (Config, string, *fakeCmd) {
	t.Helper()
	cfg, prefix := stageRoots(t)
	fake := activeFake()
	out := &strings.Builder{}
	in := &Installer{Cfg: cfg, Cmd: fake, Out: out}
	if err := in.Install(context.Background()); err != nil {
		t.Fatalf("fresh install: %v\n%s", err, out)
	}
	return cfg, prefix, fake
}

func TestFreshInstallProvisionsStandardLayout(t *testing.T) {
	cfg, prefix, fake := installedTree(t)

	for _, want := range []string{
		"getent group opentambo",
		"groupadd --system opentambo",
		"useradd --system --gid opentambo --home-dir " + filepath.Join(prefix, "/var/lib/opentambo") + " --shell /usr/sbin/nologin --no-create-home opentambo",
		"runuser -u opentambo -- test -x /usr/local/bin/llama-server",
		"runuser -u opentambo -- test -r /var/lib/opentambo/models",
		"chown root:root " + filepath.Join(prefix, "/etc/opentambo"),
		"systemctl daemon-reload",
		"systemctl enable --now opentambo.service",
	} {
		if !fake.has(want) {
			t.Errorf("install did not run %q; calls: %v", want, fake.calls)
		}
	}

	unitData, err := os.ReadFile(filepath.Join(prefix, cfg.UnitPath))
	if err != nil {
		t.Fatal(err)
	}
	unit := string(unitData)
	for _, want := range []string{
		"User=opentambo", "Group=opentambo",
		"ExecStart=/usr/local/bin/opentambo --models /var/lib/opentambo/models",
		"--data /var/lib/opentambo --llama-server /usr/local/bin/llama-server --listen 127.0.0.1:8090",
		"KillMode=control-group", "TimeoutStopSec=15", "StandardOutput=journal",
		"ReadWritePaths=/var/lib/opentambo",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "/home/") || strings.Contains(unit, "emil") {
		t.Errorf("unit embeds a personal path:\n%s", unit)
	}

	binData, err := os.ReadFile(filepath.Join(prefix, cfg.BinPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(binData) != "\x7fELF fake release" {
		t.Errorf("installed binary content = %q", binData)
	}

	rec, found, err := loadRecord(cfg.RecordPath())
	if err != nil || !found {
		t.Fatalf("installation record: found=%v err=%v", found, err)
	}
	if !rec.CreatedUser || !rec.CreatedGroup || !rec.CreatedModelsDir {
		t.Errorf("record lost what the installer created: %+v", rec)
	}

	stateInfo, err := os.Stat(filepath.Join(prefix, cfg.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	if stateInfo.Mode().Perm() != 0750 {
		t.Errorf("state dir mode = %o, want 0750", stateInfo.Mode().Perm())
	}
	recInfo, err := os.Stat(cfg.RecordPath())
	if err != nil {
		t.Fatal(err)
	}
	if recInfo.Mode().Perm() != 0600 {
		t.Errorf("record mode = %o, want 0600", recInfo.Mode().Perm())
	}
}

func TestReinstallIsIdempotentUpgrade(t *testing.T) {
	cfg, prefix, _ := installedTree(t)
	dbPath := filepath.Join(prefix, cfg.StateDir, "opentambo.db")
	if err := os.WriteFile(dbPath, []byte("state"), 0600); err != nil {
		t.Fatal(err)
	}

	fake := activeFake()
	fake.out["getent group opentambo"] = "opentambo:x:995:"
	fake.out["getent passwd opentambo"] = "opentambo:x:995:995::/var/lib/opentambo:/usr/sbin/nologin"
	log := &strings.Builder{}
	in := &Installer{Cfg: cfg, Cmd: fake, Out: log}
	if err := in.Install(context.Background()); err != nil {
		t.Fatalf("upgrade: %v\n%s", err, log)
	}
	if fake.has("groupadd") || fake.has("useradd") {
		t.Errorf("upgrade recreated existing identities: %v", fake.calls)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("upgrade destroyed the database: %v", err)
	}
	if !strings.Contains(log.String(), "upgrade complete") {
		t.Errorf("upgrade did not report preservation:\n%s", log)
	}
	rec, _, err := loadRecord(cfg.RecordPath())
	if err != nil || !rec.CreatedUser {
		t.Errorf("upgrade lost created_user: %+v err=%v", rec, err)
	}
}

func TestInstallRefusesInteractiveExistingUser(t *testing.T) {
	cfg, _ := stageRoots(t)
	fake := activeFake()
	fake.out["getent passwd opentambo"] = "opentambo:x:1000:1000::/home/opentambo:/bin/bash"
	err := (&Installer{Cfg: cfg, Cmd: fake}).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to repurpose") {
		t.Fatalf("interactive account = %v", err)
	}
	if fake.has("systemctl enable") {
		t.Error("install continued past the account conflict")
	}
}

func TestInstallRefusesMissingRuntime(t *testing.T) {
	cfg, prefix := stageRoots(t)
	if err := os.Remove(filepath.Join(prefix, cfg.LlamaServer)); err != nil {
		t.Fatal(err)
	}
	fake := activeFake()
	err := (&Installer{Cfg: cfg, Cmd: fake}).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "native runtime") {
		t.Fatalf("missing runtime = %v", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("preflight mutated the host before failing: %v", fake.calls)
	}
}

func TestInstallRefusesUnusableBinary(t *testing.T) {
	cfg, prefix := stageRoots(t)
	src := filepath.Join(prefix, "source", "broken")
	if err := os.WriteFile(src, []byte("not an executable"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg.SourceBinary = src
	err := (&Installer{Cfg: cfg, Cmd: activeFake()}).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not a usable executable") {
		t.Fatalf("bad source binary = %v", err)
	}
	if _, err := os.Stat(filepath.Join(prefix, cfg.BinPath)); !os.IsNotExist(err) {
		t.Error("broken source replaced the target binary")
	}
}

func TestInstallGrantsExtraGroups(t *testing.T) {
	cfg, _ := stageRoots(t)
	cfg.ExtraGroups = []string{"render", "video"}
	fake := activeFake()
	if err := (&Installer{Cfg: cfg, Cmd: fake, Out: &strings.Builder{}}).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fake.has("usermod -aG render opentambo") || !fake.has("usermod -aG video opentambo") {
		t.Errorf("extra groups not granted: %v", fake.calls)
	}
}

func TestInstallReportsInactiveService(t *testing.T) {
	cfg, _ := stageRoots(t)
	fake := &fakeCmd{out: map[string]string{"systemctl is-active opentambo.service": "failed"}}
	err := (&Installer{Cfg: cfg, Cmd: fake}).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), `reports "failed"`) {
		t.Fatalf("inactive service = %v", err)
	}
	if _, found, err := loadRecord(cfg.RecordPath()); err != nil || !found {
		t.Errorf("failed start lost the installation record: found=%v err=%v", found, err)
	}
}

func TestInstallAdoptsExistingModelDirectory(t *testing.T) {
	cfg, prefix := stageRoots(t)
	models := filepath.Join(prefix, cfg.ModelsDir)
	if err := os.MkdirAll(models, 0755); err != nil {
		t.Fatal(err)
	}
	gguf := filepath.Join(models, "m.gguf")
	if err := os.WriteFile(gguf, []byte("GGUF"), 0644); err != nil {
		t.Fatal(err)
	}
	log := &strings.Builder{}
	if err := (&Installer{Cfg: cfg, Cmd: activeFake(), Out: log}).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "adopting existing model directory") {
		t.Errorf("no adoption report:\n%s", log)
	}
	rec, _, err := loadRecord(cfg.RecordPath())
	if err != nil || rec.CreatedModelsDir {
		t.Errorf("adopted directory marked installer-created: %+v err=%v", rec, err)
	}
	if _, err := os.Stat(gguf); err != nil {
		t.Errorf("install touched the model file: %v", err)
	}
}

func TestRecordCorruptionAborts(t *testing.T) {
	cfg, prefix := stageRoots(t)
	recordPath := filepath.Join(prefix, cfg.EtcDir, "install.json")
	if err := os.MkdirAll(filepath.Dir(recordPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, []byte("{nope"), 0600); err != nil {
		t.Fatal(err)
	}
	err := (&Installer{Cfg: cfg, Cmd: activeFake()}).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt record = %v", err)
	}

	if err := os.WriteFile(recordPath, jsonMarshalIndent(Record{Version: 99}), 0600); err != nil {
		t.Fatal(err)
	}
	err = (&Installer{Cfg: cfg, Cmd: activeFake()}).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("future record version = %v", err)
	}
}

func jsonMarshalIndent(rec Record) []byte {
	data, err := json.Marshal(rec)
	if err != nil {
		panic(err)
	}
	return data
}
