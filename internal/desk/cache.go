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

	"github.com/ekelhala/opentambo/internal/desk/api"
)

// PromptCache is the launch-level configuration wire type generated from
// api/openapi.yaml. The feature defaults off; when disabled nothing in the
// inference path changes. TTLSeconds is the retention window since an
// entry's last successful use; expired entries are excluded from lookup
// immediately and removed by later maintenance.
type PromptCache = api.PromptCache

const (
	defaultCacheMaxBytes       = 4 << 30
	defaultCacheMaxEntryBytes  = 2 << 30
	defaultCacheTTLSeconds     = 7 * 24 * 3600
	defaultCacheMinReuseTokens = 256
	// cacheLeaseWait bounds how long a request queues behind the slot owner.
	cacheLeaseWait = 10 * time.Minute
)

var (
	// cacheSaveWait bounds post-response snapshot work before it is abandoned.
	// Tests shorten it to exercise the abandoned-write path.
	cacheSaveWait = 60 * time.Second
	// cacheSettleWait bounds waiting for the slot to go idle after a
	// disconnect or cancellation before releasing the lease.
	cacheSettleWait = 5 * time.Second
)

// builtinCacheDefaults reports the compiled-in limits used when no operator
// configuration supplies prompt_cache defaults.
func builtinCacheDefaults() api.CacheDefaults {
	return api.CacheDefaults{MaxBytes: defaultCacheMaxBytes, MaxEntryBytes: defaultCacheMaxEntryBytes, TTLSeconds: defaultCacheTTLSeconds, MinReuseTokens: defaultCacheMinReuseTokens}
}

// cacheDefaults is the operator-tuned source for limits applied to unset
// PromptCache options; manual mode (no configuration file) keeps the
// built-in values.
func (a *App) cacheDefaults() api.CacheDefaults { return a.cacheDef }

func normalizePromptCacheWith(defs api.CacheDefaults, c PromptCache) PromptCache {
	spec := PromptCache{Enabled: c.Enabled}
	spec.MaxBytes = defs.MaxBytes
	if c.MaxBytes > 0 {
		spec.MaxBytes = c.MaxBytes
	}
	spec.MaxEntryBytes = defs.MaxEntryBytes
	if c.MaxEntryBytes > 0 {
		spec.MaxEntryBytes = c.MaxEntryBytes
	}
	spec.TTLSeconds = defs.TTLSeconds
	if c.TTLSeconds > 0 {
		spec.TTLSeconds = c.TTLSeconds
	}
	spec.MinReuseTokens = defs.MinReuseTokens
	if c.MinReuseTokens > 0 {
		spec.MinReuseTokens = c.MinReuseTokens
	}
	return spec
}

func normalizedPromptCache(c PromptCache) PromptCache {
	return normalizePromptCacheWith(builtinCacheDefaults(), c)
}

// normalizedPromptCache resolves unset limits against the operator's
// configured defaults (issue #17).
func (a *App) normalizedPromptCache(c PromptCache) PromptCache {
	return normalizePromptCacheWith(a.cacheDef, c)
}

// validatePromptCache rejects nonsensical configurations before a child starts.
// validatePromptCache rejects nonsensical configurations before a child
// starts, resolving unset limits against the operator's configured defaults.
func (a *App) validatePromptCache(c *PromptCache) error {
	if c == nil || !c.Enabled {
		return nil
	}
	if c.MaxBytes < 0 || c.MaxEntryBytes < 0 || c.TTLSeconds < 0 || c.MinReuseTokens < 0 {
		return errors.New("prompt cache limits cannot be negative")
	}
	spec := a.normalizedPromptCache(*c)
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

	status api.CacheStatus // generated enum: checking, ready, unsupported, error
	reason string
	build  string // llama-server build reported by the capability probe

	nctx int // slot 0 context size confirmed by the capability probe

	liveTokens []int32 // verified prompt prefix live in slot 0; nil = unknown

	// slotUnsettled records that a generation was abandoned without the
	// control API confirming the slot went idle. Until idleness is re-
	// confirmed (or the child restarts), no further forwarding or restore
	// may touch the slot; a lost ownership barrier is unsafe traffic.
	slotUnsettled bool

	stats CacheStats
}

// CacheStats and CacheSummary are the generated wire types for coordinator
// statistics and the public (sanitized) view of coordinator state.
type CacheStats = api.CacheStats
type CacheSummary = api.CacheSummary

func newRunCache(spec PromptCache, namespace string, client *llamaClient, store *cacheStore) *RunCache {
	return &RunCache{spec: spec, namespace: namespace, client: client, store: store, lease: make(chan struct{}, 1), status: api.CacheStatusChecking}
}

