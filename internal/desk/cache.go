package desk

// Per-run prompt-cache coordinator. A cache-enabled run serializes all of its
// inference traffic through a lease so restore/generate/save can never
// interleave, picks the best reusable state (live slot, disk snapshot, or
// cold), and publishes new snapshots after successful generations. Cache
// maintenance problems never replace an already successful inference
// response; they are reported through run state and /api/cache instead.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// PromptCache is the launch-level configuration. The feature defaults off;
// when disabled nothing in the inference path changes.
type PromptCache struct {
	Enabled        bool  `json:"enabled"`
	MaxBytes       int64 `json:"max_bytes,omitempty"`
	MaxEntryBytes  int64 `json:"max_entry_bytes,omitempty"`
	TTLSeconds     int64 `json:"ttl_seconds,omitempty"`
	MinReuseTokens int   `json:"min_reuse_tokens,omitempty"`
}

const (
	defaultCacheMaxBytes       = 4 << 30
	defaultCacheMaxEntryBytes  = 2 << 30
	defaultCacheTTLSeconds     = 7 * 24 * 3600
	defaultCacheMinReuseTokens = 256
	// cacheLeaseWait bounds how long a request queues behind the slot owner.
	cacheLeaseWait = 10 * time.Minute
	// cacheSaveWait bounds post-response snapshot work before it is abandoned.
	cacheSaveWait = 60 * time.Second
	// cacheSettleWait bounds waiting for the slot to go idle after a
	// disconnect or cancellation before releasing the lease.
	cacheSettleWait = 5 * time.Second
)

func (c *PromptCache) normalized() PromptCache {
	spec := PromptCache{Enabled: c.Enabled}
	spec.MaxBytes = defaultCacheMaxBytes
	if c.MaxBytes > 0 {
		spec.MaxBytes = c.MaxBytes
	}
	spec.MaxEntryBytes = defaultCacheMaxEntryBytes
	if c.MaxEntryBytes > 0 {
		spec.MaxEntryBytes = c.MaxEntryBytes
	}
	spec.TTLSeconds = defaultCacheTTLSeconds
	if c.TTLSeconds > 0 {
		spec.TTLSeconds = c.TTLSeconds
	}
	spec.MinReuseTokens = defaultCacheMinReuseTokens
	if c.MinReuseTokens > 0 {
		spec.MinReuseTokens = c.MinReuseTokens
	}
	return spec
}

// validate rejects nonsensical configurations before a child starts.
func (c *PromptCache) validate() error {
	if c == nil || !c.Enabled {
		return nil
	}
	if c.MaxBytes < 0 || c.MaxEntryBytes < 0 || c.TTLSeconds < 0 || c.MinReuseTokens < 0 {
		return errors.New("prompt cache limits cannot be negative")
	}
	spec := c.normalized()
	if spec.MaxEntryBytes > spec.MaxBytes {
		return errors.New("prompt cache max_entry_bytes cannot exceed max_bytes")
	}
	if spec.MaxBytes < 1<<20 {
		return errors.New("prompt cache max_bytes must be at least 1 MiB")
	}
	return nil
}

// RunCache is the per-run coordinator and management summary.
type RunCache struct {
	mu sync.Mutex

	spec      PromptCache
	namespace string
	client    *llamaClient
	store     *cacheStore

	// lease is a one-slot channel that serializes slot-mutating traffic.
	lease chan struct{}

	status string // checking, ready, unsupported, error
	reason string
	build  string // llama-server build reported by the capability probe

	nctx int // slot 0 context size confirmed by the capability probe

	liveTokens int // verified prompt prefix live in slot 0, -1 = unknown

	stats CacheStats
}

type CacheStats struct {
	Requests        int64 `json:"requests"`
	Hits            int64 `json:"hits"` // a disk snapshot was restored and used
	Misses          int64 `json:"misses"`
	LiveKept        int64 `json:"live_kept"` // live state was at least as useful as disk
	Bypassed        int64 `json:"bypassed"`  // cache_prompt:false or ineligible traffic
	Saves           int64 `json:"saves"`
	SaveFailures    int64 `json:"save_failures"`
	Restores        int64 `json:"restores"`
	RestoreFailures int64 `json:"restore_failures"`
	QueueTimeouts   int64 `json:"queue_timeouts"`
	RestoreMsTotal  int64 `json:"restore_ms_total"`
	SaveMsTotal     int64 `json:"save_ms_total"`
	BytesRestored   int64 `json:"bytes_restored"`
	BytesSaved      int64 `json:"bytes_saved"`
}

// CacheSummary is the public (sanitized) view of coordinator state.
type CacheSummary struct {
	Requested bool       `json:"requested"`
	Status    string     `json:"status"`
	Reason    string     `json:"reason,omitempty"`
	Namespace string     `json:"namespace,omitempty"`
	Build     string     `json:"build,omitempty"`
	MinReuse  int        `json:"min_reuse_tokens,omitempty"`
	MaxBytes  int64      `json:"max_bytes,omitempty"`
	Stats     CacheStats `json:"stats"`
}

