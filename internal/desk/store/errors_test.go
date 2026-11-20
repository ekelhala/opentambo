package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// closedStoreDB returns a fully migrated database handle that is already
// closed: every call against it fails, which is the cheapest honest way
// to exercise the error-propagation branches of the store API.
func closedStoreDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), DBFileName))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err = Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err = db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return db
}

func TestClosedStorePropagatesErrors(t *testing.T) {
	db := closedStoreDB(t)
	when := time.Now().UTC()
	cases := []struct {
		name string
		call func() error
	}{
		{"Migrate", func() error { return Migrate(db) }},
		{"SetMeta", func() error { return SetMeta(db, "k", "v") }},
		{"Meta", func() error { _, _, err := Meta(db, "k"); return err }},
		{"ReplaceDownloads", func() error { return ReplaceDownloads(db, nil) }},
		{"ImportState", func() error { return ImportState(db, "v", nil, nil) }},
		{"ReplaceState", func() error { return ReplaceState(db, nil, nil) }},
		{"Downloads", func() error { _, err := Downloads(db); return err }},
		{"SaveDownload", func() error {
			return SaveDownload(db, Download{ID: "j", Created: when})
		}},
		{"SaveProfile", func() error { return SaveProfile(db, Profile{Name: "p"}) }},
		{"DeleteProfile", func() error { return DeleteProfile(db, "p") }},
		{"Profiles", func() error { _, err := Profiles(db); return err }},
		{"SaveRun", func() error { return SaveRun(db, RunRecord{ID: "r", Started: when}) }},
		{"DemoteLiveRuns", func() error { return DemoteLiveRuns(db, when, "closed") }},
		{"RunHistory", func() error { _, err := RunHistory(db, 5); return err }},
		{"SaveNamespace", func() error { return SaveNamespace(db, CacheNamespace{Dir: "/x"}) }},
		{"Namespace", func() error { _, _, err := Namespace(db, "/x"); return err }},
		{"Namespaces", func() error { _, err := Namespaces(db); return err }},
		{"ImportNamespace", func() error { return ImportNamespace(db, CacheNamespace{Dir: "/x"}, nil) }},
		{"ReplaceSnapshots", func() error { return ReplaceSnapshots(db, "/x", nil) }},
		{"InsertPublishing", func() error {
			return InsertPublishing(db, Snapshot{ID: "s", NamespaceDir: "/x", CreatedAt: when, LastUsedAt: when})
		}},
		{"CommitSnapshot", func() error {
			return CommitSnapshot(db, Snapshot{ID: "s", NamespaceDir: "/x", CreatedAt: when, LastUsedAt: when})
		}},
		{"SnapshotsInDir", func() error { _, err := SnapshotsInDir(db, "/x"); return err }},
		{"Snapshots", func() error { _, err := Snapshots(db, "/x"); return err }},
		{"AllSnapshots", func() error { _, err := AllSnapshots(db); return err }},
		{"MarkSnapshotDeleting", func() error { return MarkSnapshotDeleting(db, "s") }},
		{"DeleteSnapshots", func() error { return DeleteSnapshots(db, "s") }},
		{"UpdateSnapshotTouch", func() error { return UpdateSnapshotTouch(db, "s", when) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call(); err == nil {
				t.Fatalf("%s on a closed database returned no error", c.name)
			}
		})
	}
}

func TestNamespacesListingCarriesPolicyFlags(t *testing.T) {
	_, db := openTestStore(t)
	if err := SaveNamespace(db, CacheNamespace{Dir: "/observed", Namespace: "model-a", MaxBytes: 1, MaxEntryBytes: 1, TTLSeconds: 1}); err != nil {
		t.Fatal(err)
	}
	if err := SaveNamespace(db, CacheNamespace{Dir: "/policied", Namespace: "model-b", MaxBytes: 4096, MaxEntryBytes: 1024, TTLSeconds: 60, HasPolicy: true}); err != nil {
		t.Fatal(err)
	}
	list, err := Namespaces(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("namespaces = %d, want 2", len(list))
	}
	if list[0].Dir != "/observed" || list[0].HasPolicy {
		t.Fatalf("observed namespace = %+v, want HasPolicy=false", list[0])
	}
	if list[1].Dir != "/policied" || !list[1].HasPolicy || list[1].MaxBytes != 4096 || list[1].TTLSeconds != 60 {
		t.Fatalf("policied namespace = %+v", list[1])
	}
}

