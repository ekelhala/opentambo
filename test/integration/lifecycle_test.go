//go:build integration

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestManagerHealthAndState(t *testing.T) {
	llama := ensureLlamaServer(t)
	m := startManager(t, llama)
	for _, path := range []string{"/healthz", "/health"} {
		response := m.get(path, http.StatusOK)
		if response["status"] != "ok" {
			t.Errorf("GET %s returned %v", path, response)
		}
	}
	state := m.get("/api/state", http.StatusOK)
	settings, _ := state["settings"].(map[string]any)
	if settings == nil || settings["binary"] != llama {
		t.Errorf("state settings do not report the managed binary: %v", settings)
	}
	models, _ := state["models"].([]any)
	if len(models) == 0 {
		t.Fatal("the shelf model is missing from /api/state")
	}
	if runs, _ := state["runs"].([]any); len(runs) != 0 {
		t.Errorf("fresh manager reports %d preexisting runs", len(runs))
	}
	// Routing misses on owned surfaces answer with the contract error shape.
	response := m.get("/api/nope", http.StatusNotFound)
	if message, _ := response["error"].(string); message == "" {
		t.Errorf("routing miss body is not {\"error\": ...}: %v", response)
	}
}

func TestManagedRunLifecycle(t *testing.T) {
	llama := ensureLlamaServer(t)
	m := startManager(t, llama)
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	// Validation rejects an unusable port before any process spawns.
	bad := m.post("/api/runs", launchSpec("rejected", 80, nil), http.StatusBadRequest)
	if message, _ := bad["error"].(string); message == "" {
		t.Errorf("port rejection body: %v", bad)
	}
	// Unknown fields on owned routes are refused, not ignored.
	m.post("/api/runs", `{"name":"x","model":"`+modelRelPath+`","port":`+strconv.Itoa(port)+`,"context":2048,"gpu_layers":0,"bogus":true}`, http.StatusBadRequest)

	run := m.launchRun("tiny", port, nil)
	id, _ := run["id"].(string)
	if pid := numeric(t, run["pid"], "run pid"); pid <= 0 {
		t.Fatalf("run has no live pid: %v", run)
	}

	// The gateway lists exactly the running model.
	models := m.get("/v1/models", http.StatusOK)
	data, _ := models["data"].([]any)
	found := false
	for _, item := range data {
		entry, _ := item.(map[string]any)
		if entry["id"] == "tiny" {
			found = true
		}
	}
	if !found {
		t.Fatalf("gateway model listing is missing the run: %v", models)
	}

	completion := m.completion("tiny", "What is the capital of France?", nil)
	if usage, _ := completion["usage"].(map[string]any); numeric(t, usage["prompt_tokens"], "usage.prompt_tokens") <= 0 {
		t.Errorf("completion usage missing prompt tokens: %v", completion["usage"])
	}

	// Unknown model names keep the OpenAI-style error from the gateway.
	response := m.request(http.MethodPost, "/v1/chat/completions",
		chatBody("not-running", "hi", false, nil), http.StatusNotFound)
	proxyError, _ := response["error"].(map[string]any)
	if kind, _ := proxyError["type"].(string); kind != "model_not_found" {
		t.Errorf("unknown-model error shape: %v", response)
	}

	// Logs are captured and served for the run.
	logs := m.get("/api/runs/"+id+"/logs", http.StatusOK)
	if text, _ := logs["logs"].(string); !strings.Contains(strings.ToLower(text), "llama") {
		t.Errorf("run logs do not contain llama-server output: %q", text)
	}

	m.stopRun(id)
	// After stopping, inference returns the gateway's model_not_found again.
	response = m.request(http.MethodPost, "/v1/chat/completions",
		chatBody("tiny", "still there?", false, nil), http.StatusNotFound)
	proxyError, _ = response["error"].(map[string]any)
	if kind, _ := proxyError["type"].(string); kind != "model_not_found" {
		t.Errorf("post-stop error shape: %v", response)
	}
	// The child process is actually gone.
	for _, item := range runList(m.get("/api/state", http.StatusOK)) {
		if item["id"] == id {
			if pid, ok := item["pid"].(float64); ok && pid > 0 {
				if err := syscall.Kill(int(pid), 0); err == nil {
					t.Fatalf("child pid %d still alive after stop", int(pid))
				}
			}
		}
	}
}

