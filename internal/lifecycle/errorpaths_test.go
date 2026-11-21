package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chmodFor restricts a path for one test and restores it afterwards, so
// the shared staging tree stays removable.
func chmodFor(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	prev, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, prev.Mode()) })
}

func TestCheckHostRefusals(t *testing.T) {
	oldGoos, oldPath := hostGOOS, systemdRunPath
	defer func() { hostGOOS, systemdRunPath = oldGoos, oldPath }()

	hostGOOS = "darwin"
	if err := CheckHost(); err == nil || !strings.Contains(err.Error(), "darwin") {
		t.Fatalf("non-Linux CheckHost = %v", err)
	}
	hostGOOS = "linux"
	systemdRunPath = filepath.Join(t.TempDir(), "absent")
	if err := CheckHost(); err == nil || !strings.Contains(err.Error(), "systemd is not running") {
		t.Fatalf("no-systemd CheckHost = %v", err)
	}
}

// TestInstallFailureInjection drives the whole install flow once per
// injected host-tool or filesystem failure and asserts the flow refuses
// with the documented error, at the right step.
func TestInstallFailureInjection(t *testing.T) {
	type installCase struct {
		name   string
		tweak  func(t *testing.T, cfg *Config, fake *fakeCmd, prefix string)
		want   string
		noHost bool // tweaks that do not need the staging tree mounted first
	}
	cases := []installCase{
		{"corrupt record", func(t *testing.T, cfg *Config, _ *fakeCmd, prefix string) {
			p := filepath.Join(prefix, "/etc/opentambo")
			if err := os.MkdirAll(p, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p, "install.json"), []byte("{oops"), 0600); err != nil {
				t.Fatal(err)
			}
		}, "corrupt", false},
		{"groupadd fails", func(_ *testing.T, _ *Config, fake *fakeCmd, _ string) {
			fake.failCmd = map[string]bool{"groupadd": true}
		}, "create group", false},
		{"useradd fails", func(_ *testing.T, _ *Config, fake *fakeCmd, _ string) {
			fake.failCmd = map[string]bool{"useradd": true}
		}, "create user", false},
		{"malformed passwd entry", func(_ *testing.T, _ *Config, fake *fakeCmd, _ string) {
			fake.out["getent passwd opentambo"] = "opentambo"
		}, "unexpected passwd entry", false},
		{"usermod fails", func(_ *testing.T, cfg *Config, fake *fakeCmd, _ string) {
			cfg.ExtraGroups = []string{"render"}
			fake.failCmd = map[string]bool{"usermod": true}
		}, "grant supplementary group", false},
		{"etc collision is a file", func(t *testing.T, _ *Config, _ *fakeCmd, prefix string) {
			if err := os.WriteFile(filepath.Join(prefix, "/etc"), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}, "not a directory", false},
		{"state path is a file", func(t *testing.T, cfg *Config, _ *fakeCmd, prefix string) {
			full := filepath.Join(prefix, cfg.StateDir)
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}, "exists but is not a directory", false},
		{"models is a plain file", func(t *testing.T, cfg *Config, _ *fakeCmd, prefix string) {
			full := filepath.Join(prefix, cfg.ModelsDir)
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
		}, "not a directory", false},
		{"chown fails", func(_ *testing.T, _ *Config, fake *fakeCmd, _ string) {
			fake.fail = map[string]bool{"chown": true}
		}, "ownership of", false},
		{"runtime not executable by service user", func(_ *testing.T, _ *Config, fake *fakeCmd, _ string) {
			fake.failCmd = map[string]bool{"runuser -u opentambo -- test -x": true}
		}, "cannot execute", false},
		{"models unreadable by service user", func(_ *testing.T, _ *Config, fake *fakeCmd, _ string) {
			fake.failCmd = map[string]bool{"runuser -u opentambo -- test -r": true}
		}, "cannot read the model directory", false},
		{"unit path is a directory", func(t *testing.T, cfg *Config, _ *fakeCmd, prefix string) {
			if err := os.MkdirAll(filepath.Join(prefix, cfg.UnitPath, "nested"), 0755); err != nil {
				t.Fatal(err)
			}
		}, "write unit", false},
		{"daemon-reload fails", func(_ *testing.T, _ *Config, fake *fakeCmd, _ string) {
			fake.failCmd = map[string]bool{"systemctl daemon-reload": true}
		}, "daemon-reload", false},
		{"enable fails", func(_ *testing.T, _ *Config, fake *fakeCmd, _ string) {
			fake.failCmd = map[string]bool{"systemctl enable": true}
		}, "enable --now failed", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, prefix := stageRoots(t)
			fake := activeFake()
			tc.tweak(t, &cfg, fake, prefix)
			err := (&Installer{Cfg: cfg, Cmd: fake, Out: &strings.Builder{}}).Install(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("install with %s = %v, want error containing %q", tc.name, err, tc.want)
			}
		})
	}
}

