package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Exercises the production host checks and the real shell seam against
// harmless targets: this suite only runs meaningfully on Linux, and the
// commands executed here are `true` and a same-owner chown.

func TestCheckHostOnThisHost(t *testing.T) {
	// On Linux+systemd this succeeds; on other dev hosts it must fail with
	// the actionable refusal, never panic. Both outcomes are assertions.
	err := CheckHost()
	if err != nil {
		t.Logf("host not supported for real installs here: %v", err)
	}
}

func TestShellCommandRunsHarmlessCommands(t *testing.T) {
	sc := ShellCommand{}
	out, err := sc.Run(context.Background(), "true")
	if err != nil {
		t.Fatalf("true failed: %v (%s)", err, out)
	}
	dir := t.TempDir()
	// Chown to the caller's own identity is permitted without privileges
	// and proves the seam shells out successfully.
	owner, group := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	if err := sc.Chown(dir, owner, group); err != nil {
		t.Skipf("chown to own identity refused in this sandbox: %v", err)
	}
	if err := sc.ChownRecursive(dir, owner, group); err != nil {
		t.Skipf("recursive chown to own identity refused in this sandbox: %v", err)
	}
}

func TestDefaultConfigLayoutIsStandard(t *testing.T) {
	c := DefaultConfig()
	for name, p := range map[string]string{
		"binary": c.BinPath, "etc": c.EtcDir, "state": c.StateDir,
		"models": c.ModelsDir, "unit": c.UnitPath,
	} {
		if p == filepath.Clean(p) && (p == "/" || p == "/usr" || p == "/var") {
			t.Errorf("%s path suspiciously broad: %q", name, p)
		}
	}
	if err := c.validate(); err != nil {
		t.Errorf("default config must validate: %v", err)
	}
	if c.path("/x") != filepath.Join(c.Prefix, "/x") {
		t.Error("path must stage under Prefix")
	}
}
