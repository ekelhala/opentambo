package desk

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #33: worst-case write reservations must not starve snapshot
// refreshes before the namespace budget is full. A save may reclaim the
// committed snapshots the incoming state supersedes (leading-prefix states
// of the same conversation), never another conversation's snapshots, and a
// refused reclaim must leave every committed snapshot and byte of
// accounting untouched.

func storeBytes(t *testing.T, s *cacheStore) int64 {
	t.Helper()
	_, used, _ := s.Summary()
	return used
}

// TestCacheStoreReclaimSupersededUnblocksRefresh is the deterministic
// starvation reproduction from the issue report: a 130-byte namespace with
// a 100-byte per-entry limit and one 60-byte committed snapshot. The plain
// worst-case reservation is refused forever, while the replacing
// reservation for a state that supersedes the committed snapshot proceeds
// and keeps the committed+reserved+estimate <= maxBytes invariant exact.
func TestCacheStoreReclaimSupersededUnblocksRefresh(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 130, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "seed", make([]byte, 60))
	if _, err = store.Publish("seed", []int32{1, 2}, 2); err != nil {
		t.Fatal(err)
	}
	if store.BeginTempWrite("plain", store.TempEstimate()) {
		t.Fatal("the plain reservation must stay conservative: it never reclaims")
	}
	reclaimed, err := store.BeginTempWriteReplacing("next", store.TempEstimate(), []int32{1, 2, 3})
	if err != nil {
		t.Fatalf("a refresh superseding the committed state must be bookable: %v", err)
	}
	if reclaimed != 1 {
		t.Fatalf("expected the superseded snapshot to be reclaimed, got %d", reclaimed)
	}
	if store.HasEntry("seed") {
		t.Fatal("the superseded entry must leave the index when its room is taken")
	}
	if _, err := os.Stat(filepath.Join(store.dir, "seed.bin")); !os.IsNotExist(err) {
		t.Fatal("the superseded snapshot file must be removed with its entry")
	}
	if got := reservedPoolBytes(t, store); got != 100 {
		t.Fatalf("the reservation must still book the full worst-case estimate, got %d", got)
	}
	// #23 invariant holds at the reservation instant: only 30 bytes remain
	// free, so a second write that would exceed committed+reserved cannot
	// be booked while the refresh reservation is live.
	if store.BeginTempWrite("second", 31) {
		t.Fatal("reserved bytes must occupy the budget like committed ones")
	}
	if !store.BeginTempWrite("small", 30) {
		t.Fatal("the remaining headroom must stay bookable")
	}
	store.EndTempWrite("small")
	// The refresh publishes within its reservation.
	writeSnapshot(t, store.dir, "next", make([]byte, 90))
	entry, err := store.Publish("next", []int32{1, 2, 3}, 3)
	if err != nil {
		t.Fatalf("publishing the refreshing snapshot failed: %v", err)
	}
	if entry.Bytes != 90 || reservedPoolBytes(t, store) != 0 || storeBytes(t, store) != 90 {
		t.Fatalf("handoff drift: entry %d bytes, reserved %d, used %d", entry.Bytes, reservedPoolBytes(t, store), storeBytes(t, store))
	}
}

