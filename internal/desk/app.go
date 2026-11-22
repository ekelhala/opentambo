package desk

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ekelhala/opentambo/internal/desk/api"
)

// Model is the wire type for a GGUF file in the inventory.
type Model = api.Model

// Job pairs the generated transport record with the worker's cancel hook.
// The embedded api.Job is the authoritative wire shape; only process-local
// state lives outside it.
type Job struct {
	api.Job
	cancel context.CancelFunc
}

// Run pairs the generated transport record with manager-only state (stop
// hook, context, log buffer, cache coordinator). JSON of a *Run is exactly
// the api.Run payload.
type Run struct {
	api.Run
	stop  context.CancelFunc
	ctx   context.Context
	log   *logBuffer
	cache *RunCache
}

type App struct {
	mu                   sync.Mutex
	models, binary, data string
	db                   *sql.DB
	jobs                 map[string]*Job
	runs                 map[string]*Run
	profiles             map[string]Launch
	cacheStores          map[string]*cacheStore
	slots                chan struct{}
	ctx                  context.Context
	cancel               context.CancelFunc
	workers              sync.WaitGroup
	closeOnce            sync.Once
	client               *http.Client
	proxyTransport       *http.Transport
	cacheClient          *http.Client
}

func New(models, binary, data string, parallel int) (*App, error) {
	if parallel < 1 || parallel > 32 {
		return nil, errors.New("downloads must be between 1 and 32")
	}
	var err error
	models, err = filepath.Abs(models)
	if err != nil {
		return nil, err
	}
	data, err = filepath.Abs(data)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{models, data} {
		if err = os.MkdirAll(dir, 0750); err != nil {
			return nil, err
		}
	}
	models, err = filepath.EvalSymlinks(models)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{models: models, binary: binary, data: data, jobs: map[string]*Job{}, runs: map[string]*Run{}, profiles: map[string]Launch{}, cacheStores: map[string]*cacheStore{}, slots: make(chan struct{}, parallel), ctx: ctx, cancel: cancel, client: &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 30 * time.Second}}}
	a.proxyTransport = &http.Transport{ResponseHeaderTimeout: 10 * time.Minute}
	// Control-plane calls to children (slot save/restore) can legitimately
	// take a while on large KV caches; they never go through a proxy.
	a.cacheClient = &http.Client{Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 5 * time.Minute}}
	if err = a.openDurable(); err != nil {
		cancel()
		return nil, err
	}
	return a, nil
}

func (a *App) Close() {
	a.closeOnce.Do(func() {
		a.cancel()
		a.mu.Lock()
		for _, r := range a.runs {
			if r.stop != nil {
				r.stop()
			}
		}
		a.mu.Unlock()
		a.workers.Wait()
		a.proxyTransport.CloseIdleConnections()
		if a.db != nil {
			_ = a.db.Close()
		}
	})
}

// savedState is the legacy pre-SQLite on-disk shape, kept only so the one
// time import (persistence.go) can decode an existing state.json.
type savedState struct {
	Jobs     map[string]*Job   `json:"jobs"`
	Profiles map[string]Launch `json:"profiles"`
}

func id() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// fail reports a rejected request with the contract's Error shape
// (api/openapi.yaml → components.schemas.Error).
func fail(w http.ResponseWriter, err error) {
	respond(w, 400, api.Error{Error: err.Error()})
}

// jsonError writes the contract's Error shape for API routes. The plain-text
// helpers (http.Error, http.NotFound) are not used on owned /api/* routes so
// every documented response body validates against the schema.
func jsonError(w http.ResponseWriter, status int, message string) {
	respond(w, status, api.Error{Error: message})
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", a.state)
	mux.HandleFunc("POST /api/downloads", a.addDownload)
	mux.HandleFunc("POST /api/downloads/{id}/{action}", a.jobAction)
	mux.HandleFunc("GET /api/repository", a.browseRepository)
	mux.HandleFunc("POST /api/runs", a.launch)
	mux.HandleFunc("POST /api/runs/{id}/stop", a.stopRun)
	mux.HandleFunc("GET /api/runs/{id}/logs", a.logs)
	mux.HandleFunc("GET /api/runs/{id}/health", a.health)
	mux.HandleFunc("GET /api/cache", a.cacheSummary)
	mux.HandleFunc("DELETE /api/cache/{id}", a.cacheDelete)
	mux.HandleFunc("POST /api/cache/prune", a.cachePrune)
	mux.HandleFunc("DELETE /api/models", a.deleteModel)
	mux.HandleFunc("GET /v1/models", a.proxyModels)
	for _, endpoint := range []string{"chat/completions", "completions", "embeddings", "responses"} {
		mux.HandleFunc("POST /v1/"+endpoint, a.proxyInference)
	}
	health := func(w http.ResponseWriter, r *http.Request) { respond(w, 200, api.HealthStatus{Status: api.Ok}) }
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /health", health) // Compatible with an existing Pangolin Manager health target.

	web, _ := fs.Sub(assets, "web")
	mux.Handle("/", http.FileServer(http.FS(web)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'self'; base-uri 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			apiRoute := strings.HasPrefix(r.URL.Path, "/api/")
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				rejected(w, apiRoute, http.StatusForbidden, "cross-site request denied")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					rejected(w, apiRoute, http.StatusForbidden, "cross-origin request denied")
					return
				}
			}
			if r.Method == "POST" && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				rejected(w, apiRoute, http.StatusUnsupportedMediaType, "application/json required")
				return
			}
		}
		mux.ServeHTTP(missAsJSON(w, r.URL.Path), r)
	})
}

