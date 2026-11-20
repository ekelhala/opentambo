package store

import (
	"database/sql"
	"errors"
	"time"
)

// Snapshot publication states. Blobs are files under the namespace
// directory; the row is the committed metadata about one.
const (
	// SnapshotPublishing: metadata recorded before the file was renamed
	// into place; interrupted rows are resolved during recovery.
	SnapshotPublishing = "publishing"
	// SnapshotCommitted: file exists at filename and its checksum was
	// verified when it was published.
	SnapshotCommitted = "committed"
	// SnapshotDeleting: the file removal was announced; recovery finishes
	// the deletion (file gone -> row gone).
	SnapshotDeleting = "deleting"
)

// CacheNamespace is a prompt-cache namespace with its retained storage
// policy. Policy is durable and never rewritten by read-only listings.
type CacheNamespace struct {
	Dir           string
	Namespace     string
	MaxBytes      int64
	MaxEntryBytes int64
	TTLSeconds    int64
	// HasPolicy marks limits that were explicitly persisted (a launch or
	// an imported policy file). Observed namespaces carry identity only.
	HasPolicy bool
}

// Snapshot is metadata for one KV snapshot file. PromptTokensJSON is the
// verified input prefix (conversation data: private, hence the 0600 db).
type Snapshot struct {
	ID               string
	NamespaceDir     string
	Filename         string
	Checksum         string
	PromptTokensJSON string
	SavedTokens      int
	Bytes            int64
	CreatedAt        time.Time
	LastUsedAt       time.Time
	State            string
}

// SaveNamespace upserts a namespace row. The policy columns describe the
// caller's current limits; HasPolicy marks whether they were explicitly
// persisted, so an observed namespace never overwrites a real policy with
// defaults by accident.
func SaveNamespace(db *sql.DB, ns CacheNamespace) error {
	hasPolicy := 0
	if ns.HasPolicy {
		hasPolicy = 1
	}
	_, err := db.Exec(`INSERT INTO cache_namespaces (dir, namespace, max_bytes, max_entry_bytes, ttl_seconds, has_policy)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (dir) DO UPDATE SET
			namespace = excluded.namespace, max_bytes = excluded.max_bytes,
			max_entry_bytes = excluded.max_entry_bytes, ttl_seconds = excluded.ttl_seconds,
			has_policy = excluded.has_policy`,
		ns.Dir, ns.Namespace, ns.MaxBytes, ns.MaxEntryBytes, ns.TTLSeconds, hasPolicy)
	return err
}

// Namespace reads one namespace row; ok is false when unknown.
func Namespace(db *sql.DB, dir string) (CacheNamespace, bool, error) {
	var (
		ns        CacheNamespace
		hasPolicy int
	)
	err := db.QueryRow(`SELECT dir, namespace, max_bytes, max_entry_bytes, ttl_seconds, has_policy
		FROM cache_namespaces WHERE dir = ?`, dir).
		Scan(&ns.Dir, &ns.Namespace, &ns.MaxBytes, &ns.MaxEntryBytes, &ns.TTLSeconds, &hasPolicy)
	if errors.Is(err, sql.ErrNoRows) {
		return CacheNamespace{}, false, nil
	}
	if err != nil {
		return CacheNamespace{}, false, err
	}
	ns.HasPolicy = hasPolicy == 1
	return ns, true, nil
}

