package desk

// Namespaced disk store for prompt snapshots. Snapshots live below
// <data>/prompt-cache/<namespace>/ with private permissions. Durable
// metadata (retained policy, snapshot checksums, verified token prefixes
// and accounting) lives in the embedded SQLite database; blob files stay
// on disk and the database holds their committed rows, rewritten as one
// atomic transaction per mutation. Files are content-verified with SHA-256
// before restore. Token IDs describe private conversation content: the
// database is 0600, entries are never logged, and raw snapshots are never
// served through the API.
//
// Mutations follow one pattern: take the in-process mutex, take the
// cross-process flock, reload the committed rows, apply the change, commit,
// unlock. Crash recovery drops interrupted publications and orphan files.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ekelhala/opentambo/internal/desk/api"
	"github.com/ekelhala/opentambo/internal/desk/store"
)

// cacheIndexVersion and cachePolicyFile describe the pre-SQLite per-file
// formats (index.json / policy.json). They exist only so existing data
// directories import into the database once; new writes are database rows.
const cacheIndexVersion = 1

const cachePolicyFile = "policy.json"

type cachePolicy struct {
	MaxBytes      int64 `json:"max_bytes"`
	MaxEntryBytes int64 `json:"max_entry_bytes"`
	TTLSeconds    int64 `json:"ttl_seconds"`
}

type CacheEntry struct {
	ID           string    `json:"id"`
	Namespace    string    `json:"namespace"`
	Filename     string    `json:"filename"`
	Checksum     string    `json:"checksum"`
	PromptTokens []int32   `json:"prompt_tokens"` // verified input prefix only; never inferred output
	SavedTokens  int       `json:"saved_tokens"`
	Bytes        int64     `json:"bytes"`
	CreatedAt    time.Time `json:"created_at"`
	LastUsedAt   time.Time `json:"last_used_at"`
}

type cacheIndex struct {
	Version int
	Entries map[string]*CacheEntry
}

type cacheStore struct {
	mu            sync.Mutex
	db            *sql.DB
	dir           string
	namespace     string
	maxBytes      int64
	maxEntryBytes int64
	ttl           time.Duration
	entries       map[string]*CacheEntry
	// invalid tracks entries whose backing file vanished or no longer
	// matches the index while a pin held the file in place. They stay out
	// of restore circulation but keep their slot in the index until the
	// final Done runs cleanup (issue #22).
	invalid  map[string]bool
	reserved int64 // bytes from in-flight publishes, counted against the budget
	temps    map[string]*tempReservation
	pins     map[string]int
	now      func() time.Time
}

// tempReservation books budget for a temporary snapshot the caller is about
// to ask llama-server to write. The exact size is unknown beforehand, so the
// reservation uses the store's worst-case bound (TempEstimate). It ends by
// being transferred into committed entry bytes by Publish (success) or by
// removing the write's files and releasing the bytes together
// (AbortTempWrite / maintenance). publishing marks a reservation whose
// snapshot is mid-Publication: maintenance must not lapse it while the
// handoff into a committed entry is underway (issue #24).
type tempReservation struct {
	bytes      int64
	at         time.Time
	publishing bool
}

func newCacheStore(db *sql.DB, dir, namespace string, maxBytes, maxEntryBytes int64, ttl time.Duration) (*cacheStore, error) {
	return openCacheStore(db, dir, namespace, &cachePolicy{MaxBytes: maxBytes, MaxEntryBytes: maxEntryBytes, TTLSeconds: int64(ttl / time.Second)})
}

