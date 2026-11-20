package desk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ekelhala/opentambo/internal/desk/store"
)

// Legacy per-file cache state (policy.json + index.json) is imported into
// the database exactly once; the originals are kept aside, never deleted.

func writeLegacyIndex(t *testing.T, dir string, index cacheIndex) {
	t.Helper()
	contents, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "index.json"), contents, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyCacheImportAdoptsPolicyAndFiltersEntries(t *testing.T) {
	db := cacheTestDB(t)
	dir := filepath.Join(t.TempDir(), "ns")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	policy := `{"max_bytes": 4096, "max_entry_bytes": 2048, "ttl_seconds": 60}`
	if err := os.WriteFile(filepath.Join(dir, "policy.json"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	recent := time.Now().UTC().Add(-time.Second)
	good := &CacheEntry{ID: "good", Namespace: "ns", Filename: "good.bin",
		Checksum: "abc", PromptTokens: []int32{1, 2}, Bytes: 7, CreatedAt: recent, LastUsedAt: recent}
	noChecksum := &CacheEntry{ID: "no-checksum", Namespace: "ns", Filename: "sneaky.bin", PromptTokens: []int32{3}, Bytes: 1}
	mismatched := &CacheEntry{ID: "other-key", Namespace: "ns", Filename: "mismatch.bin",
		Checksum: "def", PromptTokens: []int32{4}, Bytes: 1}
	writeLegacyIndex(t, dir, cacheIndex{Version: cacheIndexVersion, Entries: map[string]*CacheEntry{
		"good": good, "no-checksum": noChecksum, "stale-key": mismatched,
	}})
	if err := os.WriteFile(filepath.Join(dir, "good.bin"), []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}

	opened, err := observeCacheStore(db, dir, "ns")
	if err != nil {
		t.Fatal(err)
	}
	// A listing adopts the imported policy instead of guessed defaults.
	if opened.maxBytes != 4096 || opened.maxEntryBytes != 2048 || opened.ttl != secondsDuration(60) {
		t.Fatalf("adopted limits = %d/%d/%v, want 4096/2048/60s", opened.maxBytes, opened.maxEntryBytes, opened.ttl)
	}
	names := map[string]bool{}
	for _, c := range opened.Candidates() {
		names[c.ID] = true
	}
	if !names["good"] || names["no-checksum"] || names["stale-key"] {
		t.Fatalf("imported entries = %v", names)
	}
	persisted, found, err := store.Namespace(db, opened.dir)
	if err != nil || !found {
		t.Fatalf("namespace row after import: %v %v", found, err)
	}
	if !persisted.HasPolicy || persisted.MaxBytes != 4096 {
		t.Fatalf("namespace row = %+v, want persisted policy", persisted)
	}
	for _, name := range []string{"index.json", "policy.json"} {
		if _, err = os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s still in place after import", name)
		}
		if _, err = os.Stat(filepath.Join(dir, name+".migrated")); err != nil {
			t.Fatalf("%s not kept aside after import: %v", name, err)
		}
	}
}

func TestLegacyCacheImportRejectsUnusableIndexes(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, dir string)
		wantErr string
	}{
		{
			name: "corrupt json",
			prepare: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte("{oops"), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "import legacy cache index",
		},
		{
			name: "unsupported version",
			prepare: func(t *testing.T, dir string) {
				writeLegacyIndex(t, dir, cacheIndex{Version: 99})
			},
			wantErr: "unsupported legacy cache index version 99",
		},
		{
			name: "unreadable index",
			prepare: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, "index.json"), 0700); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "ns")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			c.prepare(t, dir)
			_, err := newCacheStore(cacheTestDB(t), dir, "ns", 1<<20, 1<<19, 0)
			if err == nil {
				t.Fatal("expected import failure")
			}
			if c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, c.wantErr)
			}
		})
	}
}

