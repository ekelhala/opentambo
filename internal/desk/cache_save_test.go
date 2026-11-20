package desk

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Issue #11: unsuccessful snapshot writes must leave no unaccounted
// temporary files and must never push the store past its budget.

func tmpSnapshotFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestCacheSaveFailureRemovesPartialTempFile(t *testing.T) {
	fx := newCacheFixture(t)
	fx.post(t, chatBody(`[{"role":"user","content":"first healthy turn"}]`))
	summary := fx.rc.summary()
	if summary.Stats.Saves != 1 {
		t.Fatalf("expected one committed snapshot, got %d", summary.Stats.Saves)
	}
	entriesBefore, _, _ := fx.rc.store.Summary()

	fx.fake.mu.Lock()
	fx.fake.savePartial = true
	fx.fake.mu.Unlock()
	fx.post(t, chatBody(`[{"role":"user","content":"second turn whose snapshot write fails halfway"}]`))

	if leftovers := tmpSnapshotFiles(t, fx.fake.saveDir); len(leftovers) != 0 {
		t.Fatalf("failed save left %d unaccounted temporary snapshot(s): %v", len(leftovers), leftovers)
	}
	summary = fx.rc.summary()
	if summary.Stats.Saves != 1 || summary.Stats.SaveFailures != 1 {
		t.Fatalf("expected saves=1 save_failures=1, got saves=%d save_failures=%d", summary.Stats.Saves, summary.Stats.SaveFailures)
	}
	entriesAfter, _, _ := fx.rc.store.Summary()
	if len(entriesAfter) != len(entriesBefore) {
		t.Fatalf("failed save disturbed committed snapshots: %d -> %d", len(entriesBefore), len(entriesAfter))
	}
}

func TestCacheSaveTimeoutIsAbandonedAndCleaned(t *testing.T) {
	restore := cacheSaveWait
	cacheSaveWait = 150 * time.Millisecond
	t.Cleanup(func() { cacheSaveWait = restore })

	fx := newCacheFixture(t)
	fx.fake.mu.Lock()
	fx.fake.saveSlow = 30 * time.Second
	fx.fake.mu.Unlock()

	start := time.Now()
	fx.post(t, chatBody(`[{"role":"user","content":"turn whose snapshot write stalls forever"}]`))
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("stalled save was not abandoned at the deadline (took %s)", elapsed)
	}
	if leftovers := tmpSnapshotFiles(t, fx.fake.saveDir); len(leftovers) != 0 {
		t.Fatalf("abandoned save left %d unaccounted temporary snapshot(s): %v", len(leftovers), leftovers)
	}
	summary := fx.rc.summary()
	if summary.Stats.Saves != 0 || summary.Stats.SaveFailures != 1 {
		t.Fatalf("expected saves=0 save_failures=1, got saves=%d save_failures=%d", summary.Stats.Saves, summary.Stats.SaveFailures)
	}
}

func TestCacheInsufficientSaveCoverageCleansTempFile(t *testing.T) {
	fx := newCacheFixture(t)
	fx.fake.mu.Lock()
	fx.fake.saveNOverride = 1
	fx.fake.mu.Unlock()
	fx.post(t, chatBody(`[{"role":"user","content":"this prefix is long enough to index many tokens"}]`))

	if leftovers := tmpSnapshotFiles(t, fx.fake.saveDir); len(leftovers) != 0 {
		t.Fatalf("insufficient-coverage save left %d unaccounted temporary snapshot(s): %v", len(leftovers), leftovers)
	}
	summary := fx.rc.summary()
	if summary.Stats.Saves != 0 || summary.Stats.SaveFailures != 1 {
		t.Fatalf("expected saves=0 save_failures=1, got saves=%d save_failures=%d", summary.Stats.Saves, summary.Stats.SaveFailures)
	}
	fx.fake.mu.Lock()
	eraseCalls := fx.fake.eraseCalls
	fx.fake.mu.Unlock()
	if eraseCalls == 0 {
		t.Fatal("expected the slot to be erased when the snapshot coverage was insufficient")
	}
}