func TestInstallRejectsBadExtraGroupName(t *testing.T) {
	cfg, _ := stageRoots(t)
	cfg.ExtraGroups = []string{"not/valid"}
	err := (&Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "group") {
		t.Fatalf("install with invalid extra group = %v", err)
	}
}

func TestValidateRuntimeAccessRefusals(t *testing.T) {
	cfg, prefix := stageRoots(t)
	in := &Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}

	if err := os.Remove(filepath.Join(prefix, cfg.LlamaServer)); err != nil {
		t.Fatal(err)
	}
	rec := &Record{}
	if err := in.validateRuntimeAccess(context.Background(), rec); err == nil ||
		!strings.Contains(err.Error(), "native runtime") {
		t.Fatalf("missing runtime = %v", err)
	}
}

func TestInstallBinaryFailurePaths(t *testing.T) {
	// Self-path probe failure (bootstrap without an explicit source).
	cfg, _ := stageRoots(t)
	var prefix string
	cfg.SourceBinary = ""
	in := &Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{},
		SelfPath: func() (string, error) { return "", errors.New("no exe") }}
	if err := in.installBinary(); err == nil || !strings.Contains(err.Error(), "no exe") {
		t.Fatalf("selfPath failure = %v", err)
	}

	// Missing source release.
	cfg, _ = stageRoots(t)
	cfg.SourceBinary = "/nonexistent/release/opentambo"
	in = &Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}
	if err := in.installBinary(); err == nil || !strings.Contains(err.Error(), "source binary") {
		t.Fatalf("missing source = %v", err)
	}

	// Wrong format: not an ELF image.
	cfg, _ = stageRoots(t)
	if err := os.WriteFile(cfg.SourceBinary, []byte("MZ? no"), 0755); err != nil {
		t.Fatal(err)
	}
	in = &Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}
	if err := in.installBinary(); err == nil || !strings.Contains(err.Error(), "not a usable executable") {
		t.Fatalf("non-ELF source = %v", err)
	}

	// The binary destination collides with a non-empty directory.
	cfg, prefix = stageRoots(t)
	bin := filepath.Join(prefix, cfg.BinPath)
	if err := os.MkdirAll(filepath.Join(bin, "collide"), 0755); err != nil {
		t.Fatal(err)
	}
	in = &Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}
	if err := in.installBinary(); err == nil || !strings.Contains(err.Error(), "replace "+cfg.BinPath) {
		t.Fatalf("rename collision = %v", err)
	}
	if _, err := os.Stat(bin + ".new"); !os.IsNotExist(err) {
		t.Error("failed install left the staging file behind")
	}
}

func TestInstallUnitAndRecordFailures(t *testing.T) {
	// Unit parent directory cannot be created.
	cfg, prefix := stageRoots(t)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(prefix, "/etc/systemd")), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "/etc/systemd"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	in := &Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}
	if err := in.installUnit(); err == nil {
		t.Fatal("unit MkdirAll over a file succeeded")
	}

	// Ownership record cannot be written (its parent is a file).
	cfg, prefix = stageRoots(t)
	if err := os.WriteFile(filepath.Join(prefix, "/etc"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	fake := activeFake()
	err := (&Installer{Cfg: cfg, Cmd: fake, Out: &strings.Builder{}}).enableService(context.Background(), &Record{})
	if err == nil || !strings.Contains(err.Error(), "write installation record") {
		t.Fatalf("record write failure = %v", err)
	}
}

func TestRecordSafetyValidation(t *testing.T) {
	prefix := t.TempDir()

	if _, err := resolveSafe("relative/path", prefix); err == nil ||
		!strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("relative resolveSafe = %v", err)
	}
	if _, err := resolveSafe("/var", prefix); err == nil ||
		!strings.Contains(err.Error(), "refusing to remove system directory") {
		t.Fatalf("banned resolveSafe = %v", err)
	}
	if _, err := safeRemovalPath("/usr/local/bin/opentambo", "/srv/other/opentambo", prefix); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched safeRemovalPath = %v", err)
	}
	if _, err := resolveWithin("/opt/models", prefix, "/var/lib/opentambo"); err == nil ||
		!strings.Contains(err.Error(), "outside the managed state tree") {
		t.Fatalf("external resolveWithin = %v", err)
	}
	if _, err := resolveWithin("/var/lib/opentambo/models", prefix, "/var/lib/opentambo"); err != nil {
		t.Fatalf("internal resolveWithin = %v", err)
	}
}

