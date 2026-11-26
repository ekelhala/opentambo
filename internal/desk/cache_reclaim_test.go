package desk

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Suites: issue #33 (snapshot-refresh starvation and bounded
// same-conversation replacement) under issue #35's default pressure
// eviction. Superseded states of the incoming conversation remain the
// preferred victims and are counted separately from unrelated evictions;
// a refusal still means nothing valid was destroyed and nothing booked.

func storeBytes(t *testing.T, s *cacheStore) int64 {
	t.Helper()
	_, used, _ := s.Summary()
	return used
}

// TestCacheStoreReclaimSupersededUnblocksRefresh is the deterministic
// starvation reproduction from issue #33: a 130-byte namespace with a
// 100-byte per-entry limit and one 60-byte committed snapshot. The plain
// worst-case reservation is refused forever, while the evicting admission
// for a state that supersedes the committed snapshot proceeds and keeps
// the committed+reserved+estimate <= maxBytes invariant exact.
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
	admission, err := store.BeginTempWriteEvicting("next", store.TempEstimate(), []int32{1, 2, 3})
	if err != nil {
		t.Fatalf("a refresh superseding the committed state must be bookable: %v", err)
	}
	if admission.replacements != 1 || admission.evictions != 0 {
		t.Fatalf("expected the superseded snapshot counted as a replacement, got %+v", admission)
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

// TestCacheStoreReclaimPrefersSupersededOverUnrelated is the priority
// rule from issue #35: an unrelated snapshot is only ever taken after the
// superseded group is exhausted, even when the superseded entry is the
// more recent one and the unrelated entry alone could cover the shortfall.
func TestCacheStoreReclaimPrefersSupersededOverUnrelated(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 2100, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	store.mu.Lock()
	store.now = func() time.Time { return clock }
	store.mu.Unlock()
	// unrelated is the oldest entry; a-old is fresher but superseded.
	clock = clock.Add(-time.Hour)
	writeSnapshot(t, store.dir, "unrelated", make([]byte, 1000))
	if _, err = store.Publish("unrelated", []int32{7, 8}, 2); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	writeSnapshot(t, store.dir, "a-old", make([]byte, 700))
	if _, err = store.Publish("a-old", []int32{1, 2}, 2); err != nil {
		t.Fatal(err)
	}
	// Shortfall: 1700 committed + 1000 estimate - 2100 budget = 600
	// bytes; the superseded group alone covers it.
	admission, err := store.BeginTempWriteEvicting("a-new", 1000, []int32{1, 2, 3})
	if err != nil {
		t.Fatalf("A's refresh must be bookable by reclaiming A's stale state: %v", err)
	}
	if admission.replacements != 1 || admission.evictions != 0 {
		t.Fatalf("the superseded entry must go first, unrelated untouched, got %+v", admission)
	}
	if store.HasEntry("a-old") {
		t.Fatal("the superseded snapshot must be reclaimed")
	}
	if !store.HasEntry("unrelated") {
		entries, _, _ := store.Summary()
		t.Fatalf("the superseded group covered the shortfall, so the unrelated snapshot must survive: %+v", entries)
	}
	if _, err := os.Stat(filepath.Join(store.dir, "unrelated.bin")); err != nil {
		t.Fatal("the surviving snapshot file must be intact")
	}
	if got := storeBytes(t, store) + reservedPoolBytes(t, store); got > 2100 {
		t.Fatalf("committed plus reserved exceeded the budget: %d", got)
	}
}

// TestCacheStoreReclaimTakesLeastRecentlyUsedFirst pins the bounded
// selection inside the superseded group: victims are taken
// least-recently-used first and only until the shortfall is covered, so
// the freshest usable state of the conversation is kept when capacity
// allows.
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
	admission, err := store.BeginTempWriteEvicting("next", 400, []int32{1, 2, 3})
	if err != nil {
		t.Fatalf("reclaim should cover the shortfall: %v", err)
	}
	if admission.replacements != 1 || admission.evictions != 0 || !store.HasEntry("fresher") || !store.HasEntry("unrelated") {
		entries, _, _ := store.Summary()
		t.Fatalf("only the least recently used superseded snapshot may go, got %+v: %+v", admission, entries)
	}
	if store.HasEntry("stale") {
		t.Fatal("the least recently used superseded snapshot must be reclaimed")
	}
}

