package desk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSnapshot mimics llama-server writing a slot snapshot through the
// --slot-save-path the manager handed it.
func writeSnapshot(t *testing.T, storeDir, entryID string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(storeDir, entryID+".tmp"), content, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestStorePublishAndReload(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "aaaa", []byte("kv-snapshot-1"))
	entry, err := store.Publish("aaaa", []int32{1, 2, 3}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Checksum == "" || entry.Bytes != 13 || entry.Namespace != "ns" {
		t.Fatalf("bad entry: %+v", entry)
	}
	if _, err = os.Stat(filepath.Join(store.dir, "aaaa.bin")); err != nil {
		t.Fatal("published snapshot missing:", err)
	}
	if _, err = os.Stat(filepath.Join(store.dir, "aaaa.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp file survived publication")
	}
	info, err := os.Stat(filepath.Join(store.dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("index permissions: %v", info.Mode().Perm())
	}
	reopened, err := newCacheStore(dir, "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	candidates := reopened.Candidates()
	if len(candidates) != 1 || candidates[0].ID != "aaaa" {
		t.Fatalf("entry lost across restart: %+v", candidates)
	}
	if got := candidates[0].PromptTokens; len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("tokens not persisted: %v", got)
	}
}

func TestStorePublishRejectsOversizedAndMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 1<<20, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Publish("ghost", []int32{1}, 1); err == nil {
		t.Fatal("published a snapshot file that was never written")
	}
	writeSnapshot(t, store.dir, "big", make([]byte, 10))
	if _, err = store.Publish("big", []int32{1}, 1); err == nil {
		t.Fatal("accepted a snapshot above the per-entry limit")
	}
	if _, err = os.Stat(filepath.Join(store.dir, "big.tmp")); !os.IsNotExist(err) {
		t.Fatal("oversized temp file left behind")
	}
	if store.HasEntry("big") || store.HasEntry("ghost") {
		t.Fatal("failed publication created an entry")
	}
}

func TestStoreQuotaEvictsLeastRecentlyUsed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 25, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return clock }
	publish := func(name string) {
		t.Helper()
		writeSnapshot(t, store.dir, name, make([]byte, 10))
		if _, err := store.Publish(name, []int32{1}, 1); err != nil {
			t.Fatalf("publish %s: %v", name, err)
		}
		clock = clock.Add(time.Second)
	}
	publish("first")
	publish("second")
	publish("third")
	if store.HasEntry("first") {
		t.Fatal("oldest entry not evicted")
	}
	if _, err = os.Stat(filepath.Join(store.dir, "first.bin")); !os.IsNotExist(err) {
		t.Fatal("evicted snapshot file left behind")
	}
	if !store.HasEntry("second") || !store.HasEntry("third") {
		t.Fatal("recent entries must survive")
	}
}

func TestStoreTouchChangesEvictionOrder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 25, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return clock }
	publish := func(name string) {
		t.Helper()
		writeSnapshot(t, store.dir, name, make([]byte, 10))
		if _, err := store.Publish(name, []int32{1}, 1); err != nil {
			t.Fatalf("publish %s: %v", name, err)
		}
		clock = clock.Add(time.Second)
	}
	publish("first")
	publish("second")
	store.Touch("first")
	reopened, err := newCacheStore(dir, "ns", 25, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range reopened.Candidates() {
		if e.ID == "first" && !e.LastUsedAt.Equal(clock) {
			t.Fatalf("touch not persisted: %+v", e)
		}
	}
	publish("third") // touching first must make "second" the eviction victim
	if store.HasEntry("second") || !store.HasEntry("first") {
		t.Fatal("touched entry should not be evicted before the untouched one")
	}
}