func TestStreamingAndCancellation(t *testing.T) {
	llama := ensureLlamaServer(t)
	m := startManager(t, llama)
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	run := m.launchRun("streamy", port, nil)
	id, _ := run["id"].(string)

	// A complete stream delivers deltas and the [DONE] sentinel.
	events := m.streamCompletion("streamy", "Count from one to five.")
	if len(events) < 2 {
		t.Fatalf("stream produced %d chunks, want at least 2", len(events))
	}

	// Cancelling mid-stream must not wedge the manager: fresh requests
	// still succeed afterwards on the same run.
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+"/v1/chat/completions",
		strings.NewReader(chatBody("streamy", "Write a very long story about a lighthouse.", true, map[string]any{"max_tokens": 256})))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
	for i := 0; i < 3 && scanner.Scan(); i++ {
	}
	response.Body.Close()
	cancel()

	// Recovery: inference on the same run works again once the child
	// notices the abandoned stream.
	deadline := time.Now().Add(60 * time.Second)
	for {
		if m.tryCompletion("streamy", "Are you still responsive?") {
			break
		}
		if time.Now().After(deadline) {
			m.dumpLog()
			m.t.Fatal("run never recovered after a cancelled stream")
		}
		time.Sleep(250 * time.Millisecond)
	}
	m.streamCompletion("streamy", "Name a color.")
	m.stopRun(id)
}

// tryCompletion reports whether a non-streaming completion succeeds without
// failing the test, for polling recovery windows.
func (m *manager) tryCompletion(model, prompt string) bool {
	request, err := http.NewRequest(http.MethodPost, m.base+"/v1/chat/completions",
		strings.NewReader(chatBody(model, prompt, false, nil)))
	if err != nil {
		return false
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 60 * time.Second}).Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode == http.StatusOK
}

// streamCompletion collects SSE data payloads until [DONE].
func (m *manager) streamCompletion(model, prompt string) []string {
	m.t.Helper()
	request, err := http.NewRequest(http.MethodPost, m.base+"/v1/chat/completions",
		strings.NewReader(chatBody(model, prompt, true, nil)))
	if err != nil {
		m.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 2 * time.Minute}).Do(request)
	if err != nil {
		m.t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		m.t.Fatalf("stream status %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		m.t.Fatalf("stream content type %q", contentType)
	}
	var events []string
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			return events
		}
		events = append(events, payload)
	}
	m.t.Fatalf("stream ended without the [DONE] sentinel after %d events", len(events))
	return nil
}

func TestConcurrentRequestsThroughGateway(t *testing.T) {
	llama := ensureLlamaServer(t)
	m := startManager(t, llama)
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	// A cache-enabled run serializes slot traffic in the coordinator, so
	// every concurrent request must succeed through the real child.
	run := m.launchRun("busy", port, map[string]any{"prompt_cache": map[string]any{"enabled": true}})
	id, _ := run["id"].(string)
	waitCacheReady(t, m, id)

	results := make(chan map[string]any, 4)
	for _, prompt := range []string{"One.", "Two.", "Three.", "Four."} {
		prompt := prompt
		go func() {
			request, err := http.NewRequest(http.MethodPost, m.base+"/v1/chat/completions",
				strings.NewReader(chatBody("busy", prompt, false, nil)))
			if err != nil {
				results <- nil
				return
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := (&http.Client{Timeout: 3 * time.Minute}).Do(request)
			if err != nil {
				results <- nil
				return
			}
			defer response.Body.Close()
			var body map[string]any
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				results <- nil
				return
			}
			body["__status"] = response.StatusCode
			results <- body
		}()
	}
	for i := 0; i < 4; i++ {
		body := <-results
		if body == nil {
			t.Fatal("a concurrent request failed outright")
		}
		if body["__status"] != http.StatusOK {
			t.Errorf("concurrent request status %v: %v", body["__status"], body)
		}
	}
	m.stopRun(id)
}

// waitCacheReady polls the run state until the prompt-cache coordinator
// finishes its capability probe.
func waitCacheReady(t *testing.T, m *manager, runID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		for _, item := range runList(m.get("/api/state", http.StatusOK)) {
			if item["id"] != runID {
				continue
			}
			summary, _ := item["cache"].(map[string]any)
			status, _ := summary["status"].(string)
			if status != "" && status != "checking" {
				return summary
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("prompt-cache probe never settled for run %s", runID)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