// TestCacheStoreReclaimRefusedLeavesEverythingIntact proves the
// all-or-nothing property: when the superseded snapshots cannot cover the
// shortfall (or none exist), nothing is removed and nothing is booked.
func TestCacheStoreReclaimRefusedLeavesEverythingIntact(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 130, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "keep", make([]byte, 60))
	if _, err = store.Publish("keep", []int32{9}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginTempWriteReplacing("next", store.TempEstimate(), []int32{1, 2, 3}); err == nil {
		t.Fatal("an unrelated conversation must not steal another conversation's room")
	} else if !strings.Contains(err.Error(), "no snapshot budget headroom") {
		t.Fatalf("refusal must name the budget headroom problem, got %v", err)
	}
	if !store.HasEntry("keep") {
		t.Fatal("a refused reclaim must preserve unrelated committed snapshots")
	}
	if reservedPoolBytes(t, store) != 0 {
		t.Fatal("a refused reservation must book nothing")
	}
	if _, err := os.Stat(filepath.Join(store.dir, "keep.bin")); err != nil {
		t.Fatalf("a refused reclaim damaged the unrelated snapshot: %v", err)
	}

	// Insufficient superseded capacity: the tiny superseded snapshot
	// cannot cover the shortfall, so nothing is removed.
	writeSnapshot(t, store.dir, "p1", make([]byte, 5))
	if _, err = store.Publish("p1", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginTempWriteReplacing("next", store.TempEstimate(), []int32{1, 2, 3}); err == nil {
		t.Fatal("reclaim must be refused while superseded snapshots free too little")
	}
	if !store.HasEntry("keep") || !store.HasEntry("p1") || reservedPoolBytes(t, store) != 0 {
		t.Fatal("an insufficient reclaim must be all-or-nothing")
	}
	if _, err := os.Stat(filepath.Join(store.dir, "p1.bin")); err != nil {
		t.Fatalf("an insufficient reclaim must not remove the partial victim: %v", err)
	}
}

// TestCacheStoreReclaimKeepsUnrelatedConversations is the reported
// workload shape: refreshing conversation A reclaims only A's own stale
// states, so conversation B stays restorable.
func TestCacheStoreReclaimKeepsUnrelatedConversations(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 2100, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "a-old", make([]byte, 700))
	if _, err = store.Publish("a-old", []int32{1, 2}, 2); err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "b-current", make([]byte, 700))
	if _, err = store.Publish("b-current", []int32{7, 8}, 2); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := store.BeginTempWriteReplacing("a-new", 1000, []int32{1, 2, 3})
	if err != nil {
		t.Fatalf("A's refresh must be bookable by reclaiming A's stale state: %v", err)
	}
	if reclaimed != 1 || store.HasEntry("a-old") {
		t.Fatalf("expected exactly A's stale snapshot reclaimed, got %d", reclaimed)
	}
	if !store.HasEntry("b-current") {
		t.Fatal("B's snapshot must survive A's replacement")
	}
	if _, err := os.Stat(filepath.Join(store.dir, "b-current.bin")); err != nil {
		t.Fatal("B's snapshot file must survive A's replacement")
	}
	if got := storeBytes(t, store) + reservedPoolBytes(t, store); got > 2100 {
		t.Fatalf("committed plus reserved exceeded the budget: %d", got)
	}
}

// TestCacheStoreReclaimTakesLeastRecentlyUsedFirst pins the bounded
// selection: victims are taken least-recently-used first and only until
// the shortfall is covered, so the freshest usable state of the
// conversation is kept when capacity allows.
func TestCacheStoreReclaimTakesLeastRecentlyUsedFirst(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 1000, 1000, 0)
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
		{"stale", 400, []int32{1}},
		{"fresher", 300, []int32{1, 2}},
		{"unrelated", 100, []int32{7}},
	} {
		clock = clock.Add(time.Minute)
		writeSnapshot(t, store.dir, seed.id, make([]byte, seed.size))
		if _, err = store.Publish(seed.id, seed.tokens, 1); err != nil {
			t.Fatal(err)
		}
	}
	// Shortfall: 800 committed + 400 estimate - 1000 budget = 200 bytes.
	reclaimed, err := store.BeginTempWriteReplacing("next", 400, []int32{1, 2, 3})
	if err != nil {
		t.Fatalf("reclaim should cover the shortfall: %v", err)
	}
	if reclaimed != 1 || !store.HasEntry("fresher") || !store.HasEntry("unrelated") {
		entries, _, _ := store.Summary()
		t.Fatalf("only the least recently used superseded snapshot may go, got %d reclaimed: %+v", reclaimed, entries)
	}
	if store.HasEntry("stale") {
		t.Fatal("the least recently used superseded snapshot must be reclaimed")
	}
}

// TestCacheStoreReclaimNeverTouchesPinnedEntries: a snapshot an in-flight
// restore owns is never a victim, even when it is superseded.
func TestCacheStoreReclaimNeverTouchesPinnedEntries(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 130, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "pinned", make([]byte, 60))
	if _, err = store.Publish("pinned", []int32{1, 2}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RestoreReady("pinned"); err != nil {
		t.Fatal(err)
	}
	defer store.Done("pinned")
	if _, err = store.BeginTempWriteReplacing("next", 100, []int32{1, 2, 3}); err == nil {
		t.Fatal("a pinned superseded entry must not be reclaimed away from its restore")
	}
	if !store.HasEntry("pinned") || reservedPoolBytes(t, store) != 0 {
		t.Fatal("a reclaim refused by pins must leave state untouched")
	}
}

