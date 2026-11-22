package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), DBFileName)
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err = Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return path, db
}

func TestOpenCreatesPrivateDatabase(t *testing.T) {
	path, db := openTestStore(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("database permissions = %o, want 0600", perm)
	}
	var mode string
	if err = db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %s, want wal", mode)
	}
	// Reopening is fine and sidecars stay private while WAL exists.
	if err = HardenSidecars(path); err != nil {
		t.Fatalf("harden with live sidecars: %v", err)
	}
}

func TestMigrateIsIdempotentAndRejectsNewerSchema(t *testing.T) {
	_, db := openTestStore(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != currentSchemaVersion {
		t.Fatalf("user_version = %d (err %v), want %d", v, err, currentSchemaVersion)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	err := Migrate(db)
	if err == nil || !strings.Contains(err.Error(), "newer than") {
		t.Fatalf("migrate on newer schema = %v, want unsupported-version error", err)
	}
}

func TestDownloadsRoundTripAndCheckpoint(t *testing.T) {
	_, db := openTestStore(t)
	created := time.Now().UTC().Truncate(time.Millisecond)
	jobs := []Download{
		{ID: "a", URL: "https://x/a.gguf", File: "a.gguf", Status: "downloading", Bytes: 10, Total: 100, ETag: `"e1"`, Created: created},
		{ID: "b", URL: "https://x/b.gguf", File: "sub/b.gguf", Status: "completed", Bytes: 50, Total: 50, Created: created.Add(time.Second)},
	}
	if err := ReplaceDownloads(db, jobs); err != nil {
		t.Fatal(err)
	}
	// Bounded checkpoint: only job b's progress row is rewritten.
	jobs[1].Bytes = 51
	jobs[1].BytesPerSecond = 1234.5
	if err := SaveDownload(db, jobs[1]); err != nil {
		t.Fatal(err)
	}
	if err := DeleteDownload(db, "a"); err != nil {
		t.Fatal(err)
	}
	got, err := Downloads(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "b" || got[0].Bytes != 51 || got[0].BytesPerSecond != 1234.5 {
		t.Fatalf("downloads = %+v", got)
	}
	if !got[0].Created.Equal(created.Add(time.Second)) {
		t.Fatalf("created round trip = %v", got[0].Created)
	}
}

func TestProfilesRoundTripAndDelete(t *testing.T) {
	_, db := openTestStore(t)
	p := Profile{Name: "smol", Model: "m.gguf", Port: 8091, Context: 4096, GPULayers: -1,
		ArgsJSON: `["--flash-attn"]`, EnvJSON: `{"TOKEN":"secret"}`, PromptCacheJSON: `{"enabled":true}`}
	if err := SaveProfile(db, p); err != nil {
		t.Fatal(err)
	}
	p.Port = 8092
	if err := SaveProfile(db, p); err != nil {
		t.Fatal(err)
	}
	got, err := Profiles(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Port != 8092 || got[0].EnvJSON != `{"TOKEN":"secret"}` || got[0].PromptCacheJSON == "" {
		t.Fatalf("profiles = %+v", got)
	}
	if err = DeleteProfile(db, "smol"); err != nil {
		t.Fatal(err)
	}
	if err = DeleteProfile(db, "missing"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if got, err = Profiles(db); err != nil || len(got) != 0 {
		t.Fatalf("after delete = %+v (%v)", got, err)
	}
}

func TestRunHistorySurvivesDemotion(t *testing.T) {
	_, db := openTestStore(t)
	started := time.Now().UTC().Truncate(time.Millisecond)
	live := RunRecord{ID: "r1", Status: "running", Model: "m.gguf", Name: "m", Port: 8091, PID: 4242,
		CommandJSON: `["llama-server"]`, LaunchJSON: `{}`, Started: started, Live: true}
	if err := SaveRun(db, live); err != nil {
		t.Fatal(err)
	}
	when := started.Add(time.Hour)
	if err := DemoteLiveRuns(db, when, "manager restarted"); err != nil {
		t.Fatal(err)
	}
	hist, err := RunHistory(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("history = %+v", hist)
	}
	r := hist[0]
	if r.Live || r.Status != "stopped" || r.Ended == nil || !r.Ended.Equal(when) || r.Error != "manager restarted" {
		t.Fatalf("demoted row = %+v", r)
	}
	// Demotion is a no-op when nothing is live, and never re-dates ended rows.
	if err = DemoteLiveRuns(db, when.Add(time.Hour), "again"); err != nil {
		t.Fatal(err)
	}
	hist, _ = RunHistory(db, 10)
	if !hist[0].Ended.Equal(when) {
		t.Fatalf("ended changed on second demotion: %v", hist[0].Ended)
	}
}

func TestReplaceStateAtomicSwap(t *testing.T) {
	_, db := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	jobs := []Download{{ID: "old", URL: "u", File: "o.gguf", Status: "completed", Total: -1, Created: now}}
	profiles := []Profile{{Name: "old.gguf", Model: "old.gguf", Alias: "old", Port: 8091}}
	if err := ReplaceState(db, jobs, profiles); err != nil {
		t.Fatal(err)
	}
	swapped := []Download{{ID: "new", URL: "u2", File: "n.gguf", Status: "queued", Total: -1, Created: now}}
	if err := ReplaceState(db, swapped, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := Downloads(db); err != nil || len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("downloads after swap = %+v (%v)", got, err)
	}
	if got, err := Profiles(db); err != nil || len(got) != 0 {
		t.Fatalf("profiles after swap = %+v (%v)", got, err)
	}
	// Run history is deliberately untouched by state swaps.
	if err := SaveRun(db, RunRecord{ID: "r", Status: "stopped", Started: now}); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceState(db, nil, nil); err != nil {
		t.Fatal(err)
	}
	if hist, err := RunHistory(db, 5); err != nil || len(hist) != 1 {
		t.Fatalf("run history lost during state swap: %+v (%v)", hist, err)
	}
}

func TestImportStateRecordsMarkerAtomically(t *testing.T) {
	_, db := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	jobs := []Download{{ID: "j1", URL: "u", File: "a.gguf", Status: "completed", Total: 10, Created: now}}
	profiles := []Profile{{Name: "a.gguf", Model: "a.gguf", Alias: "a", Port: 8090}}
	if err := ImportState(db, "3 jobs", jobs, profiles); err != nil {
		t.Fatal(err)
	}
	v, ok, err := Meta(db, "import.state_json")
	if err != nil || !ok || v != "3 jobs" {
		t.Fatalf("import marker = (%q,%v,%v)", v, ok, err)
	}
	if got, _ := Downloads(db); len(got) != 1 || got[0].Status != "completed" {
		t.Fatalf("imported downloads = %+v", got)
	}
	if got, _ := Profiles(db); len(got) != 1 || got[0].Alias != "a" {
		t.Fatalf("imported profiles = %+v", got)
	}
}

func TestMetaMarkers(t *testing.T) {
	_, db := openTestStore(t)
	if _, ok, err := Meta(db, "import.state_json"); err != nil || ok {
		t.Fatalf("missing marker = (ok %v, err %v)", ok, err)
	}
	if err := SetMeta(db, "import.state_json", "done"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := Meta(db, "import.state_json"); err != nil || !ok || v != "done" {
		t.Fatalf("marker = (%q,%v,%v)", v, ok, err)
	}
	if err := SetMeta(db, "import.state_json", "done:3 jobs"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := Meta(db, "import.state_json"); v != "done:3 jobs" {
		t.Fatalf("marker not upserted: %q", v)
	}
}

func TestSnapshotPublicationStates(t *testing.T) {
	_, db := openTestStore(t)
	ns := CacheNamespace{Dir: "/d/prompt-cache/ns", Namespace: "ns", MaxBytes: 1000, MaxEntryBytes: 500}
	if err := SaveNamespace(db, ns); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := Namespace(db, ns.Dir)
	if err != nil || !ok || loaded.MaxBytes != 1000 {
		t.Fatalf("namespace = %+v (%v,%v)", loaded, ok, err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	s := Snapshot{ID: "s1", NamespaceDir: ns.Dir, Filename: "s1.dat", Checksum: "abc",
		PromptTokensJSON: `[1,2,3]`, SavedTokens: 3, Bytes: 90, CreatedAt: now, LastUsedAt: now}
	if err = InsertPublishing(db, s); err != nil {
		t.Fatal(err)
	}
	all, err := AllSnapshots(db)
	if err != nil || len(all) != 1 || all[0].State != SnapshotPublishing {
		t.Fatalf("after publishing insert = %+v (%v)", all, err)
	}
	committed, err := Snapshots(db, ns.Dir)
	if err != nil || len(committed) != 0 {
		t.Fatalf("publishing row must not list as committed: %+v (%v)", committed, err)
	}
	if err = CommitSnapshot(db, s); err != nil {
		t.Fatal(err)
	}
	if err = UpdateSnapshotTouch(db, "s1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	committed, err = Snapshots(db, ns.Dir)
	if err != nil || len(committed) != 1 || committed[0].State != SnapshotCommitted ||
		!committed[0].LastUsedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("committed = %+v (%v)", committed, err)
	}
	if err = MarkSnapshotDeleting(db, "s1"); err != nil {
		t.Fatal(err)
	}
	if err = DeleteSnapshots(db, "s1", "missing-ok"); err != nil {
		t.Fatal(err)
	}
	if all, err = AllSnapshots(db); err != nil || len(all) != 0 {
		t.Fatalf("after delete = %+v (%v)", all, err)
	}
}
