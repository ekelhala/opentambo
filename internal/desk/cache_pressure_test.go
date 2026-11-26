package desk

// Default pressure-eviction matrix for issue #35: admitting a snapshot
// save reclaims expired entries first, then superseded states, then the
// least recently used unrelated snapshots, only as much as the shortfall
// needs, never pins or live reservations, and with counts and failure
// classes that keep replacements, evictions, budget refusals and storage
// faults distinguishable.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCacheStorePressureEvictsUnrelatedToAdmit is the deterministic
// reproduction from the issue: a 130-byte namespace, a 100-byte per-entry
// ceiling, TTL disabled, one 60-byte snapshot for tokens [1]. A new save
// for the unrelated conversation [2] needs a 100-byte reservation; the
// 30-byte shortfall is covered by evicting the unrelated unpinned entry,
// whose file and durable metadata go with it.
func TestCacheStorePressureEvictsUnrelatedToAdmit(t *testing.T) {
	dir := t.TempDir()
	db := cacheTestDB(t)
	store, err := newCacheStore(db, filepath.Join(dir, "ns"), "ns", 130, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "old-chat", make([]byte, 60))
	if _, err = store.Publish("old-chat", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	admission, err := store.BeginTempWriteEvicting("new-chat", 100, []int32{2})
	if err != nil {
		t.Fatalf("the unrelated cold snapshot must be evictable to admit the save: %v", err)
	}
	if admission.evictions != 1 || admission.replacements != 0 {
		t.Fatalf("an unrelated victim must count as a pressure eviction, got %+v", admission)
	}
	if store.HasEntry("old-chat") {
		t.Fatal("the evicted entry must leave the index")
	}
	if _, err := os.Stat(filepath.Join(store.dir, "old-chat.bin")); !os.IsNotExist(err) {
		t.Fatal("the evicted file must be removed with its entry")
	}
	if got := reservedPoolBytes(t, store); got != 100 {
		t.Fatalf("the admitted write must book its full estimate, got %d", got)
	}
	// Metadata persisted: a fresh instance shows the eviction is durable.
	writeSnapshot(t, store.dir, "new-chat", make([]byte, 70))
	if _, err = store.Publish("new-chat", []int32{2}, 1); err != nil {
		t.Fatal(err)
	}
	fresh, err := openCacheStore(db, filepath.Join(dir, "ns"), "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.HasEntry("old-chat") {
		t.Fatal("the eviction must be durable across reopen")
	}
	if entries, used, budget := fresh.Summary(); len(entries) != 1 || used != 70 || used > budget {
		t.Fatalf("accounting after reopen: %+v used %d of %d", entries, used, budget)
	}
}

// TestCacheStorePressureRollingWorkloadKeepsFlowing runs repeated
// unrelated conversations against a budget that can never hold all of
// them: every save is admitted (evicting cold entries rather than
// permanently refusing once early prompts occupy the cache), everything
// stays within the budget, and the state survives a reopen. TTL is zero
// here, proving pressure eviction is independent of expiry.
func TestCacheStorePressureRollingWorkloadKeepsFlowing(t *testing.T) {
	dir := t.TempDir()
	db := cacheTestDB(t)
	nsDir := filepath.Join(dir, "ns")
	store, err := newCacheStore(db, nsDir, "ns", 130, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		tokens := []int32{int32(100 + i)}
		admission, err := store.BeginTempWriteEvicting("chat", 100, tokens)
		if err != nil {
			t.Fatalf("save %d refused under pressure: %v", i, err)
		}
		if i > 0 && admission.evictions != 1 {
			t.Fatalf("save %d: expected exactly one cold eviction, got %+v", i, admission)
		}
		writeSnapshot(t, store.dir, "chat", make([]byte, 60))
		if _, err = store.Publish("chat", tokens, 1); err != nil {
			t.Fatal(err)
		}
		if entries, used, budget := store.Summary(); used > budget || len(entries) != 1 {
			t.Fatalf("rolling budget drift after save %d: %+v used %d of %d", i, entries, used, budget)
		}
	}
	fresh, err := openCacheStore(db, nsDir, "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	if entries, used, budget := fresh.Summary(); len(entries) != 1 || used != 60 || used > budget {
		t.Fatalf("rolled state after reopen: %+v used %d of %d", entries, used, budget)
	}
}

// TestCacheStorePressureExactFitEvictsNothing keeps the negative
// guarantees: when the estimate fits (down to the exact boundary), no
// valid snapshot is evicted and both counters stay zero.
func TestCacheStorePressureExactFitEvictsNothing(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 130, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "keeper", make([]byte, 30))
	if _, err = store.Publish("keeper", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	admission, err := store.BeginTempWriteEvicting("next", 100, []int32{2})
	if err != nil {
		t.Fatalf("an exactly-fitting reservation must be admitted: %v", err)
	}
	if admission.evictions != 0 || admission.replacements != 0 {
		t.Fatalf("exact fit must evict nothing, got %+v", admission)
	}
	if !store.HasEntry("keeper") {
		t.Fatal("exact fit must preserve valid snapshots")
	}
	if reservedPoolBytes(t, store) != 100 {
		t.Fatal("the fitting reservation must still be booked")
	}
	// One byte over the boundary the next admission must evict toward
	// the minimum victim set instead of refusing.
	store.EndTempWrite("next")
	writeSnapshot(t, store.dir, "over", make([]byte, 31))
	if _, err = store.Publish("over", []int32{3}, 1); err != nil {
		t.Fatal(err)
	}
	admission, err = store.BeginTempWriteEvicting("next", 100, []int32{4})
	if err != nil {
		t.Fatalf("one byte over the boundary must evict toward the minimum: %v", err)
	}
	if admission.evictions+admission.replacements == 0 {
		t.Fatal("the shortfall must take at least one victim")
	}
	if got := storeBytes(t, store) + reservedPoolBytes(t, store); got > 130 {
		t.Fatalf("admitted state must stay inside the budget, got %d", got)
	}
}

// TestCacheStorePressureMinimalVictimPrefix removes only the shortest
// prefix of the ordered victim list: with three equal cold entries and a
// shortfall one entry covers, exactly one - the oldest - goes.
func TestCacheStorePressureMinimalVictimPrefix(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 1000, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	store.mu.Lock()
	store.now = func() time.Time { return clock }
	store.mu.Unlock()
	for _, id := range []string{"cold", "middle", "warm"} {
		clock = clock.Add(time.Minute)
		writeSnapshot(t, store.dir, id, make([]byte, 300))
		if _, err = store.Publish(id, []int32{int32('a' + rune(id[0])), 42}, 1); err != nil {
			t.Fatal(err)
		}
	}
	// Shortfall: 900 committed + 400 estimate - 1000 budget = 300 bytes.
	admission, err := store.BeginTempWriteEvicting("next", 400, []int32{7, 7})
	if err != nil {
		t.Fatal(err)
	}
	if admission.evictions != 1 || admission.replacements != 0 {
		t.Fatalf("only one victim was needed, got %+v", admission)
	}
	if store.HasEntry("cold") || !store.HasEntry("middle") || !store.HasEntry("warm") {
		entries, _, _ := store.Summary()
		t.Fatalf("the oldest entry alone must go, got %+v", entries)
	}
}

// TestCacheStorePressureCleansExpiredFirst proves expiry is the first
// step of admission and is not counted as a pressure eviction: one
// expired and one fresh superseded entry, where dropping only the
// expired one leaves room for the write without evicting anything valid.
func TestCacheStorePressureCleansExpiredFirst(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 250, 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	store.mu.Lock()
	current := base.Add(-2 * time.Minute)
	store.now = func() time.Time { return current }
	store.mu.Unlock()
	writeSnapshot(t, store.dir, "expired", make([]byte, 100))
	if _, err = store.Publish("expired", []int32{7}, 1); err != nil {
		t.Fatal(err)
	}
	current = base
	writeSnapshot(t, store.dir, "fresh", make([]byte, 100))
	if _, err = store.Publish("fresh", []int32{8}, 1); err != nil {
		t.Fatal(err)
	}
	// Shortfall: 200 committed + 100 estimate - 250 budget = 50 bytes;
	// the expired entry alone covers it once cleanup runs first.
	admission, err := store.BeginTempWriteEvicting("next", 100, []int32{1, 2})
	if err != nil {
		t.Fatalf("expired cleanup must admit the save: %v", err)
	}
	if admission.evictions != 0 || admission.replacements != 0 {
		t.Fatalf("expired cleanup is not a pressure eviction, got %+v", admission)
	}
	if store.HasEntry("expired") {
		t.Fatal("the expired entry must be cleaned at admission")
	}
	if _, err := os.Stat(filepath.Join(store.dir, "expired.bin")); !os.IsNotExist(err) {
		t.Fatal("the expired file must be removed by cleanup")
	}
	if !store.HasEntry("fresh") {
		t.Fatal("no valid snapshot may be evicted when cleanup suffices")
	}
}

// TestCacheStorePressureTouchChangesVictim proves real recency drives the
// unrelated order: touching the older entry moves it behind the fresher
// one, so the next pressure eviction takes the untouched entry.
func TestCacheStorePressureTouchChangesVictim(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 250, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	store.mu.Lock()
	store.now = func() time.Time { return clock }
	store.mu.Unlock()
	writeSnapshot(t, store.dir, "older", make([]byte, 100))
	if _, err = store.Publish("older", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	writeSnapshot(t, store.dir, "newer", make([]byte, 100))
	if _, err = store.Publish("newer", []int32{2}, 1); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	store.Touch("older")
	// Shortfall: 200 + 100 - 250 = 50 bytes; one victim.
	admission, err := store.BeginTempWriteEvicting("next", 100, []int32{3})
	if err != nil {
		t.Fatal(err)
	}
	if admission.evictions != 1 {
		t.Fatalf("exactly one eviction expected, got %+v", admission)
	}
	if store.HasEntry("newer") || !store.HasEntry("older") {
		entries, _, _ := store.Summary()
		t.Fatalf("the touched entry must survive, the untouched one must go: %+v", entries)
	}
}

// TestCacheStorePressureMixedGroups covers a shortfall larger than the
// superseded group: the whole superseded group goes (counted as
// replacements) plus the fewest unrelated victims (counted as evictions),
// and no more.
func TestCacheStorePressureMixedGroups(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 450, 450, 0)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	store.mu.Lock()
	store.now = func() time.Time { return clock }
	store.mu.Unlock()
	for _, seed := range []struct {
		id     string
		size   int
		tokens []int32
	}{
		{"u1", 100, []int32{7}},
		{"u2", 200, []int32{8}},
		{"superseded", 100, []int32{1}},
	} {
		clock = clock.Add(time.Minute)
		writeSnapshot(t, store.dir, seed.id, make([]byte, seed.size))
		if _, err = store.Publish(seed.id, seed.tokens, 1); err != nil {
			t.Fatal(err)
		}
	}
	// Shortfall: 400 committed + 200 estimate - 450 budget = 150 bytes:
	// superseded (100) plus the oldest unrelated u1 (100).
	admission, err := store.BeginTempWriteEvicting("next", 200, []int32{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if admission.replacements != 1 || admission.evictions != 1 {
		t.Fatalf("expected one replacement plus one eviction, got %+v", admission)
	}
	if store.HasEntry("superseded") || store.HasEntry("u1") || !store.HasEntry("u2") {
		entries, _, _ := store.Summary()
		t.Fatalf("mixed-group victims wrong: %+v", entries)
	}
}

// TestCacheStorePressureNeverStealsLiveReservation: another writer's live
// reservation occupies budget like committed bytes and is never a victim;
// when only that reservation stands in the way, the admission refuses as
// a budget refusal and leaves the reservation untouched.
func TestCacheStorePressureNeverStealsLiveReservation(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 150, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "small", make([]byte, 50))
	if _, err = store.Publish("small", []int32{5}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginTempWriteEvicting("other-writer", 100, []int32{6}); err != nil {
		t.Fatal(err)
	}
	// Shortfall: 50 committed + 100 reserved + 100 estimate - 150 = 100;
	// only the 50-byte entry is eligible, so the admission must refuse.
	_, err = store.BeginTempWriteEvicting("mine", 100, []int32{7})
	if err == nil {
		t.Fatal("bytes held by a live reservation must not be stolen")
	}
	if !BudgetRefused(err) || !strings.Contains(err.Error(), "reclaimable snapshots hold 50") {
		t.Fatalf("expected a budget refusal naming the eligible capacity, got %v", err)
	}
	if got := reservedPoolBytes(t, store); got != 100 {
		t.Fatalf("the other writer's reservation must be untouched, got %d", got)
	}
	if !store.HasEntry("small") {
		t.Fatal("a refusal must not evict toward a shortfall that cannot be covered")
	}
}

// TestCacheSaveFailureAfterEvictionStillCountsAndRecovers walks the
// failure-after-reclamation contract at coordinator level: evicted
// snapshots stay evicted, the eviction counts stay visible, the failed
// write leaves no files behind, the budget refusal counter stays clean,
// and the next save proceeds.
func TestCacheSaveFailureAfterEvictionStillCountsAndRecovers(t *testing.T) {
	fx := newCacheFixture(t)
	store := fx.rc.store
	for i, name := range []string{"blocker-1", "blocker-2"} {
		contents := make([]byte, 400_000)
		for j := range contents {
			contents[j] = byte('a' + j%26)
		}
		writeSnapshot(t, store.dir, name, contents)
		if _, err := store.Publish(name, []int32{int32(90_000 + i)}, 1); err != nil {
			t.Fatal(err)
		}
	}
	fx.fake.mu.Lock()
	fx.fake.failSave = true
	fx.fake.mu.Unlock()
	fx.post(t, singleTurnBody(conversationA[0]))
	summary := fx.rc.summary()
	if summary.Stats.SaveEvictions != 1 || summary.Stats.SaveFailures != 1 || summary.Stats.SaveRefusedBudget != 0 {
		t.Fatalf("eviction happened but was not counted with the save failure: %+v", summary.Stats)
	}
	if store.HasEntry("blocker-1") {
		t.Fatal("an evicted snapshot stays evicted when the incoming save fails")
	}
	if _, err := os.Stat(filepath.Join(store.dir, "blocker-1.bin")); !os.IsNotExist(err) {
		t.Fatal("the evicted file must not come back")
	}
	if leftovers := tmpSnapshotFiles(t, store.dir); len(leftovers) != 0 {
		t.Fatalf("the failed write left temporary files: %v", leftovers)
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("the failed write must release its reservation, got %d", got)
	}

	// A later save proceeds normally and is credited without double
	// counting the earlier eviction.
	fx.fake.mu.Lock()
	fx.fake.failSave = false
	fx.fake.mu.Unlock()
	fx.post(t, extendedTurnBody(conversationB[0], conversationB[1]))
	summary = fx.rc.summary()
	if summary.Stats.Saves != 1 || summary.Stats.SaveFailures != 1 || summary.Stats.SaveEvictions != 1 {
		t.Fatalf("follow-up save accounting drift: %+v", summary.Stats)
	}
	if summary.LastError != "" {
		t.Fatalf("the recovery save must clear the last error, got %q", summary.LastError)
	}
}

// TestCacheAdmissionStorageFaultIsNotBudgetRefusal injects a read-only
// namespace directory so the eviction removal fails mid-save: the
// coordinator must attribute the failure to the admission reclaim (not a
// budget refusal), count no evictions it did not perform, call neither
// the backend save nor refuse paths behind an aggregate counter, and
// leave the cache fully intact.
func TestCacheAdmissionStorageFaultIsNotBudgetRefusal(t *testing.T) {
	fx := newCacheFixture(t)
	store := fx.rc.store
	for i, name := range []string{"blocker-1", "blocker-2"} {
		contents := make([]byte, 400_000)
		for j := range contents {
			contents[j] = byte('a' + j%26)
		}
		writeSnapshot(t, store.dir, name, contents)
		if _, err := store.Publish(name, []int32{int32(90_000 + i)}, 1); err != nil {
			t.Fatal(err)
		}
	}
	// Probe that the fault is real on this filesystem (uid 0 ignores it).
	writeSnapshot(t, store.dir, "probe", make([]byte, 8))
	if err := os.Chmod(store.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	probeFault := os.Remove(filepath.Join(store.dir, "probe.bin")) != nil
	if err := os.Chmod(store.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !probeFault {
		t.Skip("uid 0 removes files from read-only directories; the removal fault cannot be injected")
	}
	if err := os.Chmod(store.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(store.dir, 0o755) }()

	fx.fake.mu.Lock()
	savesBefore := fx.fake.saveCalls
	fx.fake.mu.Unlock()
	fx.post(t, singleTurnBody(conversationA[0]))
	summary := fx.rc.summary()
	if !strings.Contains(summary.LastError, "admission reclaim failed") {
		t.Fatalf("the storage fault must be attributed to the admission, got %q", summary.LastError)
	}
	if strings.Contains(summary.LastError, "reservation refused") {
		t.Fatalf("a filesystem fault must not read as a budget refusal: %q", summary.LastError)
	}
	if summary.Stats.SaveFailures != 1 || summary.Stats.SaveRefusedBudget != 0 || summary.Stats.SaveEvictions != 0 {
		t.Fatalf("fault classification drift: %+v", summary.Stats)
	}
	if fx.fake.saveCalls != savesBefore {
		t.Fatalf("the backend save must not be called after a failed admission, got %d calls", fx.fake.saveCalls)
	}
	if err := os.Chmod(store.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, used, budget := store.Summary()
	if len(entries) != 2 || used != 800_000 || used > budget {
		t.Fatalf("the failed admission must leave the cache intact: %+v used %d of %d", entries, used, budget)
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("a failed admission must book nothing, got %d", got)
	}
}