func newRunCache(spec PromptCache, namespace string, client *llamaClient, store *cacheStore) *RunCache {
	return &RunCache{spec: spec, namespace: namespace, client: client, store: store, lease: make(chan struct{}, 1), status: "checking", liveTokens: -1}
}

func (rc *RunCache) summary() CacheSummary {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return CacheSummary{Requested: true, Status: rc.status, Reason: rc.reason, Namespace: rc.namespace, Build: rc.build, MinReuse: rc.spec.MinReuseTokens, MaxBytes: rc.spec.MaxBytes, Stats: rc.stats}
}

// acquire takes the slot lease with cancellation and a bounded wait.
func (rc *RunCache) acquire(ctx context.Context) error {
	timer := time.NewTimer(cacheLeaseWait)
	defer timer.Stop()
	select {
	case rc.lease <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		rc.mu.Lock()
		rc.stats.QueueTimeouts++
		rc.mu.Unlock()
		return errors.New("timed out waiting for the model slot")
	}
}

func (rc *RunCache) release() {
	select {
	case <-rc.lease:
	default:
	}
}

// cachePlan describes what Prepare decided and what Finish must publish.
type cachePlan struct {
	request  *cacheRequest // nil when the request does not participate
	body     json.RawMessage
	bypass   bool // cache_prompt:false from the client or inactive cache
	entry    *CacheEntry
	restored bool
	publish  bool
}

// Prepare inspects the request and slot state and returns the (possibly
// rewritten) body to forward. The lease must be held.
func (rc *RunCache) Prepare(ctx context.Context, endpoint string, body map[string]any) (*cachePlan, json.RawMessage, error) {
	rc.mu.Lock()
	rc.stats.Requests++
	status := rc.status
	rc.mu.Unlock()

	clientBypass := false
	if value, present := body["cache_prompt"]; present && value == false {
		clientBypass = true
	}

	// Pin cache-enabled traffic to the managed slot so the coordinator owns
	// the one state that save/restore operate on.
	cacheActive := status == "ready"
	var req *cacheRequest
	if !clientBypass && cacheActive && eligibleCacheEndpoint(endpoint) {
		req = parseCacheRequest(endpoint, body)
	}

	if req == nil {
		// Ineligible, bypassed, or the cache is not ready: keep the original
		// path but still own the slot (the caller holds the lease) and forget
		// any live prefix because this traffic may have mutated it.
		rc.invalidateLive()
		out, err := withInternalControls(body, cacheActive)
		if err != nil {
			return nil, nil, err
		}
		return &cachePlan{body: out, bypass: true}, out, nil
	}

	// Derive the exact served token sequence through the child itself.
	if err := req.deriveTokens(ctx, rc.client); err != nil {
		// Token derivation failed: the request must still run. Fall back to
		// the plain path; live state is uncertain only if generation mutates
		// the slot, which Finish handles after forwarding.
		rc.markStat(func(s *CacheStats) { s.Bypassed++ })
		out, encErr := withInternalControls(body, cacheActive)
		if encErr != nil {
			return nil, nil, encErr
		}
		return &cachePlan{body: out, bypass: true}, out, nil
	}

	if rc.nctx > 0 && len(req.tokens) >= rc.nctx {
		// The engine may truncate; those cases are excluded from indexing.
		rc.invalidateLive()
		out, err := withInternalControls(body, cacheActive)
		if err != nil {
			return nil, nil, err
		}
		return &cachePlan{body: out, bypass: true}, out, nil
	}

	plan := &cachePlan{request: req, bypass: false, publish: true}

	// Choose between compatible disk candidates and verified live state.
	match := matchPrefix(rc.store.Candidates(), req.tokens)
	rc.mu.Lock()
	live := rc.liveTokens
	minReuse := rc.spec.MinReuseTokens
	rc.mu.Unlock()
	switch {
	case match == nil:
		// Cold: rely on live state if any (it cannot hurt; the engine itself
		// reuses an exact token prefix).
		plan.restored = false
	case live >= len(match.PromptTokens):
		// Live state is at least as useful as the best disk candidate.
		rc.markStat(func(s *CacheStats) { s.LiveKept++ })
		plan.restored = false
	case len(match.PromptTokens) < minReuse:
		// Restoring a large file for a negligible prefix is not worthwhile.
		rc.markStat(func(s *CacheStats) { s.Misses++ })
		plan.restored = false
	default:
		verified, err := rc.store.RestoreReady(match.ID)
		if err != nil {
			rc.markStat(func(s *CacheStats) { s.RestoreFailures++ })
			if clearErr := rc.client.eraseSlot(ctx, chatSlot); clearErr != nil {
				rc.invalidateLive()
				rc.mu.Lock()
				rc.status = "error"
				rc.reason = "failed to clear the slot after an unusable snapshot: " + clearErr.Error()
				rc.mu.Unlock()
				return nil, nil, fmt.Errorf("prompt cache: snapshot unusable and the slot could not be cleared safely")
			}
			match = nil
		} else {
			start := time.Now()
			res, restoreErr := rc.client.restoreSlot(ctx, chatSlot, verified.Filename)
			rc.store.Done(match.ID)
			if restoreErr != nil {
				rc.store.Touch(match.ID)
				rc.markStat(func(s *CacheStats) { s.RestoreFailures++ })
				rc.invalidateEntry(match.ID)
				if clearErr := rc.client.eraseSlot(ctx, chatSlot); clearErr != nil {
					rc.invalidateLive()
					rc.mu.Lock()
					rc.status = "error"
					rc.reason = "failed to clear the slot after a failed restore: " + clearErr.Error()
					rc.mu.Unlock()
					return nil, nil, fmt.Errorf("prompt cache: restore failed and the slot could not be cleared safely")
				}
				match = nil
			} else {
				elapsed := time.Since(start).Milliseconds()
				rc.mu.Lock()
				rc.stats.Restores++
				rc.stats.RestoreMsTotal += elapsed
				rc.stats.BytesRestored += verified.Bytes
				rc.stats.Hits++
				rc.mu.Unlock()
				rc.store.Touch(match.ID)
				plan.entry = match
				plan.restored = true
				_ = res
			}
		}
	}
	if match == nil && plan.entry == nil && req != nil {
		rc.markStat(func(s *CacheStats) { s.Misses++ })
	}

	out, err := withInternalControls(body, cacheActive)
	if err != nil {
		return nil, nil, err
	}
	plan.body = out
	return plan, out, nil
}

