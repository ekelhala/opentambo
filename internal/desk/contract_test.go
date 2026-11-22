package desk

// Runtime schema conformance: live handler responses are validated against
// the authoritative contract in api/openapi.yaml. This is the teeth of the
// contract — a handler that drifts from the documented shape fails here even
// when no test asserts on the specific field.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ekelhala/opentambo/internal/desk/api"
	"github.com/getkin/kin-openapi/openapi3"
)

func loadContract(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("load api/openapi.yaml: %v", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("api/openapi.yaml does not validate: %v", err)
	}
	return doc
}

// validateResponse checks a recorded handler response (status + JSON body)
// against the documented response schema for the contract path template.
func validateResponse(t *testing.T, doc *openapi3.T, specPath, method string, status int, body []byte) {
	t.Helper()
	item := doc.Paths.Find(specPath)
	if item == nil {
		t.Fatalf("contract has no path %q", specPath)
	}
	op := item.GetOperation(strings.ToUpper(method))
	if op == nil {
		t.Fatalf("contract %q has no %s operation", specPath, method)
	}
	respRef := op.Responses.Status(status)
	if respRef == nil || respRef.Value == nil {
		respRef = op.Responses.Default() // every owned operation documents a default Error reply
	}
	if respRef == nil || respRef.Value == nil {
		t.Fatalf("contract %s %q documents neither %d nor a default response", method, specPath, status)
	}
	media := respRef.Value.Content.Get("application/json")
	if media == nil || media.Schema == nil || media.Schema.Value == nil {
		t.Fatalf("contract %s %q %d has no application/json schema", method, specPath, status)
	}
	if len(body) == 0 {
		t.Fatalf("response body is empty; contract requires JSON for %s %q %d", method, specPath, status)
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if err := media.Schema.Value.VisitJSON(value, openapi3.MultiErrors()); err != nil {
		t.Fatalf("%s %q %d body violates the contract: %v\nbody: %s", method, specPath, status, err, body)
	}
}

func TestContractCoversOwnedRoutesOnly(t *testing.T) {
	doc := loadContract(t)
	seen := map[string]bool{}
	for _, p := range doc.Paths.InMatchingOrder() {
		seen[p] = true
	}
	for _, want := range []string{"/api/state", "/api/downloads", "/api/downloads/{id}/cancel", "/api/downloads/{id}/resume", "/api/repository", "/api/runs", "/api/runs/{id}/stop", "/api/runs/{id}/logs", "/api/runs/{id}/health", "/api/cache", "/api/cache/prune", "/api/cache/{id}", "/api/models", "/health", "/healthz"} {
		if !seen[want] {
			t.Fatalf("contract missing owned route %q", want)
		}
	}
	for path := range seen {
		if strings.HasPrefix(path, "/v1/") {
			t.Fatalf("OpenAI-compatible surface %q must stay out of the contract", path)
		}
	}
}

func TestStateHealthAndCacheReportConform(t *testing.T) {
	doc := loadContract(t)
	a := testApp(t, "unused")
	if err := os.WriteFile(filepath.Join(a.models, "demo.gguf"), []byte("GGUFdemo"), 0640); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, specPath, reqPath string }{
		{"GET", "/health", "/health"},
		{"GET", "/healthz", "/healthz"},
		{"GET", "/api/state", "/api/state"},
		{"GET", "/api/cache", "/api/cache"},
	} {
		rec := request(t, a, c.method, c.reqPath, "")
		validateResponse(t, doc, c.specPath, c.method, rec.Code, rec.Body.Bytes())
	}
	rec := request(t, a, "POST", "/api/cache/prune", "{}")
	validateResponse(t, doc, "/api/cache/prune", "POST", rec.Code, rec.Body.Bytes())
	rec = request(t, a, "DELETE", "/api/cache/missing", "")
	validateResponse(t, doc, "/api/cache/{id}", "DELETE", rec.Code, rec.Body.Bytes())
	rec = request(t, a, "DELETE", "/api/models?path=nope.gguf", "")
	validateResponse(t, doc, "/api/models", "DELETE", rec.Code, rec.Body.Bytes())
}

