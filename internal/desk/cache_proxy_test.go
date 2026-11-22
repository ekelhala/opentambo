package desk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLlama is a deterministic stand-in for llama-server's control and
// generation APIs used to exercise the coordinator through the proxy. The
// fake tokenizer keeps byte prefixes stable so prefix matching behaves like
// the real engine for extended conversations, and generation always reports
// the token count that apply-template+tokenize would produce (parity).
type fakeLlama struct {
	t       *testing.T
	saveDir string

	mu            sync.Mutex
	files         map[string][]byte
	slotTokens    int
	inflight      int
	maxInflight   int
	saveCalls     int
	restoreCalls  int
	eraseCalls    int
	generations   []map[string]any
	rawBodies     [][]byte
	failSave      bool
	failRestore   bool
	savePartial   bool          // write partial bytes to the temp file before failing
	saveSlow      time.Duration // stall the save request (simulates an abandoned write)
	saveNOverride int           // when set, report this n_saved instead of the token count
	slotsFail     bool          // /slots returns an error (control API unreachable-ish)
	slotsEmpty    bool          // /slots lists no slots at all (owned slot absent)
}

func (f *fakeLlama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var fields map[string]any
	_ = json.Unmarshal(body, &fields)
	// Proxy-forwarded requests carry a model name; capture the exact bytes so
	// tests can assert on byte-level fidelity (JSON integers, field order of
	// unknown fields).
	if fields != nil {
		if _, hasModel := fields["model"]; hasModel {
			f.mu.Lock()
			f.rawBodies = append(f.rawBodies, append([]byte(nil), body...))
			f.mu.Unlock()
		}
	}
	switch r.URL.Path {
	case "/props":
		writeJSON(w, map[string]any{"build_info": "12345 abcdef", "total_slots": 1})
	case "/slots":
		f.mu.Lock()
		processing := f.inflight > 0
		fail := f.slotsFail
		empty := f.slotsEmpty
		f.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":true,"message":"slots unavailable"}`)
			return
		}
		if empty {
			writeJSON(w, []map[string]any{})
			return
		}
		writeJSON(w, []map[string]any{{"id": 0, "n_ctx": 4096, "is_processing": processing}})
	case "/apply-template":
		writeJSON(w, map[string]string{"prompt": fakeRender(fields)})
	case "/tokenize":
		content, _ := fields["content"].(string)
		writeJSON(w, map[string]any{"tokens": fakeTokenize(content)})
	case "/v1/chat/completions", "/v1/completions":
		f.generate(w, r, fields)
	case "/v1/embeddings":
		writeJSON(w, map[string]any{"object": "list", "data": []map[string]any{{"embedding": []float64{0.1, 0.2}}}})
	default:
		if strings.HasPrefix(r.URL.Path, "/slots/") {
			f.slotAction(w, r, fields)
			return
		}
		http.NotFound(w, r)
	}
}