// TestCacheStoreReclaimRejectsImpossibleEstimates keeps the hard limits:
// an estimate beyond the namespace budget (or not positive) is refused
// with an actionable message and no reclamation attempt.
func TestCacheStoreReclaimRejectsImpossibleEstimates(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 130, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "seed", make([]byte, 100))
	if _, err = store.Publish("seed", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginTempWriteReplacing("next", 131, []int32{1, 2}); err == nil {
		t.Fatal("an estimate beyond the whole namespace budget must be refused")
	} else if !strings.Contains(err.Error(), "does not fit") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if _, err = store.BeginTempWriteReplacing("zero", 0, []int32{1, 2}); err == nil {
		t.Fatal("a zero estimate must be refused")
	}
	if _, err = store.BeginTempWriteReplacing("negative", -5, []int32{1, 2}); err == nil {
		t.Fatal("a negative estimate must be refused")
	}
	if !store.HasEntry("seed") {
		t.Fatal("an invalid estimate must never evict")
	}
}

// TestCacheStoreReclaimSurvivesReopen proves the reclaimed state is durable:
// after the refresh publishes, reopening the namespace shows only the new
// snapshot and reconciled accounting.
func TestCacheStoreReclaimSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	db := cacheTestDB(t)
	store, err := newCacheStore(db, filepath.Join(dir, "ns"), "ns", 130, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "seed", make([]byte, 60))
	if _, err = store.Publish("seed", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginTempWriteReplacing("next", 100, []int32{1, 2}); err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "next", make([]byte, 80))
	if _, err = store.Publish("next", []int32{1, 2}, 2); err != nil {
		t.Fatal(err)
	}
	reopened, err := openCacheStore(db, filepath.Join(dir, "ns"), "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.HasEntry("seed") {
		t.Fatal("the reclaimed superseded entry must stay gone after reopen")
	}
	if !reopened.HasEntry("next") {
		t.Fatal("the refreshing snapshot must survive reopen")
	}
	if entries, used, budget := reopened.Summary(); len(entries) != 1 || used != 80 || used > budget {
		t.Fatalf("reopened accounting drift: %+v used %d of %d", entries, used, budget)
	}
}

// conversationA and conversationB are disjoint conversations used to drive
// the coordinator under a tight namespace budget. The second turn of each
// extends the first, so its snapshot supersedes the first turn's state.
var conversationA = [2]string{
	"conversation alpha state one, held tight against the namespace budget",
	"conversation alpha state two, extending the same leading prefix",
}

var conversationB = [2]string{
	"zebra boulevard market scene, an unrelated conversation entirely",
	"zebra boulevard scene two, extended along its own leading prefix",
}

// extendedTurnBody renders the two-turn continuation of a conversation: the
// first message establishes the leading prefix, the second extends it.
func extendedTurnBody(first, second string) string {
	return chatBody(fmt.Sprintf(`[{"role":"user","content":%q},{"role":"assistant","content":"noted"},{"role":"user","content":%q}]`, first, second))
}

func singleTurnBody(content string) string {
	return chatBody(fmt.Sprintf(`[{"role":"user","content":%q}]`, content))
}

