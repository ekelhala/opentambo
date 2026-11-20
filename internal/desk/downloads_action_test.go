package desk

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ekelhala/opentambo/internal/desk/api"
	"github.com/ekelhala/opentambo/internal/desk/store"
)

// --- download job actions (jobAction) --------------------------------------

// A transfer slower than the checkpoint interval must persist its resume
// cursor while running, not only when it finishes.
type chunkedSlowBody struct{ remain []byte }

func (s *chunkedSlowBody) Read(p []byte) (int, error) {
	if len(s.remain) == 0 {
		return 0, io.EOF
	}
	time.Sleep(700 * time.Millisecond)
	n := copy(p, s.remain[:min(len(s.remain), 8)])
	s.remain = s.remain[n:]
	return n, nil
}

func TestDownloadCheckpointsResumeCursorWhileTransferring(t *testing.T) {
	a := testApp(t, "unused")
	payload := append([]byte("GGUF"), make([]byte, 32)...)
	a.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(&chunkedSlowBody{remain: payload}),
			ContentLength: int64(len(payload)),
			Header:        http.Header{"ETag": []string{`"slow1"`}},
			Request:       r,
		}, nil
	})}
	a.mu.Lock()
	a.jobs["slow"] = &Job{Job: api.Job{
		ID:     "slow",
		Status: api.JobStatusInterrupted,
		URL:    "https://huggingface.co/org/repo/resolve/main/slow.gguf",
		File:   "org/repo/slow.gguf",
	}}
	a.mu.Unlock()
	if rec := request(t, a, "POST", "/api/downloads/slow/resume", ""); rec.Code != 202 {
		t.Fatalf("resume status = %d, want 202", rec.Code)
	}
	sawCheckpoint := false
	deadline := time.Now().Add(20 * time.Second)
	for {
		rows, err := store.Downloads(a.db)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == "slow" && row.Bytes > 0 {
				sawCheckpoint = true
			}
		}
		a.mu.Lock()
		status := a.jobs["slow"].Status
		a.mu.Unlock()
		if status == api.JobStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never completed (%v)", status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawCheckpoint {
		t.Fatal("no progress row landed in the database during the transfer")
	}
	if _, err := os.Stat(filepath.Join(a.models, "org/repo/slow.gguf")); err != nil {
		t.Fatalf("slow download did not publish its file: %v", err)
	}
}

func TestJobActionUnknownAndMissing(t *testing.T) {
	a := testApp(t, "unused")
	if rec := request(t, a, "POST", "/api/downloads/nope/cancel", ""); rec.Code != 404 {
		t.Fatalf("missing job status = %d, want 404", rec.Code)
	}
	a.mu.Lock()
	a.jobs["job-1"] = &Job{Job: api.Job{ID: "job-1", Status: api.JobStatusInterrupted}}
	a.mu.Unlock()
	if rec := request(t, a, "POST", "/api/downloads/job-1/teleport", ""); rec.Code != 404 {
		t.Fatalf("unknown action status = %d, want 404", rec.Code)
	}
	if rec := request(t, a, "POST", "/api/downloads/job-1/cancel", ""); rec.Code != 202 {
		t.Fatalf("cancel without a live hook status = %d, want 202", rec.Code)
	}
}

func TestJobActionResumeValidation(t *testing.T) {
	a := testApp(t, "unused")
	a.mu.Lock()
	a.jobs["done"] = &Job{Job: api.Job{ID: "done", Status: api.JobStatusCompleted}}
	a.jobs["badurl"] = &Job{Job: api.Job{ID: "badurl", Status: api.JobStatusInterrupted, URL: "http://example.com/file.gguf"}}
	a.mu.Unlock()
	if rec := request(t, a, "POST", "/api/downloads/done/resume", ""); rec.Code != 400 {
		t.Fatalf("completed job resume status = %d, want 400", rec.Code)
	}
	if rec := request(t, a, "POST", "/api/downloads/badurl/resume", ""); rec.Code != 400 {
		t.Fatalf("off-host resume status = %d, want 400", rec.Code)
	}
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestJobActionResumeTransfersThroughValidatedTarget(t *testing.T) {
	a := testApp(t, "unused")
	payload := []byte("GGUF synthetic resume payload")
	a.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader(payload)),
			ContentLength: int64(len(payload)),
			Header:        http.Header{"ETag": []string{`"abc123"`}},
			Request:       r,
		}, nil
	})}
	a.mu.Lock()
	a.jobs["int"] = &Job{Job: api.Job{
		ID:     "int",
		Status: api.JobStatusInterrupted,
		URL:    "https://huggingface.co/org/repo/resolve/main/model.gguf",
		File:   "org/repo/model.gguf",
	}}
	a.mu.Unlock()
	if rec := request(t, a, "POST", "/api/downloads/int/resume", ""); rec.Code != 202 {
		t.Fatalf("resume status = %d, want 202", rec.Code)
	}
	// Wait for the worker to publish the completed job and the renamed file.
	deadline := time.Now().Add(10 * time.Second)
	for {
		a.mu.Lock()
		status := a.jobs["int"].Status
		a.mu.Unlock()
		if status == api.JobStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			a.mu.Lock()
			jobErr := a.jobs["int"].Error
			a.mu.Unlock()
			t.Fatalf("job never completed (%v): %v", status, jobErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(a.models, "org/repo/model.gguf")); err != nil {
		t.Fatalf("resumed download did not publish its file: %v", err)
	}
}
