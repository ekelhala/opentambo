package store

import (
	"database/sql"
	"errors"
	"time"
)

// Time layout: UTC RFC3339 with nanoseconds, sortable and round-trip safe.
const timeLayout = "2006-01-02T15:04:05.999999999Z07:00"

func encodeTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func decodeTime(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

// Download is one Hugging Face download job with its resume checkpoint.
// Progress (Bytes/BytesPerSecond) is a bounded checkpoint rewritten
// periodically by the worker, not a stream of per-event writes.
type Download struct {
	ID             string
	URL            string
	File           string
	Status         string
	Bytes          int64
	Total          int64
	BytesPerSecond float64
	ETag           string
	Error          string
	Created        time.Time
}

// Profile is a saved launch configuration. Args/Env are JSON documents
// owned by the caller; PromptCacheJSON is the raw persisted prompt-cache
// spec ("" when the profile launched without one).
type Profile struct {
	Name            string
	Model           string
	Alias           string
	Port            int
	Context         int
	GPULayers       int
	ArgsJSON        string
	EnvJSON         string
	PromptCacheJSON string
}

// RunRecord is one managed-process history row. Live marks a process the
// running manager supervises; history rows are immutable after their
// terminal update.
type RunRecord struct {
	ID          string
	Status      string
	Model       string
	Name        string
	Port        int
	PID         int
	CommandJSON string
	LaunchJSON  string
	Started     time.Time
	Ended       *time.Time
	Error       string
	Live        bool
}

// SetMeta stores a bookkeeping value (import markers, import details).
func SetMeta(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Meta reads a bookkeeping value; ok is false when the key is absent.
func Meta(db *sql.DB, key string) (string, bool, error) {
	var value string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// ReplaceDownloads rewrites the whole download table inside one
// transaction, so a crash can never leave a half-written job set.
// Completed rows are kept out only when callers drop them; this layer
// stores exactly what it is given.
func ReplaceDownloads(db *sql.DB, jobs []Download) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = replaceDownloadsTx(tx, jobs); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceDownloadsTx(tx *sql.Tx, jobs []Download) error {
	if _, err := tx.Exec(`DELETE FROM downloads`); err != nil {
		return err
	}
	for _, j := range jobs {
		if _, err := tx.Exec(`INSERT INTO downloads
			(id, url, file, status, bytes, total, bytes_per_second, etag, error, created)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.ID, j.URL, j.File, j.Status, j.Bytes, j.Total, j.BytesPerSecond, j.ETag, j.Error, encodeTime(j.Created)); err != nil {
			return err
		}
	}
	return nil
}

// ImportState atomically replaces the downloads and profiles tables and
// records the import completion marker in the same transaction, so an
// interrupted import retries from scratch without duplicating rows and a
// completed import is never applied twice.
func ImportState(db *sql.DB, markerValue string, jobs []Download, profiles []Profile) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = replaceDownloadsTx(tx, jobs); err != nil {
		return err
	}
	if err = replaceProfilesTx(tx, profiles); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO meta (key, value) VALUES ('import.state_json', ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, markerValue); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceState rewrites downloads and profiles from the manager's
// authoritative in-memory snapshot in one transaction; the run history is
// deliberately untouched.
func ReplaceState(db *sql.DB, jobs []Download, profiles []Profile) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = replaceDownloadsTx(tx, jobs); err != nil {
		return err
	}
	if err = replaceProfilesTx(tx, profiles); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceProfilesTx(tx *sql.Tx, profiles []Profile) error {
	if _, err := tx.Exec(`DELETE FROM profiles`); err != nil {
		return err
	}
	for _, p := range profiles {
		if _, err := tx.Exec(`INSERT INTO profiles
			(name, model, alias, port, context, gpu_layers, args_json, env_json, prompt_cache_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.Name, p.Model, p.Alias, p.Port, p.Context, p.GPULayers, p.ArgsJSON, p.EnvJSON, p.PromptCacheJSON); err != nil {
			return err
		}
	}
	return nil
}

// Downloads returns all persisted jobs ordered by creation.
func Downloads(db *sql.DB) ([]Download, error) {
	rows, err := db.Query(`SELECT id, url, file, status, bytes, total, bytes_per_second, etag, error, created
		FROM downloads ORDER BY created`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Download
	for rows.Next() {
		var (
			j       Download
			created string
		)
		if err = rows.Scan(&j.ID, &j.URL, &j.File, &j.Status, &j.Bytes, &j.Total,
			&j.BytesPerSecond, &j.ETag, &j.Error, &created); err != nil {
			return nil, err
		}
		if j.Created, err = decodeTime(created); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// SaveDownload upserts one job row: the bounded progress checkpoint path
// workers use without touching other jobs' rows.
func SaveDownload(db *sql.DB, j Download) error {
	_, err := db.Exec(`INSERT INTO downloads
		(id, url, file, status, bytes, total, bytes_per_second, etag, error, created)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			status = excluded.status, bytes = excluded.bytes, total = excluded.total,
			bytes_per_second = excluded.bytes_per_second, etag = excluded.etag, error = excluded.error`,
		j.ID, j.URL, j.File, j.Status, j.Bytes, j.Total, j.BytesPerSecond, j.ETag, j.Error, encodeTime(j.Created))
	return err
}

// DeleteDownload removes one job row.
func DeleteDownload(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM downloads WHERE id = ?`, id)
	return err
}

// SaveProfile upserts a launch profile by name.
func SaveProfile(db *sql.DB, p Profile) error {
	_, err := db.Exec(`INSERT INTO profiles
		(name, model, alias, port, context, gpu_layers, args_json, env_json, prompt_cache_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET
			model = excluded.model, alias = excluded.alias, port = excluded.port,
			context = excluded.context, gpu_layers = excluded.gpu_layers,
			args_json = excluded.args_json, env_json = excluded.env_json,
			prompt_cache_json = excluded.prompt_cache_json`,
		p.Name, p.Model, p.Alias, p.Port, p.Context, p.GPULayers, p.ArgsJSON, p.EnvJSON, p.PromptCacheJSON)
	return err
}

// DeleteProfile removes a saved profile; deleting a missing name is a no-op.
func DeleteProfile(db *sql.DB, name string) error {
	_, err := db.Exec(`DELETE FROM profiles WHERE name = ?`, name)
	return err
}

// Profiles returns every saved profile ordered by name.
func Profiles(db *sql.DB) ([]Profile, error) {
	rows, err := db.Query(`SELECT name, model, alias, port, context, gpu_layers, args_json, env_json,
		COALESCE(prompt_cache_json, '') FROM profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Profile
	for rows.Next() {
		var p Profile
		if err = rows.Scan(&p.Name, &p.Model, &p.Alias, &p.Port, &p.Context, &p.GPULayers,
			&p.ArgsJSON, &p.EnvJSON, &p.PromptCacheJSON); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SaveRun upserts a run history row.
func SaveRun(db *sql.DB, r RunRecord) error {
	live := 0
	if r.Live {
		live = 1
	}
	var ended any
	if r.Ended != nil {
		ended = encodeTime(*r.Ended)
	}
	_, err := db.Exec(`INSERT INTO runs
		(id, status, model, name, port, pid, command_json, launch_json, started, ended, error, live)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			status = excluded.status, pid = excluded.pid, command_json = excluded.command_json,
			launch_json = excluded.launch_json, ended = excluded.ended, error = excluded.error,
			live = excluded.live`,
		r.ID, r.Status, r.Model, r.Name, r.Port, r.PID, r.CommandJSON, r.LaunchJSON,
		encodeTime(r.Started), ended, r.Error, live)
	return err
}

// DemoteLiveRuns marks every still-live row as stopped by a manager exit.
// Called at startup: a recorded PID is never proof of a supervised
// process, so history is corrected before the UI could ever trust it.
func DemoteLiveRuns(db *sql.DB, when time.Time, reason string) error {
	_, err := db.Exec(`UPDATE runs SET live = 0, status = 'stopped', ended = ?,
		error = CASE WHEN error = '' THEN ? ELSE error END WHERE live = 1`,
		encodeTime(when), reason)
	return err
}

// RunHistory returns run rows newest-first, bounded by limit.
func RunHistory(db *sql.DB, limit int) ([]RunRecord, error) {
	rows, err := db.Query(`SELECT id, status, model, name, port, pid, command_json, launch_json,
		started, COALESCE(ended, ''), error, live FROM runs ORDER BY started DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RunRecord
	for rows.Next() {
		var (
			r       RunRecord
			started string
			ended   string
			live    int
		)
		if err = rows.Scan(&r.ID, &r.Status, &r.Model, &r.Name, &r.Port, &r.PID,
			&r.CommandJSON, &r.LaunchJSON, &started, &ended, &r.Error, &live); err != nil {
			return nil, err
		}
		if r.Started, err = decodeTime(started); err != nil {
			return nil, err
		}
		if ended != "" {
			end, err := decodeTime(ended)
			if err != nil {
				return nil, err
			}
			r.Ended = &end
		}
		r.Live = live == 1
		out = append(out, r)
	}
	return out, rows.Err()
}