func TestErrorAndRoutingMissesConform(t *testing.T) {
	doc := loadContract(t)
	a := testApp(t, "unused")
	// Rejected request body → documented 400 Error.
	if rec := request(t, a, "POST", "/api/downloads", `{"repo":"not a repo"}`); rec.Code == 202 {
		t.Fatalf("expected rejection, got %d", rec.Code)
	} else {
		validateResponse(t, doc, "/api/downloads", "POST", rec.Code, rec.Body.Bytes())
	}
	// Unknown API path and wrong method must answer with the Error schema too.
	for _, c := range []struct{ method, path, specPath string }{
		{"GET", "/api/does-not-exist", ""},
		{"POST", "/api/state", "/api/state"},
	} {
		rec := request(t, a, c.method, c.path, `{}`)
		var errBody api.Error
		if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil || errBody.Error == "" {
			t.Fatalf("%s %s: routing miss must answer {\"error\":...}, got %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

func TestRunLifecycleConformsAndLaunchRoundTrips(t *testing.T) {
	doc := loadContract(t)
	binary := filepath.Join(t.TempDir(), "fake-llama-server")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntrap 'exit 0' TERM\necho ready\nwhile :; do sleep 1; done\n"), 0755); err != nil {
		t.Fatal(err)
	}
	a := testApp(t, binary)
	if err := os.WriteFile(filepath.Join(a.models, "test.gguf"), []byte("GGUFtest"), 0640); err != nil {
		t.Fatal(err)
	}
	launch := api.Launch{
		Model:       "test.gguf",
		Context:     8192,
		GPULayers:   99,
		Port:        18123,
		Args:        []string{"--jinja"},
		Env:         map[string]string{"TEST_VALUE": "value with spaces"},
		PromptCache: &api.PromptCache{Enabled: false, MaxBytes: 1 << 30, TTLSeconds: 900},
	}
	body, _ := json.Marshal(launch)
	rec := request(t, a, "POST", "/api/runs", string(body))
	if rec.Code != 201 {
		t.Fatalf("launch failed: %d %s", rec.Code, rec.Body.String())
	}
	validateResponse(t, doc, "/api/runs", "POST", rec.Code, rec.Body.Bytes())
	var run api.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}

	// Launch configuration must round trip through the run record and the
	// saved profile without normalization beyond documented defaults.
	if run.Launch.PromptCache == nil || run.Launch.PromptCache.MaxBytes != 1<<30 || run.Launch.PromptCache.TTLSeconds != 900 {
		t.Fatalf("prompt_cache lost in round trip: %+v", run.Launch)
	}
	if run.Launch.Context != launch.Context || run.Launch.Port != launch.Port || run.Launch.Env["TEST_VALUE"] != launch.Env["TEST_VALUE"] || run.Launch.Args[0] != "--jinja" {
		t.Fatalf("launch config changed in round trip: %+v", run.Launch)
	}
	stateRec := request(t, a, "GET", "/api/state", "")
	validateResponse(t, doc, "/api/state", "GET", stateRec.Code, stateRec.Body.Bytes())
	var state api.State
	if err := json.Unmarshal(stateRec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Runs) != 1 || state.Runs[0].Launch.PromptCache == nil || state.Runs[0].Launch.PromptCache.MaxBytes != 1<<30 {
		t.Fatalf("state did not round trip prompt_cache: %+v", state.Runs)
	}
	if p := state.Profiles["test.gguf"]; p.PromptCache == nil || p.PromptCache.TTLSeconds != 900 || !slices.Equal(p.Args, launch.Args) {
		t.Fatalf("saved profile did not round trip prompt_cache: %+v", p)
	}

	logsRec := request(t, a, "GET", "/api/runs/"+run.ID+"/logs", "")
	validateResponse(t, doc, "/api/runs/{id}/logs", "GET", logsRec.Code, logsRec.Body.Bytes())
	// The fake child serves no /health, so the pass-through must fall into the
	// documented {status, detail} unreachable shape while the run is running.
	healthRec := request(t, a, "GET", "/api/runs/"+run.ID+"/health", "")
	validateResponse(t, doc, "/api/runs/{id}/health", "GET", healthRec.Code, healthRec.Body.Bytes())
	stopRec := request(t, a, "POST", "/api/runs/"+run.ID+"/stop", `{}`)
	validateResponse(t, doc, "/api/runs/{id}/stop", "POST", stopRec.Code, stopRec.Body.Bytes())

	deadline := time.Now().Add(5 * time.Second)
	for {
		a.mu.Lock()
		status := a.runs[run.ID].Status
		a.mu.Unlock()
		if status == api.RunStatusStopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not stop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stateRec = request(t, a, "GET", "/api/state", "")
	validateResponse(t, doc, "/api/state", "GET", stateRec.Code, stateRec.Body.Bytes())
}

// TestGeneratedCacheStatusMatchesContract pins issue #13's drift class in Go:
// the generated enum constants and the contract enumeration must agree.
func TestGeneratedCacheStatusMatchesContract(t *testing.T) {
	doc := loadContract(t)
	ref, ok := doc.Components.Schemas["CacheStatus"]
	if !ok || ref == nil || ref.Value == nil {
		t.Fatal("contract defines no CacheStatus schema")
	}
	got := []api.CacheStatus{api.CacheStatusChecking, api.CacheStatusReady, api.CacheStatusUnsupported, api.CacheStatusError}
	if len(ref.Value.Enum) != len(got) {
		t.Fatalf("contract CacheStatus has %d members, Go has %d", len(ref.Value.Enum), len(got))
	}
	for _, member := range got {
		if !slices.ContainsFunc(ref.Value.Enum, func(v any) bool { return v == string(member) }) {
			t.Fatalf("Go constant %q is not in the contract enum %v", member, ref.Value.Enum)
		}
	}
}
