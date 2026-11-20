package desk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ekelhala/opentambo/internal/desk/api"
	"github.com/ekelhala/opentambo/internal/desk/store"
)

func writeLegacyState(t *testing.T, dir string, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func seedDataDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "models"), 0750); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, "data")
}

func TestLegacyStateImportMarksInterruptedAndBacksUp(t *testing.T) {
	data := seedDataDir(t)
	if err := os.MkdirAll(data, 0750); err != nil {
		t.Fatal(err)
	}
	legacy := `{"jobs":{"j1":{"id":"j1","url":"https://h/f.gguf","file":"f.gguf","status":"downloading","bytes":120,"total":1000,"etag":"\"e1\"","created":"2026-01-01T00:00:00Z"}},"profiles":{"m.gguf":{"model":"m.gguf","name":"smol","port":8091,"context":2048,"gpu_layers":0,"args":["--flash-attn"],"env":{"SECRET":"s3cret"},"prompt_cache":{"enabled":true,"max_bytes":4096}}}}`
	path := writeLegacyState(t, data, legacy)

	a := appOnData(t, filepath.Dir(data), data)
	job := a.jobs["j1"]
	if job == nil {
		t.Fatal("legacy job not imported")
	}
	if job.Status != api.JobStatusInterrupted || job.Bytes != 120 || job.ETag != `"e1"` {
		t.Fatalf("imported job = %+v, want interrupted with resume checkpoint", job)
	}
	spec := a.profiles["m.gguf"]
	if spec.Name != "smol" || spec.Port != 8091 || spec.Env["SECRET"] != "s3cret" ||
		len(spec.Args) != 1 || spec.PromptCache == nil || !spec.PromptCache.Enabled || spec.PromptCache.MaxBytes != 4096 {
		t.Fatalf("imported profile = %+v", spec)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("legacy state.json must be renamed aside, stat err = %v", err)
	}
	if _, err := os.Stat(path + ".migrated"); err != nil {
		t.Fatalf("legacy backup missing: %v", err)
	}
	marker, ok, err := store.Meta(a.db, importStateMarker)
	if err != nil || !ok || !strings.Contains(marker, "imported 1 downloads, 1 profiles") {
		t.Fatalf("import marker = (%q,%v,%v)", marker, ok, err)
	}
	a.Close()

	// Reopening must not re-import: delete the profile row from the
	// database, restore the backup under its old name, and verify the
	// completion marker keeps the import from running a second time.
	db2, err := store.Open(filepath.Join(data, store.DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteProfile(db2, "m.gguf"); err != nil {
		t.Fatal(err)
	}
	if err = db2.Close(); err != nil {
		t.Fatal(err)
	}
	if err = restoreFile(t, path+".migrated", path); err != nil {
		t.Fatal(err)
	}
	b := appOnData(t, filepath.Dir(data), data)
	defer b.Close()
	if _, ok := b.profiles["m.gguf"]; ok {
		t.Fatal("legacy import ran twice: profile re-imported despite the marker")
	}
}

func restoreFile(t *testing.T, from, to string) error {
	t.Helper()
	contents, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, contents, 0600)
}

func TestLegacyStateCorruptAbortsStartup(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	if err := os.MkdirAll(data, 0750); err != nil {
		t.Fatal(err)
	}
	path := writeLegacyState(t, data, `{"jobs": oops`)
	_, err := New(filepath.Join(base, "models"), "missing-llama-server", data, 2)
	if err == nil || !strings.Contains(err.Error(), "repair or move") {
		t.Fatalf("corrupt legacy import = %v, want a repair-or-move error", err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("corrupt legacy file must stay untouched: %v", err)
	}
}

func TestDurableSaveRoundTripsSecretsAndJobs(t *testing.T) {
	data := seedDataDir(t)
	if err := os.MkdirAll(data, 0750); err != nil {
		t.Fatal(err)
	}
	a := appOnData(t, filepath.Dir(data), data)
	a.mu.Lock()
	a.profiles["secret.gguf"] = Launch{Model: "secret.gguf", Name: "sec", Context: 512, Env: map[string]string{"API_KEY": "hunter2"}}
	a.jobs["dl"] = &Job{Job: api.Job{ID: "dl", URL: "https://h/x.gguf", File: "x.gguf", Status: api.JobStatusCompleted, Total: 5, Bytes: 5, Created: time.Now().UTC().Truncate(time.Millisecond)}}
	if err := a.save(); err != nil {
		t.Fatal(err)
	}
	a.mu.Unlock()
	a.Close()

	b := appOnData(t, filepath.Dir(data), data)
	defer b.Close()
	if got := b.profiles["secret.gguf"].Env["API_KEY"]; got != "hunter2" {
		t.Fatalf("profile secret lost across restart: %q", got)
	}
	if job := b.jobs["dl"]; job == nil || job.Status != api.JobStatusCompleted || job.Bytes != 5 {
		t.Fatalf("completed job lost across restart: %+v", job)
	}
}

func TestRunHistoryDemotedAfterRestart(t *testing.T) {
	data := seedDataDir(t)
	if err := os.MkdirAll(data, 0750); err != nil {
		t.Fatal(err)
	}
	a := appOnData(t, filepath.Dir(data), data)
	run := &Run{Run: api.Run{ID: "r-live", Status: api.RunStatusRunning, PID: 4242,
		Launch:  Launch{Model: "m.gguf", Name: "m", Port: 8091},
		Command: []string{"llama-server", "--model", "m.gguf"}, Started: time.Now().UTC()}}
	a.mu.Lock()
	err := a.recordRunLocked(run)
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	live, err := store.RunHistory(a.db, 10)
	if err != nil || len(live) != 1 || !live[0].Live {
		t.Fatalf("launch must record a live row: %+v (%v)", live, err)
	}
	a.Close()

	b := appOnData(t, filepath.Dir(data), data)
	defer b.Close()
	history, err := store.RunHistory(b.db, 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %+v (%v)", history, err)
	}
	row := history[0]
	if row.Live || row.Status != "stopped" || row.Ended == nil {
		t.Fatalf("stale live row not demoted: %+v", row)
	}
	if !strings.Contains(row.Error, "no longer supervised") {
		t.Fatalf("demoted row lacks explanation: %q", row.Error)
	}
	var spec Launch
	if err = json.Unmarshal([]byte(row.LaunchJSON), &spec); err != nil {
		t.Fatalf("stored launch not valid JSON: %v", err)
	}
	if spec.Model != "m.gguf" || spec.Port != 8091 {
		t.Fatalf("launch payload = %+v", spec)
	}
}