func (rc *RunCache) summary() CacheSummary {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return CacheSummary{Requested: true, Status: rc.status, Reason: rc.reason, Namespace: rc.namespace, Build: rc.build, MinReuseTokens: rc.spec.MinReuseTokens, MaxBytes: rc.spec.MaxBytes, Stats: rc.stats}
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
	unsettled := rc.slotUnsettled
	rc.mu.Unlock()

	if unsettled {
		// A previous generation was abandoned without the control API
		// confirming the slot went idle. Attempt one re-confirmation inside
		// the request window; until the slot is proven idle again (or the
		// child restarts), no forwarding or restore may touch the slot.
		if rc.reconfirmSlotIdle(ctx) {
			return nil, nil, fmt.Errorf("prompt cache: slot %d may still be processing an abandoned generation", chatSlot)
		}
	}

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
	liveReusable := commonPrefixLen(live, req.tokens)
	switch {
	case match == nil:
		// Cold: rely on live state if any (it cannot hurt; the engine itself
		// reuses an exact token prefix).
		plan.restored = false
	case liveReusable >= len(match.PromptTokens):
		// The verified live prefix is at least as useful as the best disk
		// candidate. Identity matters: a long unrelated conversation shares
		// no prefix and must not suppress restoring a matching snapshot.
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
			// Give the engine a moment to stop working on the slot. An
			// unconfirmed idle slot is an ownership barrier: handing the
			// lease on while the child may still be generating (or while
			// the control API cannot answer) must block all further slot
			// traffic until idle is re-confirmed or the child restarts.
			settleCtx, cancel := context.WithTimeout(context.Background(), cacheSettleWait)
			if !rc.client.waitSlotIdle(settleCtx, chatSlot) {
				rc.mu.Lock()
				rc.slotUnsettled = true
				rc.mu.Unlock()
			}
			cancel()
		}
		return
	}
	req := plan.request
	rc.setLive(req.tokens)

	entryID := id()
	tmpName := entryID + ".tmp"
	// Reserve the write budget before llama-server produces the snapshot,
	// booked at the per-entry limit: the largest snapshot the store could
	// ever publish. Traffic that cannot fit inside the configured limits
	// with its worst-case write must never reach the volume as an
	// unaccounted temp file (issue #23).
	if !rc.store.BeginTempWrite(entryID, rc.store.TempEstimate()) {
		rc.markStat(func(s *CacheStats) { s.SaveFailures++ })
		return
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), cacheSaveWait)
	defer cancel()
	saved, err := rc.client.saveSlot(saveCtx, chatSlot, tmpName)
	if err != nil {
		// Remove whatever the partial write produced and release the
		// reservation together, in that order: releasing the bytes first
		// would authorize another write over a file still on disk.
		_ = rc.store.AbortTempWrite(entryID)
		rc.markStat(func(s *CacheStats) { s.SaveFailures++ })
		return
	}
	// The reservation stays live through Publish, which transfers it into
	// committed entry bytes; ending it here would let another run reserve
	// the same disk while the .tmp file still occupies it (issue #24).
	if saved.NSaved < len(req.tokens) {
		// The saved state does not provably cover the indexed prefix.
		_ = rc.store.AbortTempWrite(entryID)
		rc.markStat(func(s *CacheStats) { s.SaveFailures++ })
		_ = rc.client.eraseSlot(saveCtx, chatSlot)
		return
	}
	if _, err := rc.store.Publish(entryID, req.tokens, saved.NSaved); err != nil {
		// Publish releases the reservation when it hands the bytes to a
		// committed entry and leaves them accounted for when removal of a
		// rejected file failed; this covers the paths that never reached
		// the handoff (issue #24).
		_ = rc.store.AbortTempWrite(entryID)
		rc.markStat(func(s *CacheStats) { s.SaveFailures++ })
		return
	}
	rc.mu.Lock()
	rc.stats.Saves++
	rc.stats.SaveMsTotal += int64(saved.TimingsMs.SaveMs)
	rc.stats.BytesSaved += int64(saved.NWritten)
	rc.mu.Unlock()
}

// reconfirmSlotIdle re-checks whether the owned slot went idle after an
// unconfirmed settle. It returns false once idleness is confirmed (clearing
// the barrier) and true while the slot is still busy or unobservable.
func (rc *RunCache) reconfirmSlotIdle(ctx context.Context) bool {
	waitCtx, cancel := context.WithTimeout(ctx, cacheSettleWait)
	defer cancel()
	if rc.client.waitSlotIdle(waitCtx, chatSlot) {
		rc.mu.Lock()
		rc.slotUnsettled = false
		rc.liveTokens = nil
		rc.mu.Unlock()
		return false
	}
	return true
}

func (rc *RunCache) invalidateLive() {
	rc.mu.Lock()
	rc.liveTokens = nil
	rc.mu.Unlock()
}

func (rc *RunCache) setLive(tokens []int32) {
	rc.mu.Lock()
	rc.liveTokens = tokens
	rc.mu.Unlock()
}