// missAsJSON converts the ServeMux plain-text routing misses (404/405) on
// owned /api/* paths into the contract's JSON Error shape. Handlers that
// already respond with JSON keep working untouched.
func missAsJSON(w http.ResponseWriter, path string) http.ResponseWriter {
	if !strings.HasPrefix(path, "/api/") {
		return w
	}
	return &jsonMissWriter{ResponseWriter: w}
}

type jsonMissWriter struct {
	http.ResponseWriter
	missed bool
}

func (w *jsonMissWriter) WriteHeader(status int) {
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		ct := w.Header().Get("Content-Type")
		if ct == "" || strings.HasPrefix(ct, "text/plain") {
			// The mux is answering a routing miss; serve the contract's body.
			w.Header().Set("Content-Type", "application/json")
			w.ResponseWriter.WriteHeader(status)
			w.missed = true
			body, _ := json.Marshal(api.Error{Error: strings.ToLower(http.StatusText(status))})
			_, _ = w.ResponseWriter.Write(body)
			return
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *jsonMissWriter) Write(p []byte) (int, error) {
	if w.missed {
		return len(p), nil // contract body already written
	}
	return w.ResponseWriter.Write(p)
}

// rejected answers a guard failure before routing; owned API routes get the
// contract's JSON Error shape, everything else stays plain text.
func rejected(w http.ResponseWriter, apiRoute bool, status int, message string) {
	if apiRoute {
		jsonError(w, status, message)
		return
	}
	http.Error(w, message, status)
}

// apiJobs projects manager jobs onto the generated transport records for
// response bodies (the cancel hook is process-local and never serialized).
func apiJobs(jobs []*Job) []api.Job {
	out := make([]api.Job, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.Job)
	}
	return out
}

func (a *App) inventory() ([]Model, error) {
	models := []Model{}
	err := filepath.WalkDir(a.models, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(strings.ToLower(d.Name()), ".gguf") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(a.models, path)
		models = append(models, Model{Path: filepath.ToSlash(rel), Size: info.Size(), Modified: info.ModTime()})
		return nil
	})
	sort.Slice(models, func(i, j int) bool { return models[i].Path < models[j].Path })
	return models, err
}

func (a *App) modelPath(rel string) (string, error) {
	if !filepath.IsLocal(rel) || !strings.HasSuffix(strings.ToLower(rel), ".gguf") {
		return "", errors.New("select a GGUF inside the model directory")
	}
	path := filepath.Join(a.models, rel)
	actual, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if actual != path {
		return "", errors.New("symlinked model files are not supported")
	}
	info, err := os.Stat(actual)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("model must be a regular file")
	}
	return path, nil
}

func (a *App) state(w http.ResponseWriter, r *http.Request) {
	models, err := a.inventory()
	if err != nil {
		fail(w, err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	jobs := []*Job{}
	for _, j := range a.jobs {
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Created.After(jobs[j].Created) })
	runs := []api.Run{}
	for _, run := range a.runs {
		view := run.Run
		if run.cache != nil {
			summary := run.cache.summary()
			view.Cache = &summary
		}
		runs = append(runs, view)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Started.After(runs[j].Started) })
	respond(w, 200, api.State{
		Settings: api.StateSettings{
			Models:              a.models,
			Binary:              a.binary,
			ParallelDownloads:   cap(a.slots),
			PromptCacheDefaults: promptCacheDefaults(),
		},
		Models:   models,
		Catalog:  catalogFrom(models),
		Jobs:     apiJobs(jobs),
		Runs:     runs,
		Profiles: a.profiles,
	})
}

// promptCacheDefaults reports the server-side limits applied to unset
// PromptCache launch options (settings.prompt_cache_defaults and /api/cache).
func promptCacheDefaults() api.PromptCacheDefaults {
	return api.PromptCacheDefaults{Enabled: false, MaxBytes: defaultCacheMaxBytes, MaxEntryBytes: defaultCacheMaxEntryBytes, TTLSeconds: defaultCacheTTLSeconds, MinReuseTokens: defaultCacheMinReuseTokens}
}

func (a *App) deleteModel(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rel := r.URL.Query().Get("path")
	path, err := a.modelPath(rel)
	if err != nil {
		fail(w, err)
		return
	}
	for _, run := range a.runs {
		if run.Status == api.RunStatusRunning || run.Status == api.RunStatusStopping {
			if run.Launch.Model == rel || sameShardSet(rel, run.Launch.Model) {
				fail(w, errors.New("stop the model server before deleting its file"))
				return
			}
		}
	}
	if err = os.Remove(path); err != nil {
		fail(w, err)
		return
	}
	respond(w, 200, api.Deleted{Deleted: true})
}

// sameShardSet reports whether two model paths are members of one split GGUF
// set: the llama.cpp naming convention shares the base name and shard total
// across every file of the same quantization. All shards of a running model
// are active assets, not just the first one passed to llama-server.
func sameShardSet(a, b string) bool {
	fa := shardPattern.FindStringSubmatch(filepath.Base(a))
	fb := shardPattern.FindStringSubmatch(filepath.Base(b))
	if fa == nil || fb == nil {
		return false
	}
	return fa[1] == fb[1] && fa[2] == fb[2] && strings.EqualFold(filepath.Dir(a), filepath.Dir(b))
}

func getJSON(client *http.Client, ctx context.Context, url string) (int, json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	if !json.Valid(b) {
		return 0, nil, errors.New("server returned a non-JSON response")
	}
	return res.StatusCode, b, nil
}
