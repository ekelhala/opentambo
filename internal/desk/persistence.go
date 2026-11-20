package desk

// Durable state lives in the embedded SQLite database (store.Open at
// <data>/opentambo.db). This file translates between wire records and
// storage rows, imports legacy state.json once, and keeps the in-memory
// maps of App as the working set that save() snapshots transactionally.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ekelhala/opentambo/internal/desk/api"
	"github.com/ekelhala/opentambo/internal/desk/store"
)

const (
	legacyStateFile   = "state.json"
	importStateMarker = "import.state_json"
)

// openDurable opens (or creates) the database under the data directory,
// applies migrations, imports any legacy state.json, and records that a
// previous manager's live runs are no longer supervised (a recorded PID is
// never proof). Nothing here needs App.mu: the app is not reachable yet.
func (a *App) openDurable() error {
	db, err := store.Open(filepath.Join(a.data, store.DBFileName))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	if err = store.Migrate(db); err != nil {
		_ = db.Close()
		return fmt.Errorf("database schema: %w", err)
	}
	a.db = db
	if err = a.importLegacyState(); err != nil {
		_ = db.Close()
		return err
	}
	if err = a.loadDurable(); err != nil {
		_ = db.Close()
		return err
	}
	if err = store.DemoteLiveRuns(db, time.Now(), "the manager restarted; the recorded process is no longer supervised"); err != nil {
		_ = db.Close()
		return fmt.Errorf("demote stale run records: %w", err)
	}
	if err = a.loadRunHistory(); err != nil {
		_ = db.Close()
		return err
	}
	return nil
}

// recentRunHistoryLimit bounds how much run history the working set keeps;
// the database always holds everything.
const recentRunHistoryLimit = 50

// loadRunHistory repopulates the run working map with recent history rows
// (already demoted: a recorded PID is never proof of a supervised process),
// so restarting the manager shows what ran without ever claiming a dead
// process is live. These rows have no cancellation hook, no log buffer
// content and no cache coordinator.
func (a *App) loadRunHistory() error {
	records, err := store.RunHistory(a.db, recentRunHistoryLimit)
	if err != nil {
		return fmt.Errorf("read run history: %w", err)
	}
	for _, rec := range records {
		run, err := runFromRecord(rec)
		if err != nil {
			return fmt.Errorf("read run history %s: %w", rec.ID, err)
		}
		a.runs[run.ID] = run
	}
	return nil
}

func runFromRecord(rec store.RunRecord) (*Run, error) {
	var launch Launch
	if err := json.Unmarshal([]byte(rec.LaunchJSON), &launch); err != nil {
		return nil, fmt.Errorf("launch document: %w", err)
	}
	var command []string
	if err := json.Unmarshal([]byte(rec.CommandJSON), &command); err != nil {
		return nil, fmt.Errorf("command document: %w", err)
	}
	if launch.Model == "" {
		launch.Model = rec.Model
	}
	if launch.Name == "" {
		launch.Name = rec.Name
	}
	if launch.Port == 0 {
		launch.Port = rec.Port
	}
	return &Run{Run: api.Run{
		ID: rec.ID, Launch: launch, Command: command, PID: rec.PID,
		Status: api.RunStatus(rec.Status), Started: rec.Started, Ended: rec.Ended,
		Error: rec.Error,
	}, log: &logBuffer{}}, nil
}

// loadDurable repopulates the job and profile working maps from the
// database. Interrupted transfers surface as resumable, like the legacy
// JSON store did.
func (a *App) loadDurable() error {
	downloads, err := store.Downloads(a.db)
	if err != nil {
		return fmt.Errorf("read downloads: %w", err)
	}
	for _, d := range downloads {
		job := &Job{Job: jobFromDownload(d)}
		if job.Status == api.JobStatusDownloading || job.Status == api.JobStatusQueued {
			job.Status = api.JobStatusInterrupted
			job.Error = "Service stopped; click Resume to continue."
		}
		a.jobs[job.ID] = job
	}
	profiles, err := store.Profiles(a.db)
	if err != nil {
		return fmt.Errorf("read profiles: %w", err)
	}
	for _, p := range profiles {
		spec, err := launchFromProfile(p)
		if err != nil {
			return fmt.Errorf("read profile %s: %w", p.Name, err)
		}
		a.profiles[p.Name] = spec
	}
	return nil
}

