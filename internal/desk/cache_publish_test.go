package desk

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Issue #24: publication must own the reservation-to-entry handoff. A
// completed temporary snapshot stays accounted for until it is committed
// or removed, so a second writer sharing the namespace can never book
// bytes a .tmp file still occupies on disk.

func reservedPoolBytes(t *testing.T, s *cacheStore) int64 {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserved
}

// TestCacheStoreReservationPublicationHandoff is the deterministic
// reproduction from the issue: in a 130-byte namespace with a 100-byte
// per-entry limit, a written-but-unpublished snapshot must keep the second
// reservation refused, and publication transfers the reservation into
// committed bytes without a gap.
func TestCacheStoreReservationPublicationHandoff(t *testing.T) {
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(t.TempDir(), "ns"), "ns", 130, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !store.BeginTempWrite("first", store.TempEstimate()) {
		t.Fatal("initial reservation refused")
	}
	writeSnapshot(t, store.dir, "first", make([]byte, 100))

	// The Finish path no longer releases the reservation before Publish:
	// the 100 bytes remain booked while first.tmp occupies the volume.
	if store.BeginTempWrite("second", store.TempEstimate()) {
		t.Fatal("authorized 100 more bytes while first.tmp still occupies 100 bytes")
	}

	entry, err := store.Publish("first", []int32{7}, 1)
	if err != nil {
		t.Fatalf("publication of a covered snapshot failed: %v", err)
	}
	if entry.Bytes != 100 {
		t.Fatalf("committed entry accounts %d bytes, want 100", entry.Bytes)
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("the transferred reservation leaked %d bytes into the temp pool", got)
	}
	// Committed bytes take over the accounting: 30 free bytes cannot back
	// another worst-case write.
	if store.BeginTempWrite("second", store.TempEstimate()) {
		t.Fatal("authorized a 100-byte write with only 30 bytes uncommitted")
	}
}

// TestCacheStoreReservationHandoffDoesNotDoubleCount proves the
// reservation is replaced by (not stacked with) the committed entry bytes:
// a covered publication succeeds, keeps older committed snapshots, and
// leaves no phantom reservation behind.
func TestCacheStoreReservationHandoffDoesNotDoubleCount(t *testing.T) {
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(t.TempDir(), "ns"), "ns", 300, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "seed", make([]byte, 60))
	if _, err = store.Publish("seed", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	// 60 committed + 100 worst-case reservation fit in 300.
	if !store.BeginTempWrite("next", store.TempEstimate()) {
		t.Fatal("reservation of 100 bytes with 240 free must fit")
	}
	writeSnapshot(t, store.dir, "next", make([]byte, 90))
	if _, err = store.Publish("next", []int32{2}, 1); err != nil {
		t.Fatalf("publication must succeed when committed bytes plus the reservation fit: %v", err)
	}
	if !store.HasEntry("seed") {
		t.Fatal("publication evicted an older committed snapshot")
	}
	_, used, _ := store.Summary()
	if used != 150 {
		t.Fatalf("committed accounting drift: used %d, want 150", used)
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("reservation double-counted: %d bytes left in the temp pool", got)
	}
	// With no phantom reservation, the next worst-case write fits exactly
	// (150 + 100 <= 300). Double-counting any leftover would refuse it.
	if !store.BeginTempWrite("third", store.TempEstimate()) {
		t.Fatal("150 committed + 100 reserved must fit in 300 without double-counting")
	}
	store.EndTempWrite("third")
}

// TestCacheStoreInsufficientReservationCoverageCleansBeforeRelease covers
// the save path where the produced snapshot is larger than the reservation
// that backed it: the file is removed before its bytes return to the pool.
func TestCacheStoreInsufficientReservationCoverageCleansBeforeRelease(t *testing.T) {
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(t.TempDir(), "ns"), "ns", 1<<20, 1<<18, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !store.BeginTempWrite("sparse", 50) {
		t.Fatal("reservation refused")
	}
	writeSnapshot(t, store.dir, "sparse", make([]byte, 100))
	if _, err = store.Publish("sparse", []int32{1}, 1); err == nil {
		t.Fatal("published a snapshot exceeding its reservation")
	}
	if store.HasEntry("sparse") {
		t.Fatal("failed publication created an entry")
	}
	if _, err = os.Stat(filepath.Join(store.dir, "sparse.tmp")); !os.IsNotExist(err) {
		t.Fatalf("uncovered temp file left behind: %v", err)
	}
	if _, err = os.Stat(filepath.Join(store.dir, "sparse.bin")); !os.IsNotExist(err) {
		t.Fatalf("uncovered binary file left behind: %v", err)
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("reservation kept after its file was removed: %d bytes", got)
	}
}