func TestBrokenSchemaFailsTransactionalWriters(t *testing.T) {
	download := Download{ID: "job-1", URL: "https://example.test/m.gguf", File: "/tmp/m.gguf",
		Status: "downloading", Created: time.Now().UTC()}
	profile := Profile{Name: "p", Model: "/models/m.gguf", Port: 8080}
	snapshot := Snapshot{ID: "snap-1", Filename: "snap-1.bin", Checksum: "abc",
		CreatedAt: time.Now().UTC(), LastUsedAt: time.Now().UTC()}

	cases := []struct {
		name    string
		schema  string
		wantErr string
		call    func(db *sql.DB) error
	}{
		{
			name:    "downloads table missing",
			schema:  `DROP TABLE downloads`,
			wantErr: "no such table: downloads",
			call:    func(db *sql.DB) error { return ReplaceDownloads(db, []Download{download}) },
		},
		{
			name:    "profiles table missing",
			schema:  `DROP TABLE profiles`,
			wantErr: "no such table: profiles",
			call:    func(db *sql.DB) error { return ReplaceState(db, []Download{download}, []Profile{profile}) },
		},
		{
			name:    "meta table missing blocks import marker",
			schema:  `DROP TABLE meta`,
			wantErr: "no such table: meta",
			call:    func(db *sql.DB) error { return ImportState(db, "imported", []Download{download}, []Profile{profile}) },
		},
		{
			name:   "duplicate job ids rejected",
			schema: ``,
			call: func(db *sql.DB) error {
				dup := download
				dup.ID = "job-1"
				dup.URL = "https://example.test/other.gguf"
				return ReplaceDownloads(db, []Download{download, dup})
			},
		},
		{
			name:   "duplicate profile names rejected",
			schema: ``,
			call: func(db *sql.DB) error {
				dup := profile
				dup.Port = 8081
				return ReplaceState(db, nil, []Profile{profile, dup})
			},
		},
		{
			name:   "import state stops at duplicate jobs",
			schema: ``,
			call: func(db *sql.DB) error {
				dup := download
				return ImportState(db, "imported", []Download{download, dup}, nil)
			},
		},
		{
			name:   "import state stops at duplicate profiles",
			schema: ``,
			call: func(db *sql.DB) error {
				dup := profile
				return ImportState(db, "imported", nil, []Profile{profile, dup})
			},
		},
		{
			name:    "snapshot rows missing blocks namespace import",
			schema:  `DROP TABLE cache_snapshots`,
			wantErr: "no such table: cache_snapshots",
			call: func(db *sql.DB) error {
				return ImportNamespace(db, CacheNamespace{Dir: "/ns", Namespace: "m"}, []Snapshot{snapshot})
			},
		},
		{
			name:    "broken snapshot column aborts namespace import",
			schema:  `ALTER TABLE cache_snapshots DROP COLUMN saved_tokens`,
			wantErr: "saved_tokens",
			call: func(db *sql.DB) error {
				return ImportNamespace(db, CacheNamespace{Dir: "/ns", Namespace: "m", HasPolicy: true}, []Snapshot{snapshot})
			},
		},
		{
			name:    "broken snapshot column aborts replace",
			schema:  `ALTER TABLE cache_snapshots DROP COLUMN saved_tokens`,
			wantErr: "saved_tokens",
			call:    func(db *sql.DB) error { return ReplaceSnapshots(db, "/ns", []Snapshot{snapshot}) },
		},
		{
			name:    "snapshot delete failure surfaces",
			schema:  `DROP TABLE cache_snapshots`,
			wantErr: "no such table: cache_snapshots",
			call:    func(db *sql.DB) error { return DeleteSnapshots(db, "snap-1") },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, db := openTestStore(t)
			if c.schema != "" {
				if _, err := db.Exec(c.schema); err != nil {
					t.Fatalf("break schema: %v", err)
				}
			}
			err := c.call(db)
			if err == nil {
				t.Fatal("expected failure, got none")
			}
			if c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, c.wantErr)
			}
		})
	}
}