// Finish publishes the snapshot for a successful generation and settles slot
// bookkeeping before the lease is released. Errors here never change the
// client response; they update statistics and run state. The lease must be
// held when Finish starts.
func (rc *RunCache) Finish(cleanupCtx context.Context, plan *cachePlan, responseOK, clientComplete bool) {
	if plan == nil {
		return
	}
	if !plan.publish || !responseOK || !clientComplete {
		// Failed, aborted, or bypassed traffic leaves unknown slot state.
		rc.invalidateLive()
		if plan.bypass || !clientComplete {
			// Give the engine a moment to stop working on the slot.
			settleCtx, cancel := context.WithTimeout(context.Background(), cacheSettleWait)
			rc.client.waitSlotIdle(settleCtx, chatSlot)
			cancel()
		}
		return
	}
	req := plan.request
	rc.setLive(len(req.tokens))

	entryID := id()
	saveCtx, cancel := context.WithTimeout(context.Background(), cacheSaveWait)
	defer cancel()
	saved, err := rc.client.saveSlot(saveCtx, chatSlot, entryID+".tmp")
	if err != nil {
		rc.markStat(func(s *CacheStats) { s.SaveFailures++ })
		return
	}
	if saved.NSaved < len(req.tokens) {
		// The saved state does not provably cover the indexed prefix.
		rc.markStat(func(s *CacheStats) { s.SaveFailures++ })
		_ = rc.client.eraseSlot(saveCtx, chatSlot)
		return
	}
	if _, err := rc.store.Publish(entryID, req.tokens, saved.NSaved); err != nil {
		rc.markStat(func(s *CacheStats) { s.SaveFailures++ })
		return
	}
	rc.mu.Lock()
	rc.stats.Saves++
	rc.stats.SaveMsTotal += int64(saved.TimingsMs.SaveMs)
	rc.stats.BytesSaved += int64(saved.NWritten)
	rc.mu.Unlock()
}

func (rc *RunCache) invalidateLive() {
	rc.mu.Lock()
	rc.liveTokens = -1
	rc.mu.Unlock()
}

func (rc *RunCache) setLive(tokens int) {
	rc.mu.Lock()
	rc.liveTokens = tokens
	rc.mu.Unlock()
}

func (rc *RunCache) invalidateEntry(entryID string) {
	_, _ = rc.store.Delete(entryID)
}

func (rc *RunCache) markStat(f func(*CacheStats)) {
	rc.mu.Lock()
	f(&rc.stats)
	rc.mu.Unlock()
}