// TestCacheStoreOversizedReservedOutputCleansBeforeRelease covers the
// documented limitation case: the child writes past the per-entry limit
// even though the reservation obeyed it. The oversized file must be
// removed together with — not after — its accounting.
func TestCacheStoreOversizedReservedOutputCleansBeforeRelease(t *testing.T) {
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(t.TempDir(), "ns"), "ns", 130, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !store.BeginTempWrite("huge", 100) {
		t.Fatal("reservation refused")
	}
	writeSnapshot(t, store.dir, "huge", make([]byte, 120))
	if _, err = store.Publish("huge", []int32{1}, 1); err == nil {
		t.Fatal("accepted a snapshot above the per-entry limit")
	}
	if _, err = os.Stat(filepath.Join(store.dir, "huge.tmp")); !os.IsNotExist(err) {
		t.Fatalf("oversized temp file left behind: %v", err)
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("reservation kept after the oversized file was removed: %d bytes", got)
	}
}

// TestCacheStoreFailedCommitReleasesReservationWithFile covers a
// publication that fails at the durable commit step (the namespace no
// longer fits the snapshot): the renamed binary is removed before the
// reservation is released, and no entry survives.
func TestCacheStoreFailedCommitReleasesReservationWithFile(t *testing.T) {
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(t.TempDir(), "ns"), "ns", 130, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !store.BeginTempWrite("tight", 100) {
		t.Fatal("reservation refused")
	}
	writeSnapshot(t, store.dir, "tight", make([]byte, 100))
	// Shrink the namespace below the pending snapshot after the write was
	// booked: the commit-time budget check must reject publication.
	if err = store.applyPolicy(90, 100, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Publish("tight", []int32{1}, 1); err == nil {
		t.Fatal("committed a snapshot past the namespace budget")
	}
	if store.HasEntry("tight") {
		t.Fatal("failed commit created an entry")
	}
	for _, name := range []string{"tight.tmp", "tight.bin"} {
		if _, err = os.Stat(filepath.Join(store.dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s left behind after failed commit: %v", name, err)
		}
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("reservation kept after its file was removed: %d bytes", got)
	}
}

// TestCacheStoreAbortKeepsAccountingWhenRemovalFails pins the ownership
// rule for cleanup failure: if the write's file cannot be removed, its
// bytes are not handed back to other writers. The unremovable non-empty
// directory stands in for a stuck file on any platform.
func TestCacheStoreAbortKeepsAccountingWhenRemovalFails(t *testing.T) {
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(t.TempDir(), "ns"), "ns", 1<<20, 1<<18, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !store.BeginTempWrite("stuck", 400_000) {
		t.Fatal("reservation refused")
	}
	stuck := filepath.Join(store.dir, "stuck.tmp")
	if err = os.Mkdir(stuck, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(stuck, "partial"), make([]byte, 10), 0600); err != nil {
		t.Fatal(err)
	}
	if err = store.AbortTempWrite("stuck"); err == nil {
		t.Fatal("abort reported success while the write's bytes remain on disk")
	}
	if got := reservedPoolBytes(t, store); got != 400_000 {
		t.Fatalf("bytes released while still occupied: reserved %d, want 400000", got)
	}
	// Once the bytes really leave the volume the release succeeds.
	if err = os.RemoveAll(stuck); err != nil {
		t.Fatal(err)
	}
	if err = store.AbortTempWrite("stuck"); err != nil {
		t.Fatalf("retry after the file was cleared: %v", err)
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("reservation not released after successful cleanup: %d", got)
	}
}

// TestCacheStoreMaintenanceCleansLapsedWriteCoordinatedly pins the
// deadline rule: a completed snapshot awaiting publication that lapses
// into maintenance must have its file removed and its reservation
// released together — never the reservation alone.
func TestCacheStoreMaintenanceCleansLapsedWriteCoordinatedly(t *testing.T) {
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(t.TempDir(), "ns"), "ns", 1<<20, 1<<18, 0)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	clock := base
	store.mu.Lock()
	store.now = func() time.Time { return clock }
	store.mu.Unlock()
	if !store.BeginTempWrite("pending", 400_000) {
		t.Fatal("reservation refused")
	}
	writeSnapshot(t, store.dir, "pending", make([]byte, 300))

	clock = base.Add(cacheSaveWait + time.Minute)
	store.Prune()

	if _, err = os.Stat(filepath.Join(store.dir, "pending.tmp")); !os.IsNotExist(err) {
		t.Fatalf("lapsed write left its snapshot on disk: %v", err)
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("lapsed write kept a reservation without a file: %d bytes", got)
	}
	// The namespace is fully free again — occupied and accounted agree.
	if !store.BeginTempWrite("next", 700_000) {
		t.Fatal("coordinated cleanup must return the full budget")
	}
}

// TestCacheStoreUnreservedPublicationFailureCleansBinary covers the
// explicit unreserved path when publication fails after the rename: the
// binary is removed and the in-flight booking released. Here the "snapshot"
// is a directory, so hashing fails on the renamed file.
func TestCacheStoreUnreservedPublicationFailureCleansBinary(t *testing.T) {
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(t.TempDir(), "ns"), "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	dirTmp := filepath.Join(store.dir, "dirpub.tmp")
	if err := os.Mkdir(dirTmp, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Publish("dirpub", []int32{1}, 1); err == nil {
		t.Fatal("published a directory as a snapshot")
	}
	if _, err = os.Stat(filepath.Join(store.dir, "dirpub.bin")); !os.IsNotExist(err) {
		t.Fatalf("failed unreserved publication left the binary behind: %v", err)
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("failed unreserved publication kept %d bytes booked", got)
	}
}

// TestCacheStoreAbortWithoutReservationIsNoop pins AbortTempWrite's
// idempotence for entries that never held (or already lost) a reservation.
func TestCacheStoreAbortWithoutReservationIsNoop(t *testing.T) {
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(t.TempDir(), "ns"), "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AbortTempWrite("never-reserved"); err != nil {
		t.Fatalf("aborting an unknown entry must succeed: %v", err)
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("no-op abort moved accounting: %d bytes", got)
	}
}

// TestCacheStoreAbortNeverTouchesCommittedBinary covers the case where the
// entry ID also names a committed snapshot: the abort discards the pending
// temp file and releases its reservation, but the committed entry's file
// must survive.
func TestCacheStoreAbortNeverTouchesCommittedBinary(t *testing.T) {
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(t.TempDir(), "ns"), "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, store.dir, "dup", []byte("committed-snapshot"))
	if _, err = store.Publish("dup", []int32{1}, 1); err != nil {
		t.Fatal(err)
	}
	if !store.BeginTempWrite("dup", 400_000) {
		t.Fatal("reservation refused")
	}
	writeSnapshot(t, store.dir, "dup", []byte("replacement"))
	if err = store.AbortTempWrite("dup"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(store.dir, "dup.tmp")); !os.IsNotExist(err) {
		t.Fatalf("abort left the temp file behind: %v", err)
	}
	info, err := os.Stat(filepath.Join(store.dir, "dup.bin"))
	if err != nil || info.Size() != int64(len("committed-snapshot")) {
		t.Fatalf("abort damaged the committed entry's file: %v", err)
	}
	if !store.HasEntry("dup") {
		t.Fatal("abort removed the committed entry")
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("abort did not release its reservation: %d bytes", got)
	}
}

// TestCacheStoreMaintenanceKeepsAccountingWhenCleanupFails pins the
// coordinated-cleanup rule at the deadline: if a lapsed write's file
// cannot be removed, maintenance must keep the reservation instead of
// declaring the still-occupied bytes free.
func TestCacheStoreMaintenanceKeepsAccountingWhenCleanupFails(t *testing.T) {
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(t.TempDir(), "ns"), "ns", 1<<20, 1<<18, 0)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	clock := base
	store.mu.Lock()
	store.now = func() time.Time { return clock }
	store.mu.Unlock()
	if !store.BeginTempWrite("zombie", 400_000) {
		t.Fatal("reservation refused")
	}
	zombie := filepath.Join(store.dir, "zombie.tmp")
	if err := os.Mkdir(zombie, 0700); err != nil { // unremovable non-empty dir
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zombie, "partial"), make([]byte, 10), 0600); err != nil {
		t.Fatal(err)
	}
	clock = base.Add(cacheSaveWait + time.Minute)
	store.Prune()
	if got := reservedPoolBytes(t, store); got != 400_000 {
		t.Fatalf("bytes freed while still on disk: reserved %d, want 400000", got)
	}
	// Once cleanup can succeed, the next maintenance pass releases the
	// bytes together with the file.
	if err := os.RemoveAll(zombie); err != nil {
		t.Fatal(err)
	}
	store.Prune()
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("reservation not released once the file was cleaned: %d bytes", got)
	}
}