func TestCorruptedTimestampsFailReads(t *testing.T) {
	t.Run("downloads", func(t *testing.T) {
		_, db := openTestStore(t)
		if _, err := db.Exec(`INSERT INTO downloads (id, url, file, status, created)
			VALUES ('j', 'https://example.test/m.gguf', '/tmp/m.gguf', 'done', 'not-a-timestamp')`); err != nil {
			t.Fatal(err)
		}
		if _, err := Downloads(db); err == nil || !strings.Contains(err.Error(), "not-a-timestamp") {
			t.Fatalf("Downloads with corrupt created = %v", err)
		}
	})
	t.Run("runs started", func(t *testing.T) {
		_, db := openTestStore(t)
		if _, err := db.Exec(`INSERT INTO runs (id, status, model, name, port, launch_json, started)
			VALUES ('r', 'stopped', '/m.gguf', 'r', 1, '{}', 'garbage')`); err != nil {
			t.Fatal(err)
		}
		if _, err := RunHistory(db, 10); err == nil || !strings.Contains(err.Error(), "garbage") {
			t.Fatalf("RunHistory with corrupt started = %v", err)
		}
	})
	t.Run("runs ended", func(t *testing.T) {
		_, db := openTestStore(t)
		if _, err := db.Exec(`INSERT INTO runs (id, status, model, name, port, launch_json, started, ended)
			VALUES ('r', 'stopped', '/m.gguf', 'r', 1, '{}', '2026-01-01T00:00:00Z', 'garbage')`); err != nil {
			t.Fatal(err)
		}
		if _, err := RunHistory(db, 10); err == nil || !strings.Contains(err.Error(), "garbage") {
			t.Fatalf("RunHistory with corrupt ended = %v", err)
		}
	})
	t.Run("snapshots", func(t *testing.T) {
		_, db := openTestStore(t)
		if err := SaveNamespace(db, CacheNamespace{Dir: "/ns", Namespace: "m"}); err != nil {
			t.Fatal(err)
		}
		insert := func(id, created, used string) {
			t.Helper()
			if _, err := db.Exec(`INSERT INTO cache_snapshots
				(id, ns_dir, filename, checksum, created_at, last_used_at)
				VALUES (?, '/ns', ?, 'abc', ?, ?)`, id, id+".bin", created, used); err != nil {
				t.Fatal(err)
			}
		}
		insert("bad-created", "garbage", "2026-01-01T00:00:00Z")
		if _, err := SnapshotsInDir(db, "/ns"); err == nil || !strings.Contains(err.Error(), "garbage") {
			t.Fatalf("SnapshotsInDir with corrupt created_at = %v", err)
		}
		if _, err := db.Exec(`DELETE FROM cache_snapshots`); err != nil {
			t.Fatal(err)
		}
		insert("bad-used", "2026-01-01T00:00:00Z", "garbage")
		if _, err := SnapshotsInDir(db, "/ns"); err == nil || !strings.Contains(err.Error(), "garbage") {
			t.Fatalf("SnapshotsInDir with corrupt last_used_at = %v", err)
		}
	})
}

func TestOpenRejectsUnusablePaths(t *testing.T) {
	t.Run("parent is a file", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(filepath.Join(blocker, DBFileName)); err == nil {
			t.Fatal("Open through a file parent returned no error")
		}
	})
	t.Run("path is a directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), DBFileName)
		if err := os.Mkdir(dir, 0750); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); err == nil {
			t.Fatal("Open on a directory returned no error")
		}
	})
}

func TestHardenSidecarsReportsPermissionErrors(t *testing.T) {
	base := t.TempDir()
	locked := filepath.Join(base, "locked")
	if err := os.Mkdir(locked, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(locked, DBFileName)
	if err := os.WriteFile(path, []byte("db"), 0600); err != nil {
		t.Fatal(err)
	}
	// No search permission on the parent: chmod on the sidecar path fails
	// with something other than "not exist", which must surface.
	if err := os.Chmod(locked, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0755) })
	if err := HardenSidecars(path); err == nil {
		t.Fatal("HardenSidecars over an inaccessible directory returned no error")
	}
}

func TestMigrateFailedApplyRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), DBFileName)
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	// A conflicting table makes v1 fail mid-migration.
	if _, err = db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	err = Migrate(db)
	if err == nil || !strings.Contains(err.Error(), "schema migration 1") {
		t.Fatalf("Migrate with conflicting table = %v", err)
	}
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 0 {
		t.Fatalf("user_version after failed migration = %d (err %v), want 0", version, err)
	}
	// With the conflict removed, migration completes; nothing half-applied
	// blocks the retry.
	if _, err = db.Exec(`DROP TABLE meta`); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db); err != nil {
		t.Fatalf("retry migrate: %v", err)
	}
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentSchemaVersion {
		t.Fatalf("user_version after retry = %d (err %v), want %d", version, err, currentSchemaVersion)
	}
}
