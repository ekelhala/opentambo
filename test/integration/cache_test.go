//go:build integration

package integration

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Long prompts with disjoint openings exercise prefix matching honestly:
// prompt B shares no token prefix with prompt A, so any reuse observed on
// the second A can only come from A's snapshot, never from engine-side
// continuation of B.
const promptA = "Here is a careful inventory of a small library. There is a blue notebook near the window. " +
	"The reading lamp hums quietly after sunset. Two shelves hold atlases, dictionaries, and almanacs. " +
	"A green armchair sits beside the radiator, worn soft on both armrests. The librarian keeps a tin of " +
	"mints in the top drawer, next to a brass key that opens the periodicals cabinet. Every Friday the " +
	"book club meets around the long oak table, arguing gently about endings. A crock of pencils by the " +
	"door is missing exactly one yellow No. 2. The clock above the returns desk loses a minute each week."

const promptB = "Zebra crossing patterns repeat along the boulevard outside the bakery. A delivery cyclist " +
	"rings the bell twice before the shutters lift. Pigeons negotiate over crumbs near the fountain. " +
	"The tram arrives seven minutes late again, brakes hissing against the platform edge. Somewhere a " +
	"radio plays a brass tune while paint dries on the new benches. Children chase a red ball between " +
	"the market stalls, laughing loudly, while their parents haggle over tomatoes and flatbread."

// timedCompletion runs a chat completion with per-token timings enabled.
// Reuse evidence must come from the backend's own counters: llama.cpp
// reports cache_n as the tokens skipped by KV reuse and prompt_n as only
// the tokens it actually evaluated, so the total prompt size comes from
// usage.prompt_tokens.
func (m *manager) timedCompletion(model, prompt string) (response map[string]any, cacheN, totalPrompt float64) {
	m.t.Helper()
	response = m.completion(model, prompt, map[string]any{"timings_per_token": true})
	timings, ok := response["timings"].(map[string]any)
	if !ok {
		m.t.Fatalf("response has no timings block: %v", response)
	}
	usage, ok := response["usage"].(map[string]any)
	if !ok {
		m.t.Fatalf("response has no usage block: %v", response)
	}
	return response, numeric(m.t, timings["cache_n"], "timings.cache_n"),
		numeric(m.t, usage["prompt_tokens"], "usage.prompt_tokens")
}

func assertWarm(t *testing.T, label string, cacheN, totalPrompt, cold float64) {
	t.Helper()
	if totalPrompt < 64 {
		t.Fatalf("%s: prompt is %.0f tokens, too short for a meaningful reuse proof", label, totalPrompt)
	}
	if cacheN < 64 || cacheN < totalPrompt/2 {
		t.Fatalf("%s: cache_n %.0f of %.0f prompt tokens does not evidence KV reuse (cold baseline %.0f)", label, cacheN, totalPrompt, cold)
	}
	t.Logf("%s: cache_n=%.0f of %.0f prompt tokens", label, cacheN, totalPrompt)
}

// TestPromptCacheReusesKVWithinARun is the A/B/A scenario from issue #19:
// a cold baseline for prompt A, an unrelated prompt B that clobbers the
// slot, then A again restoring from the published snapshot.
func TestPromptCacheReusesKVWithinARun(t *testing.T) {
	llama := ensureLlamaServer(t)
	m := startManager(t, llama)
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{"prompt_cache": map[string]any{"enabled": true, "min_reuse_tokens": 64}}
	run := m.launchRun("cached", port, spec)
	id, _ := run["id"].(string)
	summary := waitCacheReady(t, m, id)
	if status, _ := summary["status"].(string); status != "ready" {
		m.dumpLog()
		t.Fatalf("capability probe on the pinned build must be ready: %v", summary)
	}

	_, coldCache, coldPrompt := m.timedCompletion("cached", promptA)
	if coldCache > coldPrompt/4 {
		t.Fatalf("first run of A looks warm (cache_n %.0f of %.0f prompt tokens); baseline is unclean", coldCache, coldPrompt)
	}
	m.timedCompletion("cached", promptB)
	warmResponse, warmCache, warmPrompt := m.timedCompletion("cached", promptA)
	assertWarm(t, "A/B/A second A", warmCache, warmPrompt, coldCache)

	// The usage block mirrors the reuse on the restoring response itself
	// when the child reports details.
	if details := usageCachedTokens(t, warmResponse); details >= 0 && details == 0 {
		t.Errorf("usage reports cached_tokens 0 on a restoring run: %v", warmResponse["usage"])
	}

	// The coordinator's own counters agree with the backend.
	summary = waitCacheReady(t, m, id)
	st, _ := summary["stats"].(map[string]any)
	if hits := numeric(t, st["hits"], "cache stats hits"); hits < 1 {
		t.Errorf("cache summary shows no hits after reuse: %v", summary["stats"])
	}
	m.stopRun(id)
}

