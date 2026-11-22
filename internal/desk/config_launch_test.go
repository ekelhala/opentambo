package desk

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ekelhala/opentambo/internal/config"
	"github.com/ekelhala/opentambo/internal/desk/api"
)

// configApp builds an App around a fake llama-server with an operator
// configuration snapshot applied (issue #17).
func configApp(t *testing.T, binary string, cfg *config.Effective) *App {
	t.Helper()
	dir := t.TempDir()
	a, err := NewWithConfig(filepath.Join(dir, "models"), binary, filepath.Join(dir, "data"), 2, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func TestConfiguredCacheDefaultsSurfaced(t *testing.T) {
	cfg := &config.Effective{
		MaxTotalBytes: 20 << 20,
		Cache: config.CachePolicy{
			Enabled: true, MaxNamespace: 5 << 20, MaxEntry: 1 << 20,
			TTLSec: 90, MinReuseTokens: 64,
		},
	}
	a := configApp(t, "missing-llama-server", cfg)
	rec := request(t, a, "GET", "/api/state", "")
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var state api.State
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	defs := state.Settings.PromptCacheDefaults
	if !defs.Enabled || defs.MaxBytes != 5<<20 || defs.MaxEntryBytes != 1<<20 || defs.TTLSeconds != 90 || defs.MinReuseTokens != 64 {
		t.Fatalf("operator cache defaults not surfaced: %+v", defs)
	}
	rec = request(t, a, "GET", "/api/cache", "")
	var report api.CacheReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Defaults.MaxBytes != 5<<20 || report.Defaults.MinReuseTokens != 64 {
		t.Fatalf("/api/cache defaults wrong: %+v", report.Defaults)
	}
}

func TestManualModeKeepsBuiltInCacheDefaultsDisabled(t *testing.T) {
	a := testApp(t, "missing-llama-server")
	rec := request(t, a, "GET", "/api/state", "")
	var state api.State
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	defs := state.Settings.PromptCacheDefaults
	if defs.Enabled || defs.MaxBytes != 4<<30 {
		t.Fatalf("manual mode must report disabled built-in defaults: %+v", defs)
	}
}

func TestConfiguredLaunchDefaultsAndEnvLayering(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fake-llama-server")
	script := "#!/bin/sh\ntrap 'exit 0' TERM\necho \"rt:$RT_VAR launch:$LAUNCH_VAR spec:$SPEC_VAR ARGS:$*\"\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Effective{
		RuntimeEnv: map[string]string{"RT_VAR": "one"},
		Launch: config.LaunchPolicy{
			ContextSize: 4321,
			GPULayers:   7,
			Args:        []string{"--flash-attn"},
			Env:         map[string]string{"LAUNCH_VAR": "two", "SPEC_VAR": "from-launch"},
		},
		Cache: config.CachePolicy{MaxNamespace: 4 << 20, MaxEntry: 1 << 20},
	}
	a := configApp(t, binary, cfg)
	_ = os.WriteFile(filepath.Join(a.models, "test.gguf"), []byte("GGUFtest"), 0640)
	body := `{"model":"test.gguf","port":18432,"env":{"SPEC_VAR":"mine"}}`
	rec := request(t, a, "POST", "/api/runs", body)
	if rec.Code != 201 {
		t.Fatal(rec.Body.String())
	}
	var run Run
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	command := strings.Join(run.Command, " ")
	if !strings.Contains(command, "--ctx-size 4321") || !strings.Contains(command, "--n-gpu-layers 7") {
		t.Errorf("configured scalar defaults not injected: %s", command)
	}
	if !strings.Contains(command, "--flash-attn") {
		t.Errorf("launch_defaults.args missing: %s", command)
	}
	deadline := time.Now().Add(5 * time.Second)
	var logs string
	for {
		rec = request(t, a, "GET", "/api/runs/"+run.ID+"/logs", "")
		logs = rec.Body.String()
		if strings.Contains(logs, "rt:one") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no child output: %s", logs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logs, "launch:two") || !strings.Contains(logs, "spec:mine") {
		t.Errorf("env layers wrong (launch_defaults must apply, per-launch must win): %s", logs)
	}
}

func TestLaunchResolvedRejections(t *testing.T) {
	a := configApp(t, "missing-llama-server", nil)
	_ = os.WriteFile(filepath.Join(a.models, "test.gguf"), []byte("GGUFtest"), 0640)
	cases := []struct{ name, body, want string }{
		{"negative cache limit", `{"model":"test.gguf","port":18450,"prompt_cache":{"enabled":true,"max_bytes":-1}}`, "cannot be negative"},
		{"cache model unreadable", `{"model":"test.gguf","port":18451,"prompt_cache":{"enabled":true}}`, "GGUF metadata"},
		{"context too small", `{"model":"test.gguf","port":18452,"context":-5}`, "context must be positive"},
		{"gpu layers negative", `{"model":"test.gguf","port":18453,"gpu_layers":-2}`, "context must be positive"},
		{"name with comma", `{"model":"test.gguf","port":18454,"name":"a,b","context":2048}`, "model name must be"},
		{"blocked arg", `{"model":"test.gguf","port":18455,"context":2048,"args":["--host=0.0.0.0"]}`, "managed by opentambo"},
		{"env bad key", `{"model":"test.gguf","port":18456,"context":2048,"env":{"A=B":"x"}}`, "invalid environment"},
		{"env LLAMA_ARG", `{"model":"test.gguf","port":18457,"context":2048,"env":{"LLAMA_ARG_CTX_SIZE":"1"}}`, "LLAMA_ARG_*"},
		{"binary unresolvable", `{"model":"test.gguf","port":18458,"context":2048}`, "executable"},
	}
	for _, tc := range cases {
		rec := request(t, a, "POST", "/api/runs", tc.body)
		if rec.Code != 400 {
			t.Errorf("%s: status = %d, want 400 (%s)", tc.name, rec.Code, rec.Body.String())
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: body = %s, want substring %q", tc.name, rec.Body.String(), tc.want)
		}
	}
}

func TestLaunchPortConflictAndExecFailure(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fake-llama-server")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n"), 0755); err != nil {
		t.Fatal(err)
	}
	a := configApp(t, binary, nil)
	_ = os.WriteFile(filepath.Join(a.models, "test.gguf"), []byte("GGUFtest"), 0640)
	if rec := request(t, a, "POST", "/api/runs", `{"model":"test.gguf","port":18460,"context":2048}`); rec.Code != 201 {
		t.Fatalf("first launch: %d %s", rec.Code, rec.Body.String())
	}
	rec := request(t, a, "POST", "/api/runs", `{"model":"test.gguf","port":18460,"name":"other","context":2048}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "port is already used") {
		t.Errorf("port conflict: %d %s", rec.Code, rec.Body.String())
	}
	rec = request(t, a, "POST", "/api/runs", `{"model":"test.gguf","port":18461,"name":"test","context":2048}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "name is already used") {
		t.Errorf("name conflict: %d %s", rec.Code, rec.Body.String())
	}

	// A present-but-unexecutable binary passes path resolution and fails at
	// process start.
	notExec := filepath.Join(t.TempDir(), "llama-server")
	if err := os.WriteFile(notExec, []byte("#!/bin/sh\n"), 0644); err != nil {
		t.Fatal(err)
	}
	broken := configApp(t, notExec, nil)
	_ = os.WriteFile(filepath.Join(broken.models, "test.gguf"), []byte("GGUFtest"), 0640)
	rec = request(t, broken, "POST", "/api/runs", `{"model":"test.gguf","port":18462,"context":2048}`)
	if rec.Code != 400 {
		t.Errorf("exec failure: status = %d, want 400", rec.Code)
	}
}

