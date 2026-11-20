//go:build integration

package integration

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runInstrumentedCLI executes the coverage-instrumented OpenTambo binary as
// a real subprocess with its own GOCOVERDIR bucket and returns exit code +
// combined output. The process must be one the CLI itself rejects or
// terminates; nothing here is mocked.
func runInstrumentedCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cover, err := os.MkdirTemp(coverDir, "cli-*")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sharedBinary, args...)
	cmd.Env = append(os.Environ(), "GOCOVERDIR="+cover)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	runErr := cmd.Run()
	code := 0
	if exit, ok := runErr.(*exec.ExitError); ok {
		status, _ := exit.Sys().(syscall.WaitStatus)
		code = status.ExitStatus()
	} else if runErr != nil {
		t.Fatalf("running %v: %v", args, runErr)
	}
	return code, combined.String()
}

func TestCLIRejectionsAndHelp(t *testing.T) {
	// An unknown flag must exit with usage error code 2 and a flag error.
	code, out := runInstrumentedCLI(t, "-no-such-flag")
	if code != 2 {
		t.Errorf("-no-such-flag exit code %d, want 2 (output: %s)", code, out)
	}
	if !strings.Contains(out, "no-such-flag") {
		t.Errorf("usage error must name the offending flag: %s", out)
	}

	// -h exits successfully with the flag summary on stdout.
	code, out = runInstrumentedCLI(t, "-h")
	if code != 0 {
		t.Errorf("-h exit code %d, want 0", code)
	}
	for _, want := range []string{"-listen", "-models", "-llama-server", "-data", "-downloads"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %s: %s", want, out)
		}
	}

	// Download parallelism outside the accepted range fails startup.
	code, out = runInstrumentedCLI(t, "-downloads", "0",
		"-listen", "127.0.0.1:0",
		"-models", filepath.Join(t.TempDir(), "models"),
		"-data", filepath.Join(t.TempDir(), "data"))
	if code != 1 {
		t.Errorf("invalid -downloads exit code %d, want 1 (output: %s)", code, out)
	}
	if !strings.Contains(out, "downloads") {
		t.Errorf("startup failure must explain the rejection: %s", out)
	}
}

// TestCovdiffSelfCheck builds the changed-line gate tool with -cover from
// the tested commit and runs it as a real instrumented subprocess on both
// outcomes: a docs-only repository diff passes as N/A, and a missing
// profile fails with the error shape. This keeps the gate tool itself
// honest and inside the coverage denominator.
func TestCovdiffSelfCheck(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "covdiff")
	build := exec.Command("go", "build", "-cover", "-o", binary, "./tools/covdiff")
	build.Dir = repoRoot
	if combined, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building covdiff with -cover: %v\n%s", err, combined)
	}

	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.invalid"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if combined, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, combined)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "add", "README.md")
	add.Dir = repo
	if combined, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, combined)
	}
	commit := exec.Command("git", "commit", "-q", "-m", "initial")
	commit.Dir = repo
	if combined, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, combined)
	}
	profile := filepath.Join(repo, "coverage.out")
	if err := os.WriteFile(profile, []byte("mode: atomic\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cover, err := os.MkdirTemp(coverDir, "covdiff-*")
	if err != nil {
		t.Fatal(err)
	}

	runIn := func(args ...string) (int, string) {
		cmd := exec.Command(binary, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GOCOVERDIR="+cover)
		var combined bytes.Buffer
		cmd.Stdout = &combined
		cmd.Stderr = &combined
		runErr := cmd.Run()
		if exit, ok := runErr.(*exec.ExitError); ok {
			status, _ := exit.Sys().(syscall.WaitStatus)
			return status.ExitStatus(), combined.String()
		}
		if runErr != nil {
			t.Fatalf("running covdiff %v: %v", args, runErr)
		}
		return 0, combined.String()
	}

	code, out := runIn("-base", "main", profile)
	if code != 0 || !strings.Contains(out, "N/A") {
		t.Errorf("docs-only diff must pass as N/A: exit %d, out %q", code, out)
	}
	code, out = runIn("-base", "main", filepath.Join(repo, "missing.out"))
	if code != 1 || !strings.Contains(out, "covdiff:") {
		t.Errorf("a missing profile must fail with the covdiff error prefix: exit %d, out %q", code, out)
	}
}

// TestOPENTAMBO_LISTENEnvironmentIsHonoured starts the real binary with the
// documented environment default and confirms it binds exactly there.
func TestOPENTAMBO_LISTENEnvironmentIsHonoured(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	cover, err := os.MkdirTemp(coverDir, "env-*")
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	cmd := exec.Command(sharedBinary,
		"-models", filepath.Join(t.TempDir(), "models"),
		"-data", dataDir)
	cmd.Env = append(os.Environ(),
		"GOCOVERDIR="+cover,
		"OPENTAMBO_LISTEN=127.0.0.1:"+strconv.Itoa(port))
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil && cmd.ProcessState != nil && !cmd.ProcessState.Exited() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_, _ = cmd.Process.Wait()
		}
	})
	client := &http.Client{Timeout: 2 * time.Second}
	addr := "http://127.0.0.1:" + strconv.Itoa(port)
	healthy := false
	for attempt := 0; attempt < 300; attempt++ {
		response, err := client.Get(addr + "/healthz")
		if err == nil {
			response.Body.Close()
			healthy = response.StatusCode == 200
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !healthy {
		t.Fatalf("OPENTAMBO_LISTEN address never became healthy (output: %s)", combined.String())
	}
	if !strings.Contains(combined.String(), "127.0.0.1:"+strconv.Itoa(port)) {
		t.Errorf("startup line does not report the env-configured address: %s", combined.String())
	}
	// Graceful termination via SIGTERM.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("SIGTERM shutdown must exit cleanly: %v (output: %s)", err, combined.String())
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("binary did not exit within 20s of SIGTERM")
	}
}