// TestCacheStoreConcurrentPublicationsRespectBudget runs the real
// BeginTempWrite → write → Publish/Abort pattern from multiple runs over
// one shared store and asserts the invariant never breaks: at every
// moment any writer can observe, committed plus reserved bytes stay inside
// the namespace budget.
func TestCacheStoreConcurrentPublicationsRespectBudget(t *testing.T) {
	store, err := newCacheStore(cacheTestDB(t), filepath.Join(t.TempDir(), "ns"), "ns", 2000, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	var (
		wg        sync.WaitGroup
		violation atomic.Value
		stop      = make(chan struct{})
		samples   atomic.Int64
	)
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			store.mu.Lock()
			used := store.reserved
			for _, e := range store.entries {
				used += e.Bytes
			}
			maxBytes := store.maxBytes
			store.mu.Unlock()
			if used > maxBytes {
				violation.CompareAndSwap(nil, fmt.Sprintf("committed+reserved %d exceeds budget %d", used, maxBytes))
			}
			samples.Add(1)
		}
	}()
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				entryID := fmt.Sprintf("w%d-%d", w, i)
				if !store.BeginTempWrite(entryID, store.TempEstimate()) {
					continue // full namespace: refused before any write
				}
				size := 300 + (i*17+w*7)%200 // within the reserved worst case
				if err := os.WriteFile(filepath.Join(store.dir, entryID+".tmp"), make([]byte, size), 0600); err != nil {
					violation.CompareAndSwap(nil, "temp write: "+err.Error())
					return
				}
				if _, err := store.Publish(entryID, []int32{int32(w*1000 + i + 1)}, 1); err != nil {
					if err2 := store.AbortTempWrite(entryID); err2 != nil {
						violation.CompareAndSwap(nil, fmt.Sprintf("abort after %v failed: %v", err, err2))
						return
					}
				}
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	<-watchdogDone
	if v := violation.Load(); v != nil {
		t.Fatal(v.(string))
	}
	if samples.Load() == 0 {
		t.Fatal("watchdog sampled nothing")
	}
	if got := reservedPoolBytes(t, store); got != 0 {
		t.Fatalf("writers left %d bytes reserved after completing", got)
	}
	if leftovers := tmpSnapshotFiles(t, store.dir); len(leftovers) != 0 {
		t.Fatalf("writers left %d temporary snapshot(s): %v", len(leftovers), leftovers)
	}
	// Every committed entry's file exists and the committed total obeys
	// the budget.
	entries, used, budget := store.Summary()
	if used > budget {
		t.Fatalf("final accounting exceeds budget: used %d, budget %d", used, budget)
	}
	for _, e := range entries {
		info, err := os.Stat(filepath.Join(store.dir, e.ID+".bin"))
		if err != nil || info.Size() != e.Bytes {
			t.Fatalf("committed entry %s has no backing file of its size: %v", e.ID, err)
		}
	}
}