func TestRecordIOErrors(t *testing.T) {
	dir := t.TempDir()

	// Record parent chain collides with a regular file.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := saveRecord(filepath.Join(blocker, "install.json"), Record{Version: RecordVersion}); err == nil {
		t.Fatal("saveRecord under a file succeeded")
	}

	// Directory not writable: MkdirAll passes (exists), WriteFile fails.
	ro := filepath.Join(dir, "ro")
	if err := os.MkdirAll(ro, 0755); err != nil {
		t.Fatal(err)
	}
	chmodFor(t, ro, 0500)
	if err := saveRecord(filepath.Join(ro, "install.json"), Record{Version: RecordVersion}); err == nil {
		t.Fatal("saveRecord into a read-only directory succeeded")
	}

	// Record unreadable: a read error distinct from "absent".
	blocked := filepath.Join(dir, "blocked.json")
	if err := os.WriteFile(blocked, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	chmodFor(t, blocked, 0000)
	if _, _, err := loadRecord(blocked); err == nil {
		t.Fatal("loadRecord of an unreadable file succeeded")
	}

	// Corrupt JSON and unsupported versions are refused, never guessed.
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{oops"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadRecord(corrupt); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt record = %v", err)
	}
	future := filepath.Join(dir, "future.json")
	data, err := json.Marshal(Record{Version: RecordVersion + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(future, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadRecord(future); err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("future record = %v", err)
	}
}

func TestMigrateCopyFailurePaths(t *testing.T) {
	// Unreadable state file aborts the copy and names the file.
	cfg, prefix := stageRoots(t)
	src := stageLegacy(t, prefix, "/home/olduser/.local/share/opentambo")
	chmodFor(t, filepath.Join(src, "cache", "ns1", "e.bin"), 0000)
	cfg.MigrateFrom = filepath.ToSlash(src[len(prefix):])
	err := (&Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).migrateState(&Record{})
	if err == nil || !strings.Contains(err.Error(), "read") {
		t.Fatalf("unreadable source file = %v", err)
	}

	// Unreadable subdirectory aborts the tree walk.
	cfg, prefix = stageRoots(t)
	src = stageLegacy(t, prefix, "/home/olduser/.local/share/opentambo")
	chmodFor(t, filepath.Join(src, "cache"), 0000)
	cfg.MigrateFrom = filepath.ToSlash(src[len(prefix):])
	if err := (&Installer{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).migrateState(&Record{}); err == nil {
		t.Fatal("unreadable source directory copy succeeded")
	}

	// Special files are skipped, regular files copied.
	src2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(src2, "opentambo.db"), []byte("db"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(src2, "link")); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := copyStateTree(src2, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "opentambo.db")); err != nil {
		t.Errorf("regular file not copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "link")); !os.IsNotExist(err) {
		t.Error("symlink was copied into the state tree")
	}

	// Destination not writable: copy fails on write, not silently.
	writableSrc := t.TempDir()
	if err := os.WriteFile(filepath.Join(writableSrc, "opentambo.db"), []byte("db"), 0600); err != nil {
		t.Fatal(err)
	}
	roDst := filepath.Join(t.TempDir(), "dst")
	if err := os.MkdirAll(roDst, 0755); err != nil {
		t.Fatal(err)
	}
	chmodFor(t, roDst, 0500)
	if err := copyStateTree(writableSrc, roDst); err == nil ||
		!strings.Contains(err.Error(), "write") {
		t.Fatalf("read-only destination = %v", err)
	}

	// Ownership hand-off failure is reported (the copy itself succeeded).
	cfg, prefix = stageRoots(t)
	src = stageLegacy(t, prefix, "/home/olduser/.local/share/opentambo")
	cfg.MigrateFrom = filepath.ToSlash(src[len(prefix):])
	fake := &fakeCmd{out: map[string]string{}, fail: map[string]bool{"chown": true}}
	err = (&Installer{Cfg: cfg, Cmd: fake, Out: &strings.Builder{}}).migrateState(&Record{})
	if err == nil || !strings.Contains(err.Error(), "hand imported state") {
		t.Fatalf("chown failure = %v", err)
	}
}

func TestUninstallFailurePaths(t *testing.T) {
	t.Run("corrupt record aborts", func(t *testing.T) {
		cfg, prefix := installedForUninstall(t)
		if err := os.WriteFile(filepath.Join(prefix, "/etc/opentambo/install.json"), []byte("{oops"), 0600); err != nil {
			t.Fatal(err)
		}
		err := (&Uninstaller{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).
			Uninstall(context.Background(), UninstallOptions{})
		if err == nil || !strings.Contains(err.Error(), "corrupt") {
			t.Fatalf("corrupt record uninstall = %v", err)
		}
	})

	t.Run("systemctl failures are tolerated", func(t *testing.T) {
		cfg, _ := installedForUninstall(t)
		fake := &fakeCmd{out: map[string]string{}, failCmd: map[string]bool{
			"systemctl stop": true, "systemctl disable": true, "systemctl daemon-reload": true,
		}}
		log := &strings.Builder{}
		if err := (&Uninstaller{Cfg: cfg, Cmd: fake, Out: log}).Uninstall(context.Background(), UninstallOptions{}); err != nil {
			t.Fatalf("uninstall with failing systemctl = %v", err)
		}
		if !strings.Contains(log.String(), "continued") {
			t.Errorf("tolerated failures not reported:\n%s", log)
		}
	})

	t.Run("undeletable unit file aborts", func(t *testing.T) {
		cfg, prefix := installedForUninstall(t)
		unit := filepath.Join(prefix, cfg.UnitPath)
		if err := os.Remove(unit); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(unit, "collide"), 0755); err != nil {
			t.Fatal(err)
		}
		err := (&Uninstaller{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).
			Uninstall(context.Background(), UninstallOptions{})
		if err == nil || !strings.Contains(err.Error(), "remove") {
			t.Fatalf("unit collision uninstall = %v", err)
		}
	})

	t.Run("undeletable binary aborts", func(t *testing.T) {
		cfg, prefix := installedForUninstall(t)
		bin := filepath.Join(prefix, cfg.BinPath)
		if err := os.Remove(bin); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(bin, "collide"), 0755); err != nil {
			t.Fatal(err)
		}
		err := (&Uninstaller{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).
			Uninstall(context.Background(), UninstallOptions{})
		if err == nil || !strings.Contains(err.Error(), "remove") {
			t.Fatalf("binary collision uninstall = %v", err)
		}
	})

	t.Run("purge refuses banned state path", func(t *testing.T) {
		cfg, _ := installedForUninstall(t)
		cfg.StateDir = "/var"
		err := (&Uninstaller{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).
			Uninstall(context.Background(), UninstallOptions{PurgeState: true})
		if err == nil || !strings.Contains(err.Error(), "refusing to remove system directory") {
			t.Fatalf("purge with StateDir=/var = %v", err)
		}
	})

	t.Run("keep-models rename failure aborts", func(t *testing.T) {
		cfg, prefix := installedForUninstall(t)
		state := filepath.Join(prefix, cfg.StateDir)
		chmodFor(t, state, 0500)
		err := (&Uninstaller{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).
			Uninstall(context.Background(), UninstallOptions{PurgeState: true})
		if err == nil || !strings.Contains(err.Error(), "move models aside") {
			t.Fatalf("models aside failure = %v", err)
		}
	})

	t.Run("state tree removal failure aborts", func(t *testing.T) {
		cfg, prefix := installedForUninstall(t)
		cfg.ModelsDir = "/mnt/external/models"
		chmodFor(t, filepath.Join(prefix, "/var/lib"), 0500)
		err := (&Uninstaller{Cfg: cfg, Cmd: activeFake(), Out: &strings.Builder{}}).
			Uninstall(context.Background(), UninstallOptions{PurgeState: true})
		if err == nil || !strings.Contains(err.Error(), "remove state tree") {
			t.Fatalf("state removal failure = %v", err)
		}
	})
}

func TestPurgeModelsDirFailurePaths(t *testing.T) {
	// An installer-created model directory outside the state tree is
	// retained, never removed (the resolveWithin guard).
	cfg, _ := stageRoots(t)
	cfg.ModelsDir = "/mnt/nas/models"
	log := &strings.Builder{}
	if err := (&Uninstaller{Cfg: cfg, Out: log}).purgeModelsDir(Record{CreatedModelsDir: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "outside the managed state tree") {
		t.Errorf("external purge retention not reported:\n%s", log)
	}

	// A model directory the parent tree refuses to release reports the error.
	cfg, prefix := stageRoots(t)
	models := filepath.Join(prefix, cfg.StateDir, "models")
	if err := os.MkdirAll(filepath.Join(models, "deep"), 0750); err != nil {
		t.Fatal(err)
	}
	chmodFor(t, filepath.Join(prefix, cfg.StateDir), 0500)
	err := (&Uninstaller{Cfg: cfg, Out: &strings.Builder{}}).purgeModelsDir(Record{CreatedModelsDir: true})
	if err == nil || !strings.Contains(err.Error(), "remove models directory") {
		t.Fatalf("models removal failure = %v", err)
	}
}

func TestRemoveEtcFailurePaths(t *testing.T) {
	// The ownership record collides with a non-empty directory.
	cfg, prefix := stageRoots(t)
	recordPath := filepath.Join(prefix, cfg.EtcDir, "install.json")
	if err := os.MkdirAll(filepath.Join(recordPath, "collide"), 0755); err != nil {
		t.Fatal(err)
	}
	un := &Uninstaller{Cfg: cfg, Out: &strings.Builder{}}
	err := un.removeEtc(recordFromConfig(cfg), UninstallOptions{})
	if err == nil || !strings.Contains(err.Error(), "remove") {
		t.Fatalf("record directory collision = %v", err)
	}

	// etc unreadable with a foreign record: ReadDir error surfaces.
	cfg, prefix = stageRoots(t)
	etc := filepath.Join(prefix, cfg.EtcDir)
	if err := os.MkdirAll(etc, 0755); err != nil {
		t.Fatal(err)
	}
	chmodFor(t, etc, 0300)
	foreign := recordFromConfig(cfg)
	foreign.EtcDir = "/etc/other-install"
	err = (&Uninstaller{Cfg: cfg, Out: &strings.Builder{}}).removeEtc(foreign, UninstallOptions{PurgeAll: true})
	if err == nil {
		t.Fatal("unreadable etc with PurgeAll succeeded")
	}

	// An empty etc whose parent refuses the unlink surfaces the error.
	cfg, prefix = stageRoots(t)
	if err := os.MkdirAll(filepath.Join(prefix, cfg.EtcDir), 0755); err != nil {
		t.Fatal(err)
	}
	chmodFor(t, filepath.Join(prefix, "/etc"), 0500)
	err = (&Uninstaller{Cfg: cfg, Out: &strings.Builder{}}).removeEtc(recordFromConfig(cfg), UninstallOptions{PurgeAll: true})
	if err == nil {
		t.Fatal("removing etc from a read-only parent succeeded")
	}
}

func TestRemoveIdentityCommandFailures(t *testing.T) {
	cfg, _ := stageRoots(t)
	un := &Uninstaller{Cfg: cfg, Cmd: &fakeCmd{failCmd: map[string]bool{"userdel": true}}, Out: &strings.Builder{}}
	err := un.removeIdentity(context.Background(), Record{CreatedUser: true}, UninstallOptions{PurgeAll: true})
	if err == nil || !strings.Contains(err.Error(), "remove installer-created user") {
		t.Fatalf("userdel failure = %v", err)
	}

	fake := &fakeCmd{out: map[string]string{"getent group opentambo": "opentambo:x:995:"},
		failCmd: map[string]bool{"groupdel": true}}
	err = (&Uninstaller{Cfg: cfg, Cmd: fake, Out: &strings.Builder{}}).
		removeIdentity(context.Background(), Record{CreatedUser: true, CreatedGroup: true}, UninstallOptions{PurgeAll: true})
	if err == nil || !strings.Contains(err.Error(), "remove installer-created group") {
		t.Fatalf("groupdel failure = %v", err)
	}
}

func TestRenderUnitMountGuard(t *testing.T) {
	cfg := DefaultConfig()
	if strings.Contains(renderUnit(cfg), "RequiresMountsFor") {
		t.Error("unit has a mount guard without --mount")
	}
	cfg.MountGuard = "/srv/models"
	unit := renderUnit(cfg)
	for _, want := range []string{"RequiresMountsFor=/srv/models", "mountpoint -q /srv/models"} {
		if !strings.Contains(unit, want) {
			t.Errorf("mount-guard unit missing %q:\n%s", want, unit)
		}
	}
}
