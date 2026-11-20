package desk

// Prompt-cache management API. Responses expose sanitized metadata only:
// snapshot bytes and prompt token IDs never leave the server.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/ekelhala/opentambo/internal/desk/api"
)

// cacheStoreFor opens (or returns the cached) store for a namespace directory.
// Callers must not hold a.mu.
func (a *App) cacheStoreFor(dir, namespace string, spec PromptCache) (*cacheStore, error) {
	resolved, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.cacheStores == nil {
		a.cacheStores = map[string]*cacheStore{}
	}
	store := a.cacheStores[resolved]
	a.mu.Unlock()
	if store != nil {
		// Reopening a namespace adopts the supplied policy deterministically
		// (the most recent launch wins when runs share the namespace).
		if err = store.applyPolicy(spec.MaxBytes, spec.MaxEntryBytes, secondsDuration(spec.TTLSeconds)); err != nil {
			return nil, err
		}
		return store, nil
	}
	opened, err := newCacheStore(a.db, resolved, namespace, spec.MaxBytes, spec.MaxEntryBytes, secondsDuration(spec.TTLSeconds))
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if existing := a.cacheStores[resolved]; existing != nil {
		store = existing
	} else {
		store = opened
		a.cacheStores[resolved] = store
	}
	a.mu.Unlock()
	if store != opened {
		if err = store.applyPolicy(spec.MaxBytes, spec.MaxEntryBytes, secondsDuration(spec.TTLSeconds)); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// registryCacheStores returns every store the process knows about, opening
// namespace directories that exist on disk but were not launched yet. Each
// directory yields at most one store.
func (a *App) registryCacheStores() []*cacheStore {
	a.mu.Lock()
	known := make(map[string]*cacheStore, len(a.cacheStores))
	for dir, store := range a.cacheStores {
		known[filepath.Clean(dir)] = store
	}
	root := filepath.Join(a.data, "prompt-cache")
	a.mu.Unlock()
	if items, err := os.ReadDir(root); err == nil {
		for _, item := range items {
			if !item.IsDir() {
				continue
			}
			dir := filepath.Clean(filepath.Join(root, item.Name()))
			if _, seen := known[dir]; seen {
				continue
			}
			store, err := observeCacheStore(a.db, dir, item.Name())
			if err != nil {
				continue
			}
			a.mu.Lock()
			if a.cacheStores == nil {
				a.cacheStores = map[string]*cacheStore{}
			}
			if existing := a.cacheStores[dir]; existing != nil {
				store = existing
			} else {
				a.cacheStores[dir] = store
			}
			a.mu.Unlock()
			known[dir] = store
		}
	}
	stores := make([]*cacheStore, 0, len(known))
	for _, store := range known {
		stores = append(stores, store)
	}
	sort.Slice(stores, func(i, j int) bool { return stores[i].namespace < stores[j].namespace })
	return stores
}

func (a *App) cacheSummary(w http.ResponseWriter, r *http.Request) {
	namespaces := []api.CacheNamespace{}
	a.mu.Lock()
	runs := make([]*Run, 0, len(a.runs))
	for _, run := range a.runs {
		runs = append(runs, run)
	}
	a.mu.Unlock()
	for _, store := range a.registryCacheStores() {
		entries, used, budget := store.Summary()
		belongs := []api.CacheNamespaceRun{}
		for _, run := range runs {
			if run.cache != nil && run.cache.store == store {
				summary := run.cache.summary()
				belongs = append(belongs, api.CacheNamespaceRun{RunID: run.ID, Model: run.Launch.Name, RunStatus: run.Status, Cache: summary})
			}
		}
		namespaces = append(namespaces, api.CacheNamespace{Namespace: store.namespace, Entries: entries, BytesUsed: used, MaxBytes: budget, Runs: belongs})
	}
	respond(w, 200, api.CacheReport{Namespaces: namespaces, Defaults: cacheDefaults()})
}

// cacheDefaults reports the server-side limits applied to unset PromptCache
// options; state settings reuse the same source.
func cacheDefaults() api.CacheDefaults {
	return api.CacheDefaults{MaxBytes: defaultCacheMaxBytes, MaxEntryBytes: defaultCacheMaxEntryBytes, TTLSeconds: defaultCacheTTLSeconds, MinReuseTokens: defaultCacheMinReuseTokens}
}

func (a *App) cacheDelete(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")
	for _, store := range a.registryCacheStores() {
		if !store.HasEntry(entryID) {
			continue
		}
		deleted, err := store.Delete(entryID)
		if err != nil {
			respond(w, http.StatusConflict, api.Error{Error: err.Error()})
			return
		}
		if !deleted {
			jsonError(w, http.StatusNotFound, "no such cache entry")
			return
		}
		respond(w, 200, api.Deleted{Deleted: true})
		return
	}
	jsonError(w, http.StatusNotFound, "no such cache entry")
}

func (a *App) cachePrune(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			fail(w, err)
			return
		}
	}
	removed := 0
	for _, store := range a.registryCacheStores() {
		removed += store.Prune()
	}
	respond(w, 200, api.PruneResult{Removed: removed})
}