func usageCachedTokens(t *testing.T, response map[string]any) float64 {
	t.Helper()
	usage, _ := response["usage"].(map[string]any)
	details, ok := usage["prompt_tokens_details"].(map[string]any)
	if !ok {
		return -1
	}
	cached, ok := details["cached_tokens"]
	if !ok {
		return -1
	}
	return numeric(t, cached, "usage.prompt_tokens_details.cached_tokens")
}

// TestPromptCacheHybridModelProbeAndReuse is why the capability gate became
// measured instead of a static architecture list: LFM2 is a hybrid model
// (causal-convolution recurrent layers interleaved with attention) that the
// old allow-list would have rejected at launch even though the pinned
// llama.cpp build saves, restores, and reuses its recurrent memory. The
// probe must classify it ready from evidence, and reuse must survive a
// child restart — for a hybrid, the restored file carries the recurrent
// state, so warmth after restart can only come from disk.
func TestPromptCacheHybridModelProbeAndReuse(t *testing.T) {
	llama := ensureLlamaServer(t)
	m := startManager(t, llama)
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{
		"model":        hybridModelRelPath,
		"prompt_cache": map[string]any{"enabled": true, "min_reuse_tokens": 64},
	}
	run := m.launchRun("hybrid", port, spec)
	id, _ := run["id"].(string)
	summary := waitCacheReady(t, m, id)
	if status, _ := summary["status"].(string); status != "ready" {
		m.dumpLog()
		t.Fatalf("hybrid probe must classify measured reuse as ready: %v", summary)
	}

	_, coldCache, coldPrompt := m.timedCompletion("hybrid", promptA)
	if coldCache > coldPrompt/4 {
		t.Fatalf("hybrid cold baseline is not cold: cache_n %.0f of %.0f prompt tokens", coldCache, coldPrompt)
	}
	m.timedCompletion("hybrid", promptB)
	_, warmCache, warmPrompt := m.timedCompletion("hybrid", promptA)
	assertWarm(t, "hybrid A/B/A second A", warmCache, warmPrompt, coldCache)

	// Child restart: the recurrent memory is gone from RAM, so warmth here
	// proves the snapshot round-trips hybrid state, not just attention KV.
	m.stopRun(id)
	run = m.launchRun("hybrid", port, spec)
	id, _ = run["id"].(string)
	waitCacheReady(t, m, id)
	_, warmCache, warmPrompt = m.timedCompletion("hybrid", promptA)
	assertWarm(t, "hybrid after child restart", warmCache, warmPrompt, coldCache)
	m.stopRun(id)
}

// TestPromptCacheSurvivesChildAndManagerRestart proves the snapshot
// namespace is stable: the same model + binary + configuration finds the
// published snapshots after the child restarts and after OpenTambo itself
// restarts over the same data directory.
func TestPromptCacheSurvivesChildAndManagerRestart(t *testing.T) {
	llama := ensureLlamaServer(t)
	m := startManager(t, llama)
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{"prompt_cache": map[string]any{"enabled": true, "min_reuse_tokens": 64}}
	run := m.launchRun("durable", port, spec)
	id, _ := run["id"].(string)
	waitCacheReady(t, m, id)

	_, coldCache, coldPrompt := m.timedCompletion("durable", promptA)
	if coldCache > coldPrompt/4 {
		t.Fatalf("cold baseline is not cold: cache_n %.0f of %.0f prompt tokens", coldCache, coldPrompt)
	}
	m.stopRun(id)

	// Child restart (same manager): slot memory is gone, only disk remains.
	run = m.launchRun("durable", port, spec)
	id, _ = run["id"].(string)
	waitCacheReady(t, m, id)
	_, warmCache, warmPrompt := m.timedCompletion("durable", promptA)
	assertWarm(t, "after child restart", warmCache, warmPrompt, coldCache)
	m.stopRun(id)
	dataDir := m.dataDir
	m.stop()

	// OpenTambo restart over the same data directory.
	m2 := startManagerWith(t, llama, dataDir)
	if profiles, _ := m2.get("/api/state", http.StatusOK)["profiles"].(map[string]any); len(profiles) == 0 {
		t.Error("launch profiles did not survive the manager restart")
	}
	run = m2.launchRun("durable", port, spec)
	id, _ = run["id"].(string)
	waitCacheReady(t, m2, id)
	_, warmCache, warmPrompt = m2.timedCompletion("durable", promptA)
	assertWarm(t, "after manager restart", warmCache, warmPrompt, coldCache)
	m2.stopRun(id)
}

