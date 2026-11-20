package store

import (
	"database/sql"
	"fmt"
)

// Schema versioning uses SQLite's PRAGMA user_version. Each migration
// applies exactly once inside its own transaction; a crash mid-migration
// rolls that version back and the next startup retries it. A database
// newer than this binary is rejected outright rather than silently
// downgraded.

const currentSchemaVersion = 1

type migration struct {
	version int
	apply   func(tx *sql.Tx) error
}

var migrations = []migration{{version: 1, apply: applyV1}}

// Migrate brings the database schema up to currentSchemaVersion.
func Migrate(db *sql.DB) error {
	version, err := userVersion(db)
	if err != nil {
		return err
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than this OpenTambo build supports (%d); upgrade OpenTambo before opening this data directory", version, currentSchemaVersion)
	}
	for _, m := range migrations {
		if m.version <= version || m.version > currentSchemaVersion {
			continue
		}
		if err = migrateOne(db, m); err != nil {
			return fmt.Errorf("schema migration %d: %w", m.version, err)
		}
	}
	return nil
}

func migrateOne(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = m.apply(tx); err != nil {
		return err
	}
	if _, err = tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return err
	}
	return tx.Commit()
}

func userVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

const schemaV1 = `
CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

-- Download jobs: destination, validator for resume, bounded progress
-- checkpoint (bytes + rate are rewritten periodically, not per event).
CREATE TABLE downloads (
	id               TEXT PRIMARY KEY,
	url              TEXT NOT NULL,
	file             TEXT NOT NULL,
	status           TEXT NOT NULL,
	bytes            INTEGER NOT NULL DEFAULT 0,
	total            INTEGER NOT NULL DEFAULT -1,
	bytes_per_second REAL NOT NULL DEFAULT 0,
	etag             TEXT NOT NULL DEFAULT '',
	error            TEXT NOT NULL DEFAULT '',
	created          TEXT NOT NULL
);

-- Saved launch profiles. args/env/prompt_cache are JSON documents;
-- scalars are columns so queries never parse payloads. env may contain
-- secrets: the 0600 database file is the protection boundary.
CREATE TABLE profiles (
	name              TEXT PRIMARY KEY,
	model             TEXT NOT NULL,
	alias             TEXT NOT NULL DEFAULT '',
	port              INTEGER NOT NULL,
	context           INTEGER NOT NULL,
	gpu_layers        INTEGER NOT NULL,
	args_json         TEXT NOT NULL DEFAULT '[]',
	env_json          TEXT NOT NULL DEFAULT '{}',
	prompt_cache_json TEXT
);

-- Run history. live=1 means a process this manager currently believes it
-- supervises; startup demotes live rows (a recorded PID is never proof).
CREATE TABLE runs (
	id           TEXT PRIMARY KEY,
	status       TEXT NOT NULL,
	model        TEXT NOT NULL,
	name         TEXT NOT NULL,
	port         INTEGER NOT NULL,
	pid          INTEGER NOT NULL DEFAULT 0,
	command_json TEXT NOT NULL DEFAULT '[]',
	launch_json  TEXT NOT NULL,
	started      TEXT NOT NULL,
	ended        TEXT,
	error        TEXT NOT NULL DEFAULT '',
	live         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX runs_started_idx ON runs (started DESC);

-- Prompt-cache namespaces: retained storage policy, never rewritten by
-- read-only listings. dir is the namespace directory absolute path.
CREATE TABLE cache_namespaces (
	dir             TEXT PRIMARY KEY,
	namespace       TEXT NOT NULL,
	max_bytes       INTEGER NOT NULL,
	max_entry_bytes INTEGER NOT NULL,
	ttl_seconds     INTEGER NOT NULL
);

-- Snapshot metadata: checksums, verified prompt prefix, accounting.
-- state tracks the publication protocol: 'publishing' rows interrupted
-- by a crash are resolved on restart; blobs live as files under dir.
CREATE TABLE cache_snapshots (
	id                 TEXT PRIMARY KEY,
	ns_dir             TEXT NOT NULL REFERENCES cache_namespaces (dir) ON DELETE CASCADE,
	filename           TEXT NOT NULL,
	checksum           TEXT NOT NULL,
	prompt_tokens_json TEXT NOT NULL DEFAULT '[]',
	saved_tokens       INTEGER NOT NULL DEFAULT 0,
	bytes              INTEGER NOT NULL DEFAULT 0,
	created_at         TEXT NOT NULL,
	last_used_at       TEXT NOT NULL,
	state              TEXT NOT NULL DEFAULT 'committed'
);
CREATE INDEX cache_snapshots_ns_idx ON cache_snapshots (ns_dir);
`

func applyV1(tx *sql.Tx) error {
	_, err := tx.Exec(schemaV1)
	return err
}