func TestCacheEnabledDefaultInjectsPromptCache(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fake-llama-server")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Effective{
		Cache: config.CachePolicy{Enabled: true, MaxNamespace: 4 << 20, MaxEntry: 1 << 20, TTLSec: 60},
	}
	a := configApp(t, binary, cfg)
	writeArchitectureGGUF(t, filepath.Join(a.models, "test.gguf"), "llama")
	rec := request(t, a, "POST", "/api/runs", `{"model":"test.gguf","port":18465,"context":2048}`)
	if rec.Code != 201 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var run Run
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Launch.PromptCache == nil || !run.Launch.PromptCache.Enabled {
		t.Fatalf("prompt_cache.defaults.enabled not injected: %+v", run.Launch.PromptCache)
	}
	if !strings.Contains(strings.Join(run.Command, " "), "--slot-save-path") {
		t.Errorf("cache slot dir not passed to child: %v", run.Command)
	}
}

func TestAutostartConfiguredProfileWithCache(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fake-llama-server")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Effective{
		Cache: config.CachePolicy{Enabled: true, MaxNamespace: 4 << 20, MaxEntry: 1 << 20, TTLSec: 60},
		Profiles: map[string]config.EffectiveProfile{
			"cached": {
				Name: "cached", Model: "test.gguf", Port: 18470, Autostart: true,
				Context: 2048, GPULayers: 0,
				Cache: config.CachePolicy{Enabled: true, MaxNamespace: 4 << 20, MaxEntry: 1 << 20, TTLSec: 60},
			},
		},
	}
	a := configApp(t, binary, cfg)
	writeArchitectureGGUF(t, filepath.Join(a.models, "test.gguf"), "llama")
	a.AutostartConfigured()
	rec := request(t, a, "GET", "/api/state", "")
	var state api.State
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Runs) != 1 {
		t.Fatalf("expected one autostart run, got %+v", state.Runs)
	}
	pc := state.Runs[0].Launch.PromptCache
	if pc == nil || !pc.Enabled || pc.MaxBytes != 4<<20 || pc.TTLSeconds != 60 {
		t.Fatalf("profile cache policy not applied: %+v", pc)
	}
}

func TestAutostartConfiguredLaunchesProfiles(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fake-llama-server")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntrap 'exit 0' TERM\necho ready\nwhile :; do sleep 1; done\n"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Effective{
		Launch: config.LaunchPolicy{ContextSize: config.DefaultContextSize, GPULayers: config.DefaultGPULayers},
		Cache:  config.CachePolicy{MaxNamespace: 4 << 20, MaxEntry: 1 << 20},
		Profiles: map[string]config.EffectiveProfile{
			"beta":  {Name: "beta", Model: "test.gguf", Port: 18441, Autostart: true, Context: 2048, GPULayers: 1},
			"alpha": {Name: "alpha", Model: "test.gguf", Port: 18440, Context: 2048, GPULayers: 1},
			// A missing model must fail only this profile, not the manager.
			"broken": {Name: "broken", Model: "missing.gguf", Port: 18442, Autostart: true, Context: 2048, GPULayers: 1},
		},
	}
	a := configApp(t, binary, cfg)
	_ = os.WriteFile(filepath.Join(a.models, "test.gguf"), []byte("GGUFtest"), 0640)
	a.AutostartConfigured()
	rec := request(t, a, "GET", "/api/state", "")
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	var state api.State
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Runs) != 1 || state.Runs[0].Launch.Name != "beta" {
		t.Fatalf("expected only the autostart profile running, got %+v", state.Runs)
	}
	if state.Runs[0].Launch.Context != 2048 || state.Runs[0].Launch.GPULayers != 1 {
		t.Errorf("profile scalars not applied: %+v", state.Runs[0].Launch)
	}
}