// TestCacheTightBudgetRefreshKeepsFlowing is the issue's workload-level
// reproduction with the fix: under a budget too small for another
// worst-case reservation, a conversation keeps refreshing its snapshot by
// reclaiming only its own superseded state, other conversations'
// snapshots survive, and a state with nothing superseded reports an
// actionable refusal instead of failing silently behind an aggregate
// counter.
func TestCacheTightBudgetRefreshKeepsFlowing(t *testing.T) {
	fx := newCacheFixture(t)
	store := fx.rc.store

	// Fill the 1 MiB namespace with two committed snapshots unrelated to
	// both conversations: no save can reclaim them, and none leaves room
	// for a new 512 KiB worst-case reservation.
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
	if code := fx.post(t, singleTurnBody(conversationA[0])); code != 200 {
		t.Fatalf("generation failed: %d", code)
	}
	summary := fx.rc.summary()
	if summary.Stats.Saves != 0 || summary.Stats.SaveRefusedBudget != 1 || summary.Stats.SaveFailures != 1 {
		t.Fatalf("expected an explicit budget refusal, got %+v", summary.Stats)
	}
	if !strings.Contains(summary.LastError, "reservation refused") {
		t.Fatalf("refusal must name its kind, got %q", summary.LastError)
	}
	if entries, _, _ := store.Summary(); len(entries) != 2 {
		t.Fatalf("a refused save must preserve committed snapshots, got %d", len(entries))
	}

	// Clear the namespace and let conversation A establish its first
	// snapshot, then plant a stale oversized state of that same prefix:
	// this is what starved saves could not get rid of before.
	for _, entryID := range []string{"blocker-1", "blocker-2"} {
		if _, err := store.Delete(entryID); err != nil {
			t.Fatal(err)
		}
	}
	if code := fx.post(t, singleTurnBody(conversationA[0])); code != 200 {
		t.Fatalf("generation failed: %d", code)
	}
	candidates := store.Candidates()
	if len(candidates) != 1 {
		t.Fatalf("expected exactly A's first snapshot, got %d candidates", len(candidates))
	}
	firstTokens := candidates[0].PromptTokens
	// Plant stale oversized states of that same prefix totalling more than
	// the free headroom: these are what starved saves could not get rid of.
	for _, seed := range []struct {
		id   string
		size int
	}{
		{"stale-a-big", 450_000},
		{"stale-a-small", 90_000},
	} {
		stale := make([]byte, seed.size)
		copy(stale, []byte("snapshot:12"))
		writeSnapshot(t, store.dir, seed.id, stale)
		if _, err := store.Publish(seed.id, firstTokens, 1); err != nil {
			t.Fatal(err)
		}
	}

	// Alternating extended workload: every turn publishes. A's refresh
	// reclaims its own superseded state (including the planted stale
	// snapshots); B enters afterwards and keeps refreshing too.
	for _, body := range []string{
		extendedTurnBody(conversationA[0], conversationA[1]),
		singleTurnBody(conversationB[0]),
		extendedTurnBody(conversationB[0], conversationB[1]),
		extendedTurnBody(conversationA[0], conversationA[1]),
	} {
		if code := fx.post(t, body); code != 200 {
			t.Fatalf("generation failed: %d", code)
		}
	}
	summary = fx.rc.summary()
	if summary.Stats.SaveRefusedBudget != 1 || summary.Stats.SaveFailures != 1 {
		t.Fatalf("the alternating workload hit new save failures: %+v", summary.Stats)
	}
	if summary.Stats.Saves != 5 {
		t.Fatalf("every alternating turn must keep publishing, got %+v", summary.Stats)
	}
	if summary.LastError != "" {
		t.Fatalf("a successful save must clear the last error, got %q", summary.LastError)
	}
	if summary.Stats.SaveReplacements == 0 {
		t.Fatalf("refreshes must be credited to the replacement policy, got %+v", summary.Stats)
	}
	if store.HasEntry("stale-a-big") {
		t.Fatal("the planted superseded snapshot must have been reclaimed")
	}
	if entries, used, budget := store.Summary(); used > budget || len(entries) == 0 {
		t.Fatalf("accounting drift after the workload: %d entries, used %d of %d", len(entries), used, budget)
	}
	if leftovers := tmpSnapshotFiles(t, store.dir); len(leftovers) != 0 {
		t.Fatalf("the workload left temporary files behind: %v", leftovers)
	}
}

