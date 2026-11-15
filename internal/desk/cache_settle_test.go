package desk

import (
	"context"
	"testing"
	"time"
)

// Abandoning a generation (client disconnect / cancellation) while the slot
// is still processing must not hand the slot to the next request unobserved:
// an unconfirmed idle slot blocks all further forwarding until idleness is
// re-confirmed or the child restarts.

// settleFixture prepares a cache fixture with a shortened settle window.
func settleFixture(t *testing.T) *cacheFixture {
	t.Helper()
	original := cacheSettleWait
	cacheSettleWait = 150 * time.Millisecond
	t.Cleanup(func() { cacheSettleWait = original })
	fx := newCacheFixture(t)
	return fx
}

// runAbandonedFinish simulates a bypassed request whose client went away
// while the fake was still generating, releasing the lease right after.
func runAbandonedFinish(t *testing.T, fx *cacheFixture) {
	t.Helper()
	if err := fx.rc.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	fx.rc.Finish(context.Background(), &cachePlan{bypass: true}, false, false)
	fx.rc.release()
}

func TestUnsettledSlotBlocksNextPrepare(t *testing.T) {
	fx := settleFixture(t)
	fx.fake.mu.Lock()
	fx.fake.inflight = 1 // slot stays busy: the engine never stops on its own
	fx.fake.mu.Unlock()

	runAbandonedFinish(t, fx)

	if err := fx.rc.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fx.rc.release()
	_, _, err := fx.rc.Prepare(context.Background(), "/v1/chat/completions", map[string]any{
		"model":    "cached",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	if err == nil {
		t.Fatal("Prepare forwarded traffic while the slot was still busy after an abandoned generation")
	}
}

func TestUnreachableSlotsApiBlocksNextPrepare(t *testing.T) {
	fx := settleFixture(t)
	fx.fake.mu.Lock()
	fx.fake.inflight = 1
	fx.fake.slotsFail = true // control API cannot answer: idleness is unknowable
	fx.fake.mu.Unlock()

	runAbandonedFinish(t, fx)

	fx.fake.mu.Lock()
	fx.fake.inflight = 0
	fx.fake.mu.Unlock()

	if err := fx.rc.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fx.rc.release()
	_, _, err := fx.rc.Prepare(context.Background(), "/v1/chat/completions", map[string]any{
		"model":    "cached",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	if err == nil {
		t.Fatal("Prepare forwarded traffic while the control API could not confirm the slot idle")
	}
}

func TestMissingSlotIsNotConfirmedIdle(t *testing.T) {
	fx := settleFixture(t)
	fx.fake.mu.Lock()
	fx.fake.inflight = 1
	fx.fake.mu.Unlock()

	runAbandonedFinish(t, fx)

	// The engine "finished" but the slot vanished from the listing: an
	// absent slot is unknown state, not confirmed idle.
	fx.fake.mu.Lock()
	fx.fake.inflight = 0
	fx.fake.slotsEmpty = true
	fx.fake.mu.Unlock()

	if err := fx.rc.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fx.rc.release()
	_, _, err := fx.rc.Prepare(context.Background(), "/v1/chat/completions", map[string]any{
		"model":    "cached",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	if err == nil {
		t.Fatal("an absent slot must not count as confirmed idle")
	}
}

func TestUnsettledSlotRecoversOnceIdleConfirmed(t *testing.T) {
	fx := settleFixture(t)
	fx.fake.mu.Lock()
	fx.fake.inflight = 1
	fx.fake.mu.Unlock()

	runAbandonedFinish(t, fx)

	// The engine finishes on its own; the next request's re-confirmation
	// sees the slot idle in the listing and traffic resumes.
	fx.fake.mu.Lock()
	fx.fake.inflight = 0
	fx.fake.mu.Unlock()

	if err := fx.rc.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	plan, out, err := fx.rc.Prepare(context.Background(), "/v1/chat/completions", map[string]any{
		"model":    "cached",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	fx.rc.release()
	if err != nil {
		t.Fatalf("Prepare should recover once the slot reports idle: %v", err)
	}
	if plan == nil || len(out) == 0 {
		t.Fatal("expected a forwarded plan after recovery")
	}
	if fx.rc.summary().Status != "ready" {
		t.Fatalf("cache should stay ready after recovery: %+v", fx.rc.summary())
	}
}

func TestSettledAbandonmentKeepsTrafficFlowing(t *testing.T) {
	fx := settleFixture(t)
	// Client left but the engine already stopped: the settle wait confirms
	// idle and no barrier is raised.
	runAbandonedFinish(t, fx)
	if err := fx.rc.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer fx.rc.release()
	_, _, err := fx.rc.Prepare(context.Background(), "/v1/chat/completions", map[string]any{
		"model":    "cached",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	if err != nil {
		t.Fatalf("traffic after a confirmed-idle abandonment must flow: %v", err)
	}
}