func (f *fakeLlama) generate(w http.ResponseWriter, r *http.Request, fields map[string]any) {
	prompt := fakeRender(fields)
	if raw, ok := fields["prompt"].(string); ok {
		prompt = raw
	}
	tokens := fakeTokenize(prompt)
	f.mu.Lock()
	f.inflight++
	if f.inflight > f.maxInflight {
		f.maxInflight = f.inflight
	}
	f.mu.Unlock()
	select {
	case <-time.After(15 * time.Millisecond):
	case <-r.Context().Done():
	}
	f.mu.Lock()
	f.inflight--
	f.slotTokens = len(tokens)
	// Only proxy-forwarded requests carry a model name; the probe's own
	// generation (without one) must not confuse forwarded-body assertions.
	if _, hasModel := fields["model"]; hasModel {
		f.generations = append(f.generations, fields)
	}
	truncate, _ := fields["fake_truncate"].(bool)
	fail, _ := fields["fake_fail"].(bool)
	f.mu.Unlock()
	if fail {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"error":{"message":"backend failure"}}`)
		return
	}
	if stream, _ := fields["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		if !truncate {
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}
		return
	}
	writeJSON(w, map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
		"usage":   map[string]any{"prompt_tokens": len(tokens), "completion_tokens": 1},
	})
}

func (f *fakeLlama) slotAction(w http.ResponseWriter, r *http.Request, fields map[string]any) {
	action := r.URL.Query().Get("action")
	switch action {
	case "save":
		name, _ := fields["filename"].(string)
		f.mu.Lock()
		f.saveCalls++
		fail := f.failSave
		tokens := f.slotTokens
		partial := f.savePartial
		slow := f.saveSlow
		override := f.saveNOverride
		f.mu.Unlock()
		if fail {
			w.WriteHeader(500)
			return
		}
		if name == "" || filepath.Base(name) != name {
			w.WriteHeader(400)
			return
		}
		if slow > 0 {
			// A stalled save: partial bytes land immediately, then the
			// request never completes before the caller's deadline.
			contents := []byte(fmt.Sprintf("snapshot:%d", tokens))
			_ = os.WriteFile(filepath.Join(f.saveDir, name), contents[:len(contents)/2], 0600)
			select {
			case <-time.After(slow):
			case <-r.Context().Done():
			}
			return
		}
		contents := []byte(fmt.Sprintf("snapshot:%d", tokens))
		if partial {
			// A crashed writer: partial bytes on disk, then a failure.
			_ = os.WriteFile(filepath.Join(f.saveDir, name), contents[:len(contents)/2], 0600)
			w.WriteHeader(500)
			return
		}
		if err := os.WriteFile(filepath.Join(f.saveDir, name), contents, 0600); err != nil {
			w.WriteHeader(500)
			return
		}
		f.mu.Lock()
		f.files[name] = contents
		f.mu.Unlock()
		n := tokens
		if override > 0 {
			n = override
		}
		writeJSON(w, map[string]any{"n_saved": n, "n_written": len(contents), "timings": map[string]any{"save_ms": 1.5}})
	case "restore":
		name, _ := fields["filename"].(string)
		f.mu.Lock()
		f.restoreCalls++
		fail := f.failRestore
		f.mu.Unlock()
		contents, err := os.ReadFile(filepath.Join(f.saveDir, name))
		if fail || err != nil {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"Unable to restore slot"}`)
			return
		}
		var tokens int
		_, _ = fmt.Sscanf(string(contents), "snapshot:%d", &tokens)
		f.mu.Lock()
		f.slotTokens = tokens
		f.mu.Unlock()
		writeJSON(w, map[string]any{"n_restored": tokens, "timings": map[string]any{"restore_ms": 1.0}})
	case "erase":
		f.mu.Lock()
		f.eraseCalls++
		f.slotTokens = 0
		f.mu.Unlock()
		writeJSON(w, map[string]any{"n_erased": 1})
	default:
		http.NotFound(w, r)
	}
}

func fakeRender(fields map[string]any) string {
	var out strings.Builder
	if messages, ok := fields["messages"].([]any); ok {
		for _, item := range messages {
			message, _ := item.(map[string]any)
			role, _ := message["role"].(string)
			content, _ := message["content"].(string)
			fmt.Fprintf(&out, "<|%s|>%s\n", role, content)
		}
		out.WriteString("<|assistant|>")
	}
	return out.String()
}