// TestCacheStoreReclaimNeverTouchesPinnedEntries: a snapshot an in-flight
// restore owns is never a victim, even when it is superseded, and a
// reclaim with no eligible victims at all is refused with nothing booked.
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
	if _, err = store.BeginTempWriteEvicting("next", 100, []int32{1, 2, 3}); err == nil {
		t.Fatal("a pinned superseded entry must not be reclaimed away from its restore")
	} else if !BudgetRefused(err) {
		t.Fatalf("a pinned-capacity refusal is a budget refusal, got %v", err)
	} else if !strings.Contains(err.Error(), "reclaimable snapshots hold 0") {
		t.Fatalf("the refusal must report the eligible capacity, got %v", err)
	}
	if !store.HasEntry("pinned") || reservedPoolBytes(t, store) != 0 {
		t.Fatal("a reclaim refused by pins must leave state untouched")
	}
}

// TestCacheStoreReclaimRejectsImpossibleEstimates keeps the hard limits:
// an estimate beyond the namespace budget (or not positive) is refused as
// a budget refusal with an actionable message and no eviction attempt.
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
	for _, estimate := range []int64{131, 0, -5} {
		_, err := store.BeginTempWriteEvicting("next", estimate, []int32{1, 2})
		if err == nil {
			t.Fatalf("estimate %d must be refused", estimate)
		}
		if !BudgetRefused(err) {
			t.Fatalf("an invalid estimate is a budget refusal, got %v", err)
		}
		if !strings.Contains(err.Error(), "does not fit") {
			t.Fatalf("unexpected refusal for %d: %v", estimate, err)
		}
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
	if _, err = store.BeginTempWriteEvicting("next", 100, []int32{1, 2}); err != nil {
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

// TestCacheStoreReclaimRemovalFaultAbortsReservation injects a filesystem
// fault (the namespace directory rejects removals) into the reclaim: the
// call must fail with the victim named, book nothing, report no phantom
// removal counts, and be classified as a storage fault rather than a
// budget refusal.
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
	admission, err := store.BeginTempWriteEvicting("next", 100, []int32{1, 2})
	if err == nil {
		t.Fatal("a reclaim whose removal fails must be refused, not booked")
	}
	if BudgetRefused(err) {
		t.Fatalf("a filesystem fault must not be classified as a budget refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "reclaim snapshot") || !strings.Contains(err.Error(), "victim") {
		t.Fatalf("the removal fault must name the victim, got %v", err)
	}
	if admission.replacements != 0 || admission.evictions != 0 {
		t.Fatalf("a reclaim that removed nothing must report no counts, got %+v", admission)
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
	admission, err = store.BeginTempWriteEvicting("next", 100, []int32{1, 2})
	if err != nil {
		t.Fatalf("the retry after the fault must succeed: %v", err)
	}
	if admission.replacements != 1 || store.HasEntry("victim") {
		t.Fatalf("the retry must complete the reclaim, got %+v", admission)
	}
}

// TestCacheStoreReclaimAdmitsAcrossConcurrentCommit covers the cross-store
// race: a concurrent manager instance commits an unrelated snapshot after
// this instance's preflight, growing the shortfall beyond what the
// superseded group covers. Under the default pressure policy the reclaim
// simply continues into the unrelated group (the concurrent snapshot is
// itself a candidate) and admits the write with both groups counted.
func TestCacheStoreReclaimAdmitsAcrossConcurrentCommit(t *testing.T) {
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
	other, err := openCacheStore(db, nsDir, "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, other.dir, "blocker", make([]byte, 60))
	if _, err = other.Publish("blocker", []int32{7}, 1); err != nil {
		t.Fatal(err)
	}
	admission, err := store.BeginTempWriteEvicting("next", 100, []int32{1, 2})
	if err != nil {
		t.Fatalf("pressure eviction must absorb the concurrent commit: %v", err)
	}
	if admission.replacements != 1 || admission.evictions != 1 {
		t.Fatalf("expected one superseded and one unrelated victim, got %+v", admission)
	}
	if got := reservedPoolBytes(t, store); got != 100 {
		t.Fatalf("the admission must book its estimate, got %d", got)
	}
	if store.HasEntry("stale") || store.HasEntry("blocker") {
		entries, _, _ := store.Summary()
		t.Fatalf("both victims must leave the index, got %+v", entries)
	}
	// Durable commit: a fresh instance sees the reclaimed state.
	writeSnapshot(t, store.dir, "next", make([]byte, 90))
	if _, err = store.Publish("next", []int32{1, 2}, 2); err != nil {
		t.Fatal(err)
	}
	fresh, err := openCacheStore(db, nsDir, "ns", nil)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.HasEntry("stale") || fresh.HasEntry("blocker") {
		t.Fatal("victim removals must be durable across reopen")
	}
	if entries, used, budget := fresh.Summary(); len(entries) != 1 || used != 90 || used > budget {
		t.Fatalf("reconciled accounting drift: %+v used %d of %d", entries, used, budget)
	}
}

// TestCacheStoreReclaimBackstopRefusesWhenNoVictims covers the in-mutate
// backstop for a shortfall the victim lists cannot cover - here a namespace
// whose only bytes are pinned: no deletion, no reservation, and the
// refusal is a budget refusal naming the freed total.
func TestCacheStoreReclaimBackstopRefusesWhenNoVictims(t *testing.T) {
	dir := t.TempDir()
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(dir, "ns"), "ns", 100, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "pinned", make([]byte, 60))
	if _, err = store.Publish("pinned", []int32{5}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RestoreReady("pinned"); err != nil {
		t.Fatal(err)
	}
	defer store.Done("pinned")
	err = func() error {
		store.mu.Lock()
		defer store.mu.Unlock()
		var outcome admissionOutcome
		return store.mutate(func() error { return store.admissionReclaimLocked(100, []int32{1}, &outcome) })
	}()
	if err == nil {
		t.Fatal("the backstop must refuse when pinned bytes cannot be reclaimed")
	}
	if !BudgetRefused(err) || !strings.Contains(err.Error(), "before the state changed") {
		t.Fatalf("unexpected backstop refusal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.dir, "pinned.bin")); err != nil {
		t.Fatalf("the backstop must delete nothing: %v", err)
	}
}

// TestCacheStoreReclaimVictimOrderIsDeterministic pins the tie-breaker:
// superseded victims with identical use timestamps are reclaimed by entry
// ID, so repeated attempts converge on the same victim.
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
	admission, err := store.BeginTempWriteEvicting("next", 200, []int32{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if admission.replacements != 1 || store.HasEntry("aaa") || !store.HasEntry("zzz") {
		entries, _, _ := store.Summary()
		t.Fatalf("equal use timestamps must break ties by entry ID, got %+v: %+v", admission, entries)
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

// TestCacheTightBudgetRollingKeepsFlowing is the workload-level
// reproduction with the default policy (issue #35): under a budget too
// small for another worst-case reservation, the admission evicts cold
// unrelated snapshots instead of refusing, refreshes keep preferring
// their own superseded states, everything stays inside the budget, and
// no temporary files linger.
func TestCacheTightBudgetRollingKeepsFlowing(t *testing.T) {
	fx := newCacheFixture(t)
	store := fx.rc.store

	// Fill the 1 MiB namespace with two committed snapshots unrelated to
	// both conversations. A new 512 KiB worst-case reservation no longer
	// fits: the admission evicts the single coldest blocker and proceeds.
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
	if summary.Stats.Saves != 1 || summary.Stats.SaveFailures != 0 || summary.Stats.SaveRefusedBudget != 0 {
		t.Fatalf("pressure eviction must admit the save, got %+v", summary.Stats)
	}
	if summary.Stats.SaveEvictions != 1 || summary.Stats.SaveReplacements != 0 {
		t.Fatalf("the coldest blocker alone covers the shortfall, got %+v", summary.Stats)
	}
	if store.HasEntry("blocker-1") {
		t.Fatal("the least recently used unrelated snapshot must have been evicted")
	}
	if !store.HasEntry("blocker-2") {
		t.Fatal("only the minimum prefix of the victim order may go")
	}

	// Plant stale oversized states of conversation A's prefix plus A's own
	// first snapshot: refreshes must reclaim these superseded states first
	// rather than the still-cold blocker-2.
	candidates := store.Candidates()
	var firstTokens []int32
	for _, e := range candidates {
		if e.ID != "blocker-2" {
			firstTokens = e.PromptTokens
		}
	}
	if firstTokens == nil {
		t.Fatal("A's first snapshot must exist before planting stale states")
	}
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

	// Alternating extended workload: every turn publishes; A's refresh
	// reclaims its own superseded states (including the planted stale
	// snapshots), B enters afterwards and keeps refreshing too. Nothing
	// is refused any more - pressure rolls the cache forward.
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
	if summary.Stats.SaveRefusedBudget != 0 || summary.Stats.SaveFailures != 0 {
		t.Fatalf("the alternating workload hit new refusals or failures: %+v", summary.Stats)
	}
	if summary.Stats.Saves != 5 {
		t.Fatalf("every alternating turn must keep publishing, got %+v", summary.Stats)
	}
	if summary.LastError != "" {
		t.Fatalf("a successful save must clear the last error, got %q", summary.LastError)
	}
	if summary.Stats.SaveReplacements == 0 {
		t.Fatalf("refreshes must be credited to the superseded group, got %+v", summary.Stats)
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
