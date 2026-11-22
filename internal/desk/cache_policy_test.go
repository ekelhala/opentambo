package desk

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	storedb "github.com/ekelhala/opentambo/internal/desk/store"
)

// appOnData starts a manager over an existing data directory (simulating a
// relaunch) and registers its cleanup.
func appOnData(t *testing.T, models, data string) *App {
	t.Helper()
	a, err := New(filepath.Join(models, "models"), "missing-llama-server", data, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

// ageIndexEntries rewrites the committed snapshot rows so every entry looks
// as if it was last used `age` ago (simulating snapshots from an older run).
func ageIndexEntries(t *testing.T, db *sql.DB, dir string, age time.Duration) {
	t.Helper()
	rows, err := storedb.Snapshots(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		rows[i].CreatedAt = rows[i].CreatedAt.Add(-age)
		rows[i].LastUsedAt = rows[i].LastUsedAt.Add(-age)
	}
	if err = storedb.ReplaceSnapshots(db, dir, rows); err != nil {
		t.Fatal(err)
	}
}

// forgetNamespacePolicy drops the persisted-policy flag from a namespace
// row, simulating a directory written before policies were persisted.
func forgetNamespacePolicy(t *testing.T, a *App, dir string) {
	t.Helper()
	ns, ok, err := storedb.Namespace(a.db, dir)
	if err != nil || !ok {
		t.Fatalf("namespace row missing after launch (ok %v, err %v)", ok, err)
	}
	ns.HasPolicy = false
	if err = storedb.SaveNamespace(a.db, ns); err != nil {
		t.Fatal(err)
	}
}

func listCacheNamespaces(t *testing.T, a *App) map[string]map[string]any {
	t.Helper()
	rec := request(t, a, "GET", "/api/cache", "")
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	namespaces, _ := body["namespaces"].([]any)
	byName := map[string]map[string]any{}
	for _, item := range namespaces {
		ns, _ := item.(map[string]any)
		byName[ns["namespace"].(string)] = ns
	}
	return byName
}

// A namespace reopened with new settings must adopt them instead of keeping
// the limits from the first open.
func TestCacheStoreForAppliesPolicyOnReuse(t *testing.T) {
	a := testApp(t, "missing-llama-server")
	dir := filepath.Join(a.data, "prompt-cache", "ns")
	first, err := a.cacheStoreFor(dir, "ns", normalizedSpec(PromptCache{Enabled: true, MaxBytes: 1 << 20, MaxEntryBytes: 1 << 19, TTLSeconds: 60}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.cacheStoreFor(dir, "ns", normalizedSpec(PromptCache{Enabled: true, MaxBytes: 2 << 20, MaxEntryBytes: 150 << 10, TTLSeconds: 120}))
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatal("reopened namespace produced a second store")
	}
	if first.maxBytes != 2<<20 || first.maxEntryBytes != 150<<10 || first.ttl != 120*time.Second {
		t.Fatalf("reopened namespace kept the old policy: %d %d %v", first.maxBytes, first.maxEntryBytes, first.ttl)
	}
}

// The policy is persisted next to the snapshots so a relaunch (and the cache
// listing that follows it) restores it before any destructive maintenance.
func TestCachePolicyPersistedToNamespaceDirectory(t *testing.T) {
	a := testApp(t, "missing-llama-server")
	dir := filepath.Join(a.data, "prompt-cache", "ns")
	opened, err := a.cacheStoreFor(dir, "ns", normalizedSpec(PromptCache{Enabled: true, MaxBytes: 1 << 20, MaxEntryBytes: 1 << 19, TTLSeconds: 3600}))
	if err != nil {
		t.Fatal(err)
	}
	ns, ok, err := storedb.Namespace(a.db, opened.dir)
	if err != nil || !ok {
		t.Fatalf("namespace policy was not persisted (ok %v, err %v)", ok, err)
	}
	if !ns.HasPolicy || ns.MaxBytes != 1<<20 || ns.MaxEntryBytes != 1<<19 || ns.TTLSeconds != 3600 {
		t.Fatalf("persisted policy does not match the launch spec: %+v", ns)
	}
	if _, err = os.Stat(filepath.Join(opened.dir, "policy.json")); !os.IsNotExist(err) {
		t.Fatalf("policy.json must no longer be the durable format (stat err %v)", err)
	}
}

// Reproduction from the issue: a snapshot published under a 30-day TTL and
// ten days old must survive a fresh manager listing the cache before the
// namespace is launched again.
func TestCacheListingHonorsPersistedPolicy(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	a := appOnData(t, base, data)
	spec := normalizedSpec(PromptCache{Enabled: true, MaxBytes: 1 << 20, MaxEntryBytes: 1 << 19, TTLSeconds: 30 * 24 * 3600})
	dir := filepath.Join(data, "prompt-cache", "longttl")
	store, err := a.cacheStoreFor(dir, "longttl", spec)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "old", []byte("kv-snapshot-old"))
	entry, err := store.Publish("old", []int32{1, 2, 3}, 3)
	if err != nil {
		t.Fatal(err)
	}
	ageIndexEntries(t, a.db, store.dir, 10*24*time.Hour)
	a.Close()

	relaunched := appOnData(t, base, data)
	namespaces := listCacheNamespaces(t, relaunched)
	ns, ok := namespaces["longttl"]
	if !ok {
		t.Fatal("namespace disappeared from the listing")
	}
	entries, _ := ns["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("ten-day-old snapshot pruned by the listing under guessed defaults: %v", entries)
	}
	if _, err = os.Stat(filepath.Join(dir, entry.ID+".bin")); err != nil {
		t.Fatal("snapshot file deleted by the listing:", err)
	}
}

// Legacy namespaces (written before policies were persisted) must also
// survive a listing: cache listing is observational and never prunes under
// guessed default limits.
func TestCacheListingNeverPrunesUnderDefaults(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	a := appOnData(t, base, data)
	dir := filepath.Join(data, "prompt-cache", "legacy")
	store, err := a.cacheStoreFor(dir, "legacy", normalizedSpec(PromptCache{Enabled: true, MaxBytes: 1 << 20, MaxEntryBytes: 1 << 19, TTLSeconds: 30 * 24 * 3600}))
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "old", []byte("kv-snapshot-old"))
	entry, err := store.Publish("old", []int32{1, 2, 3}, 3)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a namespace written before policies were persisted.
	forgetNamespacePolicy(t, a, store.dir)
	ageIndexEntries(t, a.db, store.dir, 10*24*time.Hour)
	a.Close()

	relaunched := appOnData(t, base, data)
	namespaces := listCacheNamespaces(t, relaunched)
	ns, ok := namespaces["legacy"]
	if !ok {
		t.Fatal("namespace disappeared from the listing")
	}
	entries, _ := ns["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("listing pruned a legacy snapshot under default TTL: %v", entries)
	}
	if _, err = os.Stat(filepath.Join(dir, entry.ID+".bin")); err != nil {
		t.Fatal("legacy snapshot file deleted by the listing:", err)
	}
}

// Listing reports the persisted budget, not the guessed default.
func TestCacheListingReportsPersistedBudget(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	a := appOnData(t, base, data)
	dir := filepath.Join(data, "prompt-cache", "budgeted")
	if _, err := a.cacheStoreFor(dir, "budgeted", normalizedSpec(PromptCache{Enabled: true, MaxBytes: 1 << 20, MaxEntryBytes: 1 << 19, TTLSeconds: 600})); err != nil {
		t.Fatal(err)
	}
	a.Close()
	relaunched := appOnData(t, base, data)
	ns := listCacheNamespaces(t, relaunched)["budgeted"]
	if ns == nil {
		t.Fatal("namespace missing")
	}
	if budget, _ := ns["max_bytes"].(float64); int64(budget) != 1<<20 {
		t.Fatalf("listing shows the guessed default budget instead of the persisted policy: %v", ns["max_bytes"])
	}
}

func normalizedSpec(spec PromptCache) PromptCache { return normalizedPromptCache(spec) }

// The saved launch profile must round-trip the full advanced prompt-cache
// policy so "Run again" reuses the real limits, not defaults.
func TestSavedProfilePreservesAdvancedCachePolicy(t *testing.T) {
	base := t.TempDir()
	a := appOnData(t, base, filepath.Join(base, "data"))
	a.mu.Lock()
	a.profiles["policy.gguf"] = Launch{Model: "policy.gguf", Name: "policy", Context: 4096, PromptCache: &PromptCache{Enabled: true, MaxBytes: 1 << 20, MaxEntryBytes: 1 << 19, TTLSeconds: 3600, MinReuseTokens: 128}}
	err := a.save()
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	relaunched := appOnData(t, base, filepath.Join(base, "data"))
	spec := relaunched.profiles["policy.gguf"].PromptCache
	if !spec.Enabled || spec.MaxBytes != 1<<20 || spec.MaxEntryBytes != 1<<19 || spec.TTLSeconds != 3600 || spec.MinReuseTokens != 128 {
		t.Fatalf("advanced cache policy lost across relaunch: %+v", spec)
	}
}
