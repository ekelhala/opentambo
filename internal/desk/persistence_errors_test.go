package desk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ekelhala/opentambo/internal/desk/store"
)

// Startup must fail loudly (and keep every byte intact) when the database
// is unusable, newer than the binary, or holds rows it cannot decode.

func seededDB(t *testing.T, data string, breakSQL ...string) {
	t.Helper()
	db, err := store.Open(filepath.Join(data, store.DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range breakSQL {
		if _, err = db.Exec(stmt); err != nil {
			t.Fatalf("break %q: %v", stmt, err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStartupFailsWhenDatabaseUnusable(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	if err := os.MkdirAll(data, 0750); err != nil {
		t.Fatal(err)
	}
	// The database path is occupied by a directory.
	if err := os.Mkdir(filepath.Join(data, store.DBFileName), 0750); err != nil {
		t.Fatal(err)
	}
	if _, err := New(filepath.Join(base, "models"), "missing-llama-server", data, 2); err == nil ||
		!strings.Contains(err.Error(), "open database") {
		t.Fatalf("startup over an occupied database path = %v", err)
	}
}

func TestStartupRejectsNewerSchema(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	if err := os.MkdirAll(data, 0750); err != nil {
		t.Fatal(err)
	}
	seededDB(t, data, `PRAGMA user_version = 99`)
	if _, err := New(filepath.Join(base, "models"), "missing-llama-server", data, 2); err == nil ||
		!strings.Contains(err.Error(), "newer than") {
		t.Fatalf("startup on a newer schema = %v", err)
	}
}

func TestStartupFailsOnUndecodableRows(t *testing.T) {
	cases := []struct {
		name     string
		breakSQL []string
		wantErr  string
	}{
		{
			name:     "download timestamp",
			breakSQL: []string{`INSERT INTO downloads (id, url, file, status, created) VALUES ('j', 'https://h/f.gguf', 'f.gguf', 'done', 'garbage')`},
			wantErr:  "read downloads",
		},
		{
			name:     "profiles table missing",
			breakSQL: []string{`DROP TABLE profiles`},
			wantErr:  "read profiles",
		},
		{
			name: "profile document",
			breakSQL: []string{`INSERT INTO profiles (name, model, port, context, gpu_layers, args_json)
				VALUES ('p', '/m.gguf', 1, 0, 0, 'oops')`},
			wantErr: "read profile p",
		},
		{
			name:     "runs table missing blocks startup reconciliation",
			breakSQL: []string{`DROP TABLE runs`},
			wantErr:  "demote stale run records",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			data := filepath.Join(base, "data")
			if err := os.MkdirAll(data, 0750); err != nil {
				t.Fatal(err)
			}
			seededDB(t, data, c.breakSQL...)
			if _, err := New(filepath.Join(base, "models"), "missing-llama-server", data, 2); err == nil ||
				!strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("startup = %v, want %q", err, c.wantErr)
			}
		})
	}
}

func TestLegacyStateImportFailurePathsKeepFilesInPlace(t *testing.T) {
	t.Run("unreadable legacy file", func(t *testing.T) {
		base := t.TempDir()
		data := filepath.Join(base, "data")
		if err := os.MkdirAll(filepath.Join(data, "state.json"), 0750); err != nil {
			t.Fatal(err)
		}
		if _, err := New(filepath.Join(base, "models"), "missing-llama-server", data, 2); err == nil ||
			!strings.Contains(err.Error(), "read legacy state") {
			t.Fatalf("startup with a directory where state.json belongs = %v", err)
		}
	})
	t.Run("marker read failure", func(t *testing.T) {
		data := t.TempDir()
		db, err := store.Open(filepath.Join(data, store.DBFileName))
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Migrate(db); err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		a := &App{db: db, data: data}
		if err = a.importLegacyState(); err == nil || !strings.Contains(err.Error(), "read import marker") {
			t.Fatalf("import over a closed database = %v", err)
		}
	})
	t.Run("row import failure leaves the legacy file untouched", func(t *testing.T) {
		data := t.TempDir()
		db, err := store.Open(filepath.Join(data, store.DBFileName))
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Migrate(db); err != nil {
			t.Fatal(err)
		}
		// The download rows cannot land, but nothing partial may commit and
		// the legacy file must survive for a repaired retry.
		if _, err = db.Exec(`DROP TABLE downloads`); err != nil {
			t.Fatal(err)
		}
		path := writeLegacyState(t, data, `{"jobs":{"j1":{"id":"j1","url":"https://h/f.gguf","file":"f.gguf","status":"downloading","created":"2026-01-01T00:00:00Z"}}}`)
		a := &App{db: db, data: data}
		if err = a.importLegacyState(); err == nil || !strings.Contains(err.Error(), "import legacy state") {
			t.Fatalf("import with a broken table = %v", err)
		}
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("legacy file must survive a failed import: %v", err)
		}
		_, done, err := store.Meta(db, importStateMarker)
		if err != nil || done {
			t.Fatalf("import marker set despite failure: %v %v", done, err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// A launch that succeeded must report a run-history failure on the run
// itself instead of losing the process the operator started.
func TestLaunchSurvivesRunHistoryFailure(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fake-llama-server")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n"), 0755); err != nil {
		t.Fatal(err)
	}
	a := testApp(t, binary)
	if _, err := a.db.Exec(`DROP TABLE runs`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.models, "hist.gguf"), []byte("GGUFhist"), 0640); err != nil {
		t.Fatal(err)
	}
	rec := request(t, a, "POST", "/api/runs", `{"model":"hist.gguf","context":8192,"gpu_layers":0,"port":18092}`)
	if rec.Code != 201 {
		t.Fatal(rec.Body.String())
	}
	var run Run
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run.Error, "Run history could not be saved") {
		t.Fatalf("launch hid the history failure: %+v", run)
	}
	if run.PID == 0 {
		t.Fatal("launch did not start the process despite the history failure")
	}
}