// TestCacheSaveFailureKindsAreAttributable walks the failure kinds so the
// run summary names each one (issue #33 acceptance: a `ready` cache must
// never hide growing failures behind an aggregate counter).
func TestCacheSaveFailureKindsAreAttributable(t *testing.T) {
	fx := newCacheFixture(t)

	fx.fake.mu.Lock()
	fx.fake.savePadBytes = 600_000
	fx.fake.mu.Unlock()
	fx.post(t, singleTurnBody(conversationA[0]))
	summary := fx.rc.summary()
	if !strings.Contains(summary.LastError, "publication failed") || !strings.Contains(summary.LastError, "per-entry limit") {
		t.Fatalf("oversized output not attributed to the per-entry limit: %q", summary.LastError)
	}
	if summary.Stats.SaveFailures != 1 || summary.Stats.SaveRefusedBudget != 0 {
		t.Fatalf("oversized output must count as a save failure only: %+v", summary.Stats)
	}
	if leftovers := tmpSnapshotFiles(t, fx.fake.saveDir); len(leftovers) != 0 {
		t.Fatalf("an oversized rejected snapshot left temporary files: %v", leftovers)
	}

	fx.fake.mu.Lock()
	fx.fake.savePadBytes = 0
	fx.fake.saveNOverride = 1
	fx.fake.mu.Unlock()
	fx.post(t, singleTurnBody(conversationA[0]))
	summary = fx.rc.summary()
	if !strings.Contains(summary.LastError, "covered") || !strings.Contains(summary.LastError, "indexed tokens") {
		t.Fatalf("coverage error kind not attributed: %q", summary.LastError)
	}

	fx.fake.mu.Lock()
	fx.fake.saveNOverride = 0
	fx.fake.failSave = true
	fx.fake.mu.Unlock()
	fx.post(t, singleTurnBody(conversationA[0]))
	summary = fx.rc.summary()
	if !strings.Contains(summary.LastError, "snapshot save failed") {
		t.Fatalf("save error kind not attributed: %q", summary.LastError)
	}

	// Recovery: a successful save clears the last error.
	fx.fake.mu.Lock()
	fx.fake.failSave = false
	fx.fake.mu.Unlock()
	fx.post(t, singleTurnBody(conversationA[0]))
	if summary = fx.rc.summary(); summary.LastError != "" {
		t.Fatalf("the recovery save must clear the last error, got %q", summary.LastError)
	}
}