func TestCacheSaveRefusedBeforeWriteWhenBudgetSpent(t *testing.T) {
	fx := newCacheFixture(t)
	store := fx.rc.store
	for i, name := range []string{"seed-1", "seed-2"} {
		contents := make([]byte, 400_000)
		for j := range contents {
			contents[j] = byte('a' + j%26)
		}
		if err := os.WriteFile(filepath.Join(store.dir, name+".tmp"), contents, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Publish(name, []int32{int32(50_000 + i)}, 1); err != nil {
			t.Fatal(err)
		}
	}
	fx.fake.mu.Lock()
	before := fx.fake.saveCalls
	fx.fake.mu.Unlock()

	fx.post(t, chatBody(`[{"role":"user","content":"a new conversation turn that wants a snapshot"}]`))

	summary := fx.rc.summary()
	if summary.Stats.Saves != 0 {
		t.Fatal("a snapshot was published although the temp write did not fit the budget")
	}
	if summary.Stats.SaveFailures != 1 {
		t.Fatalf("expected the refused pre-write reservation to count as a save failure, got %d", summary.Stats.SaveFailures)
	}
	fx.fake.mu.Lock()
	after := fx.fake.saveCalls
	fx.fake.mu.Unlock()
	if after != before {
		t.Fatal("llama-server was asked to save without reserved temporary-write budget")
	}
	if leftovers := tmpSnapshotFiles(t, fx.fake.saveDir); len(leftovers) != 0 {
		t.Fatalf("refused save left %d unaccounted temporary snapshot(s): %v", len(leftovers), leftovers)
	}
	entries, _, _ := store.Summary()
	if len(entries) != 2 {
		t.Fatalf("refused save must preserve older committed snapshots, got %d entries", len(entries))
	}
}

func TestCacheStoreTempEstimateIsWorstCase(t *testing.T) {
	dir := t.TempDir()
	// Issue #23 reproduction: a 130-byte budget with a 100-byte per-entry
	// limit and one 60-byte committed snapshot. The average-based estimate
	// reserved only 60 bytes and authorized a potential 100-byte write
	// (160 total) past the 130-byte budget.
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 130, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.TempEstimate(); got != 100 {
		t.Fatalf("an empty namespace must reserve the per-entry limit, got %d", got)
	}
	writeSnapshot(t, store.dir, "seed", make([]byte, 60))
	if _, err = store.Publish("seed", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	if got := store.TempEstimate(); got != 100 {
		t.Fatalf("history must not lower the estimate below the per-entry limit, got %d", got)
	}
	if store.BeginTempWrite("next", store.TempEstimate()) {
		t.Fatal("authorized a potential 100-byte write with only 70 bytes available")
	}
	// A refused save must leave older committed snapshots intact.
	if !store.HasEntry("seed") {
		t.Fatal("refused reservation dropped a committed snapshot")
	}
	if info, err := os.Stat(filepath.Join(store.dir, "seed.bin")); err != nil || info.Size() != 60 {
		t.Fatalf("refused reservation damaged the committed snapshot: %v", err)
	}
}

func TestCacheStoreGrowingSnapshotsStayWithinBudget(t *testing.T) {
	dir := t.TempDir()
	// Growing-snapshot regression: a small committed snapshot must not
	// under-reserve the store for a much larger snapshot written later.
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 1000, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "small", make([]byte, 50))
	if _, err = store.Publish("small", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	// 950 bytes free: the worst-case reservation (1000) exceeds the free
	// space, so the large write is refused until more room exists.
	if store.BeginTempWrite("big", store.TempEstimate()) {
		t.Fatal("worst-case reservation must be refused while it cannot fit")
	}
	// Widen the budget and the same write books fully.
	if err := store.applyPolicy(2100, 1000, 0); err != nil {
		t.Fatal(err)
	}
	if !store.BeginTempWrite("big", store.TempEstimate()) {
		t.Fatal("the large write must fit once the budget allows it")
	}
	// Simultaneous writes share the namespace: committed plus reserved
	// bytes are bounded, so a third worst-case write is refused.
	if !store.BeginTempWrite("concurrent", 1000) {
		t.Fatal("50 + 1000 + 1000 must fit in 2100")
	}
	if store.BeginTempWrite("fourth", 1000) {
		t.Fatal("50 + 3*1000 exceeds the 2100 budget and must be refused")
	}
	store.EndTempWrite("concurrent")
	store.EndTempWrite("big")
	// Partial failure releases: after abandoning the write the budget is
	// available again (covered at the pool level in the accounting test).
	writeSnapshot(t, store.dir, "big", make([]byte, 900))
	if _, err = store.Publish("big", []int32{2}, 1); err != nil {
		t.Fatal(err)
	}
	if !store.HasEntry("small") || !store.HasEntry("big") {
		t.Fatal("growing snapshot publication lost history")
	}
	entries, used, budget := store.Summary()
	if len(entries) != 2 || used != 950 || budget != 2100 {
		t.Fatalf("accounting drift: %d entries, used %d, budget %d", len(entries), used, budget)
	}
}

func TestCacheStoreTempWriteReservationAccounting(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 1<<20, 1<<18, 0)
	if err != nil {
		t.Fatal(err)
	}
	if store.BeginTempWrite("too-big", 1<<20+1) {
		t.Fatal("a reservation larger than the whole budget must be refused")
	}
	if !store.BeginTempWrite("a", 600_000) {
		t.Fatal("the first reservation must fit in an empty store")
	}
	if store.BeginTempWrite("b", 600_000) {
		t.Fatal("a combined reservation past the budget must be refused without evicting")
	}
	store.EndTempWrite("a")
	if !store.BeginTempWrite("b", 600_000) {
		t.Fatal("budget must be available again after a reservation is released")
	}
	store.EndTempWrite("b")

	// A reservation whose save deadline passed belongs to an abandoned
	// write; maintenance releases it so it cannot hold budget forever.
	base := time.Now()
	clock := base
	store.mu.Lock()
	store.now = func() time.Time { return clock }
	store.mu.Unlock()
	if !store.BeginTempWrite("stalled", 400_000) {
		t.Fatal("reservation should fit after EndTempWrite(b)")
	}
	if store.BeginTempWrite("next", 700_000) {
		t.Fatal("a live reservation must keep its budget out of reach")
	}
	clock = base.Add(cacheSaveWait + time.Minute)
	store.Prune()
	if !store.BeginTempWrite("next", 700_000) {
		t.Fatal("maintenance must release reservations past the save deadline")
	}
}

func TestCacheStoreSweepsAbandonedTempFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 1<<20, 1<<18, 0)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(store.dir, "abandoned.tmp")
	fresh := filepath.Join(store.dir, "in-flight.tmp")
	for _, name := range []string{stale, fresh} {
		if err := os.WriteFile(name, []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * cacheSaveWait)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	store.Prune()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("maintenance must sweep abandoned temp files")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh temp files (a live write) must not be swept")
	}
}