func fakeTokenize(content string) []int32 {
	tokens := make([]int32, 0, len(content)+1)
	tokens = append(tokens, 1) // beginning-of-text
	for i := 0; i < len(content); i++ {
		tokens = append(tokens, int32(100+content[i]))
	}
	return tokens
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

type cacheFixture struct {
	app  *App
	rc   *RunCache
	fake *fakeLlama
	url  string
}

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	a := testApp(t, "unused")
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeLlama{t: t, saveDir: store.dir, files: map[string][]byte{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	_, portString, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	spec := PromptCache{Enabled: true, MaxBytes: 1 << 20, MaxEntryBytes: 1 << 19, MinReuseTokens: 2}
	rc := newRunCache(spec, "ns", newLlamaClient(port, a.cacheClient), store)
	ctx := context.Background()
	if err := rc.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	rc.Probe(ctx)
	rc.release()
	if summary := rc.summary(); summary.Status != "ready" {
		t.Fatalf("probe against the fake server failed: %s (%s)", summary.Status, summary.Reason)
	}
	run := &Run{ID: "run-1", Launch: Launch{Name: "cached", Port: port}, Status: "running", Started: time.Now(), ctx: ctx, log: &logBuffer{}, cache: rc}
	a.mu.Lock()
	a.runs[run.ID] = run
	a.cacheStores[store.dir] = store
	a.mu.Unlock()
	desk := httptest.NewServer(a.Handler())
	t.Cleanup(desk.Close)
	return &cacheFixture{app: a, rc: rc, fake: fake, url: desk.URL}
}

// post sends one gateway request and returns its status code. The body is
// drained here so callers never hold an open response across the settle.
func (fx *cacheFixture) post(t *testing.T, body string) int {
	t.Helper()
	res, err := http.Post(fx.url+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_, _ = io.ReadAll(res.Body)
	// Snapshot publication happens after the response is flushed but before
	// the lease is released, so taking and returning the lease proves the
	// previous request fully finished its cache bookkeeping.
	fx.settle(t)
	return res.StatusCode
}

// settle proves the run's current coordinator finished its post-response
// bookkeeping: the lease is released only after Finish completes.
func (fx *cacheFixture) settle(t *testing.T) {
	t.Helper()
	fx.app.mu.Lock()
	rc := fx.app.runs["run-1"].cache
	fx.app.mu.Unlock()
	if rc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rc.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	rc.release()
}

func chatBody(messages string, extra ...string) string {
	if len(extra) > 0 {
		return fmt.Sprintf(`{"model":"cached","messages":%s,%s}`, messages, strings.Join(extra, ","))
	}
	return fmt.Sprintf(`{"model":"cached","messages":%s}`, messages)
}

func TestCacheProbeGatesOnParityAndSnapshots(t *testing.T) {
	fx := newCacheFixture(t)
	summary := fx.rc.summary()
	if summary.Status != "ready" || summary.Namespace != "ns" {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if summary.Build != "12345 abcdef" {
		t.Fatalf("build not recorded: %+v", summary)
	}
	// The probe saved, restored, and erased on slot 0 and cleaned its temp file.
	if fx.fake.saveCalls == 0 || fx.fake.restoreCalls == 0 || fx.fake.eraseCalls == 0 {
		t.Fatalf("probe did not exercise slot snapshots: %+v", fx.fake)
	}
	leftovers, _ := filepath.Glob(filepath.Join(fx.fake.saveDir, "*.tmp"))
	if len(leftovers) != 0 {
		t.Fatalf("probe left temp files: %v", leftovers)
	}
}

func TestProbeRejectsParityMismatch(t *testing.T) {
	a := testApp(t, "unused")
	dir := filepath.Join(t.TempDir(), "ns")
	store, err := newCacheStore(dir, "ns", 1<<20, 1<<19, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The handler reports a different prompt token count than the
	// apply-template+tokenize pipeline produces.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			writeJSON(w, map[string]any{"build_info": "b"})
		case "/slots":
			writeJSON(w, []map[string]any{{"id": 0, "n_ctx": 4096}})
		case "/apply-template":
			writeJSON(w, map[string]string{"prompt": "same prompt"})
		case "/tokenize":
			writeJSON(w, map[string]any{"tokens": fakeTokenize("same prompt")})
		case "/v1/chat/completions":
			writeJSON(w, map[string]any{"usage": map[string]any{"prompt_tokens": 999999}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	_, portString, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	rc := newRunCache(PromptCache{Enabled: true}, "ns", newLlamaClient(port, a.cacheClient), store)
	rc.Probe(context.Background())
	summary := rc.summary()
	if summary.Status != "unsupported" || !strings.Contains(summary.Reason, "parity") {
		t.Fatalf("parity mismatch not detected: %+v", summary)
	}
}

func TestCachePublishesAfterSuccessfulGeneration(t *testing.T) {
	fx := newCacheFixture(t)
	status := fx.post(t, chatBody(`[{"role":"user","content":"first question"}]`))
	if status != 200 {
		t.Fatalf("generation failed: %d", status)
	}
	summary := fx.rc.summary()
	if summary.Stats.Saves != 1 || summary.Stats.RestoreFailures != 0 {
		t.Fatalf("snapshot not published: %+v", summary.Stats)
	}
	entries, _, _ := fx.rc.store.Summary()
	if len(entries) != 1 || entries[0].PrefixTokens == 0 {
		t.Fatalf("store state: %+v", entries)
	}
	// The forwarded body carried the manager-owned slot and cache control.
	fx.fake.mu.Lock()
	forwarded := fx.fake.generations[0]
	fx.fake.mu.Unlock()
	if forwarded["id_slot"] != float64(0) || forwarded["cache_prompt"] != true {
		t.Fatalf("manager controls not injected: %+v", forwarded)
	}
}

func TestCacheRestoresAfterManagerRestart(t *testing.T) {
	fx := newCacheFixture(t)
	first := chatBody(`[{"role":"user","content":"remember this conversation"}]`)
	fx.post(t, first)
	// Simulate a opentambo restart against the same store and child server.
	restarted := newRunCache(fx.rc.spec, "ns", fx.rc.client, fx.rc.store)
	restarted.status = "ready"
	restarted.nctx = 4096
	fx.app.mu.Lock()
	fx.app.runs["run-1"].cache = restarted
	fx.app.mu.Unlock()
	fx.post(t, first)
	summary := restarted.summary()
	if summary.Stats.Hits != 1 || summary.Stats.Restores != 1 {
		t.Fatalf("expected one restore hit after restart: %+v", summary.Stats)
	}
}

func TestCacheKeepsLiveStateWhenAtLeastAsUseful(t *testing.T) {
	fx := newCacheFixture(t)
	first := chatBody(`[{"role":"user","content":"hello there"}]`)
	fx.post(t, first)
	restores := fx.fake.restoreCalls
	// Same live prefix: restoring from disk would be strictly worse.
	fx.post(t, first)
	if fx.fake.restoreCalls != restores {
		t.Fatal("restored from disk while live state covered the prompt")
	}
	if kept := fx.rc.summary().Stats.LiveKept; kept != 1 {
		t.Fatalf("live-keep not counted: %d", kept)
	}
}

func TestCacheHonorsClientBypassAndSkipsPublication(t *testing.T) {
	fx := newCacheFixture(t)
	fx.post(t, chatBody(`[{"role":"user","content":"private turn"}]`, `"cache_prompt":false`))
	summary := fx.rc.summary()
	if summary.Stats.Saves != 0 || len(fx.rc.store.Candidates()) != 0 {
		t.Fatalf("bypassed request published: %+v", summary.Stats)
	}
	fx.fake.mu.Lock()
	forwarded := fx.fake.generations[0]
	fx.fake.mu.Unlock()
	if forwarded["cache_prompt"] != false {
		t.Fatal("cache_prompt:false was rewritten")
	}
}

func TestCacheIneligibleEndpointStillRuns(t *testing.T) {
	fx := newCacheFixture(t)
	res, err := http.Post(fx.url+"/v1/embeddings", "application/json", strings.NewReader(`{"model":"cached","input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("ineligible request broke: %d", res.StatusCode)
	}
	if saves := fx.rc.summary().Stats.Saves; saves != 0 {
		t.Fatal("ineligible endpoint published a snapshot")
	}
}

func TestCacheNeverPublishesFailedOrTruncatedResponses(t *testing.T) {
	fx := newCacheFixture(t)
	status := fx.post(t, chatBody(`[{"role":"user","content":"will fail"}]`, `"fake_fail":true`))
	if status != 500 {
		t.Fatalf("upstream failure hidden: %d", status)
	}
	if saves := fx.rc.summary().Stats.Saves; saves != 0 {
		t.Fatal("published after a failed response")
	}
	// A truncated SSE stream (no [DONE]) must not publish either.
	res, err := http.Post(fx.url+"/v1/chat/completions", "application/json", strings.NewReader(chatBody(`[{"role":"user","content":"streamed"}]`, `"stream":true,"fake_truncate":true`)))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(res.Body)
	res.Body.Close()
	fx.settle(t)
	if saves := fx.rc.summary().Stats.Saves; saves != 0 {
		t.Fatal("published after a truncated stream")
	}
	// A complete stream does publish.
	res, err = http.Post(fx.url+"/v1/chat/completions", "application/json", strings.NewReader(chatBody(`[{"role":"user","content":"streamed fully"}]`, `"stream":true`)))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(res.Body)
	res.Body.Close()
	fx.settle(t)
	if saves := fx.rc.summary().Stats.Saves; saves != 1 {
		t.Fatalf("complete stream should publish: %+v", fx.rc.summary().Stats)
	}
}

func TestCacheFailedRestoreStillServesRequest(t *testing.T) {
	fx := newCacheFixture(t)
	fx.post(t, chatBody(`[{"role":"user","content":"restore me later"}]`))
	restarted := newRunCache(fx.rc.spec, "ns", fx.rc.client, fx.rc.store)
	restarted.status = "ready"
	restarted.nctx = 4096
	fx.app.mu.Lock()
	fx.app.runs["run-1"].cache = restarted
	fx.app.mu.Unlock()
	fx.fake.mu.Lock()
	fx.fake.failRestore = true
	fx.fake.mu.Unlock()
	status := fx.post(t, chatBody(`[{"role":"user","content":"restore me later"}]`))
	if status != 200 {
		t.Fatalf("a failed restore must fall back to cold generation: %d", status)
	}
	summary := restarted.summary()
	if summary.Stats.RestoreFailures == 0 || summary.Stats.Hits != 0 {
		t.Fatalf("restore failure not recorded: %+v", summary.Stats)
	}
	if fx.fake.eraseCalls == 0 {
		t.Fatal("slot was not cleared after a failed restore")
	}
}

func TestCacheSerializesSlotTraffic(t *testing.T) {
	fx := newCacheFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := http.Post(fx.url+"/v1/chat/completions", "application/json", strings.NewReader(chatBody(fmt.Sprintf(`[{"role":"user","content":"concurrent %d"}]`, i))))
			if err == nil {
				_, _ = io.ReadAll(res.Body)
				res.Body.Close()
			} else {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	fx.settle(t)
	fx.fake.mu.Lock()
	maxInflight := fx.fake.maxInflight
	fx.fake.mu.Unlock()
	if maxInflight != 1 {
		t.Fatalf("coordinator allowed %d generations at once", maxInflight)
	}
	if saves := fx.rc.summary().Stats.Saves; saves != 4 {
		t.Fatalf("expected four published snapshots: %+v", fx.rc.summary().Stats)
	}
}

func TestCacheUnavailableStatusKeepsPlainPath(t *testing.T) {
	fx := newCacheFixture(t)
	fx.rc.mu.Lock()
	fx.rc.status = "unsupported"
	fx.rc.reason = "simulated"
	fx.rc.mu.Unlock()
	status := fx.post(t, chatBody(`[{"role":"user","content":"plain"}]`))
	if status != 200 {
		t.Fatalf("plain path broke: %d", status)
	}
	if saves := fx.rc.summary().Stats.Saves; saves != 0 {
		t.Fatal("unsupported cache published")
	}
	// The managed slot pin still applies so a later enable sees a clean slot.
	fx.fake.mu.Lock()
	forwarded := fx.fake.generations[0]
	fx.fake.mu.Unlock()
	if forwarded["id_slot"] != float64(0) {
		t.Fatal("slot pin missing while unsupported")
	}
}

func TestCacheAPIManagementEndpoints(t *testing.T) {
	fx := newCacheFixture(t)
	fx.post(t, chatBody(`[{"role":"user","content":"managed entry"}]`))
	rec := request(t, fx.app, "GET", "/api/cache", "")
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var listing struct {
		Namespaces []struct {
			Namespace string             `json:"namespace"`
			Entries   []PublicCacheEntry `json:"entries"`
			BytesUsed int64              `json:"bytes_used"`
			Runs      []struct {
				RunID string `json:"run_id"`
			} `json:"runs"`
		} `json:"namespaces"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Namespaces) != 1 || len(listing.Namespaces[0].Entries) != 1 {
		t.Fatalf("unexpected listing: %s", rec.Body.String())
	}
	entry := listing.Namespaces[0].Entries[0]
	if entry.PrefixTokens == 0 || entry.Bytes == 0 || len(listing.Namespaces[0].Runs) != 1 {
		t.Fatalf("entry/association missing: %s", rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "snapshot:") || strings.Contains(body, "prompt_tokens\":[") {
		t.Fatalf("management view leaked private data: %s", body)
	}
	rec = request(t, fx.app, "DELETE", "/api/cache/"+entry.ID, "")
	if rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	rec = request(t, fx.app, "DELETE", "/api/cache/"+entry.ID, "")
	if rec.Code != 404 {
		t.Fatalf("double delete: %d", rec.Code)
	}
	rec = request(t, fx.app, "GET", "/api/state", "")
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"cache"`)) {
		t.Fatal("run state lost cache summary")
	}
	rec = request(t, fx.app, "POST", "/api/cache/prune", `{}`)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
}

// A/B/A regression: a long unrelated conversation B must not suppress the
// valid disk restoration of A. The live-keep decision must compare the
// verified live token prefix, not its length.
func TestCacheABARestoresDespiteUnrelatedLiveState(t *testing.T) {
	cases := []struct {
		name       string
		bContent   string
		expectLive bool
		expectHit  bool
	}{
		{"B longer than A", strings.Repeat("unrelated B ", 20), false, true},
		{"B same length as A", "completely different", false, true},
		{"B shorter than A", "tiny", false, true},
		{"live unchanged control", "", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newCacheFixture(t)
			a := chatBody(`[{"role":"user","content":"conversation A"}]`)
			fx.post(t, a)
			if len(fx.rc.store.Candidates()) == 0 {
				t.Fatal("A was never published")
			}
			restoresBefore := fx.rc.summary().Stats.Restores
			if tc.bContent != "" {
				fx.post(t, chatBody(fmt.Sprintf(`[{"role":"user","content":%q}]`, tc.bContent)))
			}
			status := fx.post(t, a)
			if status != 200 {
				t.Fatalf("request failed: %d", status)
			}
			summary := fx.rc.summary()
			switch {
			case tc.expectHit && summary.Stats.Restores == restoresBefore:
				t.Fatalf("A snapshot never restored after unrelated B: %+v", summary.Stats)
			case tc.expectHit && summary.Stats.Hits == 0:
				t.Fatalf("restore did not count as a hit: %+v", summary.Stats)
			case !tc.expectHit && summary.Stats.Restores != restoresBefore:
				t.Fatalf("unchanged live state was wrongly replaced by a restore: %+v", summary.Stats)
			case !tc.expectHit && summary.Stats.LiveKept == 0:
				t.Fatalf("expected a live-keep decision: %+v", summary.Stats)
			}
		})
	}
}

// A live prefix that genuinely covers the incoming prompt must still win
// over a shorter disk snapshot.
func TestCacheKeepsLiveOnlyWhenPrefixActuallyMatches(t *testing.T) {
	fx := newCacheFixture(t)
	short := chatBody(`[{"role":"user","content":"ab"}]`)
	fx.post(t, short) // publishes the short prefix, live = short tokens
	extended := chatBody(`[{"role":"user","content":"abcdefghijklmnop"}]`)
	fx.post(t, extended)
	// The live slot now holds the extended conversation. Posting it again
	// must keep live (its prefix covers the request), never restore.
	restores := fx.rc.summary().Stats.Restores
	fx.post(t, extended)
	if got := fx.rc.summary().Stats.Restores; got != restores {
		t.Fatal("restored from disk while the live prefix already matched")
	}
}

// Idle-then-resume: after the retention window passes without maintenance,
// the next matching request must go cold instead of restoring (and Touching)
// the expired snapshot.
func TestCacheExpiredSnapshotNotRestoredAfterIdle(t *testing.T) {
	fx := newCacheFixture(t)
	first := chatBody(`[{"role":"user","content":"aging conversation"}]`)
	fx.post(t, first)
	if len(fx.rc.store.Candidates()) == 0 {
		t.Fatal("nothing published")
	}
	// A manager restart loses the live slot; the next owner consults disk.
	restarted := newRunCache(fx.rc.spec, "ns", fx.rc.client, fx.rc.store)
	restarted.status = "ready"
	restarted.nctx = 4096
	fx.app.mu.Lock()
	fx.app.runs["run-1"].cache = restarted
	fx.app.mu.Unlock()
	// Idle far past the configured retention without any prune pass.
	fx.rc.spec.TTLSeconds = 60
	fx.rc.store.ttl = time.Minute
	fx.rc.store.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	status := fx.post(t, first)
	if status != 200 {
		t.Fatalf("request failed: %d", status)
	}
	summary := restarted.summary()
	if summary.Stats.Restores != 0 || summary.Stats.Hits != 0 {
		t.Fatalf("expired snapshot was restored: %+v", summary.Stats)
	}
	if summary.Stats.Misses == 0 {
		t.Fatalf("expired lookup did not count a miss: %+v", summary.Stats)
	}
}