// TestCacheStoreReclaimRemovalFaultAbortsReservation injects a filesystem
// fault (the namespace directory rejects removals) into the reclaim: the
// call must fail with the victim named, book nothing, and leave the
// committed state and accounting intact for the next attempt.
func TestCacheStoreReclaimRemovalFaultAbortsReservation(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 130, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "victim", make([]byte, 60))
	if _, err = store.Publish("victim", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	nsDir := filepath.Join(dir, "ns")
	// Probe that the fault is real on this filesystem: uid 0 ignores
	// directory write protection, and then the refusal cannot happen.
	writeSnapshot(t, store.dir, "probe", make([]byte, 8))
	if err := os.Chmod(nsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	probeFault := os.Remove(filepath.Join(nsDir, "probe.bin")) != nil
	if err := os.Chmod(nsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !probeFault {
		t.Skip("uid 0 removes files from read-only directories; the removal fault cannot be injected")
	}
	if err := os.Chmod(nsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(nsDir, 0o755) }()
	if _, err = store.BeginTempWriteReplacing("next", 100, []int32{1, 2}); err == nil {
		t.Fatal("a reclaim whose removal fails must be refused, not booked")
	} else if !strings.Contains(err.Error(), "reclaim snapshot") || !strings.Contains(err.Error(), "victim") {
		t.Fatalf("the removal fault must name the victim, got %v", err)
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("a failed reclaim must book nothing, got %d reserved", got)
	}
	if err := os.Chmod(nsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !store.HasEntry("victim") {
		t.Fatal("the failed reclaim must leave the committed index intact")
	}
	if _, err := os.Stat(filepath.Join(nsDir, "victim.bin")); err != nil {
		t.Fatalf("the failed reclaim must leave the victim file in place: %v", err)
	}
	// The retry after the fault succeeds normally.
	reclaimed, err := store.BeginTempWriteReplacing("next", 100, []int32{1, 2})
	if err != nil {
		t.Fatalf("the retry after the fault must succeed: %v", err)
	}
	if reclaimed != 1 || store.HasEntry("victim") {
		t.Fatalf("the retry must complete the reclaim, got %d reclaimed", reclaimed)
	}
}

// TestCacheStoreReclaimDetectsStaleNamespace covers the all-or-nothing
// backstop: a concurrent manager instance commits more snapshots after this
// instance's preflight, so the superseded capacity that looked sufficient
// no longer covers the shortfall. The reclaim stops with an actionable
// error, books nothing, and the bytes it already freed stay released only
// in the sense that the next reload reconciles their entries.
func TestCacheStoreReclaimDetectsStaleNamespace(t *testing.T) {
	dir := t.TempDir()
	db := cacheTestDB(t)
	nsDir := filepath.Join(dir, "ns")
	store, err := newCacheStore(db, nsDir, "ns", 150, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "stale", make([]byte, 80))
	if _, err = store.Publish("stale", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	// A second instance of the same namespace commits an unrelated
	// snapshot; this instance's preflight still sees the older state.
	other, err := openCacheStore(db, nsDir, "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, other.dir, "blocker", make([]byte, 60))
	if _, err = other.Publish("blocker", []int32{7}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginTempWriteReplacing("next", 100, []int32{1, 2}); err == nil {
		t.Fatal("a reclaim overtaken by a concurrent commit must be refused")
	} else if !strings.Contains(err.Error(), "before the state changed") {
		t.Fatalf("the refusal must say the state changed, got %v", err)
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("a refused reclaim must book nothing, got %d reserved", got)
	}
	// Reconciliation: the bytes the interrupted reclaim did free are
	// reflected after a reload; nothing else drifted.
	fresh, err := openCacheStore(db, nsDir, "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.HasEntry("stale") {
		t.Fatal("the entry whose file the interrupted reclaim removed must be dropped on reload")
	}
	if !fresh.HasEntry("blocker") {
		t.Fatal("the concurrent commit must be untouched")
	}
	if entries, used, budget := fresh.Summary(); used != 60 || used > budget || len(entries) != 1 {
		t.Fatalf("reconciled accounting drift: %+v used %d of %d", entries, used, budget)
	}
}

// TestCacheStoreReclaimVictimOrderIsDeterministic pins the tie-breaker:
// superseded victims with identical use timestamps are reclaimed by entry
// ID, so repeated attempts converge on the same victim. It also checks the
// supersession relation itself: only leading slices (equality included)
// are superseded, never longer states asked to shrink.
func TestCacheStoreReclaimVictimOrderIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 620, 620, 0)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	clock := time.Now()
	store.now = func() time.Time { return clock }
	store.mu.Unlock()
	for _, seed := range []struct {
		id   string
		size int
	}{{"zzz", 400}, {"aaa", 200}} {
		writeSnapshot(t, store.dir, seed.id, make([]byte, seed.size))
		if _, err = store.Publish(seed.id, []int32{1}, 1); err != nil {
			t.Fatal(err)
		}
	}
	// Shortfall: 600 committed + 200 estimate - 620 budget = 180 bytes,
	// covered by either victim; the tie must break toward the lower ID.
	reclaimed, err := store.BeginTempWriteReplacing("next", 200, []int32{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed != 1 || store.HasEntry("aaa") || !store.HasEntry("zzz") {
		entries, _, _ := store.Summary()
		t.Fatalf("equal use timestamps must break ties by entry ID, got %d reclaimed: %+v", reclaimed, entries)
	}
}

// TestTokensSupersedeRelation pins the verified-conversation relation used
// for victim selection: leading slices (equality included) supersede,
// divergent or longer states never do.
func TestTokensSupersedeRelation(t *testing.T) {
	cases := []struct {
		prefix, incoming []int32
		want             bool
	}{
		{[]int32{1, 2}, []int32{1, 2, 3}, true},
		{[]int32{1, 2}, []int32{1, 2}, true},
		{nil, []int32{1}, true},
		{[]int32{1, 2}, []int32{1}, false},
		{[]int32{1, 3}, []int32{1, 2}, false},
	}
	for _, tc := range cases {
		if got := tokensSupersede(tc.prefix, tc.incoming); got != tc.want {
			t.Errorf("tokensSupersede(%v, %v) = %v, want %v", tc.prefix, tc.incoming, got, tc.want)
		}
	}
}