func TestStoreTTLPrunes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 1<<20, 1<<19, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return clock }
	writeSnapshot(t, store.dir, "old", []byte("data"))
	if _, err = store.Publish("old", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Minute)
	if removed := store.Prune(); removed != 1 {
		t.Fatalf("prune removed %d entries", removed)
	}
	if store.HasEntry("old") {
		t.Fatal("expired entry survived")
	}
	if _, err = os.Stat(filepath.Join(store.dir, "old.bin")); !os.IsNotExist(err) {
		t.Fatal("expired snapshot file left behind")
	}
}

func TestStorePinBlocksDeleteAndEviction(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 25, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "pinned", make([]byte, 10))
	if _, err = store.Publish("pinned", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RestoreReady("pinned"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Delete("pinned"); err == nil {
		t.Fatal("deleted an entry during a restore")
	}
	// A pinned entry must also survive quota pressure from a new publish.
	writeSnapshot(t, store.dir, "new", make([]byte, 10))
	if _, err = store.Publish("new", []int32{2}, 1); err != nil {
		t.Fatal(err)
	}
	if !store.HasEntry("pinned") {
		t.Fatal("pinned entry was evicted mid-restore")
	}
	store.Done("pinned")
	if deleted, err := store.Delete("pinned"); err != nil || !deleted {
		t.Fatalf("delete after release: %v %v", deleted, err)
	}
}

func TestStoreRecoveryDropsOrphansAndMissingFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ns")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// An interrupted publication and a file the index never referenced.
	_ = os.WriteFile(filepath.Join(dir, "interrupted.tmp"), []byte("partial"), 0600)
	_ = os.WriteFile(filepath.Join(dir, "orphan.bin"), []byte("orphan"), 0600)
	// A committed index that references a snapshot file that vanished.
	index := cacheIndex{Version: cacheIndexVersion, Entries: map[string]*CacheEntry{
		"ghost": {ID: "ghost", Namespace: "ns", Filename: "ghost.bin", Checksum: "abc", PromptTokens: []int32{1}, Bytes: 5},
	}}
	contents, _ := json.Marshal(index)
	_ = os.WriteFile(filepath.Join(dir, "index.json"), contents, 0600)
	store, err := newCacheStore(dir, "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Candidates()) != 0 {
		t.Fatalf("stale entries survived: %+v", store.Candidates())
	}
	for _, name := range []string{"interrupted.tmp", "orphan.bin"} {
		if _, err = os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived recovery", name)
		}
	}
	committed, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(committed), "ghost") {
		t.Fatal("dropped entry still referenced in the index")
	}
}