// commonPrefixLen returns the length of the shared leading tokens. A nil live
// prefix (unknown state) shares nothing.
func commonPrefixLen(live, tokens []int32) int {
	n := len(live)
	if len(tokens) < n {
		n = len(tokens)
	}
	for i := 0; i < n; i++ {
		if live[i] != tokens[i] {
			return i
		}
	}
	return n
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

// probeReuseMinTokens is the absolute floor of prompt tokens the probe must
// see skipped by KV reuse after a save/erase/restore round-trip. The
// fraction-based floor in probeSnapshotReuse does the heavy lifting; this
// guards against a degenerate measurement on an unusually short prompt.
const probeReuseMinTokens = 8

// Probe verifies the child build can actually provide faithful prompts and
// working slot snapshots before any cache decision is made. It runs once
// after the child becomes healthy and holds the lease for its slot work.
//
// Gate (all must pass on the real child):
//   - GET /props and GET /slots answer; slot 0 exists.
//   - /apply-template + /tokenize reproduce the generation prompt token count
//     for a representative multi-turn request (and again with tools when the
//     request shape includes tools).
//   - a save/erase/restore round-trip on slot 0 measurably skips prompt
//     re-processing on a continuation request (probeSnapshotReuse).
//
// The reuse measurement is what makes capability evidence-based rather than
// architecture-based: hybrid/recurrent models (Qwen3-Next-style SSM hybrids,
// Mamba hybrids, sliding-window models) only pass when the engine can prove
// it restores their state, and older builds that restore without reusing are
// rejected here instead of silently re-prefilling every request.
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

	if reason := rc.probeSnapshotReuse(ctx, messages, len(tokens)); reason != "" {
		rc.fail(reason)
		return
	}

	rc.mu.Lock()
	rc.nctx = nctx
	rc.build = buildInfo
	rc.status = "ready"
	rc.reason = ""
	rc.liveTokens = nil
	rc.mu.Unlock()
}

// probeSnapshotReuse proves the child can round-trip slot state through a
// snapshot file and actually reuse it. It saves slot 0 (holding the probe
// conversation from generateProbe), erases the slot so no live KV or prompt
// cache survives, restores the file, and runs a continuation turn. The
// backend's own reuse counter (timings.cache_n) must show that a substantial
// prefix of the saved prompt was skipped. Because the slot was erased before
// the restore, any measured reuse can only come from the snapshot itself —
// this is the evidence gate for every architecture, including hybrid and
// recurrent models whose state save/restore support has historically been
// build- and model-dependent.
func (rc *RunCache) probeSnapshotReuse(ctx context.Context, messages []map[string]any, promptTokens int) string {
	tmpName := id() + ".tmp"
	saved, err := rc.client.saveSlot(ctx, chatSlot, tmpName)
	if err != nil {
		return "slot save failed: " + err.Error()
	}
	if saved.NSaved <= 0 {
		_ = rc.store.RemoveTemp(tmpName)
		return "slot save captured no tokens"
	}
	// Erase first: without this the continuation could reuse live engine
	// state and the measurement would prove nothing about the snapshot.
	if err = rc.client.eraseSlot(ctx, chatSlot); err != nil {
		_ = rc.store.RemoveTemp(tmpName)
		return "slot erase failed: " + err.Error()
	}
	if _, err = rc.client.restoreSlot(ctx, chatSlot, tmpName); err != nil {
		_ = rc.store.RemoveTemp(tmpName)
		if clearErr := rc.client.eraseSlot(ctx, chatSlot); clearErr != nil {
			return "slot restore failed and the slot could not be cleared: " + err.Error()
		}
		return "slot restore failed: " + err.Error()
	}
	_ = rc.store.RemoveTemp(tmpName)
	reuseCacheN, err := rc.probeContinuation(ctx, messages)
	clearErr := rc.client.eraseSlot(ctx, chatSlot)
	if err != nil {
		return "reuse probe generation failed: " + err.Error()
	}
	if clearErr != nil {
		return "slot erase failed: " + clearErr.Error()
	}
	if reuseCacheN < probeReuseMinTokens || reuseCacheN*2 < promptTokens {
		return fmt.Sprintf("snapshot restore did not reuse the prompt: %d of %d prompt tokens skipped by KV reuse (a build or architecture without validated state restore re-prefills instead)", reuseCacheN, promptTokens)
	}
	return ""
}

// probeContinuation runs the probe conversation extended with an assistant
// turn and a new user question — the agent-loop shape real traffic uses —
// and returns the number of prompt tokens the engine skipped through KV
// reuse (timings.cache_n). The continuation is pinned to the owned slot and
// asks for per-token timings so the response carries that counter.
func (rc *RunCache) probeContinuation(ctx context.Context, messages []map[string]any) (int, error) {
	turns := make([]map[string]any, 0, len(messages)+2)
	turns = append(turns, messages...)
	turns = append(turns,
		map[string]any{"role": "assistant", "content": "llama"},
		map[string]any{"role": "user", "content": "Say it again."})
	body := map[string]any{"messages": turns, "max_tokens": 1, "temperature": 0, "id_slot": chatSlot, "timings_per_token": true}
	raw, err := rc.client.doJSON(ctx, "POST", "/v1/chat/completions", body, controlResponseLimit)
	if err != nil {
		return 0, err
	}
	var out struct {
		Timings *struct {
			CacheN *float64 `json:"cache_n"`
		} `json:"timings"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("probe continuation: %w", err)
	}
	if out.Timings == nil || out.Timings.CacheN == nil {
		return 0, errors.New("server did not report timings.cache_n with timings_per_token")
	}
	return int(*out.Timings.CacheN), nil
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
