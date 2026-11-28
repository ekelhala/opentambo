package desk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/ekelhala/opentambo/internal/desk/api"
)

// Launch and Run are the generated transport records; the Run wrapper adds
// manager-only state (stop hook, context, log buffer, cache coordinator)
// that is never serialized.
type Launch = api.Launch

type logBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 128<<10 {
		b.data = bytes.Clone(b.data[len(b.data)-(128<<10):])
	}
	return len(p), nil
}
func (b *logBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }

func (a *App) launch(w http.ResponseWriter, r *http.Request) {
	var spec api.Launch
	if err := decode(w, r, &spec); err != nil {
		fail(w, err)
		return
	}
	// The contract always presents args/env as present collections; absent
	// client values normalize to empty rather than null on every round trip.
	if spec.Args == nil {
		spec.Args = []string{}
	}
	if spec.Env == nil {
		spec.Env = map[string]string{}
	}
	// Operator launch_defaults.args sit below explicit client args so a
	// launch from the UI always wins over the shared default (issue #17).
	if len(a.launchArgs) > 0 {
		spec.Args = append(append([]string{}, a.launchArgs...), spec.Args...)
	}
	// prompt_cache.defaults.enabled makes caching the default for launches
	// that do not state a preference.
	if spec.PromptCache == nil && a.cacheEnabled {
		spec.PromptCache = &api.PromptCache{Enabled: true}
	}
	run, err := a.launchResolved(spec)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, 201, run.Run)
}