func TestStoreChecksumMismatchInvalidatesEntry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "tampered", []byte("original!!"))
	if _, err = store.Publish("tampered", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	// Same size, different content: an externally modified snapshot.
	if err = os.WriteFile(filepath.Join(store.dir, "tampered.bin"), []byte("modified!!"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RestoreReady("tampered"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum invalidation, got %v", err)
	}
	if store.HasEntry("tampered") {
		t.Fatal("corrupted entry survived")
	}
	reopened, err := newCacheStore(dir, "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.HasEntry("tampered") {
		t.Fatal("invalidated entry reloaded after restart")
	}
}

func TestStoreSummaryIsSanitized(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A deterministic clock keeps the timestamp digits stable so the leak
	// check below cannot trip over a coincidental substring.
	store.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	writeSnapshot(t, store.dir, "secret", []byte("snapshot"))
	if _, err = store.Publish("secret", []int32{91, 92, 93, 94}, 4); err != nil {
		t.Fatal(err)
	}
	entries, used, budget := store.Summary()
	if len(entries) != 1 || used != 8 || budget != 1<<20 {
		t.Fatalf("%+v used=%d budget=%d", entries, used, budget)
	}
	public, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	body := string(public)
	if strings.Contains(body, "91") || strings.Contains(body, "prompt_tokens") || strings.Contains(body, ".bin") {
		t.Fatalf("public view leaks private content: %s", body)
	}
	if !strings.Contains(body, "\"prefix_tokens\":4") {
		t.Fatalf("public view lost the prefix count: %s", body)
	}
}

func TestStoreRemoveTempValidation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RemoveTemp("../outside.tmp"); err == nil {
		t.Fatal("accepted a path escape")
	}
	if err = store.RemoveTemp("snapshot.bin"); err == nil {
		t.Fatal("accepted deleting a committed snapshot")
	}
	writeSnapshot(t, store.dir, "temp", []byte("x"))
	if err = store.RemoveTemp("temp.tmp"); err != nil {
		t.Fatal(err)
	}
	if err = store.RemoveTemp("temp.tmp"); err != nil {
		t.Fatalf("removing a missing temp must be a no-op: %v", err)
	}
}

func TestStoreTTLPruneHonorsInFlightPins(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 1<<20, 1<<19, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return clock }
	writeSnapshot(t, store.dir, "pinned", []byte("data"))
	if _, err = store.Publish("pinned", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RestoreReady("pinned"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Minute)
	if removed := store.Prune(); removed != 0 {
		t.Fatalf("TTL prune removed %d entries while a snapshot was pinned", removed)
	}
	if !store.HasEntry("pinned") {
		t.Fatal("pinned entry vanished during prune")
	}
	if _, err = os.Stat(filepath.Join(store.dir, "pinned.bin")); err != nil {
		t.Fatal("prune deleted a snapshot file mid-restore:", err)
	}
	// Once the restore finishes, the expired entry is cleaned up.
	store.Done("pinned")
	if store.HasEntry("pinned") {
		t.Fatal("expired entry survived the final pin release")
	}
	if _, err = os.Stat(filepath.Join(store.dir, "pinned.bin")); !os.IsNotExist(err) {
		t.Fatal("expired snapshot file left behind after Done")
	}
}

func TestStoreInvalidFileCleanupHonorsPins(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "corrupt", []byte("ten bytes!"))
	if _, err = store.Publish("corrupt", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RestoreReady("corrupt"); err != nil {
		t.Fatal(err)
	}
	// The file changes size under a pinned restore; maintenance must wait.
	if err = os.WriteFile(filepath.Join(store.dir, "corrupt.bin"), []byte("changed!!"), 0600); err != nil {
		t.Fatal(err)
	}
	if removed := store.Prune(); removed != 0 {
		t.Fatalf("prune removed %d pinned entries", removed)
	}
	store.Done("corrupt")
	if store.HasEntry("corrupt") {
		t.Fatal("size-mismatched entry survived cleanup after Done")
	}
	if _, err = os.Stat(filepath.Join(store.dir, "corrupt.bin")); !os.IsNotExist(err) {
		t.Fatal("size-mismatched snapshot file left behind after Done")
	}
}

func TestStoreExpiryIsEnforcedAtLookup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 1<<20, 1<<19, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return clock }
	writeSnapshot(t, store.dir, "aging", []byte("data"))
	if _, err = store.Publish("aging", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	// Nonexpired control: eligible before the TTL elapses.
	if _, err = store.RestoreReady("aging"); err != nil {
		t.Fatalf("fresh entry rejected: %v", err)
	}
	store.Done("aging")
	if len(store.Candidates()) != 1 {
		t.Fatal("fresh entry missing from candidates")
	}
	// Idle for twice the TTL without any maintenance pass.
	clock = clock.Add(2 * time.Minute)
	if candidates := store.Candidates(); len(candidates) != 0 {
		t.Fatalf("expired entry still a candidate: %+v", candidates)
	}
	if _, err = store.RestoreReady("aging"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired entry restored without rejection: %v", err)
	}
	// The idle-then-resume revival path: a rejected restore and a later
	// Touch must not reset the retention clock.
	store.Touch("aging")
	if candidates := store.Candidates(); len(candidates) != 0 {
		t.Fatal("Touch revived an expired entry")
	}
	// Expiry is not resurrection: the entry keeps expiring even if the
	// index still carries it until maintenance runs.
	if _, err = store.RestoreReady("aging"); err == nil {
		t.Fatal("expired entry became restorable again after Touch")
	}
}