func TestSnapshotEntryConversionRejectsUnusableValues(t *testing.T) {
	valid := func() *CacheEntry {
		return &CacheEntry{ID: "e", Filename: "e.bin", Checksum: "abc", PromptTokens: []int32{1}}
	}
	if _, ok := snapshotFromEntry(nil); ok {
		t.Fatal("nil entry converted")
	}
	if _, ok := snapshotFromEntry(&CacheEntry{ID: "e", Filename: "../escape.bin", Checksum: "a", PromptTokens: []int32{1}}); ok {
		t.Fatal("path-escaping filename converted")
	}
	noChecksum := valid()
	noChecksum.Checksum = ""
	if _, ok := snapshotFromEntry(noChecksum); ok {
		t.Fatal("checksumless entry converted")
	}
	noTokens := valid()
	noTokens.PromptTokens = nil
	if _, ok := snapshotFromEntry(noTokens); ok {
		t.Fatal("tokenless entry converted")
	}

	rows := []store.Snapshot{
		{Filename: "x.bin", Checksum: "abc", PromptTokensJSON: "[1]"},
		{ID: "x", Filename: "../x.bin", Checksum: "abc", PromptTokensJSON: "[1]"},
		{ID: "x", Filename: "x.bin", Checksum: "", PromptTokensJSON: "[1]"},
		{ID: "x", Filename: "x.bin", Checksum: "abc", PromptTokensJSON: "not json"},
		{ID: "x", Filename: "x.bin", Checksum: "abc", PromptTokensJSON: "[]"},
	}
	for i, row := range rows {
		if _, ok := entryFromSnapshot("ns", row); ok {
			t.Fatalf("row %d converted despite invalid data: %+v", i, row)
		}
	}
	entry, ok := entryFromSnapshot("ns", store.Snapshot{
		ID: "x", Filename: "x.bin", Checksum: "abc", PromptTokensJSON: "[7,8]",
	})
	if !ok || entry.ID != "x" || len(entry.PromptTokens) != 2 || entry.Namespace != "ns" {
		t.Fatalf("valid row rejected or mangled: %+v (%v)", entry, ok)
	}
}

func TestMustJSONFallsBackOnUnencodableValues(t *testing.T) {
	if got := mustJSON(make(chan int)); got != "null" {
		t.Fatalf("mustJSON(chan) = %q, want null", got)
	}
	if got := mustJSON([]string{"a"}); got != `["a"]` {
		t.Fatalf("mustJSON([]) = %q", got)
	}
}

func TestCacheStoreOpenFailsWhenDatabaseBroken(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), store.DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "ns")
	if _, err = newCacheStore(db, dir, "ns", 1<<20, 1<<19, 0); err == nil {
		t.Fatal("opening a namespace over a closed database succeeded")
	}
}

func TestCommitSkipsEntriesTheRowCannotDescribe(t *testing.T) {
	db := cacheTestDB(t)
	dir := filepath.Join(t.TempDir(), "ns")
	opened, err := newCacheStore(db, dir, "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	opened.mu.Lock()
	opened.entries["ghost"] = &CacheEntry{ID: "ghost", Filename: "../escape.bin",
		Checksum: "abc", PromptTokens: []int32{1}}
	err = opened.commitLocked()
	opened.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.SnapshotsInDir(db, opened.dir)
	if err != nil || len(rows) != 0 {
		t.Fatalf("unrepresentable entry reached the database: %+v (%v)", rows, err)
	}
}

func TestLaunchFromProfileRejectsCorruptDocuments(t *testing.T) {
	if _, err := launchFromProfile(store.Profile{ArgsJSON: "{oops"}); err == nil ||
		!strings.Contains(err.Error(), "launch args") {
		t.Fatalf("corrupt args = %v", err)
	}
	if _, err := launchFromProfile(store.Profile{ArgsJSON: "[]", EnvJSON: "nope"}); err == nil ||
		!strings.Contains(err.Error(), "launch environment") {
		t.Fatalf("corrupt env = %v", err)
	}
	_, err := launchFromProfile(store.Profile{ArgsJSON: "[]", EnvJSON: "{}", PromptCacheJSON: "nope"})
	if err == nil || !strings.Contains(err.Error(), "prompt cache spec") {
		t.Fatalf("corrupt prompt cache = %v", err)
	}
}