// launchResolved runs the full validated launch pipeline for an
// already-decoded spec. The HTTP handler and configured profile autostart
// (#17) share it so every child gets identical validation, operator env
// layering and run history.
func (a *App) launchResolved(spec api.Launch) (*Run, error) {
	if err := a.validatePromptCache(spec.PromptCache); err != nil {
		return nil, err
	}
	// Namespace derivation hashes model files; keep that work outside App.mu.
	var cachePrep *cacheLaunch
	if spec.PromptCache != nil && spec.PromptCache.Enabled {
		var err error
		if cachePrep, err = a.preparePromptCache(spec); err != nil {
			return nil, err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	model, err := a.modelPath(spec.Model)
	if err != nil {
		return nil, err
	}
	if err = parsePort(spec.Port); err != nil {
		return nil, err
	}
	if spec.Context == 0 {
		spec.Context = a.contextSize
	}
	if spec.GPULayers == 0 && a.gpuLayers > 0 {
		spec.GPULayers = a.gpuLayers
	}
	if spec.Context < 1 || spec.GPULayers < 0 {
		return nil, errors.New("context size must be positive and GPU layers can't be negative")
	}
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		spec.Name = defaultModelName(spec.Model)
	}
	if spec.Name == "" || strings.Contains(spec.Name, ",") || strings.IndexFunc(spec.Name, unicode.IsControl) >= 0 || len(spec.Name) > 256 {
		return nil, errors.New("model name must be 256 characters or fewer, with no commas or control characters")
	}
	for _, run := range a.runs {
		if (run.Status == api.RunStatusRunning || run.Status == api.RunStatusStopping) && run.Launch.Port == spec.Port {
			return nil, errors.New("that port is already in use by a managed server")
		}
		if (run.Status == api.RunStatusRunning || run.Status == api.RunStatusStopping) && run.Launch.Name == spec.Name {
			return nil, errors.New("that model name is already in use by a managed server")
		}
	}
	// These arguments belong to the manager so health checks and displayed model stay accurate.
	blocked := map[string]bool{"-m": true, "--model": true, "-a": true, "--alias": true, "--host": true, "--port": true, "-hf": true, "--hf-repo": true, "--models-preset": true, "--models-dir": true}
	for _, arg := range spec.Args {
		if blocked[strings.SplitN(arg, "=", 2)[0]] {
			return nil, fmt.Errorf("%s is managed by opentambo", arg)
		}
	}
	for key, value := range spec.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return nil, errors.New("environment variables must use NAME=value format")
		}
		if strings.HasPrefix(key, "LLAMA_ARG_") {
			return nil, errors.New("use launch arguments instead of LLAMA_ARG_* environment variables")
		}
	}
	args := []string{"--model", model, "--ctx-size", strconv.Itoa(spec.Context), "--n-gpu-layers", strconv.Itoa(spec.GPULayers)}
	if cachePrep != nil {
		// The manager owns the slot save directory so snapshots survive runs
		// with the same compatible namespace.
		args = append(args, "--slot-save-path", cachePrep.dir+string(os.PathSeparator))
	}
	args = append(args, spec.Args...)
	args = append(args, "--alias", spec.Name, "--host", "127.0.0.1", "--port", strconv.Itoa(spec.Port))
	// Resolve the executable exactly like the cache identity hashing does so
	// a PATH basename and an absolute path launch the same binary that was
	// (or would be) recorded in the namespace.
	binaryPath, err := a.resolvedBinary()
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(a.ctx)
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	// Remove inherited llama.cpp settings and replace environment keys deterministically.
	env := map[string]string{}
	for _, pair := range os.Environ() {
		k, v, _ := strings.Cut(pair, "=")
		if !strings.HasPrefix(k, "LLAMA_ARG_") {
			env[k] = v
		}
	}
	// Operator environment layers (#17): runtime.env applies to every child,
	// launch_defaults.env to every launch; a per-launch spec.Env still wins.
	for k, v := range a.runtimeEnv {
		env[k] = v
	}
	for k, v := range a.launchEnv {
		env[k] = v
	}
	for k, v := range spec.Env {
		env[k] = v
	}
	keys := []string{}
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	run := &Run{Run: api.Run{ID: id(), Launch: spec, Command: append([]string{binaryPath}, args...), Status: api.RunStatusRunning, Started: time.Now()}, stop: stop, ctx: ctx, log: &logBuffer{}}
	if cachePrep != nil {
		run.cache = newRunCache(cachePrep.spec, cachePrep.namespace, newLlamaClient(spec.Port, a.cacheClient), cachePrep.store)
	}
	cmd.Stdout = run.log
	cmd.Stderr = run.log
	if err = cmd.Start(); err != nil {
		stop()
		return nil, err
	}
	run.PID = cmd.Process.Pid
	a.runs[run.ID] = run
	a.profiles[spec.Model] = spec
	if err = a.save(); err != nil {
		run.Error = "Launch settings could not be saved: " + err.Error()
	}
	if err = a.recordRunLocked(run); err != nil && run.Error == "" {
		// History is bookkeeping beside a launch that already succeeded;
		// startup reconciliation of live rows is the backstop.
		run.Error = "Run history could not be saved: " + err.Error()
	}
	if run.cache != nil {
		a.workers.Add(1)
		readyContext := ctx
		go func() {
			defer a.workers.Done()
			run.cache.AwaitReady(readyContext)
		}()
	}
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		// Ensure descendants are also killed when graceful shutdown expires.
		finished := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				select {
				case <-timer.C:
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				case <-finished:
				}
			case <-finished:
			}
		}()
		err := cmd.Wait()
		close(finished)
		a.mu.Lock()
		defer a.mu.Unlock()
		now := time.Now()
		run.Ended = &now
		run.Status = api.RunStatusStopped
		run.stop = nil
		if err != nil && ctx.Err() == nil {
			run.Status = api.RunStatusFailed
			run.Error = err.Error()
		}
		// Persist the terminal history row (no longer live). A failure here
		// cannot change the process outcome; startup demotion of live rows
		// remains the backstop.
		_ = a.recordRunLocked(run)
		stop()
	}()
	return run, nil
}

func (a *App) stopRun(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run := a.runs[r.PathValue("id")]
	if run == nil {
		jsonError(w, http.StatusNotFound, "no such run")
		return
	}
	if run.stop != nil {
		run.Status = api.RunStatusStopping
		run.stop()
	}
	respond(w, 202, api.Accepted{Accepted: true})
}
func (a *App) logs(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	run := a.runs[r.PathValue("id")]
	a.mu.Unlock()
	if run == nil {
		jsonError(w, http.StatusNotFound, "no such run")
		return
	}
	respond(w, 200, api.Logs{Logs: run.log.String()})
}
func (a *App) health(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	run := a.runs[r.PathValue("id")]
	if run == nil {
		a.mu.Unlock()
		jsonError(w, http.StatusNotFound, "no such run")
		return
	}
	port, status := run.Launch.Port, run.Status
	a.mu.Unlock()
	if status != api.RunStatusRunning {
		respond(w, 200, api.RunHealth{Status: api.RunHealthStatus(status)})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
	defer cancel()
	code, body, err := getJSON(a.client, ctx, fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		respond(w, 200, api.RunHealth{Status: api.RunHealthStatusUnreachable, Detail: err.Error()})
		return
	}
	var response map[string]any
	if err = json.Unmarshal(body, &response); err != nil {
		response = map[string]any{}
	}
	respond(w, 200, api.RunHealth{HTTPStatus: code, Response: response})
}