// importLegacyState imports a pre-SQLite state.json exactly once. The
// completion marker and the rows commit in one transaction, so a crash
// mid-import retries from scratch without duplicates; the legacy file is
// kept aside as state.json.migrated, never deleted outright. A corrupt
// legacy file aborts startup untouched, so the operator can repair or
// move it and retry — nothing is imported and nothing is lost.
func (a *App) importLegacyState() error {
	_, done, err := store.Meta(a.db, importStateMarker)
	if err != nil {
		return fmt.Errorf("read import marker: %w", err)
	}
	if done {
		return nil
	}
	path := filepath.Join(a.data, legacyStateFile)
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy state: %w", err)
	}
	var legacy savedState
	if err = json.Unmarshal(contents, &legacy); err != nil {
		return fmt.Errorf("import %s: %w; repair or move the file and restart — nothing was imported or deleted", path, err)
	}
	jobs := make([]store.Download, 0, len(legacy.Jobs))
	for _, job := range legacy.Jobs {
		if job != nil {
			jobs = append(jobs, downloadFromJob(&job.Job))
		}
	}
	profiles := make([]store.Profile, 0, len(legacy.Profiles))
	for name, spec := range legacy.Profiles {
		p, err := profileFromLaunch(name, spec)
		if err != nil {
			return fmt.Errorf("import legacy profile %s: %w", name, err)
		}
		profiles = append(profiles, p)
	}
	marker := fmt.Sprintf("imported %d downloads, %d profiles at %s", len(jobs), len(profiles), time.Now().UTC().Format(time.RFC3339))
	if err = store.ImportState(a.db, marker, jobs, profiles); err != nil {
		return fmt.Errorf("import legacy state: %w", err)
	}
	// The marker already committed: the rename is housekeeping. Keeping the
	// original aside (never deleting) means the operator can always diff or
	// restore it; a failed rename leaves the marker authoritative.
	if err = os.Rename(path, path+".migrated"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil //nolint:nilerr // import is durable; a leftover file is inert behind the marker
	}
	return nil
}

// save snapshots the working maps into the database in one transaction.
// Caller holds a.mu. State contains user-supplied environment values: the
// 0600 database file is the protection boundary.
func (a *App) save() error {
	jobs := make([]store.Download, 0, len(a.jobs))
	for _, job := range a.jobs {
		jobs = append(jobs, downloadFromJob(&job.Job))
	}
	profiles := make([]store.Profile, 0, len(a.profiles))
	for name, spec := range a.profiles {
		p, err := profileFromLaunch(name, spec)
		if err != nil {
			return err
		}
		profiles = append(profiles, p)
	}
	return store.ReplaceState(a.db, jobs, profiles)
}

// recordRunLocked persists one run-history row (live while the process is
// running or stopping, terminal afterwards). Caller holds a.mu. History
// failures must never replace the launch result the operator already
// received, so they surface on the run itself instead of aborting it.
func (a *App) recordRunLocked(run *Run) error {
	return store.SaveRun(a.db, store.RunRecord{
		ID:          run.ID,
		Status:      string(run.Status),
		Model:       run.Launch.Model,
		Name:        run.Launch.Name,
		Port:        run.Launch.Port,
		PID:         run.PID,
		CommandJSON: mustJSON(run.Command),
		LaunchJSON:  mustJSON(run.Launch),
		Started:     run.Started,
		Ended:       run.Ended,
		Error:       run.Error,
		Live:        run.Status == api.RunStatusRunning || run.Status == api.RunStatusStopping,
	})
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}

func downloadFromJob(job *api.Job) store.Download {
	return store.Download{
		ID: job.ID, URL: job.URL, File: job.File, Status: string(job.Status),
		Bytes: job.Bytes, Total: job.Total, BytesPerSecond: job.BytesPerSecond,
		ETag: job.ETag, Error: job.Error, Created: job.Created,
	}
}

func jobFromDownload(d store.Download) api.Job {
	return api.Job{
		ID: d.ID, URL: d.URL, File: d.File, Status: api.JobStatus(d.Status),
		Bytes: d.Bytes, Total: d.Total, BytesPerSecond: d.BytesPerSecond,
		ETag: d.ETag, Error: d.Error, Created: d.Created,
	}
}

func profileFromLaunch(name string, spec Launch) (store.Profile, error) {
	args, err := json.Marshal(nonNilArgs(spec.Args))
	if err != nil {
		return store.Profile{}, err
	}
	env, err := json.Marshal(nonNilEnv(spec.Env))
	if err != nil {
		return store.Profile{}, err
	}
	promptCache := ""
	if spec.PromptCache != nil {
		encoded, err := json.Marshal(spec.PromptCache)
		if err != nil {
			return store.Profile{}, err
		}
		promptCache = string(encoded)
	}
	return store.Profile{
		Name: name, Model: spec.Model, Alias: spec.Name, Port: spec.Port,
		Context: spec.Context, GPULayers: spec.GPULayers,
		ArgsJSON: string(args), EnvJSON: string(env), PromptCacheJSON: promptCache,
	}, nil
}

func launchFromProfile(p store.Profile) (Launch, error) {
	var spec Launch
	if err := json.Unmarshal([]byte(p.ArgsJSON), &spec.Args); err != nil {
		return spec, fmt.Errorf("launch args: %w", err)
	}
	if err := json.Unmarshal([]byte(p.EnvJSON), &spec.Env); err != nil {
		return spec, fmt.Errorf("launch environment: %w", err)
	}
	if p.PromptCacheJSON != "" {
		spec.PromptCache = &PromptCache{}
		if err := json.Unmarshal([]byte(p.PromptCacheJSON), spec.PromptCache); err != nil {
			return spec, fmt.Errorf("prompt cache spec: %w", err)
		}
	}
	spec.Model, spec.Name, spec.Port = p.Model, p.Alias, p.Port
	spec.Context, spec.GPULayers = p.Context, p.GPULayers
	return spec, nil
}

func nonNilArgs(args []string) []string {
	if args == nil {
		return []string{}
	}
	return args
}

func nonNilEnv(env map[string]string) map[string]string {
	if env == nil {
		return map[string]string{}
	}
	return env
}