// TestCorruptedSnapshotFallsBackCleanly: a snapshot file corrupted on disk
// must degrade to a normal uncached completion, never a failed request or a
// wedged run.
func TestCorruptedSnapshotFallsBackCleanly(t *testing.T) {
	llama := ensureLlamaServer(t)
	m := startManager(t, llama)
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{"prompt_cache": map[string]any{"enabled": true, "min_reuse_tokens": 64}}
	run := m.launchRun("fragile", port, spec)
	id, _ := run["id"].(string)
	waitCacheReady(t, m, id)
	_, _, _ = m.timedCompletion("fragile", promptA)

	m.stopRun(id)
	corruptSnapshots(t, m.dataDir)

	run = m.launchRun("fragile", port, spec)
	id, _ = run["id"].(string)
	if !m.tryCompletion("fragile", promptA) {
		m.dumpLog()
		t.Fatal("request over a corrupted snapshot failed instead of falling back")
	}
	if status := runStatus(m, id); status != "running" {
		t.Fatalf("run state after corrupted-snapshot fallback: %s", status)
	}
	m.stopRun(id)
}

func corruptSnapshots(t *testing.T, dataDir string) {
	t.Helper()
	found := 0
	err := filepath.WalkDir(filepath.Join(dataDir, "prompt-cache"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".bin") {
			return nil
		}
		if writeErr := os.WriteFile(path, []byte("GARBAGEGARBAGEGARBAGE"), 0o600); writeErr != nil {
			return writeErr
		}
		found++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no snapshot files were published for the corruption scenario")
	}
}

func runStatus(m *manager, id string) string {
	for _, item := range runList(m.get("/api/state", http.StatusOK)) {
		if item["id"] == id {
			status, _ := item["status"].(string)
			return status
		}
	}
	return ""
}

// TestUnsupportedBuildStaysUncached injects a crippled llama-server stub
// whose control plane lacks slot snapshots. The capability probe must mark
// the cache unsupported while inference keeps flowing uncached. The reuse
// evidence itself lives in the real-build tests above.
func TestUnsupportedBuildStaysUncached(t *testing.T) {
	stub := buildStubServer(t)
	m := startManager(t, stub)
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	run := m.launchRun("stubbed", port, map[string]any{"prompt_cache": map[string]any{"enabled": true}})
	id, _ := run["id"].(string)
	summary := waitCacheReady(t, m, id)
	status, _ := summary["status"].(string)
	if status != "unsupported" {
		m.dumpLog()
		t.Fatalf("stub build must classify as unsupported, got %v", summary)
	}
	if reason, _ := summary["reason"].(string); reason == "" {
		t.Error("unsupported summary carries no reason")
	}
	completion := m.completion("stubbed", "hello", nil)
	choices, _ := completion["choices"].([]any)
	message, _ := choices[0].(map[string]any)
	inner, _ := message["message"].(map[string]any)
	if inner["content"] != "STUB-REPLY" {
		t.Errorf("uncached fallback did not reach the stub: %v", completion)
	}
	m.stopRun(id)
}

func buildStubServer(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "stub-server")
	cmd := exec.Command("go", "build", "-o", binary, "./test/integration/testdata/stub-server")
	cmd.Dir = repoRoot
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building stub server: %v\n%s", err, combined)
	}
	return binary
}