// Namespaces lists every persisted namespace ordered by directory.
func Namespaces(db *sql.DB) ([]CacheNamespace, error) {
	rows, err := db.Query(`SELECT dir, namespace, max_bytes, max_entry_bytes, ttl_seconds, has_policy
		FROM cache_namespaces ORDER BY dir`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CacheNamespace
	for rows.Next() {
		var (
			ns        CacheNamespace
			hasPolicy int
		)
		if err = rows.Scan(&ns.Dir, &ns.Namespace, &ns.MaxBytes, &ns.MaxEntryBytes, &ns.TTLSeconds, &hasPolicy); err != nil {
			return nil, err
		}
		ns.HasPolicy = hasPolicy == 1
		out = append(out, ns)
	}
	return out, rows.Err()
}

// ImportNamespace records the namespace row and its snapshot rows in one
// transaction: the legacy file import commits complete or not at all.
// Existing snapshot rows for the directory are replaced, so a retried
// import never duplicates.
func ImportNamespace(db *sql.DB, ns CacheNamespace, snapshots []Snapshot) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	hasPolicy := 0
	if ns.HasPolicy {
		hasPolicy = 1
	}
	if _, err = tx.Exec(`INSERT INTO cache_namespaces (dir, namespace, max_bytes, max_entry_bytes, ttl_seconds, has_policy)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (dir) DO UPDATE SET
			namespace = excluded.namespace, max_bytes = excluded.max_bytes,
			max_entry_bytes = excluded.max_entry_bytes, ttl_seconds = excluded.ttl_seconds,
			has_policy = excluded.has_policy`,
		ns.Dir, ns.Namespace, ns.MaxBytes, ns.MaxEntryBytes, ns.TTLSeconds, hasPolicy); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM cache_snapshots WHERE ns_dir = ?`, ns.Dir); err != nil {
		return err
	}
	for _, s := range snapshots {
		s.NamespaceDir = ns.Dir
		if err = insertSnapshotTx(tx, s, SnapshotCommitted); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReplaceSnapshots atomically rewrites every snapshot row of one namespace
// with the committed set the caller supplies: rows absent from the
// argument are removed. This is the cache store's durable commit point;
// blob files themselves stay on disk.
func ReplaceSnapshots(db *sql.DB, dir string, snapshots []Snapshot) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`DELETE FROM cache_snapshots WHERE ns_dir = ?`, dir); err != nil {
		return err
	}
	for _, s := range snapshots {
		s.NamespaceDir = dir
		if err = insertSnapshotTx(tx, s, SnapshotCommitted); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// InsertPublishing records snapshot metadata before the blob rename so an
// interrupted publication is visible to recovery.
func InsertPublishing(db *sql.DB, s Snapshot) error {
	return insertSnapshot(db, s, SnapshotPublishing)
}

// CommitSnapshot transitions a publishing row to committed (or inserts an
// already-published file, used when importing legacy index.json rows).
func CommitSnapshot(db *sql.DB, s Snapshot) error {
	return insertSnapshot(db, s, SnapshotCommitted)
}

func insertSnapshot(db *sql.DB, s Snapshot, state string) error {
	return insertSnapshotTx(db, s, state)
}

// insertSnapshotTx works with *sql.DB and *sql.Tx alike so batch writers
// can commit whole snapshot sets in one transaction.
func insertSnapshotTx(db execer, s Snapshot, state string) error {
	_, err := db.Exec(`INSERT INTO cache_snapshots
		(id, ns_dir, filename, checksum, prompt_tokens_json, saved_tokens, bytes, created_at, last_used_at, state)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			filename = excluded.filename, checksum = excluded.checksum,
			prompt_tokens_json = excluded.prompt_tokens_json, saved_tokens = excluded.saved_tokens,
			bytes = excluded.bytes, created_at = excluded.created_at,
			last_used_at = excluded.last_used_at, state = excluded.state`,
		s.ID, s.NamespaceDir, s.Filename, s.Checksum, s.PromptTokensJSON, s.SavedTokens, s.Bytes,
		encodeTime(s.CreatedAt), encodeTime(s.LastUsedAt), state)
	return err
}

// execer is satisfied by *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// SnapshotsInDir returns every snapshot row of one namespace regardless of
// state; file recovery reconciles the directory against this set.
func SnapshotsInDir(db *sql.DB, dir string) ([]Snapshot, error) {
	return snapshotsWhere(db, `ns_dir = ?`, dir)
}

// Snapshots returns the committed snapshots of one namespace ordered by
// creation (oldest first, matching the legacy index order).
func Snapshots(db *sql.DB, dir string) ([]Snapshot, error) {
	return snapshotsWhere(db, `ns_dir = ? AND state = ?`, dir, SnapshotCommitted)
}

// AllSnapshots returns every row in every state; recovery walks this to
// reconcile with the filesystem.
func AllSnapshots(db *sql.DB) ([]Snapshot, error) {
	return snapshotsWhere(db, `1=1`)
}

func snapshotsWhere(db *sql.DB, where string, args ...any) ([]Snapshot, error) {
	rows, err := db.Query(`SELECT id, ns_dir, filename, checksum, prompt_tokens_json, saved_tokens,
		bytes, created_at, last_used_at, state FROM cache_snapshots WHERE `+where+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		var (
			s                   Snapshot
			createdAt, lastUsed string
		)
		if err = rows.Scan(&s.ID, &s.NamespaceDir, &s.Filename, &s.Checksum, &s.PromptTokensJSON,
			&s.SavedTokens, &s.Bytes, &createdAt, &lastUsed, &s.State); err != nil {
			return nil, err
		}
		if s.CreatedAt, err = decodeTime(createdAt); err != nil {
			return nil, err
		}
		if s.LastUsedAt, err = decodeTime(lastUsed); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MarkSnapshotDeleting announces a pending file removal.
func MarkSnapshotDeleting(db *sql.DB, id string) error {
	_, err := db.Exec(`UPDATE cache_snapshots SET state = ? WHERE id = ?`, SnapshotDeleting, id)
	return err
}

// DeleteSnapshots removes rows by id inside an optional transaction;
// recovery and pruning batch their row deletions through here.
func DeleteSnapshots(db *sql.DB, ids ...string) error {
	for _, id := range ids {
		if _, err := db.Exec(`DELETE FROM cache_snapshots WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// UpdateSnapshotTouch records a use timestamp for accounting.
func UpdateSnapshotTouch(db *sql.DB, id string, when time.Time) error {
	_, err := db.Exec(`UPDATE cache_snapshots SET last_used_at = ? WHERE id = ?`, encodeTime(when), id)
	return err
}