func secondsDuration(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// AwaitReady waits for the child to report healthy and then runs the
// capability probe while holding the slot lease. Until then (and permanently
// if the probe fails) requests keep the plain inference path.
func (rc *RunCache) AwaitReady(ctx context.Context) {
	healthCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	for {
		res, err := rc.client.doJSON(healthCtx, "GET", "/health", nil, controlResponseLimit)
		status := ""
		if err == nil {
			var health struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal(res, &health)
			status = health.Status
		}
		if status == "ok" {
			break
		}
		select {
		case <-healthCtx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
	if err := rc.acquire(healthCtx); err != nil {
		return
	}
	defer rc.release()
	probeCtx, probeCancel := context.WithTimeout(healthCtx, 5*time.Minute)
	defer probeCancel()
	rc.Probe(probeCtx)
}

// Probe verifies the child build can actually provide faithful prompts and
// working slot snapshots before any cache decision is made. It runs once
// after the child becomes healthy and holds the lease for its slot work.
//
// Gate (all must pass on the real child):
//   - GET /props and GET /slots answer; slot 0 exists.
//   - /apply-template + /tokenize reproduce the generation prompt token count
//     for a representative multi-turn request (and again with tools when the
//     request shape includes tools).
//   - save, restore, and erase on slot 0 succeed end-to-end.
func (rc *RunCache) Probe(ctx context.Context) {
	defer func() {
		rc.mu.Lock()
		if rc.status == "checking" {
			rc.status = "error"
			rc.reason = "probe did not complete"
		}
		rc.mu.Unlock()
	}()
	props, err := rc.client.props(ctx)
	if err != nil {
		rc.fail("cannot read server props: " + err.Error())
		return
	}
	buildInfo := props.BuildInfo
	slotList, err := rc.client.slots(ctx)
	if err != nil {
		rc.fail("cannot read server slots: " + err.Error())
		return
	}
	nctx := 0
	for _, s := range slotList {
		if s.ID == chatSlot {
			nctx = s.NCtx
		}
	}
	if nctx <= 0 {
		rc.fail("slot 0 is not available in this llama-server build")
		return
	}

	messages := []map[string]any{
		{"role": "system", "content": "You are a test assistant."},
		{"role": "user", "content": "Repeat the word llama once."},
		{"role": "assistant", "content": "llama"},
		{"role": "user", "content": "Say it again."},
	}
	probeBody := map[string]any{"messages": messages}
	projection, err := applyTemplateProjection(probeBody)
	if err != nil {
		rc.fail("probe projection: " + err.Error())
		return
	}
	prompt, err := rc.client.applyTemplate(ctx, projection)
	if err != nil {
		rc.fail("/apply-template is unavailable: " + err.Error())
		return
	}
	tokens, err := rc.client.tokenize(ctx, prompt)
	if err != nil {
		rc.fail("/tokenize is unavailable: " + err.Error())
		return
	}
	if len(tokens) == 0 {
		rc.fail("/tokenize returned no tokens")
		return
	}
	generated, err := rc.generateProbe(ctx, messages)
	if err != nil {
		rc.fail("probe generation failed: " + err.Error())
		return
	}
	if generated != len(tokens) {
		rc.fail(fmt.Sprintf("tokenization parity mismatch: apply-template+tokenize produced %d tokens but generation used %d", len(tokens), generated))
		return
	}

	tmpName := id() + ".tmp"
	saved, err := rc.client.saveSlot(ctx, chatSlot, tmpName)
	if err != nil {
		rc.fail("slot save failed: " + err.Error())
		return
	}
	if saved.NSaved <= 0 {
		rc.fail("slot save captured no tokens")
		return
	}
	if _, err = rc.client.restoreSlot(ctx, chatSlot, tmpName); err != nil {
		_ = rc.store.RemoveTemp(tmpName)
		rc.fail("slot restore failed: " + err.Error())
		return
	}
	if err = rc.client.eraseSlot(ctx, chatSlot); err != nil {
		_ = rc.store.RemoveTemp(tmpName)
		rc.fail("slot erase failed: " + err.Error())
		return
	}
	_ = rc.store.RemoveTemp(tmpName)

	rc.mu.Lock()
	rc.nctx = nctx
	rc.build = buildInfo
	rc.status = "ready"
	rc.reason = ""
	rc.liveTokens = -1
	rc.mu.Unlock()
}

// generateProbe returns the prompt token count that the generation endpoint
// itself reports for the same messages.
func (rc *RunCache) generateProbe(ctx context.Context, messages []map[string]any) (int, error) {
	body := map[string]any{"messages": messages, "max_tokens": 1, "temperature": 0, "id_slot": chatSlot}
	raw, err := rc.client.doJSON(ctx, "POST", "/v1/chat/completions", body, controlResponseLimit)
	if err != nil {
		return 0, err
	}
	var out struct {
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("probe usage: %w", err)
	}
	if out.Usage.PromptTokens <= 0 {
		return 0, errors.New("probe generation reported no prompt tokens")
	}
	return out.Usage.PromptTokens, nil
}

func (rc *RunCache) fail(reason string) {
	rc.mu.Lock()
	rc.status = "unsupported"
	rc.reason = reason
	rc.mu.Unlock()
}