// observeCacheStore opens a discovered namespace directory for reporting.
// The persisted policy is restored when present; opening never runs
// destructive maintenance, so a listing cannot prune snapshots under guessed
// default limits.
func observeCacheStore(db *sql.DB, dir, namespace string) (*cacheStore, error) {
	s, err := openCacheStore(db, dir, namespace, nil)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// openCacheStore loads a namespace directory. Durable policy and snapshot
// metadata live in the database; blobs stay as files under dir. When policy
// is non-nil it is the authoritative policy (a launch): it is persisted and
// maintenance runs under it immediately. When policy is nil the persisted
// policy is restored if present, and no destructive maintenance runs at
// open. A namespace directory that still carries pre-SQLite policy.json /
// index.json files imports them once, inside one transaction, and keeps the
// originals aside as *.migrated.
func openCacheStore(db *sql.DB, dir, namespace string, policy *cachePolicy) (*cacheStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(resolved, 0700); err != nil {
		return nil, err
	}
	s := &cacheStore{db: db, dir: resolved, namespace: namespace, maxBytes: defaultCacheMaxBytes, maxEntryBytes: defaultCacheMaxEntryBytes, ttl: secondsDuration(defaultCacheTTLSeconds), entries: map[string]*CacheEntry{}, invalid: map[string]bool{}, temps: map[string]*tempReservation{}, pins: map[string]int{}, now: time.Now}
	if err = s.restorePolicy(); err != nil {
		return nil, err
	}
	if policy != nil {
		s.maxBytes, s.maxEntryBytes, s.ttl = policy.MaxBytes, policy.MaxEntryBytes, secondsDuration(policy.TTLSeconds)
	}
	if err = s.recoverFiles(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.loadLocked(); err != nil {
		return nil, err
	}
	if policy != nil {
		if err = s.writePolicyLocked(); err != nil {
			return nil, err
		}
		s.enforceLocked(s.now())
		if err = s.commitLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// restorePolicy adopts the database-persisted limits when present,
// importing a pre-SQLite directory first when the namespace row does not
// exist yet.
func (s *cacheStore) restorePolicy() error {
	persisted, found, err := store.Namespace(s.db, s.dir)
	if err != nil {
		return err
	}
	if !found {
		if err = s.importLegacy(); err != nil {
			return err
		}
		if persisted, found, err = store.Namespace(s.db, s.dir); err != nil {
			return err
		}
	}
	if found && persisted.HasPolicy {
		s.maxBytes, s.maxEntryBytes, s.ttl = persisted.MaxBytes, persisted.MaxEntryBytes, secondsDuration(persisted.TTLSeconds)
	}
	return nil
}

// importLegacy moves a pre-SQLite namespace directory (policy.json +
// index.json) into the database in one transaction and renames the
// originals aside. A directory without legacy files is registered as an
// identity-only row (no policy), so listings keep defaults without ever
// persisting guessed limits as authoritative.
func (s *cacheStore) importLegacy() error {
	ns := store.CacheNamespace{Dir: s.dir, Namespace: s.namespace}
	policy, hadPolicy := readCachePolicy(s.dir)
	if hadPolicy {
		ns.MaxBytes, ns.MaxEntryBytes, ns.TTLSeconds, ns.HasPolicy = policy.MaxBytes, policy.MaxEntryBytes, policy.TTLSeconds, true
	}
	snapshots, err := legacyIndexSnapshots(s.dir)
	if err != nil {
		return err
	}
	if err = store.ImportNamespace(s.db, ns, snapshots); err != nil {
		return fmt.Errorf("import legacy cache namespace: %w", err)
	}
	for _, name := range []string{"index.json", cachePolicyFile} {
		path := filepath.Join(s.dir, name)
		if err = os.Rename(path, path+".migrated"); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// legacyIndexSnapshots decodes a pre-SQLite index.json into committed-ready
// rows, dropping entries the same validation the legacy loader applied
// would have dropped. A missing file yields no rows, not an error.
func legacyIndexSnapshots(dir string) ([]store.Snapshot, error) {
	contents, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var index cacheIndex
	if err = json.Unmarshal(contents, &index); err != nil {
		return nil, fmt.Errorf("import legacy cache index: %w", err)
	}
	if index.Version != cacheIndexVersion {
		return nil, fmt.Errorf("unsupported legacy cache index version %d", index.Version)
	}
	var snapshots []store.Snapshot
	for id, e := range index.Entries {
		if e == nil || e.ID != id || filepath.Base(e.Filename) != e.Filename || e.Checksum == "" || len(e.PromptTokens) == 0 {
			continue
		}
		if snap, ok := snapshotFromEntry(e); ok {
			snapshots = append(snapshots, snap)
		}
	}
	return snapshots, nil
}

func readCachePolicy(dir string) (cachePolicy, bool) {
	contents, err := os.ReadFile(filepath.Join(dir, cachePolicyFile))
	if err != nil {
		return cachePolicy{}, false
	}
	var p cachePolicy
	if err = json.Unmarshal(contents, &p); err != nil {
		return cachePolicy{}, false
	}
	if p.MaxBytes <= 0 || p.MaxEntryBytes <= 0 || p.TTLSeconds < 0 {
		return cachePolicy{}, false
	}
	return p, true
}

// writePolicyLocked persists the maintenance policy as the namespace row.
func (s *cacheStore) writePolicyLocked() error {
	return store.SaveNamespace(s.db, store.CacheNamespace{
		Dir: s.dir, Namespace: s.namespace,
		MaxBytes: s.maxBytes, MaxEntryBytes: s.maxEntryBytes,
		TTLSeconds: int64(s.ttl / time.Second), HasPolicy: true,
	})
}

// applyPolicy updates maintenance limits on an open store and persists them.
// Reopening a namespace with new settings adopts them; when runs share a
// namespace the most recent launch wins deterministically.
func (s *cacheStore) applyPolicy(maxBytes, maxEntryBytes int64, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxBytes, s.maxEntryBytes, s.ttl = maxBytes, maxEntryBytes, ttl
	if err := s.writePolicyLocked(); err != nil {
		return err
	}
	s.enforceLocked(s.now())
	return s.commitLocked()
}

// recoverFiles removes interrupted publications (temp files) and binary
// files that no committed database row references.
func (s *cacheStore) recoverFiles() error {
	keep := map[string]bool{}
	rows, err := store.SnapshotsInDir(s.db, s.dir)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if filepath.Base(row.Filename) == row.Filename {
			keep[row.Filename] = true
		}
	}
	items, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, item := range items {
		name := item.Name()
		if item.IsDir() {
			continue
		}
		if strings.HasSuffix(name, ".tmp") || (strings.HasSuffix(name, ".bin") && !keep[name]) {
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
	return nil
}

// loadLocked replaces the in-memory index from committed database rows,
// dropping entries whose files are missing or have the wrong size (an
// interrupted commit or an externally modified snapshot). A pinned entry is
// never dropped here: a live restore owns its file, so the file stays on
// disk and the entry stays tracked but is marked invalid until cleanup runs
// when the final pin is released (issue #22).
func (s *cacheStore) loadLocked() error {
	entries := map[string]*CacheEntry{}
	invalid := map[string]bool{}
	rows, err := store.Snapshots(s.db, s.dir)
	if err != nil {
		return err
	}
	for _, row := range rows {
		e, ok := entryFromSnapshot(s.namespace, row)
		if !ok {
			continue
		}
		info, err := os.Stat(filepath.Join(s.dir, e.Filename))
		broken := err != nil || !info.Mode().IsRegular() || info.Size() != e.Bytes
		if broken || s.invalid[e.ID] {
			if s.pins[e.ID] > 0 {
				// A live restore owns this file. Keep the entry tracked and
				// carry the invalid mark across reloads: the size check here
				// cannot see a same-size checksum tamper found earlier.
				entries[e.ID] = e
				invalid[e.ID] = true
				continue
			}
			_ = os.Remove(filepath.Join(s.dir, e.Filename))
			continue
		}
		entries[e.ID] = e
	}
	s.entries = entries
	s.invalid = invalid
	return nil
}

// commitLocked rewrites this namespace's committed snapshot rows in one
// transaction: the durable commit point of every mutation.
func (s *cacheStore) commitLocked() error {
	rows := make([]store.Snapshot, 0, len(s.entries))
	for _, e := range s.entries {
		snap, ok := snapshotFromEntry(e)
		if !ok {
			continue
		}
		rows = append(rows, snap)
	}
	return store.ReplaceSnapshots(s.db, s.dir, rows)
}

// snapshotFromEntry converts one index entry to its row. Entries carrying
// anything the row cannot honestly describe (foreign filenames, missing
// checksum or verified tokens) are skipped, matching the legacy loader's
// validation.
func snapshotFromEntry(e *CacheEntry) (store.Snapshot, bool) {
	if e == nil || filepath.Base(e.Filename) != e.Filename || e.Checksum == "" || len(e.PromptTokens) == 0 {
		return store.Snapshot{}, false
	}
	tokens, err := json.Marshal(e.PromptTokens)
	if err != nil {
		return store.Snapshot{}, false
	}
	return store.Snapshot{
		ID: e.ID, NamespaceDir: "", Filename: e.Filename, Checksum: e.Checksum,
		PromptTokensJSON: string(tokens), SavedTokens: e.SavedTokens, Bytes: e.Bytes,
		CreatedAt: e.CreatedAt, LastUsedAt: e.LastUsedAt,
	}, true
}

// entryFromSnapshot rebuilds an index entry from its row; rows that fail
// validation are skipped like the legacy loader did.
func entryFromSnapshot(namespace string, row store.Snapshot) (*CacheEntry, bool) {
	if row.ID == "" || filepath.Base(row.Filename) != row.Filename || row.Checksum == "" {
		return nil, false
	}
	var tokens []int32
	if err := json.Unmarshal([]byte(row.PromptTokensJSON), &tokens); err != nil || len(tokens) == 0 {
		return nil, false
	}
	return &CacheEntry{
		ID: row.ID, Namespace: namespace, Filename: row.Filename, Checksum: row.Checksum,
		PromptTokens: tokens, SavedTokens: row.SavedTokens, Bytes: row.Bytes,
		CreatedAt: row.CreatedAt, LastUsedAt: row.LastUsedAt,
	}, true
}

// mutate runs f against freshly loaded committed state under the
// cross-process lock and commits the result when f succeeds.
func (s *cacheStore) mutate(f func() error) error {
	file, err := os.OpenFile(filepath.Join(s.dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	if err = s.loadLocked(); err != nil {
		return err
	}
	if err = f(); err != nil {
		return err
	}
	return s.commitLocked()
}

// Publish stores a snapshot that llama-server wrote to <id>.tmp inside the
// store directory. The temp file is size-checked and checksummed, renamed
// into place, and the metadata commit happens atomically. A failure at any
// point leaves no entry and no occupied-but-unaccounted bytes behind: the
// file the write left (temp or unpublished binary) is removed before its
// accounting is released.
//
// When the entry carries a live reservation from BeginTempWrite, publication
// owns the reservation-to-entry handoff (issue #24): the reservation is
// held and marked in-flight for the whole operation, then transferred into
// the committed entry's bytes inside the same critical section, so no other
// writer can book the bytes of a snapshot that still occupies disk between
// the write and its publication. The reservation must cover the actual file
// size and is never stacked on top of reserveLocked, so publication neither
// double-counts nor evicts committed entries for bytes it already owns.
//
// Publication without a prior reservation stays supported (explicit,
// unreserved path): the actual size is then booked like any in-flight
// publish, evicting unpinned entries to fit.
func (s *cacheStore) Publish(entryID string, tokens []int32, savedTokens int) (*CacheEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tmpPath := filepath.Join(s.dir, entryID+".tmp")
	info, err := os.Stat(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("snapshot temp file: %w", err)
	}
	res, reserved := s.temps[entryID]
	if reserved {
		// Maintenance cannot lapse this reservation (or sweep its file)
		// while the handoff runs, even if the save deadline passed while
		// llama-server was writing the snapshot.
		res.publishing = true
	}
	if info.Size() > s.maxEntryBytes {
		// A child wrote past the per-entry limit (the documented
		// limitation in BeginTempWrite). The file goes before its
		// accounting does, reserved or not.
		if reserved {
			_ = s.abortTempLocked(entryID)
		} else {
			_ = os.Remove(tmpPath)
		}
		return nil, fmt.Errorf("snapshot of %d bytes exceeds the per-entry limit", info.Size())
	}
	if !reserved && !s.reserveLocked(info.Size()) {
		_ = os.Remove(tmpPath)
		return nil, errors.New("no snapshot disk budget available")
	}
	entry, err := s.publishLocked(entryID, tmpPath, info.Size(), tokens, savedTokens, reserved)
	if reserved {
		res.publishing = false
		if err != nil {
			// Centralized abort: remove what the failed publication left
			// in the store directory (temp or unpublished binary), then
			// release its bytes. If removal fails the reservation is kept,
			// so occupied bytes are never treated as free; maintenance
			// retries the coordinated cleanup at the save deadline.
			_ = s.abortTempLocked(entryID)
		}
	} else {
		// The unreserved booking covered the in-flight publish either way.
		s.releaseLocked(info.Size())
		if err != nil {
			_ = os.Remove(filepath.Join(s.dir, entryID+".bin"))
		}
	}
	return entry, err
}

// publishLocked renames, verifies and commits the snapshot. Publish has
// already bounded the size and booked budget: with a reservation it first
// validates that the reservation covers the actual file, and on success the
// committed row replaces the reservation in the same critical section
// (issue #24).
func (s *cacheStore) publishLocked(entryID, tmpPath string, size int64, tokens []int32, savedTokens int, reserved bool) (*CacheEntry, error) {
	if reserved {
		if res := s.temps[entryID]; res != nil && size > res.bytes {
			return nil, fmt.Errorf("snapshot of %d bytes exceeds its %d-byte write reservation", size, res.bytes)
		}
	}
	binPath := filepath.Join(s.dir, entryID+".bin")
	if err := os.Rename(tmpPath, binPath); err != nil {
		return nil, err
	}
	checksum, err := hashFile(binPath)
	if err != nil {
		return nil, err
	}
	now := s.now()
	entry := &CacheEntry{ID: entryID, Namespace: s.namespace, Filename: entryID + ".bin", Checksum: checksum, PromptTokens: tokens, SavedTokens: savedTokens, Bytes: size, CreatedAt: now, LastUsedAt: now}
	if err = s.mutate(func() error {
		s.enforceLocked(now)
		var used int64
		for _, e := range s.entries {
			used += e.Bytes
		}
		if used+size > s.maxBytes {
			return errors.New("no snapshot disk budget available")
		}
		s.entries[entry.ID] = entry
		return nil
	}); err != nil {
		return nil, err
	}
	if reserved {
		// Handoff complete: the committed row now accounts for the bytes
		// the reservation held. It leaves the temp pool here, not before
		// the row exists, so no writer observes neither representation.
		if res := s.temps[entryID]; res != nil {
			s.releaseLocked(res.bytes)
			delete(s.temps, entryID)
		}
	}
	return entry, nil
}

// Candidates returns entries that are currently eligible for reuse, longest
// indexed prefix first. Entries past their TTL are filtered at lookup so an
// expired snapshot can never be restored or revived between maintenance
// passes; the TTL is retention since last use (successful restores Touch).
func (s *cacheStore) Candidates() []*CacheEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make([]*CacheEntry, 0, len(s.entries))
	for _, e := range s.entries {
		if s.invalid[e.ID] || s.expiredLocked(e, now) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].PromptTokens) != len(out[j].PromptTokens) {
			return len(out[i].PromptTokens) > len(out[j].PromptTokens)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// expiredLocked reports whether the entry exceeded its retention since last
// use. A zero TTL never expires.
func (s *cacheStore) expiredLocked(e *CacheEntry, now time.Time) bool {
	return s.ttl > 0 && now.Sub(e.LastUsedAt) > s.ttl
}

// RestoreReady verifies file presence, expiry, and checksum, then pins the
// entry so eviction cannot remove it mid-restore. The caller must call Done.
// An entry whose file was found invalid while another restore holds it is
// rejected but keeps its file until that owner releases the pin (issue #22).
func (s *cacheStore) RestoreReady(entryID string) (*CacheEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[entryID]
	if e == nil {
		return nil, errors.New("entry no longer present")
	}
	if s.invalid[entryID] {
		return nil, errors.New("snapshot file no longer matches the index")
	}
	if s.expiredLocked(e, s.now()) {
		return nil, errors.New("entry expired")
	}
	checksum, err := hashFile(filepath.Join(s.dir, e.Filename))
	if err != nil || checksum != e.Checksum {
		if s.pins[entryID] > 0 {
			// A live restore owns this file; invalidation must not unlink
			// it out from under the current owner. The entry leaves
			// circulation and cleanup runs when the final pin is released.
			s.invalid[entryID] = true
			if err != nil {
				return nil, err
			}
			return nil, errors.New("snapshot checksum mismatch; entry invalidated")
		}
		// Committed bytes leave the used total with the entry itself; the
		// reservation pool tracks only in-flight temp writes and must not
		// be credited here (issue #23).
		_ = s.mutate(func() error {
			_ = os.Remove(filepath.Join(s.dir, e.Filename))
			delete(s.entries, entryID)
			return nil
		})
		if err != nil {
			return nil, err
		}
		return nil, errors.New("snapshot checksum mismatch; entry invalidated")
	}
	s.pins[entryID]++
	return e, nil
}

// Done releases a pin taken by RestoreReady. Expiry that was deferred while
// the entry was in flight applies as soon as the last pin goes away, and so
// does cleanup of a file that was found invalid while pinned (issue #22).
func (s *cacheStore) Done(entryID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pins[entryID] > 1 {
		s.pins[entryID]--
		return
	}
	delete(s.pins, entryID)
	e := s.entries[entryID]
	bad := s.invalid[entryID] && e != nil
	_ = s.mutate(func() error {
		if bad {
			// The file was found invalid during the restore; its owner is
			// gone, so unlink it now. A same-size tamper would otherwise
			// survive reload validation, which only checks size.
			_ = os.Remove(filepath.Join(s.dir, e.Filename))
			delete(s.entries, entryID)
			delete(s.invalid, entryID)
		}
		s.enforceLocked(s.now())
		return nil
	})
}

// Touch records successful use for LRU ordering and persists metadata. An
// entry that already exceeded its retention is not revived.
func (s *cacheStore) Touch(entryID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[entryID]
	if e == nil || s.expiredLocked(e, s.now()) {
		return
	}
	now := s.now()
	_ = s.mutate(func() error {
		if e := s.entries[entryID]; e != nil {
			e.LastUsedAt = now
		}
		return nil
	})
	if e := s.entries[entryID]; e != nil {
		e.LastUsedAt = now
	}
}

// Delete removes one entry unless pinned by an in-flight operation.
func (s *cacheStore) Delete(entryID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[entryID]
	if e == nil {
		return false, nil
	}
	if s.pins[entryID] > 0 {
		return false, errors.New("entry is in use")
	}
	err := s.mutate(func() error {
		if current := s.entries[entryID]; current != nil {
			if err := os.Remove(filepath.Join(s.dir, current.Filename)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		delete(s.entries, entryID)
		return nil
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// Prune applies TTL and quota policy and reports how many entries were removed.
func (s *cacheStore) Prune() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	_ = s.mutate(func() error {
		before := len(s.entries)
		s.enforceLocked(s.now())
		removed = before - len(s.entries)
		return nil
	})
	return removed
}

// TempEstimate reports the bytes reserved for a snapshot write whose exact
// size is unknown before llama-server produces it: the per-entry limit
// itself. Publish rejects any snapshot larger than maxEntryBytes, so this is
// the largest write the store can ever accept — a true upper bound, not a
// historical average (an average under-reserves as snapshots grow, which let
// a child write past the budget, issue #23). Reserving it means a booked
// write cannot authorize bytes beyond the configured budget, at the cost of
// refusing saves earlier when the namespace is tight.
func (s *cacheStore) TempEstimate() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxEntryBytes
}

// BeginTempWrite books budget against the store limits for a snapshot write
// the caller is about to request from llama-server. The exact size is not
// known beforehand, so the caller passes the store's worst-case estimate
// (see TempEstimate); a write that cannot fit without evicting committed
// entries is refused up front instead of letting the child fill the volume
// outside the configured budget. Refusing never evicts: committed snapshots
// stay safe against a write that may fail or be abandoned, so a full cache
// rejects new saves until TTL/LRU maintenance frees room or a reservation
// lapses. Simultaneous writes in a shared namespace each book the full
// worst-case estimate, so committed plus reserved bytes always bound every
// write the store would publish.
//
// Limitation, stated honestly: the manager cannot hard-limit the bytes the
// child writes to <id>.tmp (that would need filesystem quotas; see issue
// #15 for durable accounting). The reservation covers every size Publish
// would accept; a child misbehaving beyond that produces an oversized file
// that Publish rejects and unlinks, but the transient write itself is only
// bounded by the child, not enforced here. A reservation ends when Publish
// transfers it into committed entry bytes without a gap (issue #24) or when
// AbortTempWrite discards a failed or abandoned write together with its
// bytes; reservations whose save deadline has passed are cleaned up by
// maintenance, file removal and accounting release together, so a crashed
// writer can neither hold budget forever nor leave bytes on disk
// unaccounted for.
func (s *cacheStore) BeginTempWrite(entryID string, estimate int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endTempWriteLocked(entryID)
	if estimate <= 0 || estimate > s.maxBytes {
		return false
	}
	var used int64
	for _, e := range s.entries {
		used += e.Bytes
	}
	if used+s.reserved+estimate > s.maxBytes {
		return false
	}
	s.reserved += estimate
	s.temps[entryID] = &tempReservation{bytes: estimate, at: s.now()}
	return true
}

// EndTempWrite releases the budget booked by BeginTempWrite for the entry.
// It only releases accounting: callers that must also discard the write's
// files (a failed or abandoned save) use AbortTempWrite so the removal and
// the release stay coordinated — occupied bytes are never returned to the
// pool before the bytes they occupy leave the volume (issue #24).
func (s *cacheStore) EndTempWrite(entryID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endTempWriteLocked(entryID)
}

func (s *cacheStore) endTempWriteLocked(entryID string) {
	if res, ok := s.temps[entryID]; ok {
		s.releaseLocked(res.bytes)
		delete(s.temps, entryID)
	}
}

// AbortTempWrite ends a reserved snapshot write that will not be published:
// it removes everything the write left in the store directory (<id>.tmp and
// an unpublished <id>.bin) and releases the reservation booked by
// BeginTempWrite only once those files are provably gone. If a removal
// fails the accounting is kept and the error is returned; maintenance then
// retries the coordinated cleanup at the save deadline instead of treating
// occupied bytes as free (issue #24). It is idempotent: a second call with
// the files already gone succeeds without touching accounting twice.
func (s *cacheStore) AbortTempWrite(entryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.abortTempLocked(entryID)
}

// abortTempLocked implements AbortTempWrite under the caller-held mutex.
func (s *cacheStore) abortTempLocked(entryID string) error {
	res, ok := s.temps[entryID]
	if !ok {
		return nil
	}
	if err := s.dropWriteFilesLocked(entryID); err != nil {
		return err
	}
	s.releaseLocked(res.bytes)
	delete(s.temps, entryID)
	return nil
}

// dropWriteFilesLocked removes the files a reserved snapshot write can have
// left behind: the temporary file and a binary whose publication failed. A
// committed entry's file is never touched. Missing files are success; the
// first real removal error is returned so callers keep the accounting.
func (s *cacheStore) dropWriteFilesLocked(entryID string) error {
	for _, name := range []string{entryID + ".tmp", entryID + ".bin"} {
		if name == entryID+".bin" && s.entries[entryID] != nil {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// RemoveTemp deletes a temporary snapshot file inside the store directory.
func (s *cacheStore) RemoveTemp(name string) error {
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".tmp") {
		return errors.New("invalid temp file name")
	}
	err := os.Remove(filepath.Join(s.dir, name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// HasEntry reports whether the store currently tracks the given entry.
func (s *cacheStore) HasEntry(entryID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[entryID] != nil
}

// PublicCacheEntry is the sanitized management view of an entry; it is the
// generated CacheEntry record from api/openapi.yaml (token IDs and
// server-side filenames stay private).
type PublicCacheEntry = api.CacheEntry

// Summary returns sanitized entry metadata (token IDs are never exposed)
// with usage accounting for the management API.
func (s *cacheStore) Summary() (entries []PublicCacheEntry, used, budget int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		entries = append(entries, PublicCacheEntry{ID: e.ID, Namespace: e.Namespace, SavedTokens: e.SavedTokens, PrefixTokens: len(e.PromptTokens), Bytes: e.Bytes, CreatedAt: e.CreatedAt, LastUsedAt: e.LastUsedAt})
		used += e.Bytes
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].LastUsedAt.After(entries[j].LastUsedAt) })
	return entries, used, s.maxBytes
}

// enforceLocked applies TTL then LRU eviction to stay inside the budget.
// Pinned entries are never removed — expired or invalid files of in-flight
// restores are dropped once Done releases the final pin (see Done). Files of
// dropped entries are removed. Write reservations past their save deadline
// belong to writers that abandoned the snapshot: their files are removed
// first and the bytes released only once the removal succeeded, so a
// completed-but-unpublished snapshot can never end up occupying disk with no
// reservation covering it (issue #24). Temp files no live write owns are
// swept once older than the save deadline so an interrupted write cannot
// linger in a long-lived process.
func (s *cacheStore) enforceLocked(now time.Time) {
	for entryID, res := range s.temps {
		if res.publishing || now.Sub(res.at) <= cacheSaveWait {
			continue
		}
		if err := s.dropWriteFilesLocked(entryID); err != nil {
			continue // keep accounting while the bytes still occupy disk
		}
		s.releaseLocked(res.bytes)
		delete(s.temps, entryID)
	}
	if items, err := os.ReadDir(s.dir); err == nil {
		for _, item := range items {
			name := item.Name()
			if item.IsDir() || !strings.HasSuffix(name, ".tmp") {
				continue
			}
			if _, active := s.temps[strings.TrimSuffix(name, ".tmp")]; active {
				continue
			}
			if info, err := item.Info(); err == nil && now.Sub(info.ModTime()) > cacheSaveWait {
				_ = os.Remove(filepath.Join(s.dir, name))
			}
		}
	}
	for id, e := range s.entries {
		if s.pins[id] > 0 {
			continue
		}
		expired := s.expiredLocked(e, now)
		info, err := os.Stat(filepath.Join(s.dir, e.Filename))
		if expired || err != nil || info.Size() != e.Bytes {
			_ = os.Remove(filepath.Join(s.dir, e.Filename))
			delete(s.entries, id)
		}
	}
	var used int64
	for _, e := range s.entries {
		used += e.Bytes
	}
	for used > s.maxBytes {
		order := make([]*CacheEntry, 0, len(s.entries))
		for _, e := range s.entries {
			if s.pins[e.ID] == 0 {
				order = append(order, e)
			}
		}
		sort.Slice(order, func(i, j int) bool { return order[i].LastUsedAt.Before(order[j].LastUsedAt) })
		if len(order) == 0 {
			break // live restores hold pins; the budget is temporarily exceeded
		}
		victim := order[0]
		_ = os.Remove(filepath.Join(s.dir, victim.Filename))
		delete(s.entries, victim.ID)
		used -= victim.Bytes
	}
}

// reserveLocked accounts for bytes an in-flight publish will write, evicting
// unpinned entries until the write (and its temp file) fits.
func (s *cacheStore) reserveLocked(size int64) bool {
	if size > s.maxBytes {
		return false
	}
	var used int64
	for _, e := range s.entries {
		used += e.Bytes
	}
	if used+s.reserved+size > s.maxBytes {
		order := make([]*CacheEntry, 0, len(s.entries))
		for _, e := range s.entries {
			if s.pins[e.ID] == 0 {
				order = append(order, e)
			}
		}
		sort.Slice(order, func(i, j int) bool { return order[i].LastUsedAt.Before(order[j].LastUsedAt) })
		for _, e := range order {
			if used+s.reserved+size <= s.maxBytes {
				break
			}
			_ = os.Remove(filepath.Join(s.dir, e.Filename))
			delete(s.entries, e.ID)
			used -= e.Bytes
		}
	}
	if used+s.reserved+size > s.maxBytes {
		return false
	}
	s.reserved += size
	return true
}

func (s *cacheStore) releaseLocked(size int64) {
	s.reserved -= size
	if s.reserved < 0 {
		s.reserved = 0
	}
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
