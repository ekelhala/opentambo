// Package store is OpenTambo's embedded persistence layer. It owns the
// SQLite database under the data directory (opentambo.db) and is the
// authoritative home for durable structured state: download jobs with
// resume checkpoints, saved launch profiles, managed run history, and
// prompt-cache namespace policies with snapshot metadata.
//
// Design rules:
//   - Database schemas stay separate from HTTP wire types; callers in
//     internal/desk translate between the two.
//   - The driver is modernc.org/sqlite (pure Go): release binaries remain
//     CGO_ENABLED=0 and installations need no sqlite3 executable or any
//     external database service.
//   - The database and every journal/WAL sidecar are 0600: profiles can
//     carry environment secrets and snapshot rows describe conversation
//     prefixes.
//   - WAL plus a busy timeout let short-lived transactions from the HTTP
//     handlers and background workers interleave without holding the
//     application mutex.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // embedded SQLite driver, registered as "sqlite"
)

const (
	// DBFileName is the database file inside the data directory.
	DBFileName = "opentambo.db"
	// busyTimeoutMs bounds how long a writer waits for another writer
	// before failing; local single-manager contention is milliseconds.
	busyTimeoutMs = 5000
)

// Open opens (creating when missing) the database at path with durability
// and privacy settings suited to local service storage. Journal mode is
// WAL so readers never block the writer; synchronous=NORMAL is the
// recommended WAL pairing (committed transactions survive process crashes;
// only an OS/power loss right after commit can lose the last WAL tail,
// which recovery tolerates). A single connection serialises writers.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	// Pre-create the file 0600: the driver's own create respects the
	// existing file, and sidecars are tightened right after opening.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)",
		path, busyTimeoutMs)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err = HardenSidecars(path); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// HardenSidecars restricts the WAL and shared-memory files SQLite creates
// next to the database to owner read/write. Missing sidecars are fine:
// they exist only while a connection is open (and -shm alongside them).
func HardenSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		side := path + suffix
		if err := os.Chmod(side, 0600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
