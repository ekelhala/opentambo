package desk

// Namespaced disk store for prompt snapshots. Snapshots live below
// <data>/prompt-cache/<namespace>/ with private permissions. Metadata is a
// single JSON index committed atomically (temp file, fsync, rename). Files
// are content-verified with SHA-256 before restore. Token IDs describe
// private conversation content: the index file is 0600, entries are never
// logged, and raw snapshots are never served through the API.
//
// Mutations follow one pattern: take the in-process mutex, take the
// cross-process flock, reload the committed index, apply the change, commit,
// unlock. Crash recovery drops interrupted publications and orphan files.

import (
	"crypto/sha256"
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
)

const cacheIndexVersion = 1

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
	dir           string
	namespace     string
	maxBytes      int64
	maxEntryBytes int64
	ttl           time.Duration
	entries       map[string]*CacheEntry
	reserved      int64 // bytes from in-flight publishes, counted against the budget
	pins          map[string]int
	now           func() time.Time
}

func newCacheStore(dir, namespace string, maxBytes, maxEntryBytes int64, ttl time.Duration) (*cacheStore, error) {
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
	s := &cacheStore{dir: resolved, namespace: namespace, maxBytes: maxBytes, maxEntryBytes: maxEntryBytes, ttl: ttl, entries: map[string]*CacheEntry{}, pins: map[string]int{}, now: time.Now}
	if err = s.recoverFiles(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.loadLocked(); err != nil {
		return nil, err
	}
	s.enforceLocked(s.now())
	if err = s.commitLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// recoverFiles removes interrupted publications (temp files) and binary files
// that the committed index does not reference.
func (s *cacheStore) recoverFiles() error {
	keep := map[string]bool{}
	if contents, err := os.ReadFile(filepath.Join(s.dir, "index.json")); err == nil {
		var index cacheIndex
		if err = json.Unmarshal(contents, &index); err == nil {
			for _, e := range index.Entries {
				if e != nil && filepath.Base(e.Filename) == e.Filename {
					keep[e.Filename] = true
				}
			}
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

// loadLocked replaces the in-memory index from disk, dropping entries whose
// files are missing or have the wrong size (an interrupted commit or an
// externally modified snapshot).
func (s *cacheStore) loadLocked() error {
	entries := map[string]*CacheEntry{}
	contents, err := os.ReadFile(filepath.Join(s.dir, "index.json"))
	if err != nil {
		if os.IsNotExist(err) {
			s.entries = entries
			return nil
		}
		return err
	}
	var index cacheIndex
	if err = json.Unmarshal(contents, &index); err != nil {
		return fmt.Errorf("cache index: %w", err)
	}
	if index.Version != cacheIndexVersion {
		return fmt.Errorf("unsupported cache index version %d", index.Version)
	}
	for id, e := range index.Entries {
		if e == nil || e.ID != id || filepath.Base(e.Filename) != e.Filename || e.Checksum == "" || len(e.PromptTokens) == 0 {
			continue
		}
		info, err := os.Stat(filepath.Join(s.dir, e.Filename))
		if err != nil || !info.Mode().IsRegular() || info.Size() != e.Bytes {
			_ = os.Remove(filepath.Join(s.dir, e.Filename))
			continue
		}
		entries[id] = e
	}
	s.entries = entries
	return nil
}

func (s *cacheStore) commitLocked() error {
	contents, err := json.Marshal(cacheIndex{Version: cacheIndexVersion, Entries: s.entries})
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, "index.json")
	temporary, err := os.OpenFile(path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err = temporary.Write(contents); err == nil {
		err = temporary.Sync()
	}
	_ = temporary.Close()
	if err != nil {
		return err
	}
	if err = os.Rename(path+".tmp", path); err != nil {
		return err
	}
	directory, err := os.Open(s.dir)
	if err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
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
// point leaves no entry behind; the budget accounting is restored.
func (s *cacheStore) Publish(entryID string, tokens []int32, savedTokens int) (*CacheEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tmpPath := filepath.Join(s.dir, entryID+".tmp")
	info, err := os.Stat(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("snapshot temp file: %w", err)
	}
	if info.Size() > s.maxEntryBytes {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("snapshot of %d bytes exceeds the per-entry limit", info.Size())
	}
	if !s.reserveLocked(info.Size()) {
		_ = os.Remove(tmpPath)
		return nil, errors.New("no snapshot disk budget available")
	}
	published := false
	defer func() {
		if !published {
			_ = os.Remove(filepath.Join(s.dir, entryID+".bin"))
		}
		// The reserved bytes become committed entry bytes on success.
		s.releaseLocked(info.Size())
	}()
	if err = os.Rename(tmpPath, filepath.Join(s.dir, entryID+".bin")); err != nil {
		return nil, err
	}
	checksum, err := hashFile(filepath.Join(s.dir, entryID+".bin"))
	if err != nil {
		return nil, err
	}
	now := s.now()
	entry := &CacheEntry{ID: entryID, Namespace: s.namespace, Filename: entryID + ".bin", Checksum: checksum, PromptTokens: tokens, SavedTokens: savedTokens, Bytes: info.Size(), CreatedAt: now, LastUsedAt: now}
	if err = s.mutate(func() error {
		s.enforceLocked(now)
		var used int64
		for _, e := range s.entries {
			used += e.Bytes
		}
		if used+info.Size() > s.maxBytes {
			return errors.New("no snapshot disk budget available")
		}
		s.entries[entry.ID] = entry
		return nil
	}); err != nil {
		return nil, err
	}
	published = true
	return entry, nil
}

// Candidates returns entries, longest indexed prefix first.
func (s *cacheStore) Candidates() []*CacheEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*CacheEntry, 0, len(s.entries))
	for _, e := range s.entries {
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

// RestoreReady verifies file presence and checksum, then pins the entry so
// eviction cannot remove it mid-restore. The caller must call Done.
func (s *cacheStore) RestoreReady(entryID string) (*CacheEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[entryID]
	if e == nil {
		return nil, errors.New("entry no longer present")
	}
	checksum, err := hashFile(filepath.Join(s.dir, e.Filename))
	if err != nil || checksum != e.Checksum {
		if invalidateErr := s.mutate(func() error {
			_ = os.Remove(filepath.Join(s.dir, e.Filename))
			delete(s.entries, entryID)
			return nil
		}); invalidateErr == nil {
			s.releaseLocked(e.Bytes)
		}
		if err != nil {
			return nil, err
		}
		return nil, errors.New("snapshot checksum mismatch; entry invalidated")
	}
	s.pins[entryID]++
	return e, nil
}

// Done releases a pin taken by RestoreReady. Expiry that was deferred while
// the entry was in flight applies as soon as the last pin goes away.
func (s *cacheStore) Done(entryID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pins[entryID] > 1 {
		s.pins[entryID]--
		return
	}
	delete(s.pins, entryID)
	_ = s.mutate(func() error {
		s.enforceLocked(s.now())
		return nil
	})
}

// Touch records successful use for LRU ordering and persists metadata.
func (s *cacheStore) Touch(entryID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[entryID] == nil {
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
	s.releaseLocked(e.Bytes)
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

// PublicCacheEntry is the sanitized management view of an entry: token IDs
// and server-side filenames stay private.
type PublicCacheEntry struct {
	ID           string    `json:"id"`
	Namespace    string    `json:"namespace"`
	SavedTokens  int       `json:"saved_tokens"`
	PrefixTokens int       `json:"prefix_tokens"` // count only; the IDs are private
	Bytes        int64     `json:"bytes"`
	CreatedAt    time.Time `json:"created_at"`
	LastUsedAt   time.Time `json:"last_used_at"`
}

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
// dropped entries are removed.
func (s *cacheStore) enforceLocked(now time.Time) {
	for id, e := range s.entries {
		if s.pins[id] > 0 {
			continue
		}
		expired := s.ttl > 0 && now.Sub(e.LastUsedAt) > s.ttl
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
